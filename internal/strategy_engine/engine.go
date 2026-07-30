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
	mu             sync.RWMutex             // 读写锁
	marketAPI      *data.MarketAPI          // 行情 API
	scanner        *data.SectorScanner      // 板块扫描器
	stockSectorIdx map[string][]string      // 个股→板块倒排索引
}

// New 创建策略引擎实例。
func New(marketAPI *data.MarketAPI) *Engine {
	return &Engine{
		marketAPI:      marketAPI,
		stockSectorIdx: make(map[string][]string),
	}
}

// SetScanner 设置板块扫描器（线程安全）。
func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.mu.Unlock()
}

// Evaluate 策略评估入口：归因事件→分流个股→收拢评分池→获取行情数据→返回策略结果。
// positions 为当前持仓，watchlist 为用户自选，均纳入评分池。
func (e *Engine) Evaluate(ctx context.Context, result *newsagent.AnalysisResult, positions, watchlist []string) *StrategyResult {
	t0 := time.Now()
	log.Printf("[strategy_engine] Evaluate 开始")

	if result == nil || len(result.Events) == 0 {
		log.Printf("[strategy_engine] 无事件，返回空结果")
		return &StrategyResult{}
	}

	// 1. attribution: 事件 → 板块/个股分流
	bullSectors, bearSectors := e.attribution(result.Events)
	log.Printf("[strategy_engine] attribution: %d利好板块 %d利空板块", len(bullSectors), len(bearSectors))

	e.rebuildIndex(result.Events)

	// 2. 分流个股事件到 LongStocks / ShortStocks
	var longStocks, shortStocks []IndividualStock
	for _, ev := range result.Events {
		if ev.Level != "个股" || len(ev.CleanedStocks) == 0 {
			continue
		}
		dir := strings.TrimSpace(ev.Direction)
		isLong := strings.HasPrefix(dir, "利好")
		isShort := strings.HasPrefix(dir, "利空")
		if !isLong && !isShort {
			continue
		}
		for _, cs := range ev.CleanedStocks {
			parts := strings.SplitN(cs, "|", 2)
			if len(parts) != 2 {
				continue
			}
			code := normalizeCode(parts[1])
			ist := IndividualStock{
				Code:      code,
				Name:      parts[0],
				Direction: dir,
			}
			if isLong {
				longStocks = append(longStocks, ist)
			} else {
				shortStocks = append(shortStocks, ist)
			}
		}
	}
	log.Printf("[strategy_engine] 个股分流: %d利好 %d利空", len(longStocks), len(shortStocks))

	// 3. 收拢打分池：Stage2 个股 + 持仓 + 自选
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
	}
}

// fetchMarketData 为打分池所有个股拉取行情数据（KLine + 实时价 + 资金流向）。
func (e *Engine) fetchMarketData(ctx context.Context, codes []string) map[string]*StockMarketData {
	result := make(map[string]*StockMarketData, len(codes))
	for _, code := range codes {
		md := &StockMarketData{Code: code}

		// 实时行情
		si, err := e.marketAPI.GetSinaQuote(code)
		if err != nil || si == nil || si.Price <= 0 {
			si, err = e.marketAPI.GetRealtimeQuote(code)
		}
		if err != nil || si == nil {
			md.Error = "行情获取失败: " + err.Error()
			log.Printf("[strategy_engine] 行情失败 %s: %v", code, err)
		} else {
			md.Name = si.Name
			md.Price = si.Price
			md.ChangePct = si.ChangePct
			log.Printf("[strategy_engine] 行情 %s (%s) 价=%.2f 涨跌=%.2f%%", code, si.Name, si.Price, si.ChangePct)
		}

		// K 线
		klines, err := e.marketAPI.GetSinaKLine(code, 120)
		if err == nil && len(klines) > 0 {
			md.KLines = klines
		} else {
			klines, err = e.marketAPI.GetKLine(code, "101", 120)
			if err == nil && len(klines) > 0 {
				md.KLines = klines
			}
		}
		if len(md.KLines) == 0 {
			log.Printf("[strategy_engine] K线失败 %s: %v", code, err)
		} else {
			log.Printf("[strategy_engine] K线 %s: %d条", code, len(md.KLines))
		}

		// 资金流向
		cf, err := e.marketAPI.GetStockMoneyFlow(code)
		if err == nil && cf != nil {
			md.MoneyFlow = cf
		} else {
			log.Printf("[strategy_engine] 资金流失败 %s: %v", code, err)
		}

		result[code] = md
	}
	return result
}

// attribution 事件归因：将新闻事件按利好/利空方向分流到板块，合并相同板块的事件。
// 返回利好板块和利空板块列表。
func (e *Engine) attribution(events []newsagent.NewsEvent) (bull, bear []SectorHot) {
	bullMap := make(map[string]*SectorHot)
	bearMap := make(map[string]*SectorHot)

	for _, ev := range events {
		if ev.Level == "个股" {
			continue
		}
		allSectors := append([]string{}, ev.Sectors...)
		allSectors = append(allSectors, ev.UpstreamSectors...)
		allSectors = append(allSectors, ev.DownstreamSectors...)

		dir := strings.TrimSpace(ev.Direction)
		isBear := strings.HasPrefix(dir, "利空")

		for _, sec := range allSectors {
			if sec == "" {
				continue
			}
			m := bullMap
			if isBear {
				m = bearMap
			}
			if existing, ok := m[sec]; ok {
				existing.NewsTitles = append(existing.NewsTitles, ev.Title)
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
func enrichSectorData(sectors map[string]*SectorHot, scanner *data.SectorScanner) {
	if scanner == nil {
		return
	}
	for name, sh := range sectors {
		infos := scanner.FindSectorsByNames([]string{name})
		if len(infos) > 0 {
			sh.ChangePct = infos[0].ChangePct
			sh.LimitupCnt = infos[0].LimitupCnt
			sh.NetInflow = infos[0].NetInflow
		}
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
