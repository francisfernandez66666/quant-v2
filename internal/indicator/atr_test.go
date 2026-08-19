package indicator

import (
	"math"
	"testing"
)

// TestATRGolden 校验 ATR14 与 TrueRange 与 golden 数据。
// English: TestATRGolden verifies ATR14 and TrueRange against golden data.
func TestATRGolden(t *testing.T) {
	g := loadGolden(t)
	got := ATR14(g["high"], g["low"], g["close"])
	assertSeries(t, got, g["atr"])
	gotTR := TrueRange(g["high"], g["low"], g["close"])
	assertSeries(t, gotTR, g["atr_tr"])
}

// TestATRFirst 首根 TR 应为 H−L。
// English: TestATRFirst checks that the first TR equals H-L.
func TestATRFirst(t *testing.T) {
	g := loadGolden(t)
	tr := TrueRange(g["high"], g["low"], g["close"])
	want := g["high"][0] - g["low"][0]
	if math.Abs(tr[0]-want) > 1e-12 {
		t.Fatalf("TR[0] 应=%v，得 %v", want, tr[0])
	}
}

// TestATRFormulas 手算首值：ATR14 首值为前 14 根 TR 的简单平均。
// English: TestATRFormulas hand-computes the first value: ATR14's first value is the simple average of the first 14 TR values.
func TestATRFormulas(t *testing.T) {
	g := loadGolden(t)
	tr := TrueRange(g["high"], g["low"], g["close"])
	var s float64
	for i := 0; i < 14; i++ {
		s += tr[i]
	}
	atr := ATR14(g["high"], g["low"], g["close"])
	if math.Abs(atr[13]-(s/14)) > 1e-12 {
		t.Fatalf("ATR[13] 应=前14 TR 均值 %v，得 %v", s/14, atr[13])
	}
}

// TestATREdge 边界：长度不足应全 NaN。
// English: TestATREdge edge case: insufficient length should yield all NaN.
func TestATREdge(t *testing.T) {
	got := ATR14(make([]float64, 5), make([]float64, 5), make([]float64, 5))
	for _, v := range got {
		if !math.IsNaN(v) {
			t.Fatalf("长度不足应全 NaN: %v", v)
		}
	}
}
