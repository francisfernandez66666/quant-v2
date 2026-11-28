// Package research 因子研究库（纯离线，供研究 CLI / B4 全链路回测 / B5 优化器调用）。
//
// 核心对象是 Panel（单只股票的因子面板）与 StockSeries：基于研究 SQLite 库装配行情与因子值，
// 在其上做横截面分析——IC（信息系数）与分层回测（LayerReturns/Monotonic）、因子发现
// （DiscoverFactors，按样本内方向拟合选股）、去重聚类（Dedup*）、稳健性（Robust*）与
// 合成复合 IC（CompositeIC）。另含：
//   - 战法/因子规则的落地与统计（apply.go：ApplyFactorRule/ApplyPatternRule/UpdateAppliedFactorStats）；
//   - 灰度发布与晋级（grayscale.go + lifecycle*.go：EvaluateGrayscale）；
//   - 事件因子（event_factor.go）、情绪相位（emotion_phase.go）、事件冲击表（impact.go）；
//   - 失败聚类诊断（failure_cluster.go）、窗口分块装配（windowed.go / WindowChunks）、版本管理（versioning.go）；
//   - B5 参数优化（optimizer.go）。
//
// 全部函数无副作用（除落库类 Apply*），输入为面板/序列，输出为指标，便于单测与复算。
// English: the factor-research library — panel assembly + cross-sectional IC / layering / discovery /
// dedup / robustness, plus rule persistence, grayscale promotion, event & emotion factors, impact
// tables, failure clustering, windowed assembly and the B5 optimizer.
package research

import (
	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// Panel 单只股票的因子面板。
// English: Panel holds one stock's factor values aligned to its trade dates.
// （Panel holds one stock's factor values aligned to its trade dates.）
type Panel struct {
	Code    string              // 股票代码
	Series  *factor.StockSeries // 该股的价格/成交量序列
	DateIdx map[string]int      // 日期 → 序列下标
	// English: date -> series index.
	Factors map[string][]float64 // factorID → 与 Dates 对齐的因子值
	// English: factorID -> factor values aligned with Dates.
}

// BuildPanel 装配单只股票并计算指定因子的值。
//
// ★ 必须带市场级面板（owner 2026-11-28：「不能接线因子做完了，不拿去回测挖掘新战法」）。
// 不带面板时 IndexChange / FlowToday / Flow5d / FlowCapToday / FlowCap5d / SectorFlowQuantile /
// SectorGainRank 七列**结构性全 NaN**，于是自动研究（discover-factors 的单因子预筛与贪心选择）
// 永远看不见它们——七枚因子在册、在打分、却一条新战法都挖不出来，而读数上一切正常
// （预筛只是"没进候选池"，不会报错）。批量装配请走 BuildPanels：那里只载一次面板，
// 这里单票版自己载一次，避免 4000 只票各读一遍 D1 / 板块资金流。
//
// English: BuildPanel assembles one stock and computes the given factors.
// It must carry the market-level panels, otherwise the seven §REPLAY-INPUTS columns are all NaN
// and automated discovery can never select them (no error is raised — the factors simply never
// enter the candidate pool).
func BuildPanel(db *store.DB, code, start, end string, defs []factor.Def) (*Panel, error) {
	return BuildPanelWithMarket(db, code, start, end, defs, LoadPanels(db, start, end))
}

// BuildPanelWithMarket 同上，但复用调用方已装配好的市场级面板（批量装配的唯一正确姿势）。
// English: same as BuildPanel but reuses the caller's market panels (the only correct batch shape).
func BuildPanelWithMarket(db *store.DB, code, start, end string, defs []factor.Def, mp *Panels) (*Panel, error) {
	series, err := AssembleWithPanels(db, code, start, end, mp)
	if err != nil {
		return nil, err
	}
	p := &Panel{
		Code:    code,
		Series:  series,
		DateIdx: make(map[string]int, len(series.Dates)),
		Factors: make(map[string][]float64, len(defs)),
	}
	for i, d := range series.Dates {
		p.DateIdx[d] = i
	}
	for _, d := range defs {
		p.Factors[d.ID] = d.Compute(series)
	}
	return p, nil
}

// BuildPanels 装配一批股票的因子面板（股票无行情时跳过并记录）。
//
// 市场级面板在这里**只载一次**再逐票复用：这是"新因子真的进了研究"的唯一入口。
// 逐票各载一次会把 D1 与板块资金流读 4000 遍（2 核 4G / researchd 500M 配额下直接拖死），
// 而不载则七枚新因子全 NaN、自动研究永远挖不到它们（见 BuildPanel 注释）。
//
// English: BuildPanels assembles panels for many stocks, skipping those without data.
// The market panels are loaded exactly once and reused for every stock — this is the only place
// that makes the §REPLAY-INPUTS factors visible to automated discovery.
func BuildPanels(db *store.DB, codes []string, start, end string, defs []factor.Def) ([]*Panel, error) {
	return BuildPanelsWithMarket(db, codes, start, end, defs, LoadPanels(db, start, end))
}

// BuildPanelsWithMarket 批量装配并复用调用方已装配好的市场级面板。
// English: batch assembly reusing the caller's market panels.
func BuildPanelsWithMarket(db *store.DB, codes []string, start, end string, defs []factor.Def, mp *Panels) ([]*Panel, error) {
	var panels []*Panel
	for _, code := range codes {
		p, err := BuildPanelWithMarket(db, code, start, end, defs, mp)
		if err != nil {
			// 无行情/区间外股票跳过
			// English: skip stocks without market data / outside the range.
			continue
		}
		panels = append(panels, p)
	}
	return panels, nil
}

// forwardReturn 未来 h 个交易日收益（hfq）；越界为 NaN。
// English: forwardReturn is the h-day forward return (hfq); NaN when out of bounds.
func forwardReturn(series *factor.StockSeries, i, h int) float64 {
	if i+h >= len(series.CloseHfq) || i < 0 {
		return nan()
	}
	cur, fwd := series.CloseHfq[i], series.CloseHfq[i+h]
	if cur <= 0 || fwd <= 0 || isNaN(cur) || isNaN(fwd) {
		return nan()
	}
	return fwd/cur - 1
}
