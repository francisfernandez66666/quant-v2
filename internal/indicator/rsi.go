// RSI 相对强弱指标（Wilder 平滑）。常用 n=14。
package indicator

// RSI 计算 RSI 序列。第 n 根起有值：首值用前 n 段涨跌的简单平均，之后按 Wilder 递推
// （avg=(avg×(n−1)+cur)/n）；平均跌幅为 0 时 RSI=100。
// （RSI computes the RSI series using Wilder smoothing; NaN during warm-up, RSI=100 when
// the average loss is zero.）
func RSI(closes []float64, n int) []float64 {
	out := make([]float64, len(closes))
	for i := range out {
		out[i] = nan()
	}
	if n <= 0 || len(closes) <= n {
		return out
	}
	gains := make([]float64, len(closes))
	losses := make([]float64, len(closes))
	for i := 1; i < len(closes); i++ {
		d := closes[i] - closes[i-1]
		if d > 0 {
			gains[i] = d
		} else {
			losses[i] = -d
		}
	}
	avgGain, avgLoss := 0.0, 0.0
	for i := 1; i <= n; i++ {
		avgGain += gains[i]
		avgLoss += losses[i]
	}
	avgGain /= float64(n)
	avgLoss /= float64(n)
	out[n] = rsiValue(avgGain, avgLoss)
	for i := n + 1; i < len(closes); i++ {
		avgGain = (avgGain*float64(n-1) + gains[i]) / float64(n)
		avgLoss = (avgLoss*float64(n-1) + losses[i]) / float64(n)
		out[i] = rsiValue(avgGain, avgLoss)
	}
	return out
}

// rsiValue 由平均涨幅/跌幅计算 RSI。
func rsiValue(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		return 100
	}
	return 100 - 100/(1+avgGain/avgLoss)
}

// RSI14 以常用参数 14 计算 RSI。
// （RSI14 computes RSI with the standard period of 14.）
func RSI14(closes []float64) []float64 {
	return RSI(closes, 14)
}