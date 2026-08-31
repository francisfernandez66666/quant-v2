// 形态算子（F1）：把四大战法的形态判定特征抽象为可复用因子算子（纯价量），
// 注册进 factor 库后自动进入 B3 IC 检验 / B4 全链路 / B5 优化器——"形态逐渐往因子式靠拢"。
// 这些算子也是形态模板搜索（F2）与实盘形态解释器（F3）的基础构件。
//
// 约定：输出与 StockSeries 等长，预热期/缺失为 NaN；价格类用 hfq 收盘。
// English: morphology operators (F1) — abstracts the four strategies' pattern-judgment features into
// reusable price-volume factor operators, registered into the factor library so they automatically join
// B3 IC validation / B4 full-chain / B5 optimizer, moving shape logic toward a factor expression. These
// are also the building blocks for pattern-template search (F2) and the live shape interpreter (F3).
package factor

import "math"

// volSurge 放量倍数 = 当日量 / 20日均量（双响炮"一突/二突放量"、N形"突破量比"基础）。
// 均量缺失（不足 20 根）→ NaN。
// English: volume surge = today's volume / 20-day average volume (basis for Double-Bump breakouts and
// N-shape breakout volume ratio). NaN when the 20-day average is unavailable.
func volSurge(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i < 20 {
			continue
		}
		var sum float64
		for j := i - 20; j < i; j++ {
			sum += s.Vol[j]
		}
		if sum <= 0 || s.Vol[i] <= 0 {
			continue
		}
		avg := sum / 20
		out[i] = s.Vol[i] / avg
	}
	return out
}

// volShrink 缩量比 = 近5日均量 / 20日均量（龙回头"回调缩量"、双响炮"调整期缩量"基础；
// <1 表示缩量，越小回调越充分）。
// English: volume-shrink ratio = 5-day avg volume / 20-day avg volume (basis for Dragon-Return pullback
// shrink and Double-Bump adjustment shrink; <1 means shrinking, smaller = deeper pullback).
func volShrink(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i < 20 {
			continue
		}
		var s5, s20 float64
		for j := i - 20; j < i; j++ {
			s20 += s.Vol[j]
		}
		for j := i - 5; j < i; j++ {
			s5 += s.Vol[j]
		}
		if s20 <= 0 || s5 <= 0 {
			continue
		}
		out[i] = (s5 / 5) / (s20 / 20)
	}
	return out
}

// priceBreakout 突破 = 收盘创近 n 日新高（1=突破 / 0=未突破；龙回头二波、双响炮二突确认）。
// 当日为近 n 日（不含当日）最高价时记 1。
// English: breakout = close makes a new n-day high (1=break / 0=not; Dragon-Return second wave and
// Double-Bump second breakout confirmation). 1 when today is the n-day (excluding today) high.
func priceBreakout(n int) func(*StockSeries) []float64 {
	return func(s *StockSeries) []float64 {
		out := make([]float64, s.Len())
		for i := range out {
			out[i] = math.NaN()
			if i < n {
				continue
			}
			hi := s.CloseHfq[i-n]
			for j := i - n; j < i; j++ {
				if s.CloseHfq[j] > hi {
					hi = s.CloseHfq[j]
				}
			}
			if s.CloseHfq[i] > 0 && s.CloseHfq[i] > hi {
				out[i] = 1
			} else {
				out[i] = 0
			}
		}
		return out
	}
}

// drawdown20 20日回撤 = 1 − 当前收盘 / 近20日最高收盘（龙回头/双响炮"回调深度"基础；
// 0 表示创新高，越大回调越深）。
// English: 20-day drawdown = 1 − close / 20-day max close (basis for Dragon-Return/Double-Bump pullback
// depth; 0 = new high, larger = deeper pullback).
func drawdown20(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i < 20 {
			continue
		}
		hi := s.CloseHfq[i-20]
		for j := i - 20; j <= i; j++ {
			if s.CloseHfq[j] > hi {
				hi = s.CloseHfq[j]
			}
		}
		if hi <= 0 || s.CloseHfq[i] <= 0 {
			continue
		}
		out[i] = 1 - s.CloseHfq[i]/hi
	}
	return out
}

// bullAlign 均线多头排列 = MA5>MA10>MA20 且收盘>MA5（1=多头 / 0=否；趋势健康度）。
// English: bull alignment = MA5>MA10>MA20 and close>MA5 (1=bull / 0=not; trend health).
func bullAlign(s *StockSeries) []float64 {
	out := make([]float64, s.Len())
	for i := range out {
		out[i] = math.NaN()
		if i < 20 {
			continue
		}
		ma5, ma10, ma20 := rollingMean(s, i, 5), rollingMean(s, i, 10), rollingMean(s, i, 20)
		if isNaNf(ma5) || isNaNf(ma10) || isNaNf(ma20) {
			continue
		}
		if ma5 > ma10 && ma10 > ma20 && s.CloseHfq[i] > ma5 {
			out[i] = 1
		} else {
			out[i] = 0
		}
	}
	return out
}

// rollingMean 近 n 日（含当日）收盘均线值；任一根无效返回 NaN。
// English: n-day (inclusive) rolling mean close; NaN when any bar is invalid.
func rollingMean(s *StockSeries, i, n int) float64 {
	var sum float64
	for j := i - n + 1; j <= i; j++ {
		if j < 0 || s.CloseHfq[j] <= 0 {
			return math.NaN()
		}
		sum += s.CloseHfq[j]
	}
	return sum / float64(n)
}

// isNaNf 判断浮点是否为 NaN。
func isNaNf(v float64) bool { return math.IsNaN(v) }

// init 在包加载阶段注册本文件定义的因子（ID/名称/分类/计算函数）进全局注册表。
// English: registers this file's factor definitions into the global registry at package init.
func init() {
	// 注册形态/量价类因子定义（放量缩量、新高突破、回撤、均线排列；init 阶段登记）。
	Register(Def{ID: "VolSurge5", Name: "放量倍数", Cat: CatLiquidity, Desc: "当日量/20日均量（放量突破）", Compute: volSurge})
	Register(Def{ID: "VolShrink", Name: "缩量比", Cat: CatLiquidity, Desc: "5日均量/20日均量（回调缩量）", Compute: volShrink})
	Register(Def{ID: "Brk20", Name: "20日新高突破", Cat: CatMomentum, Desc: "收盘创20日新高=1", Compute: priceBreakout(20)})
	Register(Def{ID: "Brk60", Name: "60日新高突破", Cat: CatMomentum, Desc: "收盘创60日新高=1", Compute: priceBreakout(60)})
	Register(Def{ID: "Drawdown20", Name: "20日回撤", Cat: CatVolatility, Desc: "1−收盘/20日最高（回调深度）", Compute: drawdown20})
	Register(Def{ID: "BullAlign", Name: "均线多头排列", Cat: CatMomentum, Desc: "MA5>MA10>MA20且收>MA5=1", Compute: bullAlign})
}
