// alpha158.go Alpha158 风格价量因子（D1：以 7 大类为骨干，抽取 Qlib Alpha158 中可复现的价量公式）。
// 全部基于 StockSeries 现有字段（hfq 开高低收/量/额/换手），不新增数据依赖；预热期 NaN。
// English: Alpha158-style price/volume factors (D1). Reproducible price-volume formulas from Qlib's
// Alpha158, all derived from existing StockSeries fields; NaN during warm-up.
package factor

import (
	"math"

	"quant-trading-v2/internal/indicator"
)

// rsi14 14 日 RSI（Wilder 平滑，指示动量强弱）。
// English: 14-day RSI (Wilder smoothing) — momentum strength.
func rsi14(s *StockSeries) []float64 {
	return indicator.RSI14(s.CloseHfq)
}

// bbi 多空指标 = (MA3+MA6+MA12+MA24)/4（短中长期均线的综合方向）。
// English: BBI bull-bear indicator = mean of MA3/6/12/24 (composite short/mid/long trend).
func bbi(s *StockSeries) []float64 {
	ma3 := indicator.SMA(s.CloseHfq, 3)
	ma6 := indicator.SMA(s.CloseHfq, 6)
	ma12 := indicator.SMA(s.CloseHfq, 12)
	ma24 := indicator.SMA(s.CloseHfq, 24)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(ma3[i]) && !math.IsNaN(ma6[i]) && !math.IsNaN(ma12[i]) && !math.IsNaN(ma24[i]) {
			out[i] = (ma3[i] + ma6[i] + ma12[i] + ma24[i]) / 4
		}
	}
	return out
}

// ema10_20 EMA10/EMA20−1：中期趋势斜率。
// English: EMA10/EMA20−1 — mid-term trend slope.
func ema10_20(s *StockSeries) []float64 {
	e10 := indicator.EMA(s.CloseHfq, 10)
	e20 := indicator.EMA(s.CloseHfq, 20)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(e10[i]) && !math.IsNaN(e20[i]) && e20[i] > 0 {
			out[i] = e10[i]/e20[i] - 1
		}
	}
	return out
}

// realizedVol n 日已实现波动率（对数收益总体标准差）。
// English: n-day realized volatility (population std of log returns).
func realizedVol(n int) func(*StockSeries) []float64 {
	return func(s *StockSeries) []float64 {
		return indicator.RollingStd(indicator.LogReturns(s.CloseHfq), n)
	}
}

// atrRatio14 ATR14/收盘：单位价格的日内真实波幅（波动率相对水平）。
// English: ATR14/close — per-unit price true range (relative volatility level).
func atrRatio14(s *StockSeries) []float64 {
	atr := indicator.ATR14(s.High, s.Low, s.CloseHfq)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(atr[i]) && s.CloseHfq[i] > 0 {
			out[i] = atr[i] / s.CloseHfq[i]
		}
	}
	return out
}

// highLow20 20 日最高价/最低价：区间宽窄（震荡/收敛）。
// English: 20-day high/low ratio — range width (expansion vs. compression).
func highLow20(s *StockSeries) []float64 {
	hi := rollingMax(s.High, 20)
	lo := rollingMin(s.Low, 20)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(hi[i]) && !math.IsNaN(lo[i]) && lo[i] > 0 {
			out[i] = hi[i] / lo[i]
		}
	}
	return out
}

// volRatio5 波动放大比 = |当日收益| / 5 日波动率（>1 表示异动放大）。
// English: volatility breakout ratio = |daily return| / 5-day volatility (≥1 flags abnormal move).
func volRatio5(s *StockSeries) []float64 {
	ret := indicator.Returns(s.CloseHfq)
	std5 := indicator.RollingStd(ret, 5)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i >= 1 && !math.IsNaN(std5[i]) && std5[i] > 0 && !math.IsNaN(ret[i]) {
			out[i] = math.Abs(ret[i]) / std5[i]
		}
	}
	return out
}

// vma 量均比 = MA_n(量)/MA20(量)：短期量能相对基准水平（>1 放量）。
// English: volume-MA ratio = MA_n(vol)/MA20(vol) — short-term volume vs. baseline (≥1 expanding).
func vma(n int) func(*StockSeries) []float64 {
	return func(s *StockSeries) []float64 {
		maN := indicator.SMA(s.Vol, n)
		ma20 := indicator.SMA(s.Vol, 20)
		out := make([]float64, s.Len())
		for i := range out {
			out[i] = math.NaN()
			if !math.IsNaN(maN[i]) && !math.IsNaN(ma20[i]) && ma20[i] > 0 {
				out[i] = maN[i] / ma20[i]
			}
		}
		return out
	}
}

// vstd20 成交量变异系数 = 20 日量 std/均值（放量稳定性）。
// English: volume coefficient of variation = 20-day vol std/mean (volume stability).
func vstd20(s *StockSeries) []float64 {
	std := indicator.RollingStd(s.Vol, 20)
	ma := indicator.SMA(s.Vol, 20)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(std[i]) && !math.IsNaN(ma[i]) && ma[i] > 0 {
			out[i] = std[i] / ma[i]
		}
	}
	return out
}

// vmax10 量峰比 = 10 日最大量/当日量（>1 当日为缩量回踩）。
// English: volume-peak ratio = 10-day max volume / today's volume (≥1 signals a pullback on shrinking volume).
func vmax10(s *StockSeries) []float64 {
	mx := rollingMax(s.Vol, 10)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(mx[i]) && s.Vol[i] > 0 {
			out[i] = mx[i] / s.Vol[i]
		}
	}
	return out
}

// vmin10 量地比 = 当日量/10 日最小量（>1 当日放量）。
// English: volume-floor ratio = today's volume / 10-day min volume (≥1 expanding).
func vmin10(s *StockSeries) []float64 {
	mn := rollingMin(s.Vol, 10)
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if !math.IsNaN(mn[i]) && mn[i] > 0 && s.Vol[i] > 0 {
			out[i] = s.Vol[i] / mn[i]
		}
	}
	return out
}

// turnoverStd20 20 日换手率波动（筹码松动程度）。
// English: 20-day turnover std (chip-distribution churn).
func turnoverStd20(s *StockSeries) []float64 {
	return indicator.RollingStd(s.Turnover, 20)
}

func init() {
	// 动量
	Register(Def{ID: "RSI14", Name: "14日RSI", Cat: CatMomentum, Desc: "Wilder 平滑 RSI14", Compute: rsi14})
	Register(Def{ID: "BBI", Name: "多空指标", Cat: CatMomentum, Desc: "(MA3+MA6+MA12+MA24)/4", Compute: bbi})
	Register(Def{ID: "EMA10_20", Name: "中期趋势斜率", Cat: CatMomentum, Desc: "EMA10/EMA20−1", Compute: ema10_20})
	// 波动率
	Register(Def{ID: "RealizedVol5", Name: "5日已实现波动率", Cat: CatVolatility, Desc: "5日对数收益总体标准差", Compute: realizedVol(5)})
	Register(Def{ID: "RealizedVol10", Name: "10日已实现波动率", Cat: CatVolatility, Desc: "10日对数收益总体标准差", Compute: realizedVol(10)})
	Register(Def{ID: "AtrRatio14", Name: "ATR14/收盘", Cat: CatVolatility, Desc: "单位价格真实波幅", Compute: atrRatio14})
	Register(Def{ID: "HighLow20", Name: "20日区间比", Cat: CatVolatility, Desc: "20日最高/最低价", Compute: highLow20})
	Register(Def{ID: "VolRatio5", Name: "5日波动放大比", Cat: CatVolatility, Desc: "|当日收益|/5日波动率", Compute: volRatio5})
	// 流动性
	Register(Def{ID: "VMA5", Name: "5日量均比", Cat: CatLiquidity, Desc: "MA5量/MA20量", Compute: vma(5)})
	Register(Def{ID: "VMA10", Name: "10日量均比", Cat: CatLiquidity, Desc: "MA10量/MA20量", Compute: vma(10)})
	Register(Def{ID: "VSTD20", Name: "20日量变异系数", Cat: CatLiquidity, Desc: "20日量 std/均值", Compute: vstd20})
	Register(Def{ID: "VMAX10", Name: "10日量峰比", Cat: CatLiquidity, Desc: "10日最大量/当日量", Compute: vmax10})
	Register(Def{ID: "VMIN10", Name: "10日量地比", Cat: CatLiquidity, Desc: "当日量/10日最小量", Compute: vmin10})
	Register(Def{ID: "TurnoverStd20", Name: "20日换手率波动", Cat: CatLiquidity, Desc: "20日换手率总体标准差", Compute: turnoverStd20})
}