// F1 形态算子测试：验证放量/缩量/突破/回撤/多头排列各算子输出正确。
package factor

import (
	"math"
	"testing"
)

// mkMorphSeries 构造 60 根序列：前半段横盘（价量恒定），后半段放量上涨。
func mkMorphSeries() *StockSeries {
	n := 60
	s := &StockSeries{Dates: make([]string, n)}
	s.CloseHfq, s.Vol = make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		s.Dates[i] = "d"
		if i < 30 {
			s.CloseHfq[i] = 100
			s.Vol[i] = 1000
		} else {
			s.CloseHfq[i] = 100 + float64(i-30)
			s.Vol[i] = 2000
		}
	}
	return s
}

func TestVolSurge(t *testing.T) {
	s := mkMorphSeries()
	vals := volSurge(s)
	// 前 20 根预热期 NaN
	if !math.IsNaN(vals[10]) {
		t.Fatalf("预热期应为 NaN, got %.2f", vals[10])
	}
	// 后半段量翻倍：当日量/20日均量 ≈ 2（后半段 20 日均量约为 (10*1000+10*2000)/20=1500）
	// 第 40 根（后半段 10 根后）20日均量含 10 根 1000 + 10 根 2000 → avg=1500 → 2000/1500≈1.33
	// 第 55 根 20日均量全 2000 → avg=2000 → 2000/2000=1
	if v := vals[55]; v <= 0 || v > 1.5 {
		t.Fatalf("后半段 volSurge[55]=%.2f 期望 ~1", v)
	}
}

func TestVolShrink(t *testing.T) {
	s := mkMorphSeries()
	vals := volShrink(s)
	if !math.IsNaN(vals[10]) {
		t.Fatalf("预热期应为 NaN, got %.2f", vals[10])
	}
	// 后半段 5日均量与20日均量同为 2000 → 比值 1
	if v := vals[59]; v < 0.8 || v > 1.2 {
		t.Fatalf("后段 volShrink=%.2f 期望 ~1", v)
	}
}

func TestPriceBreakout(t *testing.T) {
	s := mkMorphSeries()
	vals := priceBreakout(20)(s)
	if !math.IsNaN(vals[10]) {
		t.Fatalf("预热期应为 NaN")
	}
	// 后半段持续创新高：第 31 根起价格递增，多数应为突破=1（除首根因之前横盘）
	foundBreak := false
	for i := 30; i < 60; i++ {
		if vals[i] == 1 {
			foundBreak = true
		}
	}
	if !foundBreak {
		t.Fatalf("后半段应出现突破信号")
	}
}

func TestDrawdown20(t *testing.T) {
	s := mkMorphSeries()
	vals := drawdown20(s)
	if !math.IsNaN(vals[10]) {
		t.Fatalf("预热期应为 NaN")
	}
	// 后半段创新高 → 回撤趋近 0
	if v := vals[59]; v > 0.01 {
		t.Fatalf("创新高后回撤应≈0, got %.3f", v)
	}
	// 前半段横盘后首根回撤应明显（从 100 涨到 101 时，20日高点=100 → 回撤 0 或负→clamp 为 0）
	// 注：drawdown20 用 1−close/高点，创新高则≤0；这里只验证非 NaN
	if math.IsNaN(vals[30]) {
		t.Fatalf("第30根不应为 NaN")
	}
}

func TestBullAlign(t *testing.T) {
	s := mkMorphSeries()
	vals := bullAlign(s)
	if !math.IsNaN(vals[10]) {
		t.Fatalf("预热期应为 NaN")
	}
	// 后半段上涨趋势，MA5>MA10>MA20 且收>MA5 → 多数应为 1
	found := false
	for i := 30; i < 60; i++ {
		if vals[i] == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("上涨趋势应出现多头排列信号")
	}
}

// TestMorphologyRegistered 形态算子已注册进 factor 库。
func TestMorphologyRegistered(t *testing.T) {
	for _, id := range []string{"VolSurge5", "VolShrink", "Brk20", "Brk60", "Drawdown20", "BullAlign"} {
		if _, ok := Get(id); !ok {
			t.Fatalf("形态算子 %s 未注册", id)
		}
	}
}
