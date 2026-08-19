// 估值类因子：比率倒数形式（越大越"便宜"）。
// English: Valuation factors: reciprocal form of ratios (larger means "cheaper").
package factor

import "math"

// epTTM 市盈率 TTM 倒数（1/PE，>0 时有效）。
// English: epTTM reciprocal of the TTM P/E ratio (1/PE, valid when > 0).
func epTTM(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.PeTTM[i] > 0 {
			out[i] = 1 / s.PeTTM[i]
		}
	}
	return out
}

// bp 市净率倒数（1/PB）。
// English: bp reciprocal of the price-to-book ratio (1/PB).
func bp(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.Pb[i] > 0 {
			out[i] = 1 / s.Pb[i]
		}
	}
	return out
}

// spTTM 市销率 TTM 倒数（1/PS）。
// English: spTTM reciprocal of the TTM price-to-sales ratio (1/PS).
func spTTM(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.PsTTM[i] > 0 {
			out[i] = 1 / s.PsTTM[i]
		}
	}
	return out
}

// cfpTTM 市现率 TTM 倒数（1/PCF）。
// English: cfpTTM reciprocal of the TTM price-to-cash-flow ratio (1/PCF).
func cfpTTM(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.PcfTTM[i] > 0 {
			out[i] = 1 / s.PcfTTM[i]
		}
	}
	return out
}

// dp 股息率 TTM（%）。
// English: dp TTM dividend yield (%).
func dp(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = s.DvTTM[i]
	}
	return out
}

// 估值类因子注册。
// English: Registration of valuation factors.
func init() {
	Register(Def{ID: "EP_ttm", Name: "市盈率TTM倒数", Cat: CatValue, Desc: "1/PE_ttm，越大越便宜", Compute: epTTM})
	Register(Def{ID: "BP", Name: "市净率倒数", Cat: CatValue, Desc: "1/PB，越大越便宜", Compute: bp})
	Register(Def{ID: "SP_ttm", Name: "市销率TTM倒数", Cat: CatValue, Desc: "1/PS_ttm，越大越便宜", Compute: spTTM})
	Register(Def{ID: "CFP_ttm", Name: "市现率TTM倒数", Cat: CatValue, Desc: "1/PCF_ttm，越大越便宜", Compute: cfpTTM})
	Register(Def{ID: "DP", Name: "股息率TTM", Cat: CatValue, Desc: "股息率（%），越高回报倾向越好", Compute: dp})
}
