// 量能类指标与收益率/波动率序列（研究因子常用基础量）。
// English: Volume indicators and return/volatility series (common building blocks for research factors).
package indicator

import "math"

// VolMA 成交量简单移动平均（等价 SMA）。
// English: VolMA is the volume simple moving average (equivalent to SMA).
// （VolMA is the volume simple moving average.）
func VolMA(volumes []float64, n int) []float64 {
	return SMA(volumes, n)
}

// Returns 简单收益率序列：r[i]=closes[i]/closes[i−1]−1，首根为 NaN。
// English: Returns computes the simple return series: r[i]=closes[i]/closes[i-1]-1; the first element is NaN.
// （Returns computes simple returns; the first element is NaN.）
func Returns(closes []float64) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 {
		return out
	}
	out[0] = nan()
	for i := 1; i < len(closes); i++ {
		out[i] = closes[i]/closes[i-1] - 1
	}
	return out
}

// LogReturns 对数收益率序列：r[i]=ln(closes[i]/closes[i−1])，首根为 NaN。
// English: LogReturns computes the log return series: r[i]=ln(closes[i]/closes[i-1]); the first element is NaN.
// （LogReturns computes log returns; the first element is NaN.）
func LogReturns(closes []float64) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 {
		return out
	}
	out[0] = nan()
	for i := 1; i < len(closes); i++ {
		out[i] = math.Log(closes[i] / closes[i-1])
	}
	return out
}

// RollingStd 滚动总体标准差（窗口 n 的收益率波动率）；预热期为 NaN。
// English: RollingStd computes the rolling population standard deviation (return volatility over a window of n); NaN during warm-up.
// （RollingStd computes the rolling population standard deviation over n windows.）
func RollingStd(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	if n <= 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	for i := range xs {
		if i < n-1 {
			out[i] = nan()
			continue
		}
		m := mean(xs[i-n+1 : i+1])
		var v float64
		for _, x := range xs[i-n+1 : i+1] {
			d := x - m
			v += d * d
		}
		out[i] = math.Sqrt(v / float64(n))
	}
	return out
}
