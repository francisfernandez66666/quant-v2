package indicator

import (
	"math"
	"testing"
)

// TestBOLLGolden 校验 BOLL(20,2) 的 中/上/下轨 与 golden 数据。
func TestBOLLGolden(t *testing.T) {
	g := loadGolden(t)
	got := BOLLDefault(g["close"])
	want := map[string][]float64{"Mid": g["boll_mid"], "Up": g["boll_up"], "Low": g["boll_low"]}
	if len(got) != len(g["boll_mid"]) {
		t.Fatalf("长度不一致: got=%d want=%d", len(got), len(g["boll_mid"]))
	}
	for i := range got {
		vals := map[string]float64{"Mid": got[i].Mid, "Up": got[i].Up, "Low": got[i].Low}
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

// TestBOLLSymmetry 上下轨以中轨对称（Up+Low=2×Mid）。
func TestBOLLSymmetry(t *testing.T) {
	g := loadGolden(t)
	got := BOLLDefault(g["close"])
	for i := range got {
		if math.IsNaN(got[i].Mid) {
			continue
		}
		if math.Abs(got[i].Up+got[i].Low-2*got[i].Mid) > 1e-9 {
			t.Fatalf("idx %d 上下轨不对称: %v", i, got[i])
		}
	}
}

// TestBOLLConstant 恒定序列标准差为 0，三轨相等。
func TestBOLLConstant(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 10
	}
	got := BOLLDefault(closes)
	if got[29].Mid != 10 || got[29].Up != 10 || got[29].Low != 10 {
		t.Fatalf("恒定序列三轨应相等: %+v", got[29])
	}
}