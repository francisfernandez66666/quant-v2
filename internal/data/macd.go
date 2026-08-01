// Package data 提供行情数据获取、多数据源协调、情绪面分析、筹码分析、板块扫描等核心数据能力。
// macd.go 提供 MACD 指标计算，供 8a/8b 持续打分的动量分与 N 形战法使用。
package data

// MACD MACD 指标最新值。
type MACD struct {
	DIF float64 // 快线 DIF = EMA12 - EMA26
	DEA float64 // 慢线 DEA = EMA9(DIF)
	Bar float64 // 柱状图 Bar = 2 * (DIF - DEA)
}

// ema 计算收盘价序列的 n 周期指数移动平均序列（与前端/常见行情一致，系数 2/(n+1)）。
func ema(closes []float64, n int) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 {
		return out
	}
	k := 2.0 / float64(n+1)
	seed := 0.0
	// 种子值取前 n 根简单平均，后续按 EMA 递推
	if len(closes) < n {
		n = len(closes)
	}
	for i := 0; i < n; i++ {
		seed += closes[i]
	}
	out[n-1] = seed / float64(n)
	for i := n; i < len(closes); i++ {
		out[i] = closes[i]*k + out[i-1]*(1-k)
	}
	return out
}

// CalcMACD 从 K 线收盘价计算 MACD（EMA12/26 → DIF，DEA=EMA9(DIF)，BAR=2×(DIF−DEA)）。
// K 线不足 26 根时按可用数量降级计算，不足 2 根返回零值。
func CalcMACD(klines []KLine) MACD {
	closes := make([]float64, len(klines))
	for i, k := range klines {
		closes[i] = k.Close
	}
	if len(closes) < 2 {
		return MACD{}
	}
	difSeries := make([]float64, len(closes))
	// 逐点截取前缀序列计算 EMA12/EMA26 的差值得到 DIF 序列（O(n²)，数据量小可接受）
	for i := range closes {
		ema12 := ema(closes[:i+1], 12)
		ema26 := ema(closes[:i+1], 26)
		difSeries[i] = ema12[len(ema12)-1] - ema26[len(ema26)-1]
	}
	deaSeries := ema(difSeries, 9)
	last := len(closes) - 1
	dif := difSeries[last]
	dea := deaSeries[last]
	return MACD{
		DIF: dif,
		DEA: dea,
		Bar: 2 * (dif - dea),
	}
}
