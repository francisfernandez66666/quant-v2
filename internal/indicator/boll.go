// BOLL 布林带（20,2）：中轨=MA20，上下轨=中轨±2×总体标准差。
// English: BOLL Bollinger Bands (20,2): middle band=MA20, upper/lower bands=middle ±2× population standard deviation.
package indicator

import "math"

// BOLLPoint 单根K线的布林带三值。
// English: BOLLPoint holds the three Bollinger Band values for a single bar.
type BOLLPoint struct {
	Mid float64 // 中轨（MA）
	// English: Mid is the middle band (MA).
	Up float64 // 上轨
	// English: Up is the upper band.
	Low float64 // 下轨
	// English: Low is the lower band.
}

// BOLL 计算布林带序列。标准差取总体标准差（除以 n）；预热期（不足 n 根）为 NaN。
// English: BOLL computes the Bollinger Band series. The standard deviation is the population one (divided by n); NaN during warm-up (fewer than n bars).
// （BOLL computes Bollinger Bands using the population standard deviation; NaN during warm-up.）
func BOLL(closes []float64, n int, k float64) []BOLLPoint {
	out := make([]BOLLPoint, len(closes))
	for i := range out {
		out[i] = BOLLPoint{nan(), nan(), nan()}
	}
	if n <= 0 || len(closes) < n {
		return out
	}
	for i := n - 1; i < len(closes); i++ {
		mid := mean(closes[i-n+1 : i+1])
		var v float64
		for _, x := range closes[i-n+1 : i+1] {
			d := x - mid
			v += d * d
		}
		sd := math.Sqrt(v / float64(n))
		out[i] = BOLLPoint{Mid: mid, Up: mid + k*sd, Low: mid - k*sd}
	}
	return out
}

// BOLLDefault 以默认参数（20,2）计算布林带。
// English: BOLLDefault computes Bollinger Bands with the default 20,2 parameters.
// （BOLLDefault computes Bollinger Bands with the default 20,2 parameters.）
func BOLLDefault(closes []float64) []BOLLPoint {
	return BOLL(closes, 20, 2)
}
