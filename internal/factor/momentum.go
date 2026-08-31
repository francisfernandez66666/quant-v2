// 动量/反转类因子：过去 n 日收益（反转由 B3 层按 IC 符号决定方向）。
// English: Momentum/reversal factors: past n-day return (reversal direction is decided in layer B3 by the IC sign).
package factor

import "math"

// momentum 过去 n 日收益 = closeHfq[i]/closeHfq[i−n] − 1；不足 n 根为 NaN。
// English: momentum is the past n-day return = closeHfq[i]/closeHfq[i-n] - 1; NaN when fewer than n bars are available.
func momentum(n int) func(*StockSeries) []float64 {
	return func(s *StockSeries) []float64 {
		out := make([]float64, s.Len())
		for i := range out {
			out[i] = math.NaN()
			if i >= n && s.CloseHfq[i] > 0 && s.CloseHfq[i-n] > 0 {
				out[i] = s.CloseHfq[i]/s.CloseHfq[i-n] - 1
			}
		}
		return out
	}
}

// init 在包加载阶段注册本文件定义的因子（ID/名称/分类/计算函数）进全局注册表。
// English: registers this file's factor definitions into the global registry at package init.
func init() {
	// 注册动量类因子定义（多窗口收益动量；init 阶段登记进全局注册表）。
	Register(Def{ID: "Mom5", Name: "5日动量", Cat: CatMomentum, Desc: "过去5日收益", Compute: momentum(5)})
	Register(Def{ID: "Mom10", Name: "10日动量", Cat: CatMomentum, Desc: "过去10日收益", Compute: momentum(10)})
	Register(Def{ID: "Mom20", Name: "20日动量", Cat: CatMomentum, Desc: "过去20日收益", Compute: momentum(20)})
	Register(Def{ID: "Mom60", Name: "60日动量", Cat: CatMomentum, Desc: "过去60日收益", Compute: momentum(60)})
}
