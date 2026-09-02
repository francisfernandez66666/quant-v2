// 失败战法聚类（§Phase2 失败战法聚类）：把扫参/回测中表现差（失败）的战法按失败原因
// 规则聚类分组，输出每簇的成因标注与聚合统计，供改进方向参考（如统一放宽止损/缩短持仓等）。
// 数据源：optimization_results（含 win_rate/profit_factor/expectancy/trigger_count/max_drawdown_pct）。
// English: failed-strategy clustering (Phase 2). Groups underperforming strategies from sweep/backtest
// results by rule-based failure cause, producing per-cluster annotations and aggregate stats to guide
// improvement (e.g. uniformly widening stops or shortening holding periods).
package research

import (
	"sort"

	"quant-trading-v2/internal/store"
)

// FailureClusterID 失败原因簇 ID。
type FailureClusterID string

// 失败原因簇定义（判定阈值见 ClusterFailures）。
const (
	// 样本不足：触发次数太少，指标不可信（不判定具体成因）。
	ClusterInsufficient FailureClusterID = "sample_insufficient"
	// 盈亏比倒挂：profit_factor < 1（亏多赢少，即使胜率尚可整体仍亏）。
	ClusterAdversePayoff FailureClusterID = "adverse_payoff"
	// 低胜率：胜率过低，命中质量差。
	ClusterLowWinRate FailureClusterID = "low_win_rate"
	// 回撤过大：最大回撤超限，风险不可控。
	ClusterHighDrawdown FailureClusterID = "high_drawdown"
	// 期望为负但未命中上述具体成因（综合失败）。
	ClusterGeneral FailureClusterID = "general_failure"
)

// failureClusterMeta 各簇的中文名与改进建议。
// English: Chinese name and improvement advice for each cluster.
var failureClusterMeta = map[FailureClusterID]struct{ Name, Advice string }{
	ClusterInsufficient:  {"样本不足", "触发次数过少、指标不可信；建议扩大研究池/拉长回测区间或放宽触发阈值后重测"},
	ClusterAdversePayoff: {"盈亏比倒挂", "亏多赢少；建议抬升止盈/加大止损幅度、或缩短持仓让赢面尽早兑现"},
	ClusterLowWinRate:    {"低胜率", "命中质量差；建议收紧触发条件（提高 min_score/买点阈值）或更换因子组合"},
	ClusterHighDrawdown:  {"回撤过大", "风险不可控；建议加宽止损、降低仓位杠杆或缩短持仓天数"},
	ClusterGeneral:       {"综合失败", "多项指标不达标；建议从止损、持仓、触发三个维度整体重扫"},
}

// ClusterName 返回失败原因簇中文名。
func (id FailureClusterID) ClusterName() string {
	if m, ok := failureClusterMeta[id]; ok {
		return m.Name
	}
	return string(id)
}

// ClusterAdvice 返回失败原因簇改进建议。
func (id FailureClusterID) ClusterAdvice() string {
	if m, ok := failureClusterMeta[id]; ok {
		return m.Advice
	}
	return ""
}

// FailureItem 一条失败战法的聚类标注。
type FailureItem struct {
	OptID          int64            `json:"opt_id"`           // optimization_results 主键
	TaskID         int64            `json:"task_id"`          // 扫参任务 ID
	Strategy       string           `json:"strategy"`         // 战法显示名
	Cluster        FailureClusterID `json:"cluster"`          // 失败原因簇
	WinRate        float64          `json:"win_rate"`         // 胜率
	ProfitFactor   float64          `json:"profit_factor"`    // 盈亏比
	Expectancy     float64          `json:"expectancy"`       // 期望收益
	TriggerCount   int              `json:"trigger_count"`    // 触发次数
	MaxDrawdownPct float64          `json:"max_drawdown_pct"` // 最大回撤%
}

// FailureClusterStats 一个失败原因簇的聚合统计。
type FailureClusterStats struct {
	Cluster       FailureClusterID `json:"cluster"`
	Count         int              `json:"count"`             // 战法数
	AvgWinRate    float64          `json:"avg_win_rate"`      // 平均胜率
	AvgPF         float64          `json:"avg_profit_factor"` // 平均盈亏比
	AvgExpectancy float64          `json:"avg_expectancy"`    // 平均期望
	AvgDrawdown   float64          `json:"avg_drawdown_pct"`  // 平均最大回撤%
}

// minTriggers / lowWinRate / maxDrawdown 聚类判定阈值。
const (
	minTriggerCount  = 15 // 触发次数下限，低于视为样本不足
	lowWinRatePct    = 40 // 胜率下限
	maxDrawdownLimit = 20 // 最大回撤上限（%）
)

// classifyFailure 对单条优化结果判定失败原因簇。非失败（expectancy >= 0 且 PF >= 1）返回空。
// English: classifies a single optimization result into a failure-cause cluster. Returns "" for
// non-failures (expectancy >= 0 and profit factor >= 1).
func classifyFailure(r *store.OptimizationResult) FailureClusterID {
	if r.Expectancy >= 0 && r.ProfitFactor >= 1 {
		return ""
	}
	if r.TriggerCount < minTriggerCount {
		return ClusterInsufficient
	}
	if r.ProfitFactor < 1 {
		return ClusterAdversePayoff
	}
	if r.WinRate < lowWinRatePct {
		return ClusterLowWinRate
	}
	if r.MaxDrawdownPct > maxDrawdownLimit {
		return ClusterHighDrawdown
	}
	return ClusterGeneral
}

// ClusterFailures 对一批优化结果做失败聚类：
//   - 仅统计失败战法（期望为负或盈亏比<1）；
//   - 逐条判定失败原因簇；
//   - 返回（按簇聚合统计、带簇标注的失败明细）。
//
// English: ClusterFailures clusters a batch of optimization results — only failing strategies
// (negative expectancy or PF<1) are considered — and returns cluster-level aggregates plus a
// per-item failure annotation list.
func ClusterFailures(results []*store.OptimizationResult) ([]*FailureClusterStats, []*FailureItem) {
	var items []*FailureItem
	agg := map[FailureClusterID]*FailureClusterStats{}
	for _, r := range results {
		cl := classifyFailure(r)
		if cl == "" {
			continue
		}
		items = append(items, &FailureItem{
			OptID: r.ID, TaskID: r.TaskID, Strategy: r.Strategy,
			Cluster: cl, WinRate: r.WinRate, ProfitFactor: r.ProfitFactor,
			Expectancy: r.Expectancy, TriggerCount: r.TriggerCount,
			MaxDrawdownPct: r.MaxDrawdownPct,
		})
		s := agg[cl]
		if s == nil {
			s = &FailureClusterStats{Cluster: cl}
			agg[cl] = s
		}
		s.Count++
		s.AvgWinRate += r.WinRate
		s.AvgPF += r.ProfitFactor
		s.AvgExpectancy += r.Expectancy
		s.AvgDrawdown += r.MaxDrawdownPct
	}
	var stats []*FailureClusterStats
	for _, s := range agg {
		n := float64(s.Count)
		s.AvgWinRate /= n
		s.AvgPF /= n
		s.AvgExpectancy /= n
		s.AvgDrawdown /= n
		stats = append(stats, s)
	}
	// 按战法数降序
	sort.Slice(stats, func(i, j int) bool { return stats[i].Count > stats[j].Count })
	return stats, items
}
