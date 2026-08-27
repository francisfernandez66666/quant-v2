// KDJ 指标单元测试：校验 RSV/K/D/J 序列、J=3K−2D 恒等式与中性区间 RSV=50。
package indicator

import (
	"math"
	"testing"
)

// TestKDJGolden 校验 KDJ(9,3,3) 的 RSV/K/D/J 与 golden 数据。
// English: TestKDJGolden verifies KDJ(9,3,3)'s RSV/K/D/J against golden data.
func TestKDJGolden(t *testing.T) {
	g := loadGolden(t)
	got := KDJDefault(g["close"], g["high"], g["low"])
	want := map[string][]float64{
		"RSV": g["kdj_rsv"], "K": g["kdj_k"], "D": g["kdj_d"], "J": g["kdj_j"],
	}
	if len(got) != len(g["kdj_rsv"]) {
		t.Fatalf("长度不一致: got=%d want=%d", len(got), len(g["kdj_rsv"]))
	}
	for i := range got {
		vals := map[string]float64{"RSV": got[i].RSV, "K": got[i].K, "D": got[i].D, "J": got[i].J}
		for name, wantSeries := range want {
			gotV := vals[name]
			wantV := wantSeries[i]
			if math.IsNaN(wantV) {
				if !math.IsNaN(gotV) {
					t.Errorf("idx %d %s: 期望 NaN，得 %v", i, name, gotV)
				}
			} else if math.Abs(gotV-wantV) > 1e-9 {
				t.Errorf("idx %d %s: 期望 %.10f，得 %.10f", i, name, wantV, gotV)
			}
		}
	}
}

// TestKDJJRelation 校验 J=3K−2D 恒等式。
// English: TestKDJJRelation verifies the identity J=3K-2D.
func TestKDJJRelation(t *testing.T) {
	g := loadGolden(t)
	got := KDJDefault(g["close"], g["high"], g["low"])
	for i := range got {
		if want := 3*got[i].K - 2*got[i].D; math.Abs(got[i].J-want) > 1e-12 {
			t.Fatalf("idx %d J 不满足恒等式: %v != %v", i, got[i].J, want)
		}
	}
}

// TestKDJFlatRange 区间为 0 时 RSV=50（中性）。
// English: TestKDJFlatRange: when the range is 0, RSV=50 (neutral).
func TestKDJFlatRange(t *testing.T) {
	closes := []float64{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}
	got := KDJDefault(closes, closes, closes)
	if got[0].RSV != 50 {
		t.Fatalf("区间为 0 RSV 应 50，得 %v", got[0].RSV)
	}
	// 全部 10 → RSV 恒 50 → K 从 50 平滑收敛到 50（浮点误差容忍）
	// English: All values 10 -> RSV stays 50 -> K smoothly converges from 50 back to 50 (tolerating floating-point error).
	if math.Abs(got[9].K-50) > 1e-9 {
		t.Fatalf("恒定序列 K 应 50，得 %v", got[9].K)
	}
}
