package indicator

import "testing"

// TestSMA5Golden 校验 SMA5 与 golden 数据。
// English: TestSMA5Golden verifies SMA5 against golden data.
func TestSMA5Golden(t *testing.T) {
	g := loadGolden(t)
	got := SMA(g["close"], 5)
	assertSeries(t, got, g["ma5"])
}

// TestSMA10Golden 校验 SMA10 与 golden 数据。
// English: TestSMA10Golden verifies SMA10 against golden data.
func TestSMA10Golden(t *testing.T) {
	g := loadGolden(t)
	got := SMA(g["close"], 10)
	assertSeries(t, got, g["ma10"])
}

// TestSMAEdge 边界：窗口大于序列长度应全 NaN；n<=0 应全 NaN。
// English: TestSMAEdge edge cases: window larger than series length should be all NaN; n<=0 should be all NaN.
func TestSMAEdge(t *testing.T) {
	g := loadGolden(t)
	got := SMA(g["close"], 1000)
	for i, v := range got {
		if !isNaN(v) {
			t.Fatalf("窗口超长 idx %d 应 NaN，得 %v", i, v)
		}
	}
	if got := SMA([]float64{1, 2, 3}, 0); len(got) != 3 || !isNaN(got[2]) {
		t.Fatalf("n<=0 应全 NaN: %v", got)
	}
	if got := SMA(nil, 5); len(got) != 0 {
		t.Fatalf("空输入应返回空: %v", got)
	}
}

// TestSMAHand 手算校验：1..10 的 MA5 末值为 8。
// English: TestSMAHand manual check: the last MA5 value of 1..10 is 8.
func TestSMAHand(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got := SMA(closes, 5)
	if got[4] != 3 || got[9] != 8 {
		t.Fatalf("SMA5 手算不符: got[4]=%v got[9]=%v", got[4], got[9])
	}
}

// TestEMAGolden 校验 EMA12 与 golden 数据。
// English: TestEMAGolden verifies EMA12 against golden data.
func TestEMAGolden(t *testing.T) {
	g := loadGolden(t)
	got := EMA(g["close"], 12)
	assertSeries(t, got, g["ema12"])
}

// TestEMAEdge 边界：长度不足窗口应全 NaN。
// English: TestEMAEdge edge case: length below the window should be all NaN.
func TestEMAEdge(t *testing.T) {
	if got := EMA([]float64{1, 2, 3}, 12); len(got) != 3 || !isNaN(got[2]) {
		t.Fatalf("长度不足应全 NaN: %v", got)
	}
}

func isNaN(v float64) bool { return v != v }
