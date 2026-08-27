// 7 大类因子单元测试：估值/成长/质量/规模/动量/波动率/流动性各公式与边界。
package factor

import (
	"math"
	"testing"
)

// TestValueFactors 估值类公式：倒数与股息率。
// English: TestValueFactors value factors: reciprocals and dividend yield.
func TestValueFactors(t *testing.T) {
	s := &StockSeries{
		Dates:  []string{"d0", "d1", "d2"},
		PeTTM:  []float64{10, -5, 0},
		Pb:     []float64{2, 0, 4},
		PsTTM:  []float64{20, 10, 0},
		PcfTTM: []float64{5, 5, -1},
		DvTTM:  []float64{3.5, 0, 2},
	}
	// EP：PE>0 取倒数，否则 NaN
	// English: EP: take reciprocal when PE>0, otherwise NaN.
	got := mustGet(t, "EP_ttm").Compute(s)
	approx(t, got, []float64{0.1, math.NaN(), math.NaN()})
	// BP：PB>0 取倒数
	// English: BP: take reciprocal when PB>0.
	got = mustGet(t, "BP").Compute(s)
	approx(t, got, []float64{0.5, math.NaN(), 0.25})
	// SP
	got = mustGet(t, "SP_ttm").Compute(s)
	approx(t, got, []float64{0.05, 0.1, math.NaN()})
	// CFP
	got = mustGet(t, "CFP_ttm").Compute(s)
	approx(t, got, []float64{0.2, 0.2, math.NaN()})
	// DP 直传
	// English: DP passed through directly.
	got = mustGet(t, "DP").Compute(s)
	approx(t, got, []float64{3.5, 0, 2})
}

// TestGrowthFactors 成长类直传；缺失字段全 NaN。
// English: TestGrowthFactors growth factors passed through; missing fields are all NaN.
func TestGrowthFactors(t *testing.T) {
	s := &StockSeries{
		Dates:              []string{"d0", "d1", "d2"},
		YoyNetProfit:       []float64{10, 20, 30},
		SingleQuarterNIYoy: []float64{5, 6, 7},
	}
	approx(t, mustGet(t, "YoyNetProfit").Compute(s), []float64{10, 20, 30})
	approx(t, mustGet(t, "SUE").Compute(s), []float64{5, 6, 7})
	empty := &StockSeries{Dates: []string{"d0", "d1"}}
	got := mustGet(t, "YoyNetProfit").Compute(empty)
	if !math.IsNaN(got[0]) {
		t.Fatalf("缺失字段应 NaN: %v", got)
	}
}

// TestQualityFactors 质量类直传。
// English: TestQualityFactors quality factors passed through directly.
func TestQualityFactors(t *testing.T) {
	s := &StockSeries{
		Dates:        []string{"d0"},
		Roe:          []float64{12.5},
		GrossMargin:  []float64{30},
		NetMargin:    []float64{15},
		DebtToAssets: []float64{60},
	}
	approx(t, mustGet(t, "ROE").Compute(s), []float64{12.5})
	approx(t, mustGet(t, "GrossMargin").Compute(s), []float64{30})
	approx(t, mustGet(t, "NetMargin").Compute(s), []float64{15})
	approx(t, mustGet(t, "DebtToAssets").Compute(s), []float64{60})
}

// TestSizeFactor 对数市值 = ln(原始价×股本)。
// English: TestSizeFactor log market cap = ln(raw price × share count).
func TestSizeFactor(t *testing.T) {
	s := &StockSeries{
		Dates:      []string{"d0", "d1"},
		CloseRaw:   []float64{10, 20},
		TotalShare: []float64{1e8, 1e8},
	}
	got := mustGet(t, "LnMktCap").Compute(s)
	want0 := math.Log(1e9)
	want1 := math.Log(2e9)
	if math.Abs(got[0]-want0) > 1e-9 || math.Abs(got[1]-want1) > 1e-9 {
		t.Fatalf("LnMktCap 不符: %v", got)
	}
	// 价格缺失 → NaN
	// English: Missing price → NaN.
	bad := &StockSeries{Dates: []string{"d0"}, CloseRaw: []float64{0}, TotalShare: []float64{1e8}}
	if !math.IsNaN(mustGet(t, "LnMktCap").Compute(bad)[0]) {
		t.Fatalf("无价应 NaN")
	}
}

// TestMomentumFactors 动量 = close[i]/close[i−n]−1，预热期 NaN。
// English: TestMomentumFactors momentum = close[i]/close[i−n]−1, NaN during warm-up.
func TestMomentumFactors(t *testing.T) {
	// 收盘 10..16，7 根
	// English: Closes 10..16, 7 bars.
	closes := []float64{10, 11, 12, 13, 14, 15, 16}
	s := &StockSeries{Dates: []string{"0", "1", "2", "3", "4", "5", "6"}, CloseHfq: closes}
	got := mustGet(t, "Mom5").Compute(s)
	want := []float64{math.NaN(), math.NaN(), math.NaN(), math.NaN(), 0.4, 15.0/10.0 - 1, 16.0/11.0 - 1}
	approx(t, got, want)
}

// TestVolatilityFactors 波动率：恒定序列为 0；振幅手算。
// English: TestVolatilityFactors volatility: constant series is 0; amplitude computed by hand.
func TestVolatilityFactors(t *testing.T) {
	// 恒定收盘 → 对数收益 0 → 波动率 0
	// English: Constant closes → log return 0 → volatility 0.
	const30 := make([]float64, 30)
	dates := make([]string, 30)
	for i := range const30 {
		const30[i] = 10
		dates[i] = itoa(i)
	}
	s := &StockSeries{Dates: dates, CloseHfq: const30, High: const30, Low: const30}
	got := mustGet(t, "Volatility20").Compute(s)
	if !math.IsNaN(got[18]) || math.Abs(got[29]) > 1e-12 {
		t.Fatalf("恒定序列波动率应 0: got[18]=%v got[29]=%v", got[18], got[29])
	}
	// 振幅：20 日均值 (H−L)/C。构造 21 根，末值 = 均值。
	// English: Amplitude: 20-day mean of (H−L)/C. Build 21 bars, last value = mean.
	amp := make([]float64, 21)
	h, l, c := make([]float64, 21), make([]float64, 21), make([]float64, 21)
	ds := make([]string, 21)
	for i := range amp {
		// 每根 (H−L)/C = 0.04 + i*0.001
		// English: Each bar (H−L)/C = 0.04 + i*0.001.
		c[i] = 100
		half := (0.04 + float64(i)*0.001) * 100 / 2
		h[i], l[i] = 100+half, 100-half
		ds[i] = itoa(i)
	}
	s = &StockSeries{Dates: ds, CloseHfq: c, High: h, Low: l}
	got = mustGet(t, "Amplitude20").Compute(s)
	// 末 20 根的均值
	// English: Mean of the last 20 bars.
	var sum float64
	for i := 1; i < 21; i++ {
		sum += 0.04 + float64(i)*0.001
	}
	want := sum / 20
	if math.Abs(got[20]-want) > 1e-9 {
		t.Fatalf("振幅末值应 %.10f，得 %.10f", want, got[20])
	}
}

// TestLiquidityFactors 流动性：STO/STOA/Amihud 手算。
// English: TestLiquidityFactors liquidity: STO/STOA/Amihud computed by hand.
func TestLiquidityFactors(t *testing.T) {
	n := 21
	dates := make([]string, n)
	turn := make([]float64, n)
	amount := make([]float64, n)
	closeH := make([]float64, n)
	for i := 0; i < n; i++ {
		dates[i] = itoa(i)
		turn[i] = 2.0
		amount[i] = 1000.0
		closeH[i] = 10.0 + float64(i)
	}
	s := &StockSeries{Dates: dates, Turnover: turn, Amount: amount, CloseHfq: closeH}
	// STO20 = 恒 2
	// English: STO20 = constant 2.
	got := mustGet(t, "STO20").Compute(s)
	if math.Abs(got[20]-2) > 1e-12 {
		t.Fatalf("STO20 应 2，得 %v", got[20])
	}
	// STOA = ln(1000)
	// English: STOA = ln(1000).
	got = mustGet(t, "STOA").Compute(s)
	if math.Abs(got[20]-math.Log(1000)) > 1e-9 {
		t.Fatalf("STOA 应 %v，得 %v", math.Log(1000), got[20])
	}
	// Amihud：|r|/amount 每根 = |1/close[i-1]|/1000；末 20 均值
	// English: Amihud: per-bar |r|/amount = |1/close[i-1]|/1000; mean of last 20.
	var sum float64
	for i := 1; i < n; i++ {
		sum += math.Abs(closeH[i]/closeH[i-1]-1) / amount[i]
	}
	got = mustGet(t, "Amihud20").Compute(s)
	if math.Abs(got[20]-sum/20) > 1e-15 {
		t.Fatalf("Amihud20 应 %.3e，得 %.3e", sum/20, got[20])
	}
}

// itoa 将 0..25 映射为字母序号 a..z（测试辅助，构造日期占位）。
func itoa(i int) string {
	return string(rune('a' + i))
}

// mustGet 取注册因子（不存在则测试失败）。
// English: mustGet returns the registered factor (fails the test if not present).
func mustGet(t *testing.T, id string) Def {
	t.Helper()
	d, ok := Get(id)
	if !ok {
		t.Fatalf("因子 %s 未注册", id)
	}
	return d
}
