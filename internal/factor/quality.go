// 质量类因子：盈利质量与偿债结构（点对时财务）。
// English: Quality factors: earnings quality and debt structure (point-in-time financials).
package factor

func roeFactor(s *StockSeries) []float64 {
	return fieldOrNaN(s.Roe, s.Len())
}

func grossMargin(s *StockSeries) []float64 {
	return fieldOrNaN(s.GrossMargin, s.Len())
}

func netMargin(s *StockSeries) []float64 {
	return fieldOrNaN(s.NetMargin, s.Len())
}

func debtToAssets(s *StockSeries) []float64 {
	return fieldOrNaN(s.DebtToAssets, s.Len())
}

func init() {
	Register(Def{ID: "ROE", Name: "净资产收益率", Cat: CatQuality, Desc: "ROE（点对时）", Compute: roeFactor})
	Register(Def{ID: "GrossMargin", Name: "毛利率", Cat: CatQuality, Desc: "毛利率（点对时）", Compute: grossMargin})
	Register(Def{ID: "NetMargin", Name: "净利率", Cat: CatQuality, Desc: "净利率（点对时）", Compute: netMargin})
	Register(Def{ID: "DebtToAssets", Name: "资产负债率", Cat: CatQuality, Desc: "负债/资产（点对时），越低越稳健", Compute: debtToAssets})
}
