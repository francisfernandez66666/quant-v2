// Package display 看板数据聚合器：将战法引擎、策略引擎、板块验证等多路数据合并为统一的看板输出。
package display

import (
	"sort"
	"sync"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// DashboardData 看板数据，汇总所有模块的最新结果用于前端展示。
type DashboardData struct {
	NewsEvents   []newsagent.NewsEvent               `json:"news_events"`             // 新闻事件（事件归因产物）
	HotSectors   []strategy_engine.SectorHot         `json:"hot_sectors"`             // 热点/利好板块
	BearSectors  []strategy_engine.SectorHot         `json:"bear_sectors,omitempty"`  // 利空板块
	BearStocks   []string                            `json:"bear_stocks,omitempty"`   // 利空个股代码列表
	VerifiedBull []sector_agent.VerifiedSector       `json:"verified_bull,omitempty"` // 已通过板块验证的利好板块
	VerifiedBear []sector_agent.VerifiedSector       `json:"verified_bear,omitempty"` // 已通过板块验证的利空板块
	BullSignals  []combat_agent.Signal               `json:"bull_signals,omitempty"`  // 做多信号
	BearSignals  []combat_agent.Signal               `json:"bear_signals,omitempty"`  // 做空信号
	AlertSignals []combat_agent.Signal               `json:"alert_signals,omitempty"` // 提醒信号（止盈/止损等）
	FinalSignals []combat_agent.Signal               `json:"final_signals,omitempty"` // 冲突裁决后的最终信号
	Scores       map[string]combat_agent.StockScores `json:"scores,omitempty"`        // 8a/8b 持续打分（自选/持仓）
	L1Score      map[string]float64                  `json:"l1_score,omitempty"`      // L1 评分（按股票代码）
	L1Blocked    map[string]bool                     `json:"l1_blocked,omitempty"`    // L1 阻断标记（按股票代码）
	Report       *report.Report                      `json:"-"`                       // 交易报表引用（不参与 JSON 序列化）
}

// Aggregator 看板数据聚合器，持有最新的 DashboardData 快照。
// 5min 主循环与近实时打分循环会并发更新快照，内部用 RWMutex 保护。
type Aggregator struct {
	mu      sync.RWMutex   // 保护 current 快照的读写锁
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
	scores map[string]combat_agent.StockScores,
	rpt *report.Report,
) *DashboardData {
	finalSignals := resolveConflict(bullSignals, bearSignals, alertSignals, result.L1Blocked)

	a.mu.Lock()
	defer a.mu.Unlock()
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
		Scores:       scores,
		L1Score:      result.L1Score,
		L1Blocked:    result.L1Blocked,
		Report:       rpt,
	}
	return a.current
}

// UpdateFast 近实时打分循环专用更新：只刷新 Scores 并把近实时信号并入最终信号，
// 保留主循环产生的新闻/板块/验证数据不动（并发安全）。
func (a *Aggregator) UpdateFast(scores map[string]combat_agent.StockScores, fastSignals []combat_agent.Signal, rpt *report.Report) *DashboardData {
	a.mu.Lock()
	defer a.mu.Unlock()

	cur := a.current
	if cur == nil {
		cur = &DashboardData{Report: rpt}
	}
	bull := append([]combat_agent.Signal{}, cur.BullSignals...)
	bull = append(bull, fastSignals...)
	final := resolveConflict(bull, cur.BearSignals, cur.AlertSignals, cur.L1Blocked)

	a.current = &DashboardData{
		NewsEvents:   cur.NewsEvents,
		HotSectors:   cur.HotSectors,
		BearSectors:  cur.BearSectors,
		BearStocks:   cur.BearStocks,
		VerifiedBull: cur.VerifiedBull,
		VerifiedBear: cur.VerifiedBear,
		BullSignals:  bull,
		BearSignals:  cur.BearSignals,
		AlertSignals: cur.AlertSignals,
		FinalSignals: final,
		Scores:       scores,
		L1Score:      cur.L1Score,
		L1Blocked:    cur.L1Blocked,
		Report:       rpt,
	}
	return a.current
}

// Current 返回当前看板数据快照。
func (a *Aggregator) Current() *DashboardData {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current
}

// resolveConflict 信号冲突裁决：合并做多/做空/提醒三类信号，按置信度去重排序。
// 被 blocked 的股票直接排除，相同股票取最新生成的信号。
func resolveConflict(bull, bear, alerts []combat_agent.Signal, blocked map[string]bool) []combat_agent.Signal {
	// 收集已有策略信号（做多/做空）覆盖的股票代码，
	// 提醒类信号不覆盖已存在策略信号的标的（策略信号优先展示）。
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

	// 做多 + 做空 全量并入，提醒信号仅在无策略信号时补入
	all := append(bull, bear...)
	for _, s := range alerts {
		if strategyCodes[s.Code] {
			continue
		}
		all = append(all, s)
	}

	// 先按置信度降序排序，再按代码去重：
	// 同代码出现多次时保留生成时间最新的信号；blocked 的股票直接剔除。
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
	// 最终结果再次按置信度降序排序，保证前端展示顺序稳定
	sort.Slice(result, func(i, j int) bool {
		return result[i].Confidence > result[j].Confidence
	})
	return result
}
