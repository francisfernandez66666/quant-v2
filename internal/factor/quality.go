// 质量类因子：盈利质量与偿债结构（点对时财务）。
// English: Quality factors: earnings quality and debt structure (point-in-time financials).
package factor

// roeFactor 取 ROE（净资产收益率）点对时序列；缺失日由 fieldOrNaN 输出 NaN。
// （roeFactor returns the point-in-time ROE series, NaN for missing days.）
func roeFactor(s *StockSeries) []float64 {
	return fieldOrNaN(s.Roe, s.Len())
}

// grossMargin 取毛利率点对时序列；缺失日输出 NaN。
// （grossMargin returns the point-in-time gross-margin series.）
func grossMargin(s *StockSeries) []float64 {
	return fieldOrNaN(s.GrossMargin, s.Len())
}

// netMargin 取净利率点对时序列；缺失日输出 NaN。
// （netMargin returns the point-in-time net-margin series.）
func netMargin(s *StockSeries) []float64 {
	return fieldOrNaN(s.NetMargin, s.Len())
}

// debtToAssets 取资产负债率点对时序列；缺失日输出 NaN。
// （debtToAssets returns the point-in-time debt-to-assets series.）
func debtToAssets(s *StockSeries) []float64 {
	return fieldOrNaN(s.DebtToAssets, s.Len())
}

// init 注册质量类因子定义（点对时财务字段，无需滚动窗口）。
// （init registers the quality-category factor definitions.）
func init() {
	Register(Def{ID: "ROE", Name: "净资产收益率", Cat: CatQuality, Desc: "ROE（点对时）", Compute: roeFactor})
	Register(Def{ID: "GrossMargin", Name: "毛利率", Cat: CatQuality, Desc: "毛利率（点对时）", Compute: grossMargin})
	Register(Def{ID: "NetMargin", Name: "净利率", Cat: CatQuality, Desc: "净利率（点对时）", Compute: netMargin})
	Register(Def{ID: "DebtToAssets", Name: "资产负债率", Cat: CatQuality, Desc: "负债/资产（点对时），越低越稳健", Compute: debtToAssets})
}
