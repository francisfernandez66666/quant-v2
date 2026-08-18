package indicator

import (
	"math"
	"testing"
)

// TestMACDGolden 校验 MACD(12,26,9) 的 DIF/DEA/Bar 与 golden 数据。
func TestMACDGolden(t *testing.T) {
	g := loadGolden(t)
	got := MACDDefault(g["close"])
	wantDIF, wantDEA, wantBar := g["macd_dif"], g["macd_dea"], g["macd_bar"]
	if len(got) != len(wantDIF) {
		t.Fatalf("长度不一致: got=%d want=%d", len(got), len(wantDIF))
	}
	for i := range got {
		check := func(name string, gotV, wantV float64) {
			if math.IsNaN(wantV) {
				if !math.IsNaN(gotV) {
					t.Errorf("idx %d %s: 期望 NaN，得 %v", i, name, gotV)
				}
			} else if math.Abs(gotV-wantV) > 1e-9 {
				t.Errorf("idx %d %s: 期望 %.10f，得 %.10f", i, name, wantV, gotV)
			}
		}
		check("DIF", got[i].DIF, wantDIF[i])
		check("DEA", got[i].DEA, wantDEA[i])
		check("Bar", got[i].Bar, wantBar[i])
	}
}

// TestMACDBarRelation 校验柱状图恒等关系 Bar=2×(DIF−DEA)。
func TestMACDBarRelation(t *testing.T) {
	g := loadGolden(t)
	got := MACDDefault(g["close"])
	for i := range got {
		want := 2 * (got[i].DIF - got[i].DEA)
		if math.Abs(got[i].Bar-want) > 1e-12 {
			t.Fatalf("idx %d Bar 不满足恒等式: %v != %v", i, got[i].Bar, want)
		}
	}
}

// TestMACDEdge 边界：长度不足 slow（26）应全 NaN；参数非法应全 NaN。
func TestMACDEdge(t *testing.T) {
	short := make([]float64, 10)
	got := MACDDefault(short)
	for i := range got {
		if !math.IsNaN(got[i].DIF) || !math.IsNaN(got[i].DEA) {
			t.Fatalf("长度不足应全 NaN: %+v", got[i])
		}
	}
	if got := MACD(short, 26, 12, 9); !math.IsNaN(got[0].DIF) {
		t.Fatalf("fast>=slow 应全 NaN: %+v", got[0])
	}
}