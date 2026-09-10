package research

import (
	"math"
	"testing"
	"time"
)

func TestImpact_Confidence_RisingAndTrim(t *testing.T) {
	tb := NewImpactTable(3)
	// 灌入 3 个正样本（30 分钟窗中位 >0）
	for _, x := range []float64{0.06, 0.09, 0.12} {
		tb.Update("公司", "发酵", "利好", nil, []float64{x}, nil, 0)
	}
	c := tb.Confidence("公司", "发酵", "利好")
	if c < 1.0 {
		t.Errorf("positive history should boost confidence, got %.3f", c)
	}
	if c > 1.3 {
		t.Errorf("boost must cap at 1.3, got %.3f", c)
	}

	// 负样本 → 下修
	tb2 := NewImpactTable(3)
	for _, x := range []float64{-0.08, -0.11, -0.14} {
		tb2.Update("公司", "发酵", "利好", nil, []float64{x}, nil, 0)
	}
	c2 := tb2.Confidence("公司", "发酵", "利好")
	if c2 > 1.0 {
		t.Errorf("negative history should trim confidence, got %.3f", c2)
	}
	if c2 < 0.7 {
		t.Errorf("trim must floor at 0.7, got %.3f", c2)
	}
}

func TestImpact_Confidence_UnderSample_IsOne(t *testing.T) {
	tb := NewImpactTable(10)
	tb.Update("公司", "发酵", "利好", nil, []float64{0.2}, nil, 0) // 样本 1 < 10
	if c := tb.Confidence("公司", "发酵", "利好"); c != 1.0 {
		t.Errorf("below MinSample must be 1.0, got %.3f", c)
	}
}

func TestImpact_HalfLifeCalibration(t *testing.T) {
	tb := NewImpactTable(2)
	tb.Update("公司", "发酵", "利好", nil, []float64{0.05}, nil, 90)
	tb.Update("公司", "启动", "利好", nil, []float64{0.05}, nil, 70)
	hl := tb.HalfLife("公司", 120*time.Minute)
	if hl <= 0 || hl > 300*time.Minute {
		t.Errorf("calibrated half-life out of sane range, got %v", hl)
	}
	// 未知类型回退
	if f := tb.HalfLife("不存在类型", 120*time.Minute); f != 120*time.Minute {
		t.Errorf("unknown type should fallback, got %v", f)
	}
}

func TestImpact_ClampHalfLife(t *testing.T) {
	if c := clampHalfLife(5); c != 15*time.Minute {
		t.Errorf("lower clamp, got %v", c)
	}
	if c := clampHalfLife(9999); c != 300*time.Minute {
		t.Errorf("upper clamp, got %v", c)
	}
}

func TestImpact_Merge(t *testing.T) {
	a := NewImpactTable(1)
	a.Update("公司", "发酵", "利好", nil, []float64{0.05}, nil, 60)
	b := NewImpactTable(1)
	b.Update("公司", "发酵", "利好", nil, []float64{0.07}, nil, 80)
	a.Merge(b)
	if got := a.Confidence("公司", "发酵", "利好"); math.Abs(got-1.0) < 1e-9 {
		t.Errorf("merged bucket should reflect samples, got %.3f", got)
	}
	if got := a.HalfLife("公司", 0); got <= 0 {
		t.Errorf("merged half-life should exist, got %v", got)
	}
}

func TestMakeImpactKey(t *testing.T) {
	k := MakeImpactKey("行业", "高潮", "利好")
	want := "行业|高潮|利好"
	if string(k) != want {
		t.Errorf("key mismatch: %s vs %s", k, want)
	}
	if k2 := MakeImpactKey(" 公司 ", "退潮", "LITONG"); string(k2) != "公司|退潮|LITONG" {
		t.Errorf("key normalization mismatch: %s", k2)
	}
}
