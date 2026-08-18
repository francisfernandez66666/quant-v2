package indicator

import (
	"math"
	"testing"
)

// TestRSIGolden 校验 RSI14 与 golden 数据。
func TestRSIGolden(t *testing.T) {
	g := loadGolden(t)
	got := RSI14(g["close"])
	assertSeries(t, got, g["rsi14"])
}

// TestRSIMonotonicUp 全部上涨的序列 RSI=100（无亏损）。
func TestRSIMonotonicUp(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	got := RSI14(closes)
	for i := 14; i < len(got); i++ {
		if got[i] != 100 {
			t.Fatalf("单边上涨 idx %d RSI 应 100，得 %v", i, got[i])
		}
	}
}

// TestRSIMonotonicDown 全部下跌的序列 RSI→0。
func TestRSIMonotonicDown(t *testing.T) {
	closes := []float64{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	got := RSI14(closes)
	for i := 14; i < len(got); i++ {
		if got[i] != 0 {
			t.Fatalf("单边下跌 idx %d RSI 应 0，得 %v", i, got[i])
		}
	}
}

// TestRSIEdge 边界：长度不足应全 NaN。
func TestRSIEdge(t *testing.T) {
	got := RSI14(make([]float64, 5))
	for _, v := range got {
		if !math.IsNaN(v) {
			t.Fatalf("长度不足应全 NaN: %v", v)
		}
	}
}