// Package strategy_engine 策略引擎：事件归因、行情数据获取、策略评分池收拢。
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
)

// Engine 策略引擎，负责事件归因、行情数据拉取、评分池收拢、个股板块索引维护。
// （Engine handles event attribution, quote fetching, scoring-pool collection and the stock→sector index.）
type Engine struct {
	mu             sync.RWMutex        // 读写锁（Read-write lock）
	marketAPI      *data.MarketAPI     // 行情 API（Market data API）
	ths            *data.THSClient     // 同花顺客户端（行情降级链路：新浪→同花顺→东财）（THS client: quote fallback Sina→THS→Eastmoney）
	scanner        *data.SectorScanner // 板块扫描器（Sector scanner）
	stockSectorIdx map[string][]string // 个股→板块倒排索引（Stock→sector inverted index）

	klineCacheMu sync.RWMutex                // 保护 klineCache 的读写锁（近实时打分并发访问）（Lock guarding klineCache for concurrent near-realtime scoring）
	klineCache   map[string]*klineCacheEntry // 日K/资金流缓存（近实时打分用）（Daily-bar / capital-flow cache for near-realtime scoring）

	minuteKCacheMu sync.RWMutex                   // 保护 minuteKCache 的读写锁（Lock guarding minuteKCache）
	minuteKCache   map[string]*minuteKCacheEntry  // 分钟K线缓存（60s TTL，避免扩大打分池后 5s 循环压垮数据源）（Minute-bar cache, 60s TTL, so the widened pool doesn't hammer the data source）

	benchChgMu  sync.RWMutex // 保护基准指数涨跌幅缓存（Lock guarding the benchmark change cache）
	benchChgVal float64      // 上证指数最新涨跌幅（%）（Latest SSE index change %）
	benchChgAt  time.Time    // 基准指数拉取时间（TTL 判断）（Benchmark fetch time, for TTL checks）

	kSrcMu sync.Mutex     // 保护 K 线源统计计数（Lock guarding the K-line-source counters）
	kSrc   map[string]int // K 线来源→次数（本轮聚合，供可观测日志）（K-line source→count, aggregated per round for observability）
}

// klineCacheEntry 日K + 资金流缓存条目（交易日内基本不变，TTL 刷新）。（klineCacheEntry is a cached daily-bar + capital-flow entry refreshed by TTL.）
type klineCacheEntry struct {
	klines    []data.KLine      // 日K线数据（近120根，趋势/均线类战法使用）（Daily bars, ~120, for trend/MA strategies）
	moneyFlow *data.CapitalFlow // 资金流向（主力净流入）（Capital flow, main-force net inflow）
	fetchedAt time.Time         // 拉取时间（用于 5 分钟 TTL 判过期）（Fetch time, for the 5-minute TTL check）
}

// minuteKCacheEntry 分钟K线缓存条目（5分钟48根≈当日；60s TTL）。
type minuteKCacheEntry struct {
	bars      []data.KLine
	fetchedAt time.Time
}

// New 创建策略引擎实例。（New creates a strategy-engine instance.）
func New(marketAPI *data.MarketAPI) *Engine {
	return &Engine{
		marketAPI:      marketAPI,
		stockSectorIdx: make(map[string][]string),
		klineCache:     make(map[string]*klineCacheEntry),
		minuteKCache:   make(map[string]*minuteKCacheEntry),
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

	e.rebuildIndex(events)

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
// （benchChg returns the current SSE change % for N-shape D2 relative strength, cached with a 30s TTL; returns 0 on failure.）
func (e *Engine) benchChg() float64 {
	e.benchChgMu.RLock()
	if time.Since(e.benchChgAt) < 30*time.Second {
		v := e.benchChgVal
		e.benchChgMu.RUnlock()
		return v
	}
	e.benchChgMu.RUnlock()

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
// K线/资金流并发拉取（每只2次请求）。
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

			// 日K：统一降级链 新浪→同花顺→腾讯→东财（Daily bars: unified fallback chain Sina→THS→Tencent→Eastmoney）
			md.KLines = e.fetchDayKLine(code)
			e.attachLiveBar(md)

			// 资金流向（主力净流入，供资金维度评分）（Capital flow, main-force net inflow, for fund-dimension scoring）
			cf, err := e.marketAPI.GetStockMoneyFlow(code)
			if err == nil && cf != nil {
				md.MoneyFlow = cf
			}

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
// （BuildScoringData builds market data for the near-realtime 8a/8b scoring loop at a 5s cadence. Live quotes prefer the
// external 5s snapshot (Sina→THS→Eastmoney) and fall back to the engine chain; daily bars + capital flow use a 5-min TTL
// cache; minute bars/MACD are fetched fresh each round for realtime momentum/N-shape scoring.）
func (e *Engine) BuildScoringData(ctx context.Context, codes []string, quotes map[string]*data.StockInfo) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		result[code] = &StockMarketData{Code: code}
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
			md.KLines, md.MoneyFlow = e.cachedKLine(code)
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
			order := []string{"新浪", "同花顺", "腾讯", "东财", "新浪分钟", "同花顺分钟", "腾讯分钟", "东财分钟", "失败", "分钟失败"}
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

// cachedKLine 返回个股日K + 资金流，走 5 分钟 TTL 缓存；缓存缺失/过期时重新拉取。
// 日K 使用统一降级链：新浪 → 同花顺 → 腾讯 → 东财（任一源可出）。
// （cachedKLine returns daily bars + capital flow via a 5-minute TTL cache, refetching when missing or expired. Daily bars
// use the unified Sina→THS→Tencent→Eastmoney fallback chain — any source may satisfy the request.）
func (e *Engine) cachedKLine(code string) ([]data.KLine, *data.CapitalFlow) {
	now := time.Now()
	e.klineCacheMu.RLock()
	ent, ok := e.klineCache[code]
	e.klineCacheMu.RUnlock()
	if ok && now.Sub(ent.fetchedAt) < 5*time.Minute && len(ent.klines) > 0 {
		return ent.klines, ent.moneyFlow
	}

	klines := e.fetchDayKLine(code)

	cf, err := e.marketAPI.GetStockMoneyFlow(code)
	if err != nil {
		cf = nil
	}

	e.klineCacheMu.Lock()
	e.klineCache[code] = &klineCacheEntry{klines: klines, moneyFlow: cf, fetchedAt: now}
	e.klineCacheMu.Unlock()
	return klines, cf
}

// fetchDayKLine 按 新浪→同花顺→腾讯→东财 降级链拉取日 K 线（120 根）。
// 任一源返回有效数据即停；全失败时统计"失败"并返回 nil。
// （fetchDayKLine fetches 120 daily bars via the Sina→THS→Tencent→Eastmoney chain. Stops at the first valid source;
// counts a "失败" (failure) and returns nil if all sources fail.）
func (e *Engine) fetchDayKLine(code string) []data.KLine {
	if klines, err := e.marketAPI.GetSinaKLine(code, 120); err == nil && len(klines) > 0 {
		e.bumpKLineSrc("新浪")
		return klines
	}
	if e.ths != nil {
		if klines, err := e.ths.GetTHSKLine(code); err == nil && len(klines) > 0 {
			e.bumpKLineSrc("同花顺")
			return klines
		}
	}
	if klines, err := e.marketAPI.GetTencentKLine(code, 120); err == nil && len(klines) > 0 {
		e.bumpKLineSrc("腾讯")
		return klines
	}
	if klines, err := e.marketAPI.GetKLine(code, "101", 120); err == nil && len(klines) > 0 {
		e.bumpKLineSrc("东财")
		return klines
	}
	e.bumpKLineSrc("失败")
	return nil
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
		if klines, err := e.ths.GetTHSMinuteKLine(code); err == nil && len(klines) >= 2 {
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
	order := []string{"新浪", "同花顺", "腾讯", "东财", "新浪分钟", "同花顺分钟", "腾讯分钟", "东财分钟", "失败", "分钟失败"}
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

// absScore 取评分的绝对值。（absScore returns the absolute value of a score.）
func absScore(s float64) float64 {
	if s < 0 {
		return -s
	}
	return s
}

// collectBearStocks 从利空板块中收集个股代码，去重后返回。（collectBearStocks collects deduplicated stock codes from bearish sectors.）
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

// countSuccess 统计行情数据获取成功的股票数量（Price>0 且无错误）。（countSuccess counts stocks fetched successfully: Price>0 with no error.）
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
