// ATR 平均真实波幅（Wilder 平滑，常用 n=14），供 C4 ATR 动态止损使用。
// English: ATR is the average true range (Wilder smoothing, commonly n=14), used by the C4 ATR dynamic stop-loss.
package indicator

// TrueRange 计算真实波幅序列：TR=max(H−L, |H−Cprev|, |L−Cprev|)，首根 TR=H−L。
// English: TrueRange computes the true range series: TR=max(H-L, |H-Cprev|, |L-Cprev|); the first TR=H-L.
// （TrueRange computes the true range series.）
func TrueRange(highs, lows, closes []float64) []float64 {
	out := make([]float64, len(highs))
	if len(highs) == 0 {
		return out
	}
	out[0] = highs[0] - lows[0]
	for i := 1; i < len(highs); i++ {
		tr := highs[i] - lows[i]
		if hc := mathAbs(highs[i] - closes[i-1]); hc > tr {
			tr = hc
		}
		if lc := mathAbs(lows[i] - closes[i-1]); lc > tr {
			tr = lc
		}
		out[i] = tr
	}
	return out
}

// ATR 计算 ATR 序列。首值取前 n 根 TR 简单平均，之后按 Wilder 递推；预热期为 NaN。
// English: ATR computes the ATR series. The first value is the simple mean of the first n TRs, then Wilder recursion; NaN during warm-up.
// （ATR computes the ATR series: the first value is the simple mean of the first n TRs,
// then Wilder smoothing; NaN during warm-up.）
func ATR(highs, lows, closes []float64, n int) []float64 {
	tr := TrueRange(highs, lows, closes)
	out := make([]float64, len(tr))
	for i := range out {
		out[i] = nan()
	}
	if n <= 0 || len(tr) < n {
		return out
	}
	out[n-1] = mean(tr[:n]) // 首值：前 n 根真实波幅的简单均值
	for i := n; i < len(tr); i++ {
		out[i] = (out[i-1]*float64(n-1) + tr[i]) / float64(n) // 后续 Wilder 平滑
	}
	return out
}

// ATR14 以常用参数 14 计算 ATR。
// English: ATR14 computes ATR with the standard period of 14.
// （ATR14 computes ATR with the standard period of 14.）
func ATR14(highs, lows, closes []float64) []float64 {
	return ATR(highs, lows, closes, 14)
}

// mathAbs 返回浮点绝对值，规避引入额外依赖。
func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
