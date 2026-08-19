// 成长类因子：净利同比（点对时）与 SUE 降级版（单季净利同比）。
// English: Growth factors: net profit YoY (point-in-time) and a SUE downgrade (single-quarter net profit YoY).
package factor

// yoyNetProfit 净利同比（%）。
// English: yoyNetProfit is the net profit YoY growth (%).
func yoyNetProfit(s *StockSeries) []float64 {
	return fieldOrNaN(s.YoyNetProfit, s.Len())
}

// sue 单季净利同比（SUE 降级版，%），横截面 Z 归一由 B3 层完成。
// English: sue is the single-quarter net profit YoY (SUE downgrade, %); cross-sectional Z normalization is done in layer B3.
func sue(s *StockSeries) []float64 {
	return fieldOrNaN(s.SingleQuarterNIYoy, s.Len())
}

func init() {
	Register(Def{ID: "YoyNetProfit", Name: "净利同比", Cat: CatGrowth, Desc: "归母净利同比（%），点对时", Compute: yoyNetProfit})
	Register(Def{ID: "SUE", Name: "单季净利同比", Cat: CatGrowth, Desc: "SUE 降级版（单季净利同比 %），B3 层做截面 Z", Compute: sue})
}
