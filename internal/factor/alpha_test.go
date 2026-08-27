// WorldQuant Alpha101 与 Alpha158 风格因子的单元测试（算子、动量/波动/流动性/反转）。
package factor

import (
	"math"
	"testing"
)

// seq 构造 n 根基础序列（收盘 10 起 +1，开高低收同序，量/额/换手恒定）。
// English: seq builds a base series of n bars (close starts at 10 and increments by 1; open/high/low follow the same order; volume/amount/turnover are constant).
func seq(n int) *StockSeries {
	dates := make([]string, n)
	closeH := make([]float64, n)
	open := make([]float64, n)
	high := make([]float64, n)
	low := make([]float64, n)
	vol := make([]float64, n)
	amount := make([]float64, n)
	turn := make([]float64, n)
	for i := 0; i < n; i++ {
		dates[i] = itoa(i)
		closeH[i] = 10 + float64(i)
		open[i] = 10 + float64(i) - 0.5
		high[i] = 10 + float64(i) + 1
		low[i] = 10 + float64(i) - 1
		vol[i] = 1000
		amount[i] = 10000
		turn[i] = 2
	}
	return &StockSeries{Dates: dates, CloseHfq: closeH, Open: open, High: high, Low: low, Vol: vol, Amount: amount, Turnover: turn}
}

// TestTSOps 算子单测：tsRank/tsArgMaxOffset/delta/rollingMax/Min。
// English: TestTSOps unit tests the operators: tsRank/tsArgMaxOffset/delta/rollingMax/Min.
func TestTSOps(t *testing.T) {
	xs := []float64{5, 1, 3, 2, 4, 0}
	// tsRank 窗口 3
	// English: tsRank window 3.
	got := tsRank(xs, 3)
	// i=2 窗口 [5,1,3]: less=1(仅1<3) eq=1(含自身) → 1.5/3
	// English: i=2 window [5,1,3]: less=1 (only 1<3) eq=1 (includes itself) -> 1.5/3.
	if math.Abs(got[2]-0.5) > 1e-12 {
		t.Fatalf("tsRank[2] 应 0.5, 得 %v", got[2])
	}
	// i=3 窗口 [1,3,2]: less=1(1<2) eq=1(含自身) → 0.5
	// English: i=3 window [1,3,2]: less=1 (1<2) eq=1 (includes itself) -> 0.5.
	if math.Abs(got[3]-0.5) > 1e-12 {
		t.Fatalf("tsRank[3] 应 0.5, 得 %v", got[3])
	}
	// i=5 窗口 [2,4,0]: less=0 eq=1(含自身) → 0.5/3
	// English: i=5 window [2,4,0]: less=0 eq=1 (includes itself) -> 0.5/3.
	if math.Abs(got[5]-1.0/6) > 1e-12 {
		t.Fatalf("tsRank[5] 应 1/6, 得 %v", got[5])
	}
	// 等值平手：窗口 [3,3,3] → less=0 eq=3 → 1.5/3
	// English: Equal-value tie: window [3,3,3] -> less=0 eq=3 -> 1.5/3.
	got = tsRank([]float64{1, 3, 3, 3, 2}, 3)
	if math.Abs(got[3]-0.5) > 1e-12 {
		t.Fatalf("tsRank 平手应 0.5, 得 %v", got[3])
	}
	// tsArgMaxOffset 窗口 3：i=3 窗口 [2,3,1]，最大值 3 在偏移 1 → 1/2
	// English: tsArgMaxOffset window 3: i=3 window [2,3,1], max value 3 is at offset 1 -> 1/2.
	got = tsArgMaxOffset([]float64{9, 2, 3, 1, 7, 8}, 3)
	if math.Abs(got[3]-0.5) > 1e-12 {
		t.Fatalf("tsArgMax[3] 应 0.5, 得 %v", got[3])
	}
	// i=4 窗口 [3,1,7] 最大为当日 7 → 偏移 0
	// English: i=4 window [3,1,7], the max is the current day's 7 -> offset 0.
	if math.Abs(got[4]) > 1e-12 {
		t.Fatalf("tsArgMax[4] 应 0, 得 %v", got[4])
	}
	// delta 窗口 1
	// English: delta window 1.
	got = deltaSeries([]float64{10, 12, 9}, 1)
	if !math.IsNaN(got[0]) || math.Abs(got[1]-2) > 1e-12 || math.Abs(got[2]+3) > 1e-12 {
		t.Fatalf("delta 不符: %v", got)
	}
	// rollingMax/Min
	// English: rollingMax/Min.
	got = rollingMax([]float64{1, 5, 3, 2}, 2)
	if !math.IsNaN(got[0]) || math.Abs(got[2]-5) > 1e-12 || math.Abs(got[3]-3) > 1e-12 {
		t.Fatalf("rollingMax 不符: %v", got)
	}
	got = rollingMin([]float64{1, 5, 3, 2}, 2)
	if !math.IsNaN(got[0]) || math.Abs(got[1]-1) > 1e-12 || math.Abs(got[2]-3) > 1e-12 {
		t.Fatalf("rollingMin 不符: %v", got)
	}
}

// TestAlpha158Momentum RSI/BBI/EMA10_20：恒涨序列下动量类应单调且终值可算。
// English: TestAlpha158Momentum RSI/BBI/EMA10_20: under a constantly rising series the momentum factors should be monotonic and their final values computable.
func TestAlpha158Momentum(t *testing.T) {
	s := seq(30)
	// RSI14：连续上涨 → 接近 100（首值在索引 14，需 15 根）
	// English: RSI14: consecutive rises -> near 100 (first valid value at index 14, needs 15 bars).
	got := mustGet(t, "RSI14").Compute(s)
	if got[14] != got[14] {
		t.Fatalf("RSI14 预热期结束（idx14）应有效")
	}
	if got[29] < 95 {
		t.Fatalf("连续上涨 RSI 应接近 100, 得 %v", got[29])
	}
	// BBI：终值 = (MA3+MA6+MA12+MA24)/4，30 根全预热完
	// English: BBI: final value = (MA3+MA6+MA12+MA24)/4, all 30 bars fully warmed up.
	got = mustGet(t, "BBI").Compute(s)
	if math.IsNaN(got[29]) {
		t.Fatalf("BBI 30 根应有效")
	}
	ma3 := smaLast(s.CloseHfq, 3)
	ma6 := smaLast(s.CloseHfq, 6)
	ma12 := smaLast(s.CloseHfq, 12)
	ma24 := smaLast(s.CloseHfq, 24)
	if math.Abs(got[29]-(ma3+ma6+ma12+ma24)/4) > 1e-9 {
		t.Fatalf("BBI 末值不符: %v", got[29])
	}
	// EMA10_20：恒涨 → 为正
	// English: EMA10_20: constantly rising -> positive.
	got = mustGet(t, "EMA10_20").Compute(s)
	if !(got[29] > 0) {
		t.Fatalf("恒涨 EMA10_20 应为正, 得 %v", got[29])
	}
	// 预热期 NaN（EMA20 首值在 idx19）
	// English: NaN during the warm-up period (EMA20's first valid value is at idx 19).
	if !math.IsNaN(got[18]) {
		t.Fatalf("EMA10_20 不足20根应 NaN")
	}
	if got[19] != got[19] {
		t.Fatalf("EMA10_20 idx19 起应有效")
	}
}

// TestAlpha158Volatility 波动率类：恒定序列为 0/1，区间比手算。
// English: TestAlpha158Volatility volatility factors: constant series give 0/1, range ratios hand-computed.
func TestAlpha158Volatility(t *testing.T) {
	n := 30
	consts := make([]float64, n)
	dates := make([]string, n)
	for i := range consts {
		consts[i] = 10
		dates[i] = itoa(i)
	}
	s := &StockSeries{Dates: dates, CloseHfq: consts, High: consts, Low: consts}
	// 恒定 → 已实现波动率 0
	// English: Constant -> realized volatility is 0.
	for _, id := range []string{"RealizedVol5", "RealizedVol10"} {
		got := mustGet(t, id).Compute(s)
		if !math.IsNaN(got[29]) && math.Abs(got[29]) > 1e-12 {
			t.Fatalf("%s 恒定序列应 0, 得 %v", id, got[29])
		}
	}
	// HighLow20 = 1（高低相等）
	// English: HighLow20 = 1 (high equals low).
	got := mustGet(t, "HighLow20").Compute(s)
	if math.Abs(got[29]-1) > 1e-12 {
		t.Fatalf("HighLow20 应 1, 得 %v", got[29])
	}
	// AtrRatio14：高低收恒定 → TR=0 → 0
	// English: AtrRatio14: constant high/low/close -> TR=0 -> 0.
	got = mustGet(t, "AtrRatio14").Compute(s)
	if math.Abs(got[29]) > 1e-12 {
		t.Fatalf("AtrRatio14 恒定应 0, 得 %v", got[29])
	}
	// 波动放大比：平静段 + 单日放量跳涨 → 该日 >1（异动放大），预热期 NaN
	// English: Volatility amplification ratio: calm segment + a single day of high-volume jump -> that day >1 (abnormal amplification), NaN during warm-up.
	closes := make([]float64, 25)
	ds := make([]string, 25)
	c := 100.0
	for i := 0; i < 20; i++ {
		closes[i] = c
		c *= 1.001
		ds[i] = itoa(i)
	}
	for i := 20; i < 25; i++ {
		closes[i] = c
		c *= 1.001
		ds[i] = itoa(i)
	}
	closes[20] = closes[19] * 1.05 // 单日 5% 跳涨
	// English: single-day 5% jump.
	spike := &StockSeries{Dates: ds, CloseHfq: closes}
	got = mustGet(t, "VolRatio5").Compute(spike)
	if !math.IsNaN(got[4]) {
		t.Fatalf("VolRatio5 预热期（不足5根）应 NaN")
	}
	if !(got[20] > 1) {
		t.Fatalf("跳涨日 VolRatio5 应 >1, 得 %v", got[20])
	}
}

// TestAlpha158Liquidity 量能类：恒定量 → VMA=1、VSTD=0、峰比/地比=1。
// English: TestAlpha158Liquidity volume factors: constant volume -> VMA=1, VSTD=0, peak/trough ratios=1.
func TestAlpha158Liquidity(t *testing.T) {
	s := seq(30)
	for _, id := range []string{"VMA5", "VMA10"} {
		got := mustGet(t, id).Compute(s)
		if math.Abs(got[29]-1) > 1e-9 {
			t.Fatalf("%s 恒量应 1, 得 %v", id, got[29])
		}
	}
	got := mustGet(t, "VSTD20").Compute(s)
	if math.Abs(got[29]) > 1e-12 {
		t.Fatalf("VSTD20 恒量应 0, 得 %v", got[29])
	}
	for _, id := range []string{"VMAX10", "VMIN10"} {
		got = mustGet(t, id).Compute(s)
		if math.Abs(got[29]-1) > 1e-12 {
			t.Fatalf("%s 恒量应 1, 得 %v", id, got[29])
		}
	}
	// TurnoverStd20 恒换手 → 0
	// English: TurnoverStd20 constant turnover -> 0.
	got = mustGet(t, "TurnoverStd20").Compute(s)
	if math.Abs(got[29]) > 1e-12 {
		t.Fatalf("TurnoverStd20 恒换手应 0, 得 %v", got[29])
	}
}

// TestAlpha101 WorldQuant 子集：Alpha4 反转方向 / Alpha12 量价背离 / Alpha101 区间位置。
// English: TestAlpha101 WorldQuant subset: Alpha4 reversal direction / Alpha12 volume-price divergence / Alpha101 range position.
func TestAlpha101(t *testing.T) {
	// Alpha4：新低 → 分位低 → 信号强（负值大）
	// English: Alpha4: new low -> low quantile -> strong signal (large negative value).
	low := []float64{5, 4, 3, 2, 1}
	s := &StockSeries{Dates: []string{"0", "1", "2", "3", "4"}, Low: low}
	got := mustGet(t, "Alpha4").Compute(s)
	// i=4 窗口 [3,2,1]? 不，窗口9不足5根——用 9 根
	// English: i=4 window [3,2,1]? No, window 9 is not enough for 5 bars -- use 9 bars.
	_ = got
	n := 9
	dates := make([]string, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	highs := make([]float64, n)
	vols := make([]float64, n)
	for i := 0; i < n; i++ {
		dates[i] = itoa(i)
		lows[i] = 10 - float64(i) // 新低
		// English: new low.
		closes[i] = 10
		highs[i] = 11
		vols[i] = 100
	}
	s = &StockSeries{Dates: dates, Low: lows, CloseHfq: closes, High: highs, Vol: vols}
	got = mustGet(t, "Alpha4").Compute(s)
	// i=8 窗口 9 根递减低价：当前为最低 → rank=(0+0.5×1)/9=1/18 → alpha=−1/18（接近 0 的弱反转）
	// English: i=8 window of 9 bars of decreasing lows: current is the lowest -> rank=(0+0.5x1)/9=1/18 -> alpha=-1/18 (weak reversal near 0).
	if math.IsNaN(got[8]) || math.Abs(got[8]+1.0/18) > 1e-9 {
		t.Fatalf("新低 Alpha4 应≈−1/18, 得 %v", got[8])
	}
	// Alpha12：量增价跌 → 正
	// English: Alpha12: rising volume with falling price -> positive.
	s = seq(5)
	got = mustGet(t, "Alpha12").Compute(s)
	// seq: close 恒涨、vol 恒量 → Δvol=0 → sign=0 → 全 0
	// English: seq: close constantly rises, vol is constant -> dvol=0 -> sign=0 -> all 0.
	if math.Abs(got[4]) > 1e-12 {
		t.Fatalf("恒量恒涨 Alpha12 应 0, 得 %v", got[4])
	}
	volUp := []float64{100, 200, 300}
	closeDown := []float64{10, 9, 8}
	ds := []string{"0", "1", "2"}
	s = &StockSeries{Dates: ds, Vol: volUp, CloseHfq: closeDown}
	got = mustGet(t, "Alpha12").Compute(s)
	// i=1: Δvol=+100 → 1；Δclose=−1 → −(−1)=1 → 1
	// English: i=1: dvol=+100 -> 1; dclose=-1 -> -(-1)=1 -> 1.
	if math.Abs(got[1]-1) > 1e-12 {
		t.Fatalf("量增价跌 Alpha12 应 1, 得 %v", got[1])
	}
	// i=2: 1×(−(−1))=1
	// English: i=2: 1x(-(-1))=1.
	if math.Abs(got[2]-1) > 1e-12 {
		t.Fatalf("Alpha12[2] 应 1, 得 %v", got[2])
	}
	// Alpha101：区间位置×量。close=低点 → pos=−1 → −vol；close=高点 → +vol
	// English: Alpha101: range position x volume. close=low -> pos=-1 -> -vol; close=high -> +vol.
	s = seq(3)
	got = mustGet(t, "Alpha101").Compute(s)
	// seq: close=10+i, high=close+1, low=close−1 → pos=0 → 0
	// English: seq: close=10+i, high=close+1, low=close-1 -> pos=0 -> 0.
	if math.Abs(got[0]) > 1e-12 {
		t.Fatalf("区间中点 Alpha101 应 0, 得 %v", got[0])
	}
	atLow := &StockSeries{Dates: []string{"0"}, CloseHfq: []float64{10}, High: []float64{12}, Low: []float64{10}, Vol: []float64{5}}
	got = mustGet(t, "Alpha101").Compute(atLow)
	// pos=(20−10−12)/2=−1 → −5
	// English: pos=(20-10-12)/2=-1 -> -5.
	if math.Abs(got[0]+5) > 1e-12 {
		t.Fatalf("低点 Alpha101 应 −5, 得 %v", got[0])
	}
	// Alpha1：恒涨 → 复合序列为 close²，5 日内最大值恒为当日 → offset=0
	// English: Alpha1: constantly rising -> composite series is close^2, the 5-day max is always the current day -> offset=0.
	up := seq(10)
	got = mustGet(t, "Alpha1").Compute(up)
	if math.Abs(got[9]) > 1e-12 {
		t.Fatalf("恒涨 Alpha1 应 0, 得 %v", got[9])
	}
}

// smaLast 手算 SMA 末值（测试辅助）。
// English: smaLast hand-computes the last SMA value (test helper).
func smaLast(xs []float64, n int) float64 {
	sum := 0.0
	for i := len(xs) - n; i < len(xs); i++ {
		sum += xs[i]
	}
	return sum / float64(n)
}
