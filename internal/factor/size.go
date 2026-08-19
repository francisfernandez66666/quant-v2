// 规模类因子：对数市值（原始价 × 股本，季频近似）。
// English: Size factor: log market cap (raw price × shares, quarterly approximation).
package factor

import "math"

// lnMktCap 对数市值 = ln(原始收盘 × 总股本)。用原始价（hfq 复权因子会扭曲横截面排序）。
// （lnMktCap = ln(raw close × total shares). Raw prices avoid hfq factor distorting
// cross-sectional ranks.）
func lnMktCap(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.CloseRaw[i] > 0 && s.TotalShare[i] > 0 {
			out[i] = math.Log(s.CloseRaw[i] * s.TotalShare[i])
		}
	}
	return out
}

func init() {
	Register(Def{ID: "LnMktCap", Name: "对数市值", Cat: CatSize, Desc: "ln(原始价×股本)，规模因子", Compute: lnMktCap})
}
