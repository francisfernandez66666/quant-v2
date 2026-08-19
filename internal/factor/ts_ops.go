// ts_ops.go 时间序列算子（D1：为 Alpha158/WorldQuant 公式提供 rank/argmax/delta/滚动极值等基础运算）。
// 约定与因子库一致：窗口不足/输入 NaN 时输出 NaN；窗口内个别 NaN 按缺失跳过。
// English: time-series operators for the D1 Alpha158/WorldQuant formulas (rank/argmax/delta/rolling
// extrema). Following the factor-library convention: NaN during warm-up or for NaN input; interior NaN
// values are skipped like missing observations.
package factor

import "math"

// tsRank 当前值在过去 n 日窗口内的分位排名 [0,1]：
// (窗口内严格小于当前值的个数 + 0.5×等于当前值的个数) / n；窗口不足或当前值 NaN 时为 NaN。
// English: fractional rank of the current value within the trailing n-day window in [0,1]:
// (count strictly less + 0.5×count equal) / n; NaN when the window is incomplete or value is NaN.
func tsRank(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
		if i < n-1 || math.IsNaN(xs[i]) {
			continue
		}
		less, eq := 0, 0
		for j := i - n + 1; j <= i; j++ {
			if math.IsNaN(xs[j]) {
				continue
			}
			if xs[j] < xs[i] {
				less++
			} else if xs[j] == xs[i] {
				eq++
			}
		}
		out[i] = (float64(less) + 0.5*float64(eq)) / float64(n)
	}
	return out
}

// tsArgMaxOffset 过去 n 日窗口内最大值距离当前日的偏移（0=当日，n-1=最旧），
// 输出归一化到 [0,1]（offset/(n−1)）；窗口不足或当前值 NaN 时为 NaN。
// English: normalized offset of the trailing n-day maximum from the current day (0=current, n-1=oldest),
// scaled to [0,1]; NaN when the window is incomplete or the current value is NaN.
func tsArgMaxOffset(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
		if i < n-1 || math.IsNaN(xs[i]) {
			continue
		}
		maxVal := math.Inf(-1)
		maxOff := 0
		valid := false
		for j := i - n + 1; j <= i; j++ {
			if math.IsNaN(xs[j]) {
				continue
			}
			valid = true
			if xs[j] > maxVal {
				maxVal = xs[j]
				maxOff = i - j
			}
		}
		if valid && n > 1 {
			out[i] = float64(maxOff) / float64(n-1)
		}
	}
	return out
}

// deltaSeries 差分 xs[i]−xs[i−n]；不足 n 或任一端 NaN 时为 NaN。
// English: n-day difference xs[i]−xs[i−n]; NaN during warm-up or if either end is NaN.
func deltaSeries(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
		if i < n || math.IsNaN(xs[i]) || math.IsNaN(xs[i-n]) {
			continue
		}
		out[i] = xs[i] - xs[i-n]
	}
	return out
}

// rollingMax 过去 n 日滚动最大值；窗口不足时 NaN，窗口内全 NaN 时 NaN。
// English: trailing n-day rolling maximum; NaN for incomplete windows or all-NaN windows.
func rollingMax(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
		if i < n-1 {
			continue
		}
		m := math.Inf(-1)
		for j := i - n + 1; j <= i; j++ {
			if !math.IsNaN(xs[j]) && xs[j] > m {
				m = xs[j]
			}
		}
		if m != math.Inf(-1) {
			out[i] = m
		}
	}
	return out
}

// rollingMin 过去 n 日滚动最小值；语义与 rollingMax 一致。
// English: trailing n-day rolling minimum with the same semantics as rollingMax.
func rollingMin(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
		if i < n-1 {
			continue
		}
		m := math.Inf(1)
		for j := i - n + 1; j <= i; j++ {
			if !math.IsNaN(xs[j]) && xs[j] < m {
				m = xs[j]
			}
		}
		if m != math.Inf(1) {
			out[i] = m
		}
	}
	return out
}

// signOp 符号函数：正→1、负→−1、零→0。
// English: sign function: positive→1, negative→−1, zero→0.
func signOp(v float64) float64 {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}
