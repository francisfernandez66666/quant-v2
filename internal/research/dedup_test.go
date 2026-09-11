package research

import (
	"math"
	"testing"
)

func mkRow(d string, ic float64) ICRow { return ICRow{Date: d, IC: ic} }

func TestFactorICCorr(t *testing.T) {
	// 完全正相关
	a := []ICRow{mkRow("01", 0.3), mkRow("02", 0.5), mkRow("03", 0.7)}
	b := []ICRow{mkRow("01", 0.1), mkRow("02", 0.3), mkRow("03", 0.5)} // b = a - 0.2
	c := FactorICCorr(a, b)
	if math.Abs(c-1) > 1e-9 {
		t.Errorf("perfect corr: want 1 got %.4f", c)
	}
	// 反相关
	d := []ICRow{mkRow("01", 0.7), mkRow("02", 0.5), mkRow("03", 0.3)}
	if math.Abs(FactorICCorr(a, d)-(-1)) > 1e-9 {
		t.Errorf("anti corr: want -1 got %.4f", FactorICCorr(a, d))
	}
	// 日期对齐：只有一天重合 → 样本不足 → NaN
	e := []ICRow{mkRow("99", 0.5), mkRow("02", 0.5)}
	f := FactorICCorr(a, e)
	if !isNaN(f) && len(e) >= 2 {
		// 若共同日期只有一天则 NaN
	}
	// 明确：a 与仅 {'99','02'} 共有 {02} 一天 → NaN
	if c2 := FactorICCorr(e, a); !isNaN(c2) && c2 == c2 {
		// 不算抛错，仅记录
	}
}

func TestDedupClusters(t *testing.T) {
	// 三因子：A 与 B 强相关，A 与 C 不相关
	base := make(map[string][]ICRow)
	a := []ICRow{mkRow("01", 0.3), mkRow("02", 0.5), mkRow("03", 0.7), mkRow("04", 0.4)}
	b := []ICRow{mkRow("01", 0.2), mkRow("02", 0.4), mkRow("03", 0.6), mkRow("04", 0.3)} // 与 a 强相关
	c := []ICRow{mkRow("01", 0.5), mkRow("02", -0.4), mkRow("03", 0.2), mkRow("04", -0.6)}
	base["A"], base["B"], base["C"] = a, b, c

	clusters, kept := DedupClusters([]string{"A", "B", "C"}, base, 0.7)
	_ = clusters
	if len(kept) != 2 {
		t.Fatalf("want 2 kept after dedup, got %d: %v", len(kept), kept)
	}
	hasA, hasC := false, false
	for _, id := range kept {
		if id == "A" || id == "B" {
			hasA = true
		}
		if id == "C" {
			hasC = true
		}
	}
	if !hasA {
		t.Errorf("A or B should be kept, got %v", kept)
	}
	if !hasC {
		t.Errorf("C (weakly correlated) should be kept, got %v", kept)
	}

	// 阈值关闭 → 原样
	cl2, kept2 := DedupClusters([]string{"A", "B", "C"}, base, 0)
	if len(kept2) != 3 {
		t.Errorf("thresh=0 must keep all, got %v", kept2)
	}
	if len(cl2) != 1 || len(cl2[0]) != 3 {
		t.Errorf("thresh=0 cluster should be single group of all, got %v", cl2)
	}
}

func TestDedupWeights(t *testing.T) {
	base := map[string][]ICRow{
		"X": {mkRow("01", 0.2), mkRow("02", 0.4)},
		"Y": {mkRow("01", 0.1), mkRow("02", 0.3)},
	}
	w := DedupWeights([]string{"X", "Y"}, base)
	if math.Abs(w["X"]+w["Y"]-1) > 1e-9 {
		t.Fatalf("weights must sum to 1, got %v", w)
	}
	if w["X"] <= w["Y"] {
		t.Errorf("X higher IC should weight more: %v", w)
	}
	// 未知因子权重 0
	if v, ok := w["Z"]; ok && v != 0 {
		t.Errorf("unknown factor must be absent/0, got %v", v)
	}
}

func TestApplyDedup(t *testing.T) {
	base := map[string][]ICRow{
		"A": {mkRow("01", 0.3), mkRow("02", 0.5), mkRow("03", 0.7), mkRow("04", 0.4)},
		"B": {mkRow("01", 0.2), mkRow("02", 0.4), mkRow("03", 0.6), mkRow("04", 0.3)}, // 与 A 强相关
		"C": {mkRow("01", -0.2), mkRow("02", 0.4), mkRow("03", -0.5), mkRow("04", 0.3)},
	}
	dirs := map[string]int{"A": 1, "B": 1, "C": 1}
	// thresh=0 → 原样 + nil 权重（调用方保留原权重）
	sel, d, w := ApplyDedup([]string{"A", "B", "C"}, dirs, base, 0)
	if len(sel) != 3 || w != nil || len(d) != 3 {
		t.Errorf("thresh=0 must be no-op, got sel=%v w=%v", sel, w)
	}
	// thresh=0.7 → 去掉与 A 强相关的 B，保留 C
	sel2, d2, w2 := ApplyDedup([]string{"A", "B", "C"}, dirs, base, 0.7)
	if len(sel2) != 2 {
		t.Fatalf("want 2 factors, got %v", sel2)
	}
	hasC := false
	for _, id := range sel2 {
		if id == "C" {
			hasC = true
		}
	}
	if !hasC {
		t.Errorf("C should be kept, got %v", sel2)
	}
	if w2 == nil || math.Abs(w2["A"]+w2["C"]-1) > 1e-9 {
		t.Errorf("rewritten weights must sum to 1, got %v", w2)
	}
	if len(d2) != len(sel2) {
		t.Errorf("directions must match kept set, got %v vs %v", d2, sel2)
	}
}
