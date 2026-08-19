// MACD 指标：DIF=EMA12−EMA26，DEA=EMA9(DIF)，BAR=2×(DIF−DEA)。
// 与盘中 internal/data/macd.go 同口径，但为单遍 O(n) 实现且预热期 NaN。
// English: MACD indicator: DIF=EMA12-EMA26, DEA=EMA9(DIF), BAR=2×(DIF-DEA). Same convention as the intraday internal/data/macd.go, but a single-pass O(n) implementation with NaN during warm-up.
package indicator

// MACDPoint 单根K线的 MACD 三值。
// English: MACDPoint holds the three MACD values for a single bar.
type MACDPoint struct {
	DIF float64 // 快线
	// English: DIF is the fast line.
	DEA float64 // 慢线
	// English: DEA is the slow line.
	Bar float64 // 柱 = 2×(DIF−DEA)
	// English: Bar is the histogram = 2×(DIF-DEA).
}

// MACD 计算整条 MACD 序列。
// DIF 在 EMA26 预热结束后（第 26 根起）有值；DEA 以 DIF 序列前 9 个有效值求均值作种子，
// 故 DEA/Bar 更晚出现；预热期对应字段为 NaN。
// English: MACD computes the whole MACD series. DIF is valid after the EMA26 warm-up (from bar 26); DEA seeds with the mean of the first 9 valid DIF values, so DEA/Bar appear later; the corresponding warm-up fields are NaN.
// （MACD computes the MACD series. DIF is valid from bar 25; DEA seeds on the first 9 valid
// DIF values, so DEA/Bar start later; warm-up entries are NaN.）
func MACD(closes []float64, fast, slow, signal int) []MACDPoint {
	out := make([]MACDPoint, len(closes))
	if fast <= 0 || slow <= 0 || signal <= 0 || fast >= slow || len(closes) < slow {
		for i := range out {
			out[i] = MACDPoint{nan(), nan(), nan()}
		}
		return out
	}
	emaFast := EMA(closes, fast)
	emaSlow := EMA(closes, slow)

	dif := make([]float64, len(closes))
	dea := make([]float64, len(closes))
	for i := range dif {
		dif[i] = nan()
		dea[i] = nan()
	}
	start := slow - 1 // DIF 首个有效下标
	// English: start is the first valid index of DIF.
	for i := range closes {
		if i < start {
			continue
		}
		dif[i] = emaFast[i] - emaSlow[i]
	}

	// DEA = EMA9(DIF)，种子用 DIF 前 9 个有效值（从 start 起）
	// English: DEA = EMA9(DIF); the seed uses the first 9 valid DIF values (starting from start).
	deaStart := start + signal - 1
	if deaStart < len(closes) {
		s := 0.0
		for i := 0; i < signal; i++ {
			s += dif[start+i]
		}
		dea[deaStart] = s / float64(signal)
		k := 2.0 / float64(signal+1)
		for i := deaStart + 1; i < len(closes); i++ {
			dea[i] = dif[i]*k + dea[i-1]*(1-k)
		}
	}

	for i := range closes {
		out[i] = MACDPoint{DIF: dif[i], DEA: dea[i], Bar: 2 * (dif[i] - dea[i])}
	}
	return out
}

// MACDDefault 以默认参数（12/26/9）计算 MACD 序列。
// English: MACDDefault computes MACD with the standard 12/26/9 parameters.
// （MACDDefault computes MACD with the standard 12/26/9 parameters.）
func MACDDefault(closes []float64) []MACDPoint {
	return MACD(closes, 12, 26, 9)
}
