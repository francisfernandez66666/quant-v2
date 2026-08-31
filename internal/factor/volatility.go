// 波动率类因子：收益波动率与振幅。
// English: Volatility factors: return volatility and amplitude.
package factor

import (
	"math"

	"quant-trading-v2/internal/indicator"
)

// volatility20 20 日对数收益率的总体标准差。
// （volatility20 is the 20-day population std of log returns.）
func volatility20(s *StockSeries) []float64 {
	logR := indicator.LogReturns(s.CloseHfq)
	return indicator.RollingStd(logR, 20)
}

// amplitude20 20 日平均振幅 = (最高−最低)/收盘 的均值。
// （amplitude20 is the 20-day mean of (high−low)/close.）
func amplitude20(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		if s.CloseHfq[i] > 0 {
			out[i] = (s.High[i] - s.Low[i]) / s.CloseHfq[i]
		} else {
			out[i] = math.NaN()
		}
	}
	return indicator.SMA(out, 20)
}

// init 在包加载阶段注册本文件定义的因子（ID/名称/分类/计算函数）进全局注册表。
// English: registers this file's factor definitions into the global registry at package init.
func init() {
	// 注册波动率类因子定义（init 阶段在全局注册表中登记，供因子库按 ID 查询/回放调用）。
	Register(Def{ID: "Volatility20", Name: "20日收益波动率", Cat: CatVolatility, Desc: "20日对数收益总体标准差", Compute: volatility20})
	Register(Def{ID: "Amplitude20", Name: "20日平均振幅", Cat: CatVolatility, Desc: "20日 (高−低)/收 均值", Compute: amplitude20})
}
