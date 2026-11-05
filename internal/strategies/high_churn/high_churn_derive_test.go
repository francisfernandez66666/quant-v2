package high_churn

// high_churn_derive_test.go —— 导出阈值与战法实际用的常量同源，且每一档在生产判定里
// 按导出的数生效（判据取自导出表本身，而不是把同一个数再写一遍）。

import (
	"math"
	"testing"

	"quant-trading-v2/internal/strategies/shortbase"
)

func hcFloat(t *testing.T, table map[string]interface{}, key string) float64 {
	t.Helper()
	raw, ok := table[key]
	if !ok {
		t.Fatalf("导出表缺键 %s", key)
	}
	switch v := raw.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		t.Fatalf("导出键 %s 类型不是数：%T", key, raw)
	}
	return 0
}

// TestDeriveThresholdTable 阈值表逐键锁死：导出值必须与战法源码里生产在用的常量一字不差
// （判据取自导出表、期望值取自常量定义——两边漂任何一边，这里就红，不给"派生腿悄悄换数"留缝）。
func TestDeriveThresholdTable(t *testing.T) {
	table := DeriveCarrierThresholds()
	want := map[string]float64{
		"hc_min_bars":           60,
		"hc_score_threshold":    60,
		"hc_pos_start":          0.92,
		"hc_pos_span":           0.08,
		"hc_pos_base_score":     15,
		"hc_pos_step_score":     15,
		"hc_position_max_score": 30,
		"hc_gain_start":         0.30,
		"hc_gain_span":          0.20,
		"hc_gain_base_score":    15,
		"hc_gain_step_score":    15,
		"hc_div_heavy_ratio":    1.5,
		"hc_div_stall_pct":      2.0,
		"hc_div_full_score":     40,
		"hc_div_half_score":     20,
		"hc_shadow_ratio":       0.4,
		"hc_shadow_score":       12,
		"hc_below_ma5_score":    10,
		"hc_consec_down_days":   2,
		"hc_consec_down_score":  8,
		"hc_weakness_max_score": 30,
	}
	for key, expected := range want {
		if got := hcFloat(t, table, key); math.Abs(got-expected) > 1e-9 {
			t.Errorf("%s 导出=%v 期望=%v", key, got, expected)
		}
	}
	for key := range table {
		if _, ok := want[key]; !ok {
			t.Errorf("导出表有多余键 %s（载体不读它，等于无主的数进产物）", key)
		}
	}
	if len(table) != len(want) {
		t.Errorf("导出键数=%d 期望=%d", len(table), len(want))
	}
	// 线性段自证：起点 + 跨度必须正好走到"记满分"那一档，否则分段与档位会各说一套。
	if math.Abs(hcFloat(t, table, "hc_pos_start")+hcFloat(t, table, "hc_pos_span")-1) > 1e-9 {
		t.Error("位置族：起评点 + 线性跨度应正好到 1.00（现价等于 60 日高）")
	}
	// 单边减半必须真是满分的一半，否则"减半"这个词在两个载体里意思不同。
	full := hcFloat(t, table, "hc_div_full_score")
	half := hcFloat(t, table, "hc_div_half_score")
	if math.Abs(full/2-half) > 1e-9 {
		t.Errorf("量价背离单边 %v 不是满分 %v 的一半", half, full)
	}
	// 走弱三项之和必须**不低于**帽：等于帽时帽是"收口"（三项齐备正好满分），
	// 低于帽则那枚帽永远不会起作用、也就不该再当成一个导出的阈值存在。
	weakSum := hcFloat(t, table, "hc_shadow_score") + hcFloat(t, table, "hc_below_ma5_score") +
		hcFloat(t, table, "hc_consec_down_score")
	if weakSum < hcFloat(t, table, "hc_weakness_max_score") {
		t.Errorf("走弱三项合计 %v 低于帽 %v：那枚帽永远不会起作用，形状与命名不符",
			weakSum, hcFloat(t, table, "hc_weakness_max_score"))
	}
}

// TestPositionScoreFollowsExportedBands 位置族：现价口径与涨幅口径都按导出的档位算。
func TestPositionScoreFollowsExportedBands(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	start := hcFloat(t, table, "hc_pos_start")
	span := hcFloat(t, table, "hc_pos_span")
	base := hcFloat(t, table, "hc_pos_base_score")
	step := hcFloat(t, table, "hc_pos_step_score")
	maxScore := hcFloat(t, table, "hc_position_max_score")

	if got := s.positionScore(&shortbase.Data{PosHigh: start - 0.01}); got != 0 {
		t.Errorf("低于起评点 %v 应为 0，实算 %v", start, got)
	}
	if got := s.positionScore(&shortbase.Data{PosHigh: start}); math.Abs(got-base) > 1e-9 {
		t.Errorf("正好在起评点应为 %v，实算 %v", base, got)
	}
	// 走完线性段（现价等于 60 日高）时基础分 + 步长分正好等于满分档。
	if got := s.positionScore(&shortbase.Data{PosHigh: start + span}); math.Abs(got-maxScore) > 1e-9 {
		t.Errorf("走完线性段应为满分 %v，实算 %v", maxScore, got)
	}
	// 线性段中点用 step 检验：base + (span/2)/span*step ＝ base+step/2（封顶未触发的中段）。
	// English: the mid-point pins the step dimension itself, so a wrong exported step can't hide.
	if got := s.positionScore(&shortbase.Data{PosHigh: start + span/2}); math.Abs(got-(base+step/2)) > 1e-9 {
		t.Errorf("线性段中点应为 base+step/2=%v，实算 %v", base+step/2, got)
	}
	if math.Abs(base+step-maxScore) > 1e-9 {
		t.Errorf("位置族形状：基础分 %v + 步长分 %v 应等于满分档 %v", base, step, maxScore)
	}
	gainStart := hcFloat(t, table, "hc_gain_start")
	gainSpan := hcFloat(t, table, "hc_gain_span")
	gainBase := hcFloat(t, table, "hc_gain_base_score")
	gainStep := hcFloat(t, table, "hc_gain_step_score")
	mid := gainStart + gainSpan/2
	want := gainBase + (mid-gainStart)/gainSpan*gainStep
	if got := s.positionScore(&shortbase.Data{Gain20: mid}); math.Abs(got-want) > 1e-9 {
		t.Errorf("涨幅口径线性段 %v 应为 %v，实算 %v", mid, want, got)
	}
	// 涨幅口径走完线性段后要封在位置族的帽上（不是无限上行）。
	if got := s.positionScore(&shortbase.Data{Gain20: gainStart + gainSpan*3}); math.Abs(got-maxScore) > 1e-9 {
		t.Errorf("涨幅口径超段应封顶 %v，实算 %v", maxScore, got)
	}
}

// TestDivergenceAndWeaknessUseExportedThresholds 背离档与走弱项按导出的阈值生效。
func TestDivergenceAndWeaknessUseExportedThresholds(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	heavy := hcFloat(t, table, "hc_div_heavy_ratio")
	stall := hcFloat(t, table, "hc_div_stall_pct")
	full := hcFloat(t, table, "hc_div_full_score")
	half := hcFloat(t, table, "hc_div_half_score")

	if got := s.divergenceScore(&shortbase.Data{VolRatio5_20: heavy, Last3RangePct: stall - 0.1}); got != full {
		t.Errorf("放量且滞涨应为满分 %v，实算 %v", full, got)
	}
	if got := s.divergenceScore(&shortbase.Data{VolRatio5_20: heavy, Last3RangePct: stall}); got != half {
		t.Errorf("滞涨判据是严格小于 %v：等于该档只算单边，应 %v，实算 %v", stall, half, got)
	}
	if got := s.divergenceScore(&shortbase.Data{VolRatio5_20: heavy - 0.1, Last3RangePct: stall - 0.1}); got != half {
		t.Errorf("只满足滞涨应为单边 %v，实算 %v", half, got)
	}
	if got := s.divergenceScore(&shortbase.Data{VolRatio5_20: 1, Last3RangePct: 9}); got != 0 {
		t.Errorf("两边都不满足应为 0，实算 %v", got)
	}

	shadowRatio := hcFloat(t, table, "hc_shadow_ratio")
	shadowScore := hcFloat(t, table, "hc_shadow_score")
	ma5Score := hcFloat(t, table, "hc_below_ma5_score")
	days := int(hcFloat(t, table, "hc_consec_down_days"))
	daysScore := hcFloat(t, table, "hc_consec_down_score")
	capScore := hcFloat(t, table, "hc_weakness_max_score")

	if got := s.weaknessScore(&shortbase.Data{UpperShadowPct: shadowRatio}); math.Abs(got-shadowScore) > 1e-9 {
		t.Errorf("上影到档（含等号）应加 %v，实算 %v", shadowScore, got)
	}
	if got := s.weaknessScore(&shortbase.Data{UpperShadowPct: shadowRatio - 0.01, BelowMA5: true,
		ConsecDownDays: days}); math.Abs(got-(ma5Score+daysScore)) > 1e-9 {
		t.Errorf("破5日+连阴应为 %v，实算 %v", ma5Score+daysScore, got)
	}
	if got := s.weaknessScore(&shortbase.Data{ConsecDownDays: days - 1}); got != 0 {
		t.Errorf("连阴不足 %d 天不应加分，实算 %v", days, got)
	}
	// 三项齐备必须被帽压住（否则 weakness_max_score 没主人）。
	if got := s.weaknessScore(&shortbase.Data{UpperShadowPct: shadowRatio, BelowMA5: true,
		ConsecDownDays: days}); math.Abs(got-capScore) > 1e-9 {
		t.Errorf("三项齐备应封在帽 %v，实算 %v", capScore, got)
	}
}
