package break_down

// break_down_derive_test.go —— 锁两件事：导出的阈值表与战法实际用的那批常量**同源**，
// 以及每一档边界在生产判定里真的按导出的数生效。
//
// 为什么要行为腿而不是只比数字：导出表抄自常量、常量又没人用，读数照样"绿"。
// 这里的判据是"用导出的数算一遍期望分，和 Evaluate/factor 函数实算的结果对表"——
// 谁把调用点改回内联字面量，这一组就会红。

import (
	"math"
	"testing"

	"quant-trading-v2/internal/strategies/shortbase"
)

// floatOf 把导出值取成 float64（载体侧只认数值；类型不对就是导出写错了形状）。
func floatOf(t *testing.T, table map[string]interface{}, key string) float64 {
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

// TestDeriveThresholdTable 阈值表逐键锁死：导出值必须与战法源码生产在用的常量一字不差
// （判据取自导出表、期望值另取自常量定义——两边漂任何一边这里就红，不给"派生腿悄悄换数"留缝）。
func TestDeriveThresholdTable(t *testing.T) {
	table := DeriveCarrierThresholds()
	want := map[string]float64{
		"bd_min_bars":                   25,
		"bd_score_threshold":            60,
		"bd_depth_max_score":            30,
		"bd_depth_single_cap":           25,
		"bd_depth_full_pct":             3,
		"bd_volume_max_score":           40,
		"bd_volume_full_ratio":          1.5,
		"bd_volume_half_ratio":          1,
		"bd_volume_shrink_factor":       0.5,
		"bd_intraday_max_score":         30,
		"bd_intraday_severe_pct":        -5,
		"bd_intraday_mid_pct":           -2,
		"bd_intraday_mid_base_score":    20,
		"bd_intraday_severe_span_pct":   3,
		"bd_intraday_severe_span_score": 10,
		"bd_intraday_flat_span_pct":     2,
		"bd_intraday_flat_span_score":   10,
		"bd_shadow_add_ratio":           0.5,
		"bd_shadow_add_score":           5,
		"bd_shakeout_depth_pct":         1,
		"bd_shakeout_discount":          0.7,
	}
	for key, expected := range want {
		if got := floatOf(t, table, key); math.Abs(got-expected) > 1e-9 {
			t.Errorf("%s 导出=%v 期望=%v", key, got, expected)
		}
	}
	// 键集合闭合：多一枚＝没人读的数混进产物，少一枚＝载体运行时 KeyError。
	for key := range table {
		if _, ok := want[key]; !ok {
			t.Errorf("导出表有多余键 %s（载体不读它，等于无主的数进产物）", key)
		}
	}
	if len(table) != len(want) {
		t.Errorf("导出键数=%d 期望=%d", len(table), len(want))
	}
}

// TestBandsMatchExportedValues 用导出的档位算期望分，和 intradayScore 实算逐点对表。
// 三个方向都要对上：跌破 severe 记满分、mid 与 severe 之间的线性段、0 到 mid 的浅跌段。
func TestBandsMatchExportedValues(t *testing.T) {
	table := DeriveCarrierThresholds()
	severe := floatOf(t, table, "bd_intraday_severe_pct")
	mid := floatOf(t, table, "bd_intraday_mid_pct")
	base := floatOf(t, table, "bd_intraday_mid_base_score")
	spanPct := floatOf(t, table, "bd_intraday_severe_span_pct")
	spanScore := floatOf(t, table, "bd_intraday_severe_span_score")
	flatPct := floatOf(t, table, "bd_intraday_flat_span_pct")
	flatScore := floatOf(t, table, "bd_intraday_flat_span_score")
	maxScore := floatOf(t, table, "bd_intraday_max_score")
	// 档位方向自证：severe 必须比 mid 更深（同号比较，负数更小＝跌得更狠）。
	if severe >= mid || mid >= 0 {
		t.Fatalf("跌幅档位方向错了：severe=%v mid=%v", severe, mid)
	}
	s := New(nil)
	cases := []struct {
		change float64
		want   float64
	}{
		{severe, maxScore},
		{severe - 1, maxScore},
		{mid, base},
		{(mid + severe) / 2, base + (-(mid+severe)/2-(-mid))/spanPct*spanScore},
		{-1, -(-1) / flatPct * flatScore},
		{0, 0},
		{1, 0},
	}
	for _, c := range cases {
		sd := &shortbase.Data{ChangePct: c.change}
		got := s.intradayScore(sd)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ChangePct=%v 实算=%v 按导出档位应为=%v", c.change, got, c.want)
		}
	}
}

// TestDepthAndVolumeCapsMatchExportedValues 深度族与量能族的帽与档，同样按导出的数对表。
func TestDepthAndVolumeCapsMatchExportedValues(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	depthMax := floatOf(t, table, "bd_depth_max_score")
	singleCap := floatOf(t, table, "bd_depth_single_cap")
	fullPct := floatOf(t, table, "bd_depth_full_pct")
	if singleCap >= depthMax {
		t.Fatalf("单破位上限 %v 不该不低于双破位满分 %v（帽高于满分＝帽没意义）", singleCap, depthMax)
	}
	both := &shortbase.Data{BreakMA20: true, BreakLow20: true, BreakDepthPct: fullPct}
	if got := s.depthScore(both); got != depthMax {
		t.Errorf("双破位应为满分 %v，实算 %v", depthMax, got)
	}
	deep := &shortbase.Data{BreakMA20: true, BreakDepthPct: fullPct}
	if got := s.depthScore(deep); got != singleCap {
		t.Errorf("单破位深跌应封在 %v，实算 %v", singleCap, got)
	}
	half := &shortbase.Data{BreakMA20: true, BreakDepthPct: fullPct / 2}
	if got, want := s.depthScore(half), fullPct/2/fullPct*depthMax; math.Abs(got-want) > 1e-9 {
		t.Errorf("单破位线性段 %v%% 应为 %v，实算 %v", fullPct/2, want, got)
	}

	volMax := floatOf(t, table, "bd_volume_max_score")
	fullRatio := floatOf(t, table, "bd_volume_full_ratio")
	halfRatio := floatOf(t, table, "bd_volume_half_ratio")
	shrink := floatOf(t, table, "bd_volume_shrink_factor")
	if got := s.volumeScore(&shortbase.Data{TodayVolVs5d: fullRatio}); got != volMax {
		t.Errorf("量比到 %v 应记满分 %v，实算 %v", fullRatio, volMax, got)
	}
	if got, want := s.volumeScore(&shortbase.Data{TodayVolVs5d: halfRatio}),
		halfRatio/fullRatio*volMax; math.Abs(got-want) > 1e-9 {
		t.Errorf("量比在单边档 %v 应为 %v，实算 %v", halfRatio, want, got)
	}
	thin := halfRatio / 2
	if got, want := s.volumeScore(&shortbase.Data{TodayVolVs5d: thin}),
		thin/fullRatio*volMax*shrink; math.Abs(got-want) > 1e-9 {
		t.Errorf("缩量破位应减半（系数 %v）＝%v，实算 %v", shrink, want, got)
	}
}

// TestShakeoutDiscountMatchesExportedValue 防洗盘那条缓判必须用导出的系数与深度档。
func TestShakeoutDiscountMatchesExportedValue(t *testing.T) {
	table := DeriveCarrierThresholds()
	discount := floatOf(t, table, "bd_shakeout_discount")
	depthPct := floatOf(t, table, "bd_shakeout_depth_pct")
	if discount <= 0 || discount >= 1 {
		t.Fatalf("缓判系数 %v 不在 (0,1)：它必须是打折而不是加分", discount)
	}
	s := New(nil)
	// 均线多头未死且破位浅于导出档 → 判洗盘；深一档就不该判（判据是严格小于）。
	shallow := &shortbase.Data{MA5: 11, MA10: 10, BreakMA20: true, BreakDepthPct: depthPct / 2}
	if !s.isShakeout(shallow) {
		t.Errorf("破位深度 %v 浅于 %v 且均线多头未死，应判洗盘缓判", depthPct/2, depthPct)
	}
	deep := &shortbase.Data{MA5: 11, MA10: 10, BreakMA20: true, BreakDepthPct: depthPct}
	if s.isShakeout(deep) {
		t.Errorf("破位深度到 %v 这一档就不该再算洗盘（判据是严格小于）", depthPct)
	}
}
