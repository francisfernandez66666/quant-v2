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

// init 在包加载阶段注册本文件定义的因子（ID/名称/分类/计算函数）进全局注册表。
// English: registers this file's factor definitions into the global registry at package init.
func init() {
	// 注册规模类因子定义（对数市值，init 阶段登记进全局注册表）。
	Register(Def{ID: "LnMktCap", Name: "对数市值", Cat: CatSize, Desc: "ln(原始价×股本)，规模因子", Compute: lnMktCap})
}
