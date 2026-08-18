// 股票面板：单只股票的研究序列 + 全量因子值，供横截面 IC/分层/回测。
package research

import (
	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// Panel 单只股票的因子面板。
// （Panel holds one stock's factor values aligned to its trade dates.）
type Panel struct {
	Code    string
	Series  *factor.StockSeries
	DateIdx map[string]int          // 日期 → 序列下标
	Factors map[string][]float64    // factorID → 与 Dates 对齐的因子值
}

// BuildPanel 装配单只股票并计算指定因子的值。
// （BuildPanel assembles one stock and computes the given factors.）
func BuildPanel(db *store.DB, code, start, end string, defs []factor.Def) (*Panel, error) {
	series, err := Assemble(db, code, start, end)
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
// （BuildPanels assembles panels for many stocks, skipping those without data.）
func BuildPanels(db *store.DB, codes []string, start, end string, defs []factor.Def) ([]*Panel, error) {
	var panels []*Panel
	for _, code := range codes {
		p, err := BuildPanel(db, code, start, end, defs)
		if err != nil {
			// 无行情/区间外股票跳过
			continue
		}
		panels = append(panels, p)
	}
	return panels, nil
}

// forwardReturn 未来 h 个交易日收益（hfq）；越界为 NaN。
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