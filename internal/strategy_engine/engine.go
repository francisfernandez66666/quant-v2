package strategy_engine

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
)

type Engine struct {
	mu           sync.RWMutex
	marketAPI    *data.MarketAPI
	llmClient    *llm.Client
	scanner      *data.SectorScanner

	stockSectorIdx map[string][]string // code → [板块名] 倒排索引
	l1Score        map[string]float64  // code → D1评分
	l1Blocked      map[string]bool     // code → 利空阻塞
	l1Gen          int64
}

func New(marketAPI *data.MarketAPI, llmClient *llm.Client) *Engine {
	return &Engine{
		marketAPI:      marketAPI,
		llmClient:      llmClient,
		stockSectorIdx: make(map[string][]string),
		l1Score:        make(map[string]float64),
		l1Blocked:      make(map[string]bool),
	}
}

func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.mu.Unlock()
}

// Evaluate 是策略引擎唯一入口：归因+产业链+评分
func (e *Engine) Evaluate(ctx context.Context, result *newsagent.AnalysisResult) *StrategyResult {
	t0 := time.Now()

	if result == nil || len(result.Events) == 0 {
		return &StrategyResult{}
	}

	// 1. 归因：利好/利空分类
	bullEvents, bearEvents := e.attribution(result.Events)

	// 2. 产业链LLM推断（只对利好事件）
	chainSectors := e.chainInference(ctx, bullEvents)

	// 3. 重建个股倒排索引
	e.rebuildIndex(bullEvents, bearEvents)

	// 4. D1评分矩阵
	e.rebuildD1Scores(result.Events)

	// 5. 组装热点板块
	hotSectors := e.buildHotSectors(bullEvents, chainSectors)
	bearSectors := e.buildBearSectors(bearEvents)

	log.Printf("[strategy_engine] Evaluate完成: %d利好 %d利空 %d热点板块 (%v)",
		len(bullEvents), len(bearEvents), len(hotSectors), time.Since(t0))

	blocked := make(map[string]bool)
	e.mu.RLock()
	for k, v := range e.l1Blocked {
		blocked[k] = v
	}
	e.mu.RUnlock()

	return &StrategyResult{
		HotSectors:  hotSectors,
		BearSectors: bearSectors,
		BearStocks:  e.collectBearStocks(bearSectors),
		L1Score:     e.l1Score,
		L1Blocked:   blocked,
		Events:      result.Events,
	}
}

func (e *Engine) attribution(events []newsagent.NewsEvent) (bull, bear []newsagent.NewsEvent) {
	for _, ev := range events {
		dir := strings.TrimSpace(ev.Direction)
		switch {
		case strings.HasPrefix(dir, "利空"):
			bear = append(bear, ev)
		case strings.HasPrefix(dir, "利好"):
			bull = append(bull, ev)
		default:
			bull = append(bull, ev)
		}
	}
	return
}

func (e *Engine) buildHotSectors(bullEvents []newsagent.NewsEvent, chainSectors []SectorHot) []SectorHot {
	sectorMap := make(map[string]*SectorHot)
	for _, ev := range bullEvents {
		for _, s := range ev.Sectors {
			if _, ok := sectorMap[s]; !ok {
				sectorMap[s] = &SectorHot{
					Name:      s,
					Direction: "利好",
					Score:     ev.Score,
					Reason:    ev.Reason,
				}
			}
		}
	}
	for _, cs := range chainSectors {
		if existing, ok := sectorMap[cs.Name]; ok {
			if cs.Score > existing.Score {
				existing.Score = cs.Score
			}
			if existing.Reason == "" {
				existing.Reason = cs.Reason
			}
		} else {
			sectorMap[cs.Name] = &SectorHot{
				Name:      cs.Name,
				Direction: "利好",
				Score:     cs.Score,
				Reason:    cs.Reason,
				LeadStocks: cs.LeadStocks,
			}
		}
	}
	result := make([]SectorHot, 0, len(sectorMap))
	for _, s := range sectorMap {
		result = append(result, *s)
	}
	return result
}

func (e *Engine) buildBearSectors(bearEvents []newsagent.NewsEvent) []SectorHot {
	sectorMap := make(map[string]*SectorHot)
	for _, ev := range bearEvents {
		for _, s := range ev.Sectors {
			if _, ok := sectorMap[s]; !ok {
				sectorMap[s] = &SectorHot{
					Name:      s,
					Direction: "利空",
					Score:     ev.Score,
					Reason:    ev.Reason,
				}
			}
		}
	}
	result := make([]SectorHot, 0, len(sectorMap))
	for _, s := range sectorMap {
		result = append(result, *s)
	}
	return result
}

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
