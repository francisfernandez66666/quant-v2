// Package strategy_engine 策略引擎：事件归因、行情数据获取、策略评分池收拢。
// 本包实现了策略引擎的核心逻辑：
//   - Engine: 策略引擎主结构，负责事件归因、行情数据拉取、评分池收拢
//   - Evaluate: 策略评估入口，从新闻事件到交易信号的完整流程
//   - BuildScoringData: 为近实时打分循环构建行情数据
//   - 事件归因：将新闻事件按利好/利空方向分流到板块
//   - 行情数据获取：日K复权优先（腾讯 qfq→东财 qfq→库内日K兜底，§KLINE-CHAIN-3），不复权源仅标记兜底（§H3）
//
// （Package strategy_engine is the strategy engine: event attribution, market-data fetching and scoring-pool collection.）
package strategy_engine

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/opslog"
)

// Engine 策略引擎，负责事件归因、行情数据拉取、评分池收拢。
// 这是策略引擎的核心结构体，包含多个缓存和锁机制以支持并发安全的实时评分。
// 主要职责：
//   - 事件归因：将新闻事件分流到板块和个股
//   - 行情数据获取：日K复权优先降级链（腾讯 qfq→东财 qfq→库内日K→不复权源带标记兜底，§H3/§KLINE-CHAIN-3）
//   - 评分池收拢：合并Stage2个股、持仓和自选池
//   - K线和资金流数据缓存（TTL 5分钟）
//   - 分钟K线数据缓存（TTL 60秒）
//   - 基准指数涨跌幅缓存（TTL 30秒）
//
// （Engine handles event attribution, quote fetching and scoring-pool collection.）
type Engine struct {
	mu        sync.RWMutex        // 读写锁（Read-write lock）
	marketAPI *data.MarketAPI     // 行情 API（Market data API）
	ths       *data.THSClient     // 同花顺客户端（行情降级链路：新浪→同花顺→东财）（THS client: quote fallback Sina→THS→Eastmoney）
	scanner   *data.SectorScanner // 板块扫描器（Sector scanner）

	klineCacheMu sync.RWMutex                // 保护 klineCache 的读写锁（近实时打分并发访问）（Lock guarding klineCache for concurrent near-realtime scoring）
	klineCache   map[string]*klineCacheEntry // 日K/资金流缓存（近实时打分用）（Daily-bar / capital-flow cache for near-realtime scoring）

	minuteKCacheMu sync.RWMutex                  // 保护 minuteKCache 的读写锁（Lock guarding minuteKCache）
	minuteKCache   map[string]*minuteKCacheEntry // 分钟K线缓存（60s TTL，避免扩大打分池后 5s 循环压垮数据源）（Minute-bar cache, 60s TTL, so the widened pool doesn't hammer the data source）

	benchChgMu  sync.RWMutex // 保护基准指数涨跌幅缓存（Lock guarding the benchmark change cache）
	benchChgVal float64      // 上证指数最新涨跌幅（%）（Latest SSE index change %）
	benchChgAt  time.Time    // 基准指数拉取时间（TTL 判断）（Benchmark fetch time, for TTL checks）

	kSrcMu sync.Mutex     // 保护 K 线源统计计数（Lock guarding the K-line-source counters）
	kSrc   map[string]int // K 线来源→次数（本轮聚合，供可观测日志）（K-line source→count, aggregated per round for observability）

	finaMu     sync.Mutex                  // 保护 finaLookup（Lock guarding finaLookup）
	finaLookup func(string) *FinancialData // 个股最新财务指标查询（实盘财务因子评分用；nil=未接入）

	// dayBarsLookup §KLINE-CHAIN-3（2026-09-23 夜间批）：库内日K读取函数（由 internal/engine 从研究库注入，
	// nil=未接入）。本包刻意不 import internal/store，读库/缓存细节留在提供方（同 SetFinaLookup 的纪律）。
	// English: injected reader for locally-synced daily bars (third qfq leg); this package keeps its
	// "no import of internal/store" discipline, so the DB access lives in the provider.
	dayBarsMu     sync.Mutex
	dayBarsLookup func(code string, count int) ([]data.KLine, bool)
}

// SetFinaLookup 设置个股最新财务指标查询函数（由顶层引擎注入研究库读取逻辑）。
// 用于实盘因子战法对财务类因子（ROE/净利同比等）打分。nil 表示不注入财务数据。
// English: sets the per-stock latest-financials lookup (injected by the top-level engine from the
// research DB), enabling live factor-strategy scoring of financial factors (ROE/YoyNetProfit/etc.).
// nil disables financial injection.
func (e *Engine) SetFinaLookup(fn func(string) *FinancialData) {
	e.finaMu.Lock()
	e.finaLookup = fn
	e.finaMu.Unlock()
}

// finaOf 返回某股最新财务指标（无查询或缺失返回 nil）。
// English: returns a stock's latest financials, or nil when no lookup/missing.
func (e *Engine) finaOf(code string) *FinancialData {
	e.finaMu.Lock()
	fn := e.finaLookup
	e.finaMu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(code)
}

// SetDayBarsLookup 设置库内日K查询函数（§KLINE-CHAIN-3，由顶层引擎从研究库注入）。
// 供日K链第三级兜底：两条网络复权源（腾讯/东财）全挂时，用夜里同步进库的日K顶上来，
// 而不是就此让所有吃日K的战法整轮零分（2026-09-23 实况：只有读涨停池的龙头出了信号）。
//
// 契约（提供方必须满足，本包只调用+守卫+归一，不碰数据库）：
//   - 返回**升序**序列，末根为库里最新交易日；
//   - 价格是**后复权**口径（store.HfqBars），可能是实际价的 2~3 倍，未归一不可直接使用——
//     归一（scale=实时昨收÷末根后复权收盘）在本包的 storeDayKLine 里做；
//   - Volume 沿用 store.Bar.Vol 的**手**口径，同样由本包 ×100 换成股。
//
// nil 表示不接入本腿（cmd/backtest 等离线入口即如此），日K链行为与改造前一致。
// English: injects the local-DB daily-bar reader used as the third qfq leg. The provider returns an
// ascending series in back-adjusted (hfq) prices with volume in lots; normalization to the forward-
// adjusted basis happens here, not in the provider. nil disables the leg (behaviour as before).
func (e *Engine) SetDayBarsLookup(fn func(code string, count int) ([]data.KLine, bool)) {
	e.dayBarsMu.Lock()
	e.dayBarsLookup = fn
	e.dayBarsMu.Unlock()
}

// dayBarsOf 调用注入的库内日K读取（未注入/取空返回 nil）。
// English: calls the injected local-bar reader; nil when unhooked or empty.
func (e *Engine) dayBarsOf(code string, count int) []data.KLine {
	e.dayBarsMu.Lock()
	fn := e.dayBarsLookup
	e.dayBarsMu.Unlock()
	if fn == nil {
		return nil
	}
	raw, ok := fn(code, count)
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

// klineCacheEntry 日K + 资金流缓存条目（交易日内基本不变，TTL 刷新）。
// 用于缓存日K线和资金流数据，避免频繁请求数据源。
// （klineCacheEntry is a cached daily-bar + capital-flow entry refreshed by TTL.）
type klineCacheEntry struct {
	klines    []data.KLine      // 日K线数据（近120根，趋势/均线类战法使用）（Daily bars, ~120, for trend/MA strategies）
	moneyFlow *data.CapitalFlow // 资金流向（主力净流入）（Capital flow, main-force net inflow）
	fetchedAt time.Time         // 拉取时间（用于 5 分钟 TTL 判过期）（Fetch time, for the 5-minute TTL check）
	// §H3（2026-09-22 PM 批）：true = 本轮日K只来自**不复权源**（新浪/同花顺兜底），
	// 因子打分拒参与（见 applyDayKLine），但 LastClose 等「末根现价」用途仍可用。
	// English: §H3 — true means the bars came from an UNADJUSTED fallback source; factor
	// scoring must skip them while last-close style valuation may still use them.
	unadj bool
	// src §KLINE-CHAIN-3：供数的腿名（腾讯/东财/库内/新浪/同花顺/失败）。缓存复用判断要用它——
	// 只有库内那条是"按昨收归一"的，换昨收就得重算，网络复权腿与昨收无关可照常复用。
	src string
	// anchor §KLINE-CHAIN-3：库内腿归一时用的实时昨收（其余腿为 0）。缓存命中时校验它没变。
	anchor float64
}

// cacheReusable 判断已缓存条目能否服务本次请求（§KLINE-CHAIN-3）。
// 库内那条序列是按 anchor（当时的实时昨收）等比缩放出来的：昨收变了 scale 就变，
// 整条历史基准会跟着偏（今天的涨跌幅被混进昨天的价），所以必须重取。
// 昨收缺失（prevClose<=0，即 LastClose 这类无快照入口）不改写已归一结果，照用。
// English: the local-DB leg is scaled to the prev-close anchor at fetch time, so a changed anchor
// must bust the cache; requests without an anchor never re-scale an existing entry.
func (ent *klineCacheEntry) cacheReusable(prevClose float64) bool {
	if ent.src != kLineSrcStore || prevClose <= 0 {
		return true
	}
	return math.Abs(ent.anchor-prevClose) < 1e-6
}

// minuteKCacheEntry 分钟K线缓存条目（5分钟48根≈当日；60s TTL）。
// 用于缓存分钟K线数据，避免扩大打分池后每5秒重复拉取压垮数据源。
type minuteKCacheEntry struct {
	bars      []data.KLine
	fetchedAt time.Time
}

// New 创建策略引擎实例。
// 初始化引擎的所有缓存和数据源，返回可直接使用的引擎实例。
// （New creates a strategy-engine instance.）
func New(marketAPI *data.MarketAPI) *Engine {
	return &Engine{
		marketAPI:    marketAPI,
		klineCache:   make(map[string]*klineCacheEntry),
		minuteKCache: make(map[string]*minuteKCacheEntry),
	}
}

// SetTHS 设置同花顺客户端（线程安全），接入 新浪→同花顺→东财 行情降级链路。
// （SetTHS sets the THS client, thread-safe, wiring the Sina→THS→Eastmoney fallback chain.）
func (e *Engine) SetTHS(ths *data.THSClient) {
	e.mu.Lock()
	e.ths = ths
	e.mu.Unlock()
}

// SetScanner 设置板块扫描器（线程安全）。（SetScanner sets the sector scanner, thread-safe.）
func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.mu.Unlock()
}

// Evaluate 策略评估入口：归因事件→分流个股→收拢评分池→获取行情数据→返回策略结果。
// events 为已通过阈值过滤（|score|≥0.50）的新闻事件；positions 为当前持仓，watchlist 为用户自选。
// （Evaluate is the engine entry: attribute events → split stocks → collect the scoring pool → fetch market data →
// return the strategy result. events are threshold-filtered (|score|≥0.50); positions are holdings; watchlist is the user list.）
func (e *Engine) Evaluate(ctx context.Context, events []newsagent.NewsEvent, positions, watchlist []string) *StrategyResult {
	t0 := time.Now()
	if len(events) == 0 {
		log.Printf("[strategy_engine] 无事件，仅收拢 持仓+自选 打分池")
	} else {
		log.Printf("[strategy_engine] Evaluate 开始")
	}

	// 1. attribution: 事件 → 板块/个股分流（Attribution: events → sector/stock split）
	bullSectors, bearSectors := e.attribution(events)
	log.Printf("[strategy_engine] attribution: %d利好板块 %d利空板块", len(bullSectors), len(bearSectors))

	// 2. 分流事件个股到 LongStocks / ShortStocks（按带符号 Score 判定方向）。
	//    个股级事件取 LLM 识别的关联股；板块级事件经 propagateSectorToStocks 已注入成分股（CleanedStocks），
	//    一并并入打分池，扩大 8a 个股监测覆盖（Stage2 归因仅板块、无个股时也能出候选）。
	//    （Split event stocks into LongStocks/ShortStocks by signed Score. Stock-level events use LLM-related stocks;
	//    sector-level events have constituents injected via propagateSectorToStocks and merged into the scoring pool.）
	var longStocks, shortStocks []IndividualStock
	for _, ev := range events {
		if len(ev.CleanedStocks) == 0 {
			continue
		}
		// 仅处理 个股 与 板块 级事件（上游/下游/中性事件不产个股候选）（Only 个股/板块 level events produce candidates）
		if ev.Level != "个股" && ev.Level != "板块" {
			continue
		}
		// 方向判定：Score>0 利好进做多池，Score<0 利空进做空池，Score=0（中性）跳过（Direction: Score>0→long pool, <0→short pool, 0 skipped）
		isLong := ev.Score > 0
		isShort := ev.Score < 0
		if !isLong && !isShort {
			continue
		}
		// CleanedStocks 元素形如 "名称|代码"，拆分后规范化代码（去 SH/SZ 前后缀）（Elements are "name|code"; split and normalize the code）
		for _, cs := range ev.CleanedStocks {
			parts := strings.SplitN(cs, "|", 2)
			if len(parts) != 2 {
				continue
			}
			code := normalizeCode(parts[1])
			ist := IndividualStock{
				Code:      code,
				Name:      parts[0],
				Direction: ev.Direction,
			}
			if isLong {
				longStocks = append(longStocks, ist)
			} else {
				shortStocks = append(shortStocks, ist)
			}
		}
	}
	log.Printf("[strategy_engine] 个股分流: %d利好 %d利空", len(longStocks), len(shortStocks))

	// 3. 收拢打分池：Stage2 个股 + 持仓 + 自选（Set 去重）（Collect the deduped scoring pool: Stage2 + holdings + watchlist）
	poolSet := make(map[string]bool)
	for _, st := range longStocks {
		poolSet[st.Code] = true
	}
	for _, st := range shortStocks {
		poolSet[st.Code] = true
	}
	for _, code := range positions {
		poolSet[code] = true
	}
	for _, code := range watchlist {
		poolSet[code] = true
	}
	// 从 Set 还原为无序切片，供后续统一拉行情与打分（Turn the Set back into an unordered slice for unified fetching/scoring）
	scoringPool := make([]string, 0, len(poolSet))
	for code := range poolSet {
		scoringPool = append(scoringPool, code)
	}
	log.Printf("[strategy_engine] 打分池收拢: %d只个股 (Stage2=%d 持仓=%d 自选=%d)",
		len(scoringPool), len(longStocks)+len(shortStocks), len(positions), len(watchlist))

	// 4. 获取行情数据（KLine + 实时价 + 资金流向）（Fetch market data: bars + live price + capital flow）
	marketData := e.fetchMarketData(ctx, scoringPool)
	log.Printf("[strategy_engine] 行情数据获取: %d/%d只成功", countSuccess(marketData), len(scoringPool))

	log.Printf("[strategy_engine] Evaluate 完成, 耗时 %v", time.Since(t0))

	return &StrategyResult{
		HotSectors:  bullSectors,
		BearSectors: bearSectors,
		LongStocks:  longStocks,
		ShortStocks: shortStocks,
		BearStocks:  e.collectBearStocks(bearSectors),
		ScoringPool: scoringPool,
		MarketData:  marketData,
		L1Score:     make(map[string]float64),
		L1Blocked:   make(map[string]bool),
		Events:      events,
	}
}

// benchChg 返回上证指数当前涨跌幅（%），供 N 形 D2 相对强度对比。
// 指数行情 30s TTL 缓存（非交易时段也会取到当日值，可接受），失败返回 0。
// 这个方法被多个评分函数调用，用于计算个股相对大盘的超额收益。
// （benchChg returns the current SSE change % for N-shape D2 relative strength, cached with a 30s TTL; returns 0 on failure.）
func (e *Engine) benchChg() float64 {
	e.benchChgMu.RLock()
	if time.Since(e.benchChgAt) < 30*time.Second {
		v := e.benchChgVal
		e.benchChgMu.RUnlock()
		return v
	}
	e.benchChgMu.RUnlock()

	// 缓存过期才真的取一次上证指数；行情拿不到就返回 0，
	// 让相对强度退化成绝对评分而不是整轮打分失败。
	si, err := e.marketAPI.GetIndexQuote("000001")
	if err != nil || si == nil {
		return 0
	}
	e.benchChgMu.Lock()
	e.benchChgVal = si.ChangePct
	e.benchChgAt = time.Now()
	e.benchChgMu.Unlock()
	return si.ChangePct
}

// fetchMarketData 为打分池所有个股拉取行情数据（实时价 + KLine + 资金流向）。
// 实时行情降级链路：新浪批量 CSV（一次网络请求拉全池）→ 同花顺单查 → 东财单查。
// K线/资金流并发拉取（每只2次请求），最大并发数为6。
// （fetchMarketData fetches quotes for the whole pool — live price + bars + capital flow. Real-time fallback chain:
// Sina batch CSV → THS single → Eastmoney single. Bars/flow are fetched concurrently, 2 requests per stock.）
func (e *Engine) fetchMarketData(ctx context.Context, codes []string) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		result[code] = &StockMarketData{Code: code}
	}

	// 基准指数涨跌幅（N 形 D2 相对强度对比）（Benchmark change % for N-shape D2 relative strength）
	benchChg := e.benchChg()
	for _, md := range result {
		md.BenchChg = benchChg
	}

	// 1. 批量实时行情（新浪 CSV 单次请求，全池一次拉完）（Batch realtime quotes via a single Sina CSV request for the whole pool）
	sinaQuotes := e.marketAPI.GetSinaQuotes(codes)
	for code, si := range sinaQuotes {
		if md, ok := result[code]; ok && si != nil && si.Price > 0 {
			md.Name = si.Name
			md.Price = si.Price
			md.ChangePct = si.ChangePct
			md.Quote = si
		}
	}
	// 2. 兜底：批量未命中的个股先走同花顺，仍失败再东财单查（Backfill: stocks missing from the batch go to THS first, then Eastmoney single-fetch）
	var thsMiss, emMiss int
	for code, md := range result {
		if md.Price > 0 {
			continue
		}
		if e.ths != nil {
			si, err := e.ths.GetQuote(code)
			if err == nil && si != nil && si.Price > 0 {
				md.Name = si.Name
				md.Price = si.Price
				md.ChangePct = si.ChangePct
				md.Quote = si
				continue
			}
			thsMiss++
		}
		// 最后一层兜底：东财实时报价；仍失败则记录 Error 供上层排查（Last fallback: Eastmoney realtime quote; on failure record Error for troubleshooting）
		si, err := e.marketAPI.GetRealtimeQuote(code)
		if err != nil || si == nil || si.Price <= 0 {
			emMiss++
			md.Error = "行情获取失败"
			log.Printf("[strategy_engine] 行情失败 %s: %v", code, err)
			continue
		}
		md.Name = si.Name
		md.Price = si.Price
		md.ChangePct = si.ChangePct
		md.Quote = si
	}
	if thsMiss > 0 || emMiss > 0 {
		log.Printf("[strategy_engine] 行情降级: 新浪%d只 → 同花顺兜底失败%d → 东财兜底失败%d",
			len(sinaQuotes), thsMiss, emMiss)
	}

	// 2. K线 + 资金流向 + 分钟级量价/MACD：并发拉取（限流由 data 层 limiter 保证）（Bars + capital flow + minute volume/MACD fetched concurrently; throttling by the data-layer limiter）
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for code, md := range result {
		wg.Add(1)
		sem <- struct{}{}
		go func(code string, md *StockMarketData) {
			defer wg.Done()
			defer func() { <-sem }()

			// 日K：复权优先降级链 腾讯(qfq)→东财(qfq)→库内日K(按实时昨收归一)→[标记]新浪→[标记]同花顺
			// （§H3 + §KLINE-CHAIN-3；昨收取自本轮已拿到的实时快照，没有就传 0 让库内腿自动跳过）
			kl, unadj, _ := e.fetchDayKLine(code, livePrevClose(md))
			applyDayKLine(md, kl, unadj)
			e.attachLiveBar(md)

			// §信号速度 S0：资金流（fflow）批量调用已停用——全库无评分代码消费 md.MoneyFlow，
			// 仅手动咨询路径 buildStockBlock 直连 GetStockMoneyFlow。停用可消灭每轮 56 次东财请求（熔断主源）。
			// English: §speed S0 — per-stock capital-flow (fflow) batch fetch is disabled; no scorer reads
			// md.MoneyFlow, only the manual consult path (buildStockBlock) fetches it directly. This cuts
			// 56 EastMoney calls per round (the breaker storm source). Field/method retained for manual path.
			md.MoneyFlow = nil

			// 分钟K线（5分钟，48根≈当日）→ 计算 MACD，供 8a/8b 动量分与 N 形评分使用（Minute bars (5-min, 48 ≈ a day) → MACD for 8a/8b momentum and N-shape scoring）
			minKL := e.cachedMinuteKLine(code)
			if len(minKL) >= 2 {
				md.MinuteKLine = minKL
				md.MinuteMACD = data.CalcMACD(minKL)
			}
		}(code, md)
	}
	wg.Wait()

	e.logKLineSrc()
	return result
}

// BuildScoringData 为近实时 8a/8b 打分循环构建行情数据（5s 节奏）。
// - 实时量价优先取外部快照 quotes（data.Fetcher 5s 采集：新浪→同花顺→东财），缺失的走本引擎降级链补齐；
// - 日K + 资金流走进程内缓存（TTL 5 分钟，交易日内基本不变）；
// - 分钟K线（MACD）每轮现拉，保证动量/N 形评分的实时性。
// 这个方法是近实时打分循环的核心，每5秒调用一次，为所有评分策略提供最新的行情数据。
// （BuildScoringData builds market data for the near-realtime 8a/8b scoring loop at a 5s cadence. Live quotes prefer the
// external 5s snapshot (Sina→THS→Eastmoney) and fall back to the engine chain; daily bars + capital flow use a 5-min TTL
// cache; minute bars/MACD are fetched fresh each round for realtime momentum/N-shape scoring.）
func (e *Engine) BuildScoringData(ctx context.Context, codes []string, quotes map[string]*data.StockInfo) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		result[code] = &StockMarketData{Code: code, Fina: e.finaOf(code)}
	}

	// 基准指数涨跌幅（N 形 D2 相对强度对比）（Benchmark change % for N-shape D2 relative strength）
	benchChg := e.benchChg()
	for _, md := range result {
		md.BenchChg = benchChg
	}

	// 1. 实时量价：外部快照优先，缺失走 新浪批量→同花顺→东财 兜底（Realtime price/volume: external snapshot first; missing ones fall back to Sina batch→THS→Eastmoney）
	var missing []string
	for code, md := range result {
		if si, ok := quotes[code]; ok && si != nil && si.Price > 0 {
			md.Name = si.Name
			md.Price = si.Price
			md.ChangePct = si.ChangePct
			md.Quote = si
			continue
		}
		missing = append(missing, code)
	}
	if len(missing) > 0 {
		fallback := e.fetchQuotes(missing)
		for code, md := range result {
			if md.Price > 0 {
				continue
			}
			if si, ok := fallback[code]; ok && si != nil && si.Price > 0 {
				md.Name = si.Name
				md.Price = si.Price
				md.ChangePct = si.ChangePct
				md.Quote = si
			}
		}
	}

	// 2. 日K + 资金流：走缓存（TTL 5min）；分钟K线现拉（并发，限流由 data 层保证）（Daily bars + flow from cache (5-min TTL); minute bars fetched live concurrently)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for code, md := range result {
		wg.Add(1)
		sem <- struct{}{}
		go func(code string, md *StockMarketData) {
			defer wg.Done()
			defer func() { <-sem }()
			// 日K走缓存；库内兜底腿要昨收定锚，取本轮快照的 PrevClose（缺失传 0＝不用该腿）
			kl, cf, unadj := e.cachedKLine(code, livePrevClose(md))
			md.MoneyFlow = cf
			applyDayKLine(md, kl, unadj) // §H3：不复权兜底不进 KLines，只置 KLineUnadj
			e.attachLiveBar(md)
			minKL := e.cachedMinuteKLine(code)
			if len(minKL) >= 2 {
				md.MinuteKLine = minKL
				md.MinuteMACD = data.CalcMACD(minKL)
			}
		}(code, md)
	}
	wg.Wait()

	logKLineSrc := ""
	{
		m := e.takeKLineSrc()
		if len(m) > 0 {
			order := []string{"新浪", "同花顺", "腾讯", "东财", kLineSrcStore, "新浪分钟", "同花顺分钟", "腾讯分钟", "东财分钟", "失败", "分钟失败"}
			parts := make([]string, 0, len(order))
			for _, k := range order {
				if n, ok := m[k]; ok {
					parts = append(parts, fmt.Sprintf("%s=%d", k, n))
				}
			}
			if len(parts) > 0 {
				logKLineSrc = " K线源: " + strings.Join(parts, " ")
			}
		}
	}
	log.Printf("[strategy_engine] BuildScoringData: %d只 (快照quote=%d 兜底=%d)%s",
		len(codes), len(codes)-len(missing), len(missing), logKLineSrc)
	return result
}

// fetchQuotes 只拉实时行情（降级链：新浪批量→同花顺→东财），用于快照缺失的个股。
// （fetchQuotes fetches realtime quotes only (Sina batch→THS→Eastmoney) for stocks missing from the snapshot.）
func (e *Engine) fetchQuotes(codes []string) map[string]*data.StockInfo {
	out := make(map[string]*data.StockInfo, len(codes))
	for code, si := range e.marketAPI.GetSinaQuotes(codes) {
		if si != nil && si.Price > 0 {
			out[code] = si
		}
	}
	for _, code := range codes {
		if _, ok := out[code]; ok {
			continue
		}
		if e.ths != nil {
			if si, err := e.ths.GetQuote(code); err == nil && si != nil && si.Price > 0 {
				out[code] = si
				continue
			}
		}
		if si, err := e.marketAPI.GetRealtimeQuote(code); err == nil && si != nil && si.Price > 0 {
			out[code] = si
		}
	}
	return out
}

// cachedKLine 返回个股日K + 资金流 + 不复权标记（§H3），走 5 分钟 TTL 缓存；缓存缺失/过期时重新拉取。
// §KLINE-CHAIN-3：链序改为 **腾讯前复权 → 东财前复权 → 库内日K（归一）→ 不复权兜底（标记 unadj）**，
// prevClose 是本次可用的实时昨收（没有传 0，库内腿会因无法归一自动跳过）。
// （cachedKLine returns daily bars + capital flow + the §H3 unadjusted flag via a 5-min TTL cache;
// prevClose is the live previous close used by the local-DB leg, 0 = unavailable.）
func (e *Engine) cachedKLine(code string, prevClose float64) ([]data.KLine, *data.CapitalFlow, bool) {
	now := time.Now()
	e.klineCacheMu.RLock()
	ent, ok := e.klineCache[code]
	e.klineCacheMu.RUnlock()
	if ok && now.Sub(ent.fetchedAt) < 5*time.Minute && len(ent.klines) > 0 && ent.cacheReusable(prevClose) {
		return ent.klines, ent.moneyFlow, ent.unadj
	}

	klines, unadj, src := e.fetchDayKLine(code, prevClose)

	// §信号速度 S0：资金流批量抓取已停用（无评分消费，见 BuildScoringData 同款注释），
	// 返回 nil 资金流；手动咨询路径 internal/engine buildStockBlock 直连 GetStockMoneyFlow 不受影响。
	// English: §speed S0 — capital-flow batch fetch disabled (no scorer consumes it); returns nil flow.
	// The manual consult path (internal/engine buildStockBlock) still calls GetStockMoneyFlow directly.
	var cf *data.CapitalFlow

	e.klineCacheMu.Lock()
	e.klineCache[code] = &klineCacheEntry{klines: klines, moneyFlow: cf, fetchedAt: now, unadj: unadj, src: src, anchor: prevClose}
	e.klineCacheMu.Unlock()
	return klines, cf, unadj
}

// LastClose 返回个股最近一根日K的收盘价（走日K缓存，非交易时段/停牌也有昨收），无数据返回 0。
// §纸面估值修复：模拟盘持仓若不在 5s 快照池（非跟踪股），MarkToMarket 收不到实时价导致
// Mark 一直为 0 → 前端现价显示 0.00、浮亏 -100%。引擎侧用最近收盘价回填估值价。
// English: returns the latest daily-bar close for a code (via the day-K cache — works after hours /
// for suspended stocks; 0 when unavailable). Paper marks fall back to this when a held code is missing
// from the live 5s snapshot, so it never displays 0.00 / -100%.
func (e *Engine) LastClose(code string) float64 {
	if e == nil {
		return 0
	}
	// prevClose 传 0：本入口没有实时快照 ⇒ 无法给库内后复权序列定锚，现拉时库内腿按守卫自动跳过，
	// 行为与本批改造前完全一致（绝不让未归一的后复权价冒充现价）。缓存里若已有归一好的库内序列
	// 可以复用：它的末根已经锚回实际价，不是后复权价。
	klines, _, _ := e.cachedKLine(code, 0)
	if len(klines) == 0 {
		return 0 // 无缓存 K 线（停牌等）返回 0
	}
	return klines[len(klines)-1].Close
}

// kLineSrcStore §KLINE-CHAIN-3 库内日K腿的统计名（与「东财/腾讯/新浪/同花顺/失败」同族）。
const kLineSrcStore = "库内"

// lotsToShares 库内 `store.Bar.Vol` 口径是【手】，而 data.KLine.Volume 全系统统一是【股】
// （腾讯源已在 internal/data/tencent.go §D2 处做过同样的 ×100）。
// 不换算会让"量比 > N 倍均量"这类放量条件当场把量放大 100 倍失真（双响炮首当其冲）。
const lotsToShares = 100.0

// storeBarsMaxStale §KLINE-CHAIN-3 新鲜度闸：库内日K末根早于「今天往前 10 个交易日」即判不可信、拒用。
// 放宽到 10 而不是 1~2，是因为 internal/data/trade_time.go 的日历是 weekday 口径（长假只能靠同步的
// 休市日历，缺档时跳不过国庆这种长假）——阈值收紧会在长假后误杀一份本来正常的兜底数据。
// English: reject local bars whose last trading day is older than 10 trading days; the loose bound
// exists because the calendar is weekday-based and cannot always skip long holidays.
const storeBarsMaxStale = 10

// fetchDayKLine 拉取 120 根日 K，返回 (序列, 是否不复权, 供数的腿名)。全链统一过 ValidateKLine（§D8 口径）。
//
// §KLINE-CHAIN-3（2026-09-23 夜间批）链序调整为：
//  1. 腾讯前复权（只认 qfqday，缺失即拒收——§H3 守卫不动）——**主源**；
//  2. 东财前复权（fqt=1）——第二复权源，从主源位降级；降级只到"复权链末尾"，
//     不到不复权源之后，否则不复权数据会重新参与因子计算，等于把 §H3 白做；
//  3. 库内日K（本批新增，见 storeDayKLine）：网络两条腿同时不通时（09-23 实况）用它顶住，
//     使 N形/双响炮/因子这些吃 `md.KLines` 的战法不再整轮零分；
//  4. 新浪 / 同花顺旧接口（**不复权**）：仍排最后，拿到也只置 unadj=true 由 applyDayKLine 拒其进 KLines，
//     语义与本批改造前完全一致。
//
// prevClose 为本次可用的实时昨收（≤0 表示没有），只影响第 3 级能否定锚归一，不影响前三级之外的行为。
// 当日那一根仍由 attachLiveBar 用实时快照拼/覆盖，本函数不管。
// English: §KLINE-CHAIN-3 chain order is Tencent-qfq (primary) → EastMoney-qfq → local DB bars
// (anchor-normalized) → marked-unadjusted Sina/THS. prevClose (0 = unknown) only gates the local leg.
func (e *Engine) fetchDayKLine(code string, prevClose float64) ([]data.KLine, bool, string) {
	// 1. 主源：腾讯前复权（Tencent qfq is the primary leg since 2026-09-23.）
	if klines, err := e.marketAPI.GetTencentKLine(code, 120); err == nil && data.ValidateKLine(klines) {
		e.bumpKLineSrc("腾讯")
		return klines, false, "腾讯"
	}
	// 2. 第二复权源：东财 push2（fqt=qfq 恒定，§D6 认定为最稳复权源，本批从主源位降级但**不摘掉**）
	if klines, err := e.marketAPI.GetKLine(code, "101", 120); err == nil && data.ValidateKLine(klines) {
		e.bumpKLineSrc("东财")
		return klines, false, "东财"
	}
	// 3. 第三条腿：本地研究库夜里同步好的日K（§KLINE-CHAIN-3；取到即为复权口径，unadj=false）
	if klines, ok := e.storeDayKLine(code, prevClose); ok {
		e.bumpKLineSrc(kLineSrcStore)
		return klines, false, kLineSrcStore
	}
	// 4. 不复权兜底：拿到也不进 KLines（只置标记），语义同 §H3，不再是"能不能出信号"的那道闸
	if klines, err := e.marketAPI.GetSinaKLine(code, 120); err == nil && data.ValidateKLine(klines) {
		e.bumpKLineSrc("新浪")
		e.noteUnadjustedFallback(code)
		return klines, true, "新浪" // 不复权兜底：只供 LastClose/现价类用途（not for factors）
	}
	if e.ths != nil {
		if klines, err := e.ths.GetTHSKLine(code); err == nil && data.ValidateKLine(klines) {
			e.bumpKLineSrc("同花顺")
			e.noteUnadjustedFallback(code)
			return klines, true, "同花顺"
		}
	}
	e.bumpKLineSrc("失败")
	return nil, false, "失败"
}

// storeDayKLine 日K链第三级：读库内日K并把它整备成可比的"锚在最近实际价"前复权口径序列。
//
// 库内价是**后复权**（store.HfqBars），可能是实际价的 2~3 倍，直接进 KLines 会把 LastClose、
// 止损价、一切价格型条件整体带偏，所以四道守卫缺一不可，任一不过就拒用本腿：
//  0. 末根必须是"昨天或更早"：当日及未来日期的行先丢掉（当日K 由 attachLiveBar 用实时快照供），
//     否则昨收定锚会把今天的涨跌幅摊进整条基准。
//  1. 基准归一 scale = 实时昨收 ÷ 库内末根后复权收盘，整条序列 Open/High/Low/Close 等比缩放。
//     锚必须用**昨收**而不是现价：现价含今日涨跌幅，用现价当锚等于把今天的波动混进历史基准。
//     昨收取不到（≤0）⇒ 无从归一 ⇒ 拒用（LastClose 这类无快照入口即走这条路，行为与本批改造前一致）。
//  2. 量纲：库内 Vol 是手，×100 换成股（见 lotsToShares 注释）。
//  3. 新鲜度：库内靠夜间同步，同步断了不会报错只会给旧K——末根早于 10 个交易日前拒用；
//     早于上一交易日照用但留一条 opslog 降级痕（不误杀，但要看得见）。
//
// 结果最后统一过 data.ValidateKLine：缺档/停牌行的 0 价、NaN 必须被拦下，不许流入因子计算。
// English: the local-DB leg — scale hfq prices onto the live previous close, convert lots to shares,
// enforce freshness, then gate through ValidateKLine. Any guard failing rejects this leg.
func (e *Engine) storeDayKLine(code string, prevClose float64) ([]data.KLine, bool) {
	if prevClose <= 0 {
		return nil, false // 守卫①：无昨收即无锚，不归一就不许用
	}
	raw := e.dayBarsOf(code, 120)
	if len(raw) == 0 {
		return nil, false
	}
	// 守卫⓪（排在定锚之前）：丢掉末根里"今天及以后"的行。库内是夜间同步的 T+1 数据，健康形态就是
	// 末根落在上一交易日；若同步侧提前写进了当日半成品K（或库里混进未来日期的脏行），拿**实时昨收**
	// 去锚当日那根的收盘，等于把今天的涨跌幅整体摊进历史基准——锚错位正是本腿存在的理由要防的那类失真。
	// 丢弃不损失信息：当日那一根本来就由 attachLiveBar 用实时快照拼接/覆盖（见 fetchMarketData、
	// BuildScoringData 两处调用点），本函数产出的永远是"截至昨天"的历史序列。
	// English: strip any bar dated today or later before anchoring — the live bar is attached by
	// attachLiveBar downstream, and anchoring a partial same-day bar onto yesterday's close would fold
	// today's move into the whole historical basis.
	today := data.TradingDayDate(time.Now())
	for len(raw) > 0 && raw[len(raw)-1].Date.Format("20060102") >= today {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		e.noteStoreBarsRejected(code, "库内只有当日（或未来日期）的行，没有可锚的历史序列")
		return nil, false
	}
	last := raw[len(raw)-1]
	// 守卫①的另一半：末根后复权收盘必须为正才能算 scale（脏行/空行直接不可信）。
	if last.Close <= 0 || math.IsNaN(last.Close) || last.Date.IsZero() {
		e.noteStoreBarsRejected(code, "末根收盘价非法/日期缺失，无法定锚")
		return nil, false
	}
	scale := prevClose / last.Close
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		e.noteStoreBarsRejected(code, "scale 非法")
		return nil, false
	}
	// 守卫③：新鲜度。YYYYMMDD 定长串比较即时序（与 AddTradingDays 口径一致）。
	lastDay := last.Date.Format("20060102")
	if lastDay < tradingDaysBefore(today, storeBarsMaxStale) {
		e.noteStoreBarsRejected(code, fmt.Sprintf("末根 %s 早于 %d 个交易日前，库内日K判为不可信", lastDay, storeBarsMaxStale))
		return nil, false
	}
	if lastDay < tradingDaysBefore(today, 1) {
		// 隔夜同步晚到一步：不至于不能用，但必须留痕（当日只记一条）。
		opslog.DayOnce("dayk-store-bar-stale", func() {
			opslog.Logf("data", "库内日K末根 %s 早于上一交易日（夜间同步滞后），本轮兜底照用但已降级：%s", lastDay, code)
		})
	}

	out := make([]data.KLine, 0, len(raw))
	for _, k := range raw {
		bar := k
		bar.Open *= scale
		bar.High *= scale
		bar.Low *= scale
		bar.Close *= scale
		bar.Volume = k.Volume * lotsToShares // 守卫②：手 → 股
		out = append(out, bar)
	}
	if !data.ValidateKLine(out) {
		e.noteStoreBarsRejected(code, "归一后仍有 0 价/NaN 行，ValidateKLine 拦下")
		return nil, false
	}
	return out, true
}

// noteStoreBarsRejected 库内腿被拒时留一条按日节流的运维痕（拒用是安全方向，但断更不能无声）。
// English: once-daily opslog note when the local-bar leg is rejected — safe direction, but must not be silent.
func (e *Engine) noteStoreBarsRejected(code, reason string) {
	opslog.DayOnce("dayk-store-rejected", func() {
		opslog.Logf("data", "日K库内兜底腿拒用：%s：%s（本轮回落不复权源，吃日K的战法按零分处理）", code, reason)
	})
}

// tradingDaysBefore 返回 td 之前第 n 个交易日的 YYYYMMDD（n≥1）。
// 不复用 data.AddTradingDays：它只做前推，n<0 时原样返回，拿它算"N 天前"会得到今天这个日期、
// 新鲜度闸形同虚设。这里逐日回退并用 data.IsTradingDay 跳过周末与已加载的休市日。
// English: nth trading day before td (AddTradingDays cannot go backwards, hence this helper).
func tradingDaysBefore(td string, n int) string {
	t, err := time.ParseInLocation("20060102", td, time.Local)
	if err != nil {
		return td
	}
	for moved := 0; moved < n; {
		t = t.AddDate(0, 0, -1)
		if data.IsTradingDay(t) {
			moved++
		}
	}
	return t.Format("20060102")
}

// livePrevClose 从已取到的实时快照里读昨收价，取不到一律返回 0（=本次不启用库内腿）。
// §P1-5 之所以只认 PrevClose、**不回退 Quote.Close**：Close 是历史歧义字段（"依数据源而定"，
// 盘中常就是现价）。拿现价当归一锚等于把今天的涨跌幅写进整条历史基准，宁可不兜也不能锚错。
// English: prev close from the live snapshot only (never the ambiguous Close field — anchoring on the
// current price would fold today's move into the historical baseline). 0 means "no anchor".
func livePrevClose(md *StockMarketData) float64 {
	if md == nil || md.Quote == nil {
		return 0
	}
	if md.Quote.PrevClose > 0 {
		return md.Quote.PrevClose
	}
	return 0
}

// noteUnadjustedFallback 三条复权腿（腾讯/东财/库内）全不可用、落到不复权兜底时留一条按日节流的
// 运维告警（§H3）：该状态下因子打分暂停吃日K，用户需要知道实时链路仍在但口径降级。
// §KLINE-CHAIN-3 后本告警**降级为辅助**（owner 判定：报警不解决问题，兜住才是修复）——
// 触发它意味着连库内那条都拒用了，属于双重失效。
// English: once-daily opslog alert when all three adjusted legs failed and the chain fell to unadjusted.
func (e *Engine) noteUnadjustedFallback(code string) {
	opslog.DayOnce("dayk-unadjusted-fallback", func() {
		opslog.Logf("data", "日K复权链降级：腾讯/东财 qfq 与库内日K三条腿全不可用，%s 等落到不复权源兜底——日K因子战法本轮拒参与（仅现价类用途），MA/动量口径不可信", code)
	})
}

// applyDayKLine 按复权契约写入个股日K（§H3）：复权数据进 md.KLines 参与因子计算；
// 不复权数据**不进字段**、只置 md.KLineUnadj 标记——各策略的 len 守卫自然拒参与，
// 前端/回查也能从标记看出该股当前处于降级态。
// English: §H3 gate — adjusted bars feed md.KLines; unadjusted ones only set the marker, so every
// strategy's length guard refuses them from factor math without touching 15 consumer sites.
func applyDayKLine(md *StockMarketData, klines []data.KLine, unadj bool) {
	if md == nil {
		return
	}
	if unadj {
		if len(klines) > 0 {
			md.KLineUnadj = true
		}
		return
	}
	md.KLines = klines
}

// fetchMinuteKLine 按 新浪→同花顺→腾讯→东财 降级链获取分钟K线（5分钟，48根）。
// 用于 N 形 MinuteMACD；新浪分钟被封时落底到其它源。
// （fetchMinuteKLine fetches 48 five-minute bars via the Sina→THS→Tencent→Eastmoney chain, used for N-shape MinuteMACD;
// falls through to other sources when Sina minute data is blocked.）
func (e *Engine) fetchMinuteKLine(code string) []data.KLine {
	if klines, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48); err == nil && len(klines) >= 2 {
		e.bumpKLineSrc("新浪分钟")
		return klines
	}
	if e.ths != nil {
		// §修复 THS-KLINE(20260920)：本链要的是**5 分钟**K（供 5 分钟 MACD），
		// 必须把 5 传下去——同花顺默认/旧实现的 1 分钟数据会把 MACD 算成另一个口径。
		if klines, err := e.ths.GetTHSMinuteKLine(code, 5); err == nil && len(klines) >= 2 {
			e.bumpKLineSrc("同花顺分钟")
			return klines
		}
	}
	if klines, err := e.marketAPI.GetTencentMinuteKLine(code, 5, 48); err == nil && len(klines) >= 2 {
		e.bumpKLineSrc("腾讯分钟")
		return klines
	}
	if klines, err := e.marketAPI.GetKLine(code, "5", 48); err == nil && len(klines) >= 2 {
		e.bumpKLineSrc("东财分钟")
		return klines
	}
	e.bumpKLineSrc("分钟失败")
	return nil
}

// cachedMinuteKLine 分钟K线（5分钟48根）带 60s TTL 缓存：扩大近实时打分池后避免每 5s 重复拉取压垮数据源。
// 分钟K线内容随 5 分钟 K 线收盘才更新，60s 内复用的失真可忽略。
// （cachedMinuteKLine caches minute bars with a 60s TTL so the widened near-realtime pool does not hammer the data
// source every 5s; content only refreshes on each 5-min bar close, so reuse within 60s is safe.）
func (e *Engine) cachedMinuteKLine(code string) []data.KLine {
	now := time.Now()
	e.minuteKCacheMu.RLock()
	ent, ok := e.minuteKCache[code]
	e.minuteKCacheMu.RUnlock()
	if ok && now.Sub(ent.fetchedAt) < time.Minute && len(ent.bars) >= 2 {
		return ent.bars
	}
	bars := e.fetchMinuteKLine(code)
	e.minuteKCacheMu.Lock()
	e.minuteKCache[code] = &minuteKCacheEntry{bars: bars, fetchedAt: now}
	e.minuteKCacheMu.Unlock()
	return bars
}

// bumpKLineSrc 累计一次 K 线源统计（线程安全）。（bumpKLineSrc increments the K-line source counter, thread-safe.）
func (e *Engine) bumpKLineSrc(src string) {
	e.kSrcMu.Lock()
	if e.kSrc == nil {
		e.kSrc = make(map[string]int)
	}
	e.kSrc[src]++
	e.kSrcMu.Unlock()
}

// takeKLineSrc 取出并清空本轮 K 线源统计（供可观测日志）。（takeKLineSrc drains the round's K-line source counters for observability logging.）
func (e *Engine) takeKLineSrc() map[string]int {
	e.kSrcMu.Lock()
	defer e.kSrcMu.Unlock()
	m := e.kSrc
	e.kSrc = make(map[string]int)
	return m
}

// logKLineSrc 输出并清空本轮 K 线源统计，供排查数据源故障。（logKLineSrc logs and drains the K-line source counters to diagnose data-source failures.）
func (e *Engine) logKLineSrc() {
	m := e.takeKLineSrc()
	if len(m) == 0 {
		return
	}
	order := []string{"新浪", "同花顺", "腾讯", "东财", kLineSrcStore, "新浪分钟", "同花顺分钟", "腾讯分钟", "东财分钟", "失败", "分钟失败"}
	parts := make([]string, 0, len(m))
	for _, k := range order {
		if n, ok := m[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	if len(parts) == 0 {
		return
	}
	log.Printf("[strategy_engine] K线源: %s", strings.Join(parts, " "))
}

// attachLiveBar 在日K序列尾部合成当日实时bar，让战法评分（如双凸 volScore/maScore）
// 在盘中跟随实时行情，而不是整天使用最后一根（可能是昨日）收盘K线。
// 当日实时快照含 open/high/low 与实时成交量，据此构造当日K线：
//   - 若最后一根已是今日，直接用实时价修正其 open/high/low/close（数据源盘中已含当日时）；
//   - 否则追加一根当日K线（前收作为 high 兜底，避免 high<price 偏差）。
//
// 仅当日K线日期早于今天（缓存了昨日数据）或最后bar已是今日时生效，且需有实时价可用。
// （attachLiveBar appends or patched a today bar to the daily series so strategy scoring (e.g. Double Bump volScore/maScore)
// follows live prices intraday instead of the possibly-yesterday last close. If the last bar is already today, its
// open/high/low/close are corrected with the live snapshot; otherwise a today bar is appended (prev close backs up high).）
func (e *Engine) attachLiveBar(md *StockMarketData) {
	if md == nil || len(md.KLines) == 0 || md.Price <= 0 {
		return
	}
	// 无实时快照也无涨跌幅信息时无法构造当日bar，退回原序列。（Without a live snapshot or change info, revert to the original series.）
	if md.Quote == nil && md.ChangePct == 0 {
		return
	}
	last := &md.KLines[len(md.KLines)-1]
	today := time.Now()
	todayDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())

	var open, high, low float64
	if md.Quote != nil {
		open = md.Quote.Open
		high = md.Quote.High
		low = md.Quote.Low
	} else {
		// 兜底用上一收盘价为开盘，构造当日阳/阴线基准。（Backfill: use the previous close as open to form today's bullish/bearish base.）
		open = last.Close
		high = math.Max(last.Close, md.Price)
		low = math.Min(last.Close, md.Price)
	}

	if !last.Date.Before(todayDay) {
		// 最后一根就是今日：用实时价覆盖其高/低/收，避免缓存停留在昨日快照。（Last bar is today: overwrite high/low/close with live prices so the cache isn't stale.）
		last.Close = md.Price
		if high > 0 {
			if last.High < high {
				last.High = high
			}
		} else if last.High < md.Price {
			last.High = md.Price
		}
		if low > 0 {
			if last.Low > low {
				last.Low = low
			}
		} else if last.Low <= 0 || last.Low > md.Price {
			last.Low = md.Price
		}
		if last.Open <= 0 {
			last.Open = open
		}
		if md.Quote != nil && md.Quote.Volume > 0 {
			last.Volume = md.Quote.Volume
		}
		return
	}

	// 最后一根早于今日：追加当日bar。（Last bar predates today: append today's bar.）
	bar := data.KLine{
		Date:   today,
		Open:   open,
		High:   high,
		Low:    low,
		Close:  md.Price,
		Volume: 0,
	}
	if md.Quote != nil && md.Quote.Volume > 0 {
		bar.Volume = md.Quote.Volume
		bar.Amount = md.Quote.Amount
	}
	if bar.High < bar.Close {
		bar.High = bar.Close
	}
	if bar.Low <= 0 || bar.Low > bar.Close {
		bar.Low = bar.Close
	}
	md.KLines = append(md.KLines, bar)
}

// attribution 事件归因：将新闻事件按利好/利空方向分流到板块，合并相同板块的事件。
// 仅取 ev.Sectors 一级板块（上游/下游不参与热点归因）；板块名须能匹配真实同花顺板块，否则丢弃。
// 返回利好板块和利空板块列表。
// 处理逻辑：
// 1. 遍历所有事件，跳过个股级事件
// 2. 根据事件Score符号决定归属板块池（负分进利空池，正分进利好池）
// 3. 同一板块的多次事件合并：累计新闻标题，保留 |score| 最大的一次事件属性
// 4. 从板块扫描器查询行情数据填充 SectorHot
//
// （attribution splits news events into bullish/bearish sector lists and merges events of the same sector. Only the
// primary ev.Sectors take part (upstream/downstream excluded); sector names must match real THS sectors or be dropped.）
func (e *Engine) attribution(events []newsagent.NewsEvent) (bull, bear []SectorHot) {
	bullMap := make(map[string]*SectorHot)
	bearMap := make(map[string]*SectorHot)

	for _, ev := range events {
		if ev.Level == "个股" {
			continue
		}

		// 按 Score 符号决定事件归属的板块池：负分进利空池，否则进利好池（Pool selection by Score sign: negative → bear pool, else bull pool）
		isBear := ev.Score < 0

		// 一条事件可以命中多个板块：逐个板块并入上面按分数符号选好的利好/利空池。
		for _, sec := range ev.Sectors {
			if sec == "" {
				continue
			}
			m := bullMap
			if isBear {
				m = bearMap
			}
			// 同一板块的多次事件合并：累计新闻标题，保留 |score| 最大的一次事件属性（Merge repeat events per sector: accumulate titles, keep the highest-|score| attributes）
			if existing, ok := m[sec]; ok {
				existing.NewsTitles = append(existing.NewsTitles, ev.Title)
				if absScore(ev.Score) > absScore(existing.Score) {
					existing.Score = ev.Score
					existing.Direction = ev.Direction
					existing.Reason = ev.Reason
				}
			} else {
				m[sec] = &SectorHot{
					Name:       sec,
					Direction:  ev.Direction,
					Score:      ev.Score,
					Reason:     ev.Reason,
					NewsTitles: []string{ev.Title},
				}
			}
		}
	}

	// 两池分别找 scanner 补齐板块行情（涨跌幅、龙头等），再把 map 拍平成切片供排序取用。
	enrichSectorData(bullMap, e.scanner)
	enrichSectorData(bearMap, e.scanner)
	for _, s := range bullMap {
		bull = append(bull, *s)
	}
	for _, s := range bearMap {
		bear = append(bear, *s)
	}
	return
}

// BuildHotSectors 将事件归因出利好/利空板块候选列表（供引擎"新热点立马进池"复用）。
// 与 Evaluate 内的 attribution 共用同一实现，幂等可重复调用。
// 这个方法主要用于在评估过程中实时更新热点板块列表。
// （BuildHotSectors attributes events into bullish/bearish sector candidates, reusing the same logic as
// attribution inside Evaluate so the engine can push fresh hotspots into the watch pool immediately.）
func (e *Engine) BuildHotSectors(events []newsagent.NewsEvent) (bull, bear []SectorHot) {
	return e.attribution(events)
}

// absScore 取评分的绝对值。
// 用于比较事件评分的大小，不考虑方向（正负）。
// （absScore returns the absolute value of a score.）
func absScore(s float64) float64 {
	if s < 0 {
		return -s
	}
	return s
}

// collectBearStocks 从利空板块中收集个股代码，去重后返回。
// 用于识别利空板块中的领跌股，供后续风险评估使用。
// （collectBearStocks collects deduplicated stock codes from bearish sectors.）
func (e *Engine) collectBearStocks(bearSectors []SectorHot) []string {
	seen := make(map[string]bool)
	var stocks []string
	for _, s := range bearSectors {
		for _, code := range s.LeadStocks {
			if !seen[code] {
				seen[code] = true
				stocks = append(stocks, code)
			}
		}
	}
	return stocks
}

// enrichSectorData 从 Scanner 查询板块行情数据填充 SectorHot。
// 板块名无法匹配真实同花顺板块（FindSectorsByNames 查不到）时直接丢弃（LLM 造名板块）。
// 这个函数为板块信息补充实时行情数据（涨跌幅、涨停家数、净流入）。
// （enrichSectorData fills SectorHot with sector quotes from the Scanner, dropping sectors that can't be matched to real
// THS boards (LLM-invented names) via FindSectorsByNames.）
func enrichSectorData(sectors map[string]*SectorHot, scanner *data.SectorScanner) {
	if scanner == nil {
		return
	}
	for name, sh := range sectors {
		infos := scanner.FindSectorsByNames([]string{name})
		if len(infos) == 0 {
			delete(sectors, name)
			continue
		}
		sh.ChangePct = infos[0].ChangePct
		sh.LimitupCnt = infos[0].LimitupCnt
		sh.NetInflow = infos[0].NetInflow
	}
}

// countSuccess 统计行情数据获取成功的股票数量（Price>0 且无错误）。
// 用于日志输出，帮助排查数据源问题。
// （countSuccess counts stocks fetched successfully: Price>0 with no error.）
func countSuccess(m map[string]*StockMarketData) int {
	n := 0
	for _, v := range m {
		if v.Error == "" && v.Price > 0 {
			n++
		}
	}
	return n
}

// normalizeCode 归一化股票代码：去除 SH/SZ/BJ 前缀和 .SH/.SZ/.BJ 后缀。
// 用于统一股票代码格式，确保不同来源的代码能够正确匹配。
// 例如：SH600000 → 600000，600000.SH → 600000
// （normalizeCode normalizes a stock code by stripping SH/SZ/BJ prefixes and .SH/.SZ/.BJ suffixes.）
func normalizeCode(code string) string {
	c := strings.TrimSpace(code)
	if len(c) > 2 {
		prefix := c[:2]
		if prefix == "SH" || prefix == "SZ" || prefix == "BJ" {
			c = c[2:]
		}
	}
	if len(c) > 3 {
		suffix := c[len(c)-3:]
		if suffix == ".SH" || suffix == ".SZ" || suffix == ".BJ" {
			c = c[:len(c)-3]
		}
	}
	return c
}
