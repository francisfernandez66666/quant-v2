// alpha101.go WorldQuant Alpha101 代表性子集（D1）：抽取公式化算子（rank/ts_rank/ts_argmax/delta/
// sign/rolling-extrema），映射入既有 7 大类。Alpha101 的横截面 rank 在单票序列内以时间序列分位近似
// （tsRank 与 tsArgMaxOffset），B3 层再按日横截面做 IC 检验。
// English: representative WorldQuant Alpha101 subset (D1). Formulaic operators are reused; the original
// cross-sectional rank is approximated by time-series rank inside a single stock series, and B3 later
// performs cross-sectional IC checks per day.
package factor

import (
	"math"

	"quant-trading-v2/internal/indicator"
)

// alpha1 趋势强度 = 过去 5 日复合动量序列（收益为负取 20 日波动率、否则取收盘价，平方后）最大值偏移。
// 反映"强势极值是否贴近当下"：最新最强（偏移≈0）时值小，视为动量延续信号。
// English: trend-strength alpha — normalized offset of the trailing 5-day max of the composite series
// (20-day vol when return is negative, else close, squared). Smaller value means the strength peak is
// recent (momentum continuation).
func alpha1(s *StockSeries) []float64 {
	n := len(s.CloseHfq)
	composite := make([]float64, n)
	ret := indicator.Returns(s.CloseHfq)
	std20 := indicator.RollingStd(ret, 20)
	for i := range composite {
		composite[i] = math.NaN()
		// 收益为负时用 20 日波动率代替收盘价，再取平方：小值表示强度峰值更近
		if !math.IsNaN(ret[i]) && !math.IsNaN(std20[i]) {
			base := s.CloseHfq[i]
			if ret[i] < 0 {
				base = std20[i]
			}
			composite[i] = base * base
		}
	}
	return tsArgMaxOffset(composite, 5)
}

// alpha4 反转 = −ts_rank(低价,9)：近 9 日低价分位越低（创新低）信号越强，博弈超跌反弹。
// English: reversal alpha = −ts_rank(low,9): the lower the 9-day low percentile (making new lows), the
// stronger the oversold-rebound signal.
func alpha4(s *StockSeries) []float64 {
	rank := tsRank(s.Low, 9)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(rank[i]) {
			out[i] = -rank[i]
		}
	}
	return out
}

// alpha12 量价背离 = sign(Δ量)×(−Δ价)：量增价跌为正（承接/恐慌），量价同向走平。
// English: volume-price divergence alpha = sign(Δvol)×(−Δprice): positive on rising-volume price
// declines, flat when volume and price move together.
func alpha12(s *StockSeries) []float64 {
	dVol := deltaSeries(s.Vol, 1)
	dClose := deltaSeries(s.CloseHfq, 1)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(dVol[i]) && !math.IsNaN(dClose[i]) {
			out[i] = signOp(dVol[i]) * (-dClose[i])
		}
	}
	return out
}

// alpha101 价格区间位置×量 = (2×close−low−high)/(high−low)×volume：
// 收盘在日内区间的相对偏移乘以成交量，衡量强势收阳时的量能配合（Alpha101 的实用变体，
// 原式中 max(low,close)/min(high,close) 在 close∈[low,high] 时退化为 0）。
// English: range-position alpha = (2·close−low−high)/(high−low)·volume — the offset of close within the
// day's range scaled by volume (practical variant; the literal Alpha101 degenerates to 0 since
// close∈[low,high]).
func alpha101(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if s.High[i] > s.Low[i] && s.Vol[i] > 0 && !math.IsNaN(s.High[i]) && !math.IsNaN(s.Low[i]) && !math.IsNaN(s.CloseHfq[i]) {
			// 收盘价在当日区间内的相对位置（-1~1），以成交量加权
			pos := (2*s.CloseHfq[i] - s.Low[i] - s.High[i]) / (s.High[i] - s.Low[i])
			out[i] = pos * s.Vol[i]
		}
	}
	return out
}

// init 在包加载阶段注册本文件定义的因子（ID/名称/分类/计算函数）进全局注册表。
// English: registers this file's factor definitions into the global registry at package init.
func init() {
	Register(Def{ID: "Alpha1", Name: "趋势强度", Cat: CatMomentum, Desc: "WQ-Alpha1 风格：复合动量5日极值偏移", Compute: alpha1})
	Register(Def{ID: "Alpha4", Name: "超跌反转", Cat: CatMomentum, Desc: "WQ-Alpha4 风格：−ts_rank(低价,9)", Compute: alpha4})
	Register(Def{ID: "Alpha12", Name: "量价背离", Cat: CatLiquidity, Desc: "WQ-Alpha12 风格：sign(Δ量)×(−Δ价)", Compute: alpha12})
	Register(Def{ID: "Alpha101", Name: "区间位置×量", Cat: CatLiquidity, Desc: "WQ-Alpha101 变体：(2c−l−h)/(h−l)×量", Compute: alpha101})
}
