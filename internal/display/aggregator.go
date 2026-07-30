// Package display 看板数据聚合器：将战法引擎、策略引擎、板块验证等多路数据合并为统一的看板输出。
package display

import (
	"sort"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// DashboardData 看板数据，汇总所有模块的最新结果用于前端展示。
type DashboardData struct {
	NewsEvents     []newsagent.NewsEvent          `json:"news_events"`
	HotSectors     []strategy_engine.SectorHot    `json:"hot_sectors"`
	BearSectors    []strategy_engine.SectorHot    `json:"bear_sectors,omitempty"`
	BearStocks     []string                       `json:"bear_stocks,omitempty"`
	VerifiedBull   []sector_agent.VerifiedSector  `json:"verified_bull,omitempty"`
	VerifiedBear   []sector_agent.VerifiedSector  `json:"verified_bear,omitempty"`
	BullSignals    []combat_agent.Signal           `json:"bull_signals,omitempty"`
	BearSignals    []combat_agent.Signal           `json:"bear_signals,omitempty"`
	AlertSignals   []combat_agent.Signal           `json:"alert_signals,omitempty"`
	FinalSignals   []combat_agent.Signal           `json:"final_signals,omitempty"`
	L1Score        map[string]float64              `json:"l1_score,omitempty"`
	L1Blocked      map[string]bool                 `json:"l1_blocked,omitempty"`
	Report         *report.Report                  `json:"-"`
}

// Aggregator 看板数据聚合器，持有最新的 DashboardData 快照。
type Aggregator struct {
	current *DashboardData // 当前看板数据快照
}

// New 创建看板数据聚合器。
func New() *Aggregator {
	return &Aggregator{}
}

// Update 更新看板数据：聚合策略结果、板块验证、做多/做空/提醒信号，完成冲突裁决后生成最终信号。
func (a *Aggregator) Update(
	result *strategy_engine.StrategyResult,
	verifiedBull, verifiedBear []sector_agent.VerifiedSector,
	bullSignals, bearSignals, alertSignals []combat_agent.Signal,
	rpt *report.Report,
) *DashboardData {
	finalSignals := resolveConflict(bullSignals, bearSignals, alertSignals, result.L1Blocked)

	a.current = &DashboardData{
		NewsEvents:   result.Events,
		HotSectors:   result.HotSectors,
		BearSectors:  result.BearSectors,
		BearStocks:   result.BearStocks,
		VerifiedBull: verifiedBull,
		VerifiedBear: verifiedBear,
		BullSignals:  bullSignals,
		BearSignals:  bearSignals,
		AlertSignals: alertSignals,
		FinalSignals: finalSignals,
		L1Score:      result.L1Score,
		L1Blocked:    result.L1Blocked,
		Report:       rpt,
	}
	return a.current
}

// Current 返回当前看板数据快照。
func (a *Aggregator) Current() *DashboardData {
	return a.current
}

// resolveConflict 信号冲突裁决：合并做多/做空/提醒三类信号，按置信度去重排序。
// 被 blocked 的股票直接排除，相同股票取最新生成的信号。
func resolveConflict(bull, bear, alerts []combat_agent.Signal, blocked map[string]bool) []combat_agent.Signal {
	strategyCodes := make(map[string]bool)
	for _, s := range bull {
		if strategy.IsActionWatchOrAbove(s.Action) {
			strategyCodes[s.Code] = true
		}
	}
	for _, s := range bear {
		if strategy.IsActionWatchOrAbove(s.Action) {
			strategyCodes[s.Code] = true
		}
	}

	all := append(bull, bear...)
	for _, s := range alerts {
		if strategyCodes[s.Code] {
			continue
		}
		all = append(all, s)
	}

	seen := make(map[string]*combat_agent.Signal)
	sort.Slice(all, func(i, j int) bool {
		return all[i].Confidence > all[j].Confidence
	})
	for _, s := range all {
		if blocked[s.Code] {
			continue
		}
		if existing, ok := seen[s.Code]; ok {
			if s.GeneratedAt.After(existing.GeneratedAt) {
				seen[s.Code] = &s
			}
		} else {
			seen[s.Code] = &s
		}
	}
	result := make([]combat_agent.Signal, 0, len(seen))
	for _, s := range seen {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Confidence > result[j].Confidence
	})
	return result
}
