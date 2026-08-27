// 量能/收益率类指标单元测试：成交量 MA、store.Bar→序列 提取、简单/对数收益与滚动波动率。
package indicator

import (
	"math"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestVolMAGolden 校验成交量 MA5 与 golden 数据。
// English: TestVolMAGolden verifies volume MA5 against golden data.
func TestVolMAGolden(t *testing.T) {
	g := loadGolden(t)
	got := VolMA(g["volume"], 5)
	assertSeries(t, got, g["vma5"])
}

// TestBarAdapters 校验 store.Bar → 序列 的提取函数。
// English: TestBarAdapters verifies the extraction functions from store.Bar to series.
func TestBarAdapters(t *testing.T) {
	bars := []store.Bar{
		{Date: "20200102", Open: 1, High: 2, Low: 0.5, Close: 1.5, Vol: 100, Amount: 1000},
		{Date: "20200103", Open: 1.5, High: 2.5, Low: 1, Close: 2, Vol: 200, Amount: 3000},
	}
	if got := ClosesOf(bars); len(got) != 2 || got[0] != 1.5 || got[1] != 2 {
		t.Fatalf("ClosesOf 不符: %v", got)
	}
	if got := HighsOf(bars); got[0] != 2 || got[1] != 2.5 {
		t.Fatalf("HighsOf 不符: %v", got)
	}
	if got := LowsOf(bars); got[0] != 0.5 || got[1] != 1 {
		t.Fatalf("LowsOf 不符: %v", got)
	}
	if got := VolumesOf(bars); got[0] != 100 || got[1] != 200 {
		t.Fatalf("VolumesOf 不符: %v", got)
	}
}

// TestReturns 校验简单/对数收益率与滚动波动率。
// English: TestReturns verifies simple/log returns and rolling volatility.
func TestReturns(t *testing.T) {
	closes := []float64{10, 11, 9.9}
	r := Returns(closes)
	if !math.IsNaN(r[0]) || math.Abs(r[1]-0.1) > 1e-12 || math.Abs(r[2]-(9.9/11-1)) > 1e-12 {
		t.Fatalf("Returns 不符: %v", r)
	}
	lr := LogReturns(closes)
	if !math.IsNaN(lr[0]) || math.Abs(lr[1]-math.Log(1.1)) > 1e-12 {
		t.Fatalf("LogReturns 不符: %v", lr)
	}
	// 滚动波动率：恒定序列标准差为 0
	// English: Rolling volatility: standard deviation of a constant series is 0
	const5 := make([]float64, 10)
	for i := range const5 {
		const5[i] = 1
	}
	rs := RollingStd(const5, 5)
	if rs[4] != 0 {
		t.Fatalf("恒定序列波动率应 0: %v", rs[4])
	}
	if !math.IsNaN(rs[3]) {
		t.Fatalf("预热期应 NaN: %v", rs[3])
	}
	// 手算：{1,2,3} 的总体标准差 = sqrt((1+0+1)/3)=sqrt(2/3)
	// English: By hand: population std dev of {1,2,3} = sqrt((1+0+1)/3)=sqrt(2/3)
	rs2 := RollingStd([]float64{1, 2, 3, 4, 5, 6}, 3)
	want := math.Sqrt(2.0 / 3.0)
	if math.Abs(rs2[2]-want) > 1e-12 {
		t.Fatalf("波动率手算不符: %v != %v", rs2[2], want)
	}
}
