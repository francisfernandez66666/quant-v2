// 研究引擎纯函数测试（无 DB）：SUE、IC/IR、分层。
package research

import (
	"math"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

func TestSpearmanIC(t *testing.T) {
	if ic := SpearmanIC([]float64{1, 2, 3, 4, 5}, []float64{10, 20, 30, 40, 50}); math.Abs(ic-1) > 1e-9 {
		t.Fatalf("同序期望 1，得 %v", ic)
	}
	if ic := SpearmanIC([]float64{1, 2, 3, 4, 5}, []float64{50, 40, 30, 20, 10}); math.Abs(ic+1) > 1e-9 {
		t.Fatalf("反序期望 -1，得 %v", ic)
	}
	if ic := SpearmanIC([]float64{1, math.NaN(), 2, 3, 4}, []float64{5, 99, 6, 7, 8}); math.Abs(ic-1) > 1e-9 {
		t.Fatalf("NaN 跳过期望 1，得 %v", ic)
	}
	// 并列处理：{1,1,2} vs {1,2,3} → 秩 {1.5,1.5,3} 与 {1,2,3} 的皮尔逊相关 = 0.866
	if ic := SpearmanIC([]float64{1, 1, 2}, []float64{1, 2, 3}); math.Abs(ic-0.8660254) > 1e-6 {
		t.Fatalf("并列平均秩期望 0.866，得 %v", ic)
	}
	if !isNaN(SpearmanIC([]float64{1, 2}, []float64{3, 4})) {
		t.Fatal("不足 3 对期望 NaN")
	}
	if !isNaN(SpearmanIC([]float64{1, 1, 1, 1}, []float64{3, 4, 5, 6})) {
		t.Fatal("无变差期望 NaN")
	}
}

// mkPanel 构造测试面板：fvals 为因子值，closes 为收盘（末位用于算前瞻收益）。
func mkPanel(dates []string, fvals, closes []float64) *Panel {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	return &Panel{
		Series:  &factor.StockSeries{Dates: dates, CloseHfq: closes},
		DateIdx: idx,
		Factors: map[string][]float64{"f": fvals},
	}
}

func TestICByDate(t *testing.T) {
	// 3 只股票在 20230103 形成完全同序截面 → IC=1；其中一只后续日期越界不影响
	p1 := mkPanel([]string{"20230103", "20230104", "20230105", "20230106"},
		[]float64{1, 2, 3, 4}, []float64{100, 100, 105, 100})
	p2 := mkPanel([]string{"20230103", "20230104"},
		[]float64{3, 4}, []float64{100, 105})
	p3 := mkPanel([]string{"20230103"},
		[]float64{5}, []float64{100, 106})
	rows := ICByDate([]*Panel{p1, p2, p3}, "f", 1, 3)
	found := false
	for _, r := range rows {
		if r.Date == "20230103" {
			found = true
			if math.Abs(r.IC-1) > 1e-9 || r.N != 3 {
				t.Fatalf("20230103 期望 IC=1 N=3，得 %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 20230103 的 IC 行，得 %+v", rows)
	}
}

func TestSingleQuarterNetProfitYoy(t *testing.T) {
	income := []store.IncomeRow{
		{EndDate: "20200331", NIncomeAttrP: 10},
		{EndDate: "20200630", NIncomeAttrP: 30},
		{EndDate: "20200930", NIncomeAttrP: 60},
		{EndDate: "20201231", NIncomeAttrP: 100},
		{EndDate: "20210331", NIncomeAttrP: 12},
		{EndDate: "20210630", NIncomeAttrP: 34},
		{EndDate: "20210930", NIncomeAttrP: 67},
	}
	got := SingleQuarterNetProfitYoy(income)
	expect := []float64{nan(), nan(), nan(), nan(), 0.2, 0.1, 0.1}
	for i, e := range expect {
		if isNaN(e) {
			if !isNaN(got[i]) {
				t.Fatalf("idx %d 期望 NaN，得 %v", i, got[i])
			}
			continue
		}
		if math.Abs(got[i]-e) > 1e-9 {
			t.Fatalf("idx %d 期望 %v，得 %v", i, e, got[i])
		}
	}
}

func TestLayerReturns(t *testing.T) {
	p1 := mkPanel([]string{"20230103"}, []float64{1}, []float64{100, 101})
	p2 := mkPanel([]string{"20230103"}, []float64{2}, []float64{100, 102})
	layers := LayerReturns([]*Panel{p1, p2}, "f", 1, 2, 2)
	if len(layers) != 2 {
		t.Fatalf("期望 2 层，得 %d", len(layers))
	}
	if math.Abs(layers[0].MeanReturn-0.01) > 1e-9 || layers[0].N != 1 {
		t.Fatalf("层0 期望 0.01/N1，得 %+v", layers[0])
	}
	if math.Abs(layers[1].MeanReturn-0.02) > 1e-9 || layers[1].N != 1 {
		t.Fatalf("层1 期望 0.02/N1，得 %+v", layers[1])
	}
	if mono, dir := Monotonic(layers); !mono || dir != 1 {
		t.Fatalf("期望单调递增，得 mono=%v dir=%d", mono, dir)
	}
}

func TestIR(t *testing.T) {
	rows := []ICRow{{Date: "a", IC: 0.1}, {Date: "b", IC: 0.2}, {Date: "c", IC: 0.3}}
	ir := IR(rows)
	if math.Abs(ir-2.4494897) > 1e-4 {
		t.Fatalf("期望 IR≈2.449，得 %v", ir)
	}
	if !isNaN(IR(nil)) {
		t.Fatal("空输入期望 NaN")
	}
}