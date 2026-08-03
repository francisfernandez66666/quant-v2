// Package strategy_engine 策略引擎：事件归因、行情数据获取、策略评分池收拢。
package strategy_engine

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// Engine 策略引擎，负责事件归因、行情数据拉取、评分池收拢、个股板块索引维护。
type Engine struct {
	mu             sync.RWMutex        // 读写锁
	marketAPI      *data.MarketAPI     // 行情 API
	ths            *data.THSClient     // 同花顺客户端（行情降级链路：新浪→同花顺→东财）
	scanner        *data.SectorScanner // 板块扫描器
	stockSectorIdx map[string][]string // 个股→板块倒排索引

	klineCacheMu sync.RWMutex                // 保护 klineCache 的读写锁（近实时打分并发访问）
	klineCache   map[string]*klineCacheEntry // 日K/资金流缓存（近实时打分用）
}

// klineCacheEntry 日K + 资金流缓存条目（交易日内基本不变，TTL 刷新）。
type klineCacheEntry struct {
	klines    []data.KLine      // 日K线数据（近120根，趋势/均线类战法使用）
	moneyFlow *data.CapitalFlow // 资金流向（主力净流入）
	fetchedAt time.Time         // 拉取时间（用于 5 分钟 TTL 判过期）
}

// New 创建策略引擎实例。
func New(marketAPI *data.MarketAPI) *Engine {
	return &Engine{
		marketAPI:      marketAPI,
		stockSectorIdx: make(map[string][]string),
		klineCache:     make(map[string]*klineCacheEntry),
	}
}

// SetTHS 设置同花顺客户端（线程安全），接入 新浪→同花顺→东财 行情降级链路。
func (e *Engine) SetTHS(ths *data.THSClient) {
	e.mu.Lock()
	e.ths = ths
	e.mu.Unlock()
}

// SetScanner 设置板块扫描器（线程安全）。
func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.mu.Unlock()
}

// Evaluate 策略评估入口：归因事件→分流个股→收拢评分池→获取行情数据→返回策略结果。
// events 为已通过阈值过滤（|score|≥0.50）的新闻事件；positions 为当前持仓，watchlist 为用户自选。
func (e *Engine) Evaluate(ctx context.Context, events []newsagent.NewsEvent, positions, watchlist []string) *StrategyResult {
	t0 := time.Now()
	if len(events) == 0 {
		log.Printf("[strategy_engine] 无事件，仅收拢 持仓+自选 打分池")
	} else {
		log.Printf("[strategy_engine] Evaluate 开始")
	}

	// 1. attribution: 事件 → 板块/个股分流
	bullSectors, bearSectors := e.attribution(events)
	log.Printf("[strategy_engine] attribution: %d利好板块 %d利空板块", len(bullSectors), len(bearSectors))

	e.rebuildIndex(events)

	// 2. 分流个股事件到 LongStocks / ShortStocks（按带符号 Score 判定方向）
	var longStocks, shortStocks []IndividualStock
	for _, ev := range events {
		if ev.Level != "个股" || len(ev.CleanedStocks) == 0 {
			continue
		}
		// 方向判定：Score>0 利好进做多池，Score<0 利空进做空池，Score=0（中性）跳过
		isLong := ev.Score > 0
		isShort := ev.Score < 0
		if !isLong && !isShort {
			continue
		}
		// CleanedStocks 元素形如 "名称|代码"，拆分后规范化代码（去 SH/SZ 前后缀）
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

	// 3. 收拢打分池：Stage2 个股 + 持仓 + 自选（Set 去重）
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
	// 从 Set 还原为无序切片，供后续统一拉行情与打分
	scoringPool := make([]string, 0, len(poolSet))
	for code := range poolSet {
		scoringPool = append(scoringPool, code)
	}
	log.Printf("[strategy_engine] 打分池收拢: %d只个股 (Stage2=%d 持仓=%d 自选=%d)",
		len(scoringPool), len(longStocks)+len(shortStocks), len(positions), len(watchlist))

	// 4. 获取行情数据（KLine + 实时价 + 资金流向）
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

// fetchMarketData 为打分池所有个股拉取行情数据（实时价 + KLine + 资金流向）。
// 实时行情降级链路：新浪批量 CSV（一次网络请求拉全池）→ 同花顺单查 → 东财单查。
// K线/资金流并发拉取（每只2次请求）。
func (e *Engine) fetchMarketData(ctx context.Context, codes []string) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		result[code] = &StockMarketData{Code: code}
	}

	// 1. 批量实时行情（新浪 CSV 单次请求，全池一次拉完）
	sinaQuotes := e.marketAPI.GetSinaQuotes(codes)
	for code, si := range sinaQuotes {
		if md, ok := result[code]; ok && si != nil && si.Price > 0 {
			md.Name = si.Name
			md.Price = si.Price
			md.ChangePct = si.ChangePct
			md.Quote = si
		}
	}
	// 2. 兜底：批量未命中的个股先走同花顺，仍失败再东财单查
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
		// 最后一层兜底：东财实时报价；仍失败则记录 Error 供上层排查
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

	// 2. K线 + 资金流向 + 分钟级量价/MACD：并发拉取（限流由 data 层 limiter 保证）
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for code, md := range result {
		wg.Add(1)
		sem <- struct{}{}
		go func(code string, md *StockMarketData) {
			defer wg.Done()
			defer func() { <-sem }()

			// 日K：优先新浪，失败降级东财（101=日线）
			klines, err := e.marketAPI.GetSinaKLine(code, 120)
			if err == nil && len(klines) > 0 {
				md.KLines = klines
			} else {
				k2, err2 := e.marketAPI.GetKLine(code, "101", 120)
				if err2 == nil && len(k2) > 0 {
					md.KLines = k2
				}
			}

			// 资金流向（主力净流入，供资金维度评分）
			cf, err := e.marketAPI.GetStockMoneyFlow(code)
			if err == nil && cf != nil {
				md.MoneyFlow = cf
			}

			// 分钟K线（5分钟，48根≈当日）→ 计算 MACD，供 8a/8b 动量分与 N 形评分使用
			minKL, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48)
			if err == nil && len(minKL) >= 2 {
				md.MinuteKLine = minKL
				md.MinuteMACD = data.CalcMACD(minKL)
			}
		}(code, md)
	}
	wg.Wait()

	return result
}

// BuildScoringData 为近实时 8a/8b 打分循环构建行情数据（5s 节奏）。
// - 实时量价优先取外部快照 quotes（data.Fetcher 5s 采集：新浪→同花顺→东财），缺失的走本引擎降级链补齐；
// - 日K + 资金流走进程内缓存（TTL 5 分钟，交易日内基本不变）；
// - 分钟K线（MACD）每轮现拉，保证动量/N 形评分的实时性。
func (e *Engine) BuildScoringData(ctx context.Context, codes []string, quotes map[string]*data.StockInfo) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		result[code] = &StockMarketData{Code: code}
	}

	// 1. 实时量价：外部快照优先，缺失走 新浪批量→同花顺→东财 兜底
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

	// 2. 日K + 资金流：走缓存（TTL 5min）；分钟K线现拉（并发，限流由 data 层保证）
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for code, md := range result {
		wg.Add(1)
		sem <- struct{}{}
		go func(code string, md *StockMarketData) {
			defer wg.Done()
			defer func() { <-sem }()
			md.KLines, md.MoneyFlow = e.cachedKLine(code)
			minKL, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48)
			if err == nil && len(minKL) >= 2 {
				md.MinuteKLine = minKL
				md.MinuteMACD = data.CalcMACD(minKL)
			}
		}(code, md)
	}
	wg.Wait()

	log.Printf("[strategy_engine] BuildScoringData: %d只 (快照quote=%d 兜底=%d)", len(codes), len(codes)-len(missing), len(missing))
	return result
}

// fetchQuotes 只拉实时行情（降级链：新浪批量→同花顺→东财），用于快照缺失的个股。
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
func (e *Engine) cachedKLine(code string) ([]data.KLine, *data.CapitalFlow) {
	now := time.Now()
	e.klineCacheMu.RLock()
	ent, ok := e.klineCache[code]
	e.klineCacheMu.RUnlock()
	if ok && now.Sub(ent.fetchedAt) < 5*time.Minute && len(ent.klines) > 0 {
		return ent.klines, ent.moneyFlow
	}

	klines, err := e.marketAPI.GetSinaKLine(code, 120)
	if err != nil || len(klines) == 0 {
		if k2, err2 := e.marketAPI.GetKLine(code, "101", 120); err2 == nil && len(k2) > 0 {
			klines = k2
		}
	}
	// 日K两条数据源均失败时做一次瞬时重试，避免网络抖动导致自选/持仓持续无K线（0分误导）
	if len(klines) == 0 {
		time.Sleep(50 * time.Millisecond)
		klines, err = e.marketAPI.GetSinaKLine(code, 120)
		if err == nil && len(klines) == 0 {
			if k2, err2 := e.marketAPI.GetKLine(code, "101", 120); err2 == nil && len(k2) > 0 {
				klines = k2
			}
		}
	}
	cf, err := e.marketAPI.GetStockMoneyFlow(code)
	if err != nil {
		cf = nil
	}

	e.klineCacheMu.Lock()
	e.klineCache[code] = &klineCacheEntry{klines: klines, moneyFlow: cf, fetchedAt: now}
	e.klineCacheMu.Unlock()
	return klines, cf
}

// attribution 事件归因：将新闻事件按利好/利空方向分流到板块，合并相同板块的事件。
// 仅取 ev.Sectors 一级板块（上游/下游不参与热点归因）；板块名须能匹配真实同花顺板块，否则丢弃。
// 返回利好板块和利空板块列表。
func (e *Engine) attribution(events []newsagent.NewsEvent) (bull, bear []SectorHot) {
	bullMap := make(map[string]*SectorHot)
	bearMap := make(map[string]*SectorHot)

	for _, ev := range events {
		if ev.Level == "个股" {
			continue
		}

		// 按 Score 符号决定事件归属的板块池：负分进利空池，否则进利好池
		isBear := ev.Score < 0

		for _, sec := range ev.Sectors {
			if sec == "" {
				continue
			}
			m := bullMap
			if isBear {
				m = bearMap
			}
			// 同一板块的多次事件合并：累计新闻标题，保留 |score| 最大的一次事件属性
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

// absScore 取评分的绝对值。
func absScore(s float64) float64 {
	if s < 0 {
		return -s
	}
	return s
}

// collectBearStocks 从利空板块中收集个股代码，去重后返回。
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
