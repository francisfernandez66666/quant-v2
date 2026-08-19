// 均线类指标：SMA / EMA。
// English: Moving-average indicators: SMA / EMA.
package indicator

// SMA 简单移动平均。输出第 i 根为最近 n 根收盘均值；不足 n 根为 NaN。
// English: SMA is the simple moving average; the i-th output is the mean of the last n closes; NaN when fewer than n bars are available.
// （SMA returns the simple moving average; NaN during the first n-1 bars.）
func SMA(closes []float64, n int) []float64 {
	out := make([]float64, len(closes))
	if n <= 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	sum := 0.0
	for i := range closes {
		sum += closes[i]
		if i >= n {
			sum -= closes[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		} else {
			out[i] = nan()
		}
	}
	return out
}

// EMA 指数移动平均。种子取前 n 根简单平均，系数 k=2/(n+1)，不足 n 根为 NaN。
// 口径与 internal/data/macd.go 的 ema 一致（但后者预热期补 0，本库为 NaN）。
// English: EMA is the exponential moving average seeded with the simple mean of the first n bars, factor k=2/(n+1); NaN during warm-up. It matches the convention of the ema in internal/data/macd.go (though that one fills the warm-up with 0, while this library uses NaN).
// （EMA returns the exponential moving average seeded with the mean of the first n bars,
// k=2/(n+1); NaN during warm-up.）
func EMA(closes []float64, n int) []float64 {
	out := make([]float64, len(closes))
	for i := range out {
		out[i] = nan()
	}
	if n <= 0 || len(closes) < n {
		return out
	}
	seed := 0.0
	for i := 0; i < n; i++ {
		seed += closes[i]
	}
	out[n-1] = seed / float64(n)
	k := 2.0 / float64(n+1)
	for i := n; i < len(closes); i++ {
		out[i] = closes[i]*k + out[i-1]*(1-k)
	}
	return out
}
