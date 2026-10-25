// ic_test.go — §SIZING-EVO IC/IR 独立实现单测（口径与 research/ic.go 逐字对齐，
// 但零 import——架构测试另锁）。钉死：平均秩 Spearman、逐日分组/跳过规则、
// IR=mean/std（n-1）、NaN 弃权、分层归组。
package sizing_evo

import (
	"math"
	"testing"
)

// TestSpearmanICKnownValues 已知值钉死：完全正相关 1、完全负相关 -1、并列平均秩、
// NaN 跳过、样本不足/无变差 → NaN。
// English: known-value pins for the independent Spearman implementation.
func TestSpearmanICKnownValues(t *testing.T) {
	cases := []struct {
		name string
		f, r []float64
		want float64
	}{
		{"完全正相关", []float64{1, 2, 3}, []float64{10, 20, 30}, 1},
		{"完全负相关", []float64{1, 2, 3}, []float64{30, 20, 10}, -1},
		{"单调不等距仍为1", []float64{1, 2, 3}, []float64{1, 100, 101}, 1},
		{"含NaN跳过", []float64{1, math.NaN(), 3}, []float64{10, 999, 30}, 1},
		{"并列平均秩", []float64{1, 1, 2}, []float64{5, 7, 9}, 0.8660254037844386},
		{"样本不足", []float64{1, 2}, []float64{1, 2}, math.NaN()},
		{"无变差", []float64{1, 1, 1}, []float64{1, 2, 3}, math.NaN()},
	}
	for _, c := range cases {
		got := SpearmanIC(c.f, c.r)
		if math.IsNaN(c.want) {
			if !math.IsNaN(got) {
				t.Errorf("%s: got %v, want NaN", c.name, got)
			}
			continue
		}
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestICByDateGrouping 逐日分组：组内 <minN 跳过、NaN 侧跳过、输出按日期升序。
// English: per-date grouping with the skip rules and ascending output.
func TestICByDateGrouping(t *testing.T) {
	samples := []ICSample{
		// D1：3 个观测，完全正相关 → IC=1
		{Date: "20261022", Code: "A", Scale: 1, Ret: 0.01},
		{Date: "20261022", Code: "B", Scale: 2, Ret: 0.02},
		{Date: "20261022", Code: "C", Scale: 3, Ret: 0.03},
		// D2：只有 2 个观测 < minN=3 → 整日跳过
		{Date: "20261023", Code: "A", Scale: 1, Ret: 0.01},
		{Date: "20261023", Code: "B", Scale: 2, Ret: -0.02},
		// D3：3 个观测但一个 Ret=NaN → 剩 2 个 < minN → 跳过
		{Date: "20261024", Code: "A", Scale: 1, Ret: math.NaN()},
		{Date: "20261024", Code: "B", Scale: 2, Ret: 0.01},
		{Date: "20261024", Code: "C", Scale: 3, Ret: 0.02},
	}
	rows := ICByDate(samples, 3)
	if len(rows) != 1 {
		t.Fatalf("only D1 should survive, got %d rows: %+v", len(rows), rows)
	}
	if rows[0].Date != "20261022" || rows[0].N != 3 || math.Abs(rows[0].IC-1) > 1e-12 {
		t.Fatalf("D1 row = %+v, want date=20261022 n=3 ic=1", rows[0])
	}
}

// TestIRMeanStd IR = mean/std（n-1）；<2 行或零变差 → NaN。
// English: IR is mean over sample std; NaN below two rows or zero variance.
func TestIRMeanStd(t *testing.T) {
	if !math.IsNaN(IR(nil)) {
		t.Error("empty rows must be NaN")
	}
	if !math.IsNaN(IR([]ICRow{{Date: "d", IC: 0.5, N: 3}})) {
		t.Error("single row must be NaN (no std)")
	}
	rows := []ICRow{{IC: 0.2}, {IC: 0.4}, {IC: 0.6}}
	// mean=0.4, std=sqrt((0.04+0+0.04)/2)=0.2 → IR=2
	if got := IR(rows); math.Abs(got-2) > 1e-12 {
		t.Errorf("IR = %v, want 2", got)
	}
	if got := IR([]ICRow{{IC: 0.3}, {IC: 0.3}}); !math.IsNaN(got) {
		t.Errorf("zero variance must be NaN, got %v", got)
	}
	if got := MeanIC(rows); math.Abs(got-0.4) > 1e-12 {
		t.Errorf("MeanIC = %v, want 0.4", got)
	}
}

// TestICByState R10 分层：按 State 归组，各组独立算；空串独自成组。
// English: stratification by market state (R10).
func TestICByState(t *testing.T) {
	samples := []ICSample{
		{Date: "d1", Code: "A", Scale: 1, Ret: 0.01, State: "bull"},
		{Date: "d1", Code: "B", Scale: 2, Ret: 0.02, State: "bull"},
		{Date: "d1", Code: "C", Scale: 3, Ret: 0.03, State: "bull"},
		{Date: "d1", Code: "A", Scale: 3, Ret: 0.01, State: "bear"},
		{Date: "d1", Code: "B", Scale: 2, Ret: 0.02, State: "bear"},
		{Date: "d1", Code: "C", Scale: 1, Ret: 0.03, State: "bear"},
	}
	byState := ICByState(samples, 3)
	if len(byState) != 2 {
		t.Fatalf("want 2 state groups, got %d", len(byState))
	}
	if math.Abs(byState["bull"][0].IC-1) > 1e-12 {
		t.Errorf("bull IC = %v, want 1", byState["bull"][0].IC)
	}
	if math.Abs(byState["bear"][0].IC+1) > 1e-12 {
		t.Errorf("bear IC = %v, want -1", byState["bear"][0].IC)
	}
	// 空 state 也单独成组（不并入任何市场状态）。
	mixed := append(samples, ICSample{Date: "d1", Code: "D", Scale: 1, Ret: 0.01, State: ""},
		ICSample{Date: "d1", Code: "E", Scale: 2, Ret: 0.02, State: ""},
		ICSample{Date: "d1", Code: "F", Scale: 3, Ret: 0.03, State: ""})
	if got := ICByState(mixed, 3); len(got) != 3 {
		t.Fatalf("empty state must form its own group, got %d groups", len(got))
	}
}

// TestICByDateMinNDefault minN<=0 回落 3（与 SpearmanIC 最小对数同口径）。
func TestICByDateMinNDefault(t *testing.T) {
	samples := []ICSample{
		{Date: "d1", Scale: 1, Ret: 0.01},
		{Date: "d1", Scale: 2, Ret: 0.02},
	}
	if rows := ICByDate(samples, 0); len(rows) != 0 {
		t.Fatalf("2 obs below default minN=3 must be skipped, got %+v", rows)
	}
}
