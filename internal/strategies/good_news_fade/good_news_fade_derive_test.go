package good_news_fade

// good_news_fade_derive_test.go —— 导出阈值与战法实际用的常量同源，且事件年龄、
// 兑现幅度、防误杀窗口三族都按导出的数生效。

import (
	"math"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/shortbase"
)

func gfFloat(t *testing.T, table map[string]interface{}, key string) float64 {
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
		"gf_min_bars":             20,
		"gf_score_threshold":      60,
		"gf_event_fresh_age":      1,
		"gf_event_stale_age":      5,
		"gf_event_max_score":      25,
		"gf_event_min_score":      5,
		"gf_event_age_span":       4,
		"gf_event_age_drop":       20,
		"gf_gain_start":           0.10,
		"gf_gain_full":            0.25,
		"gf_gain_span":            0.15,
		"gf_gain_max_score":       35,
		"gf_stall_pct":            1.0,
		"gf_stall_score":          15,
		"gf_heavy_ratio":          1.5,
		"gf_heavy_score":          10,
		"gf_below_ma5_score":      8,
		"gf_shadow_ratio":         0.4,
		"gf_shadow_score":         7,
		"gf_weakness_max_score":   40,
		"gf_surge_window":         3,
		"gf_surge_step_pct":       0.05,
		"gf_propagation_discount": 0.7,
	}
	for key, expected := range want {
		if got := gfFloat(t, table, key); math.Abs(got-expected) > 1e-9 {
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
	// 年龄族的形状：满分减掉"跌幅"正好等于保底分，跨度正好是从新鲜档到陈旧档。
	if math.Abs(gfFloat(t, table, "gf_event_max_score")-gfFloat(t, table, "gf_event_age_drop")-
		gfFloat(t, table, "gf_event_min_score")) > 1e-9 {
		t.Error("事件年龄族：满分 - 衰减量应正好等于保底分")
	}
	if math.Abs(gfFloat(t, table, "gf_event_stale_age")-gfFloat(t, table, "gf_event_fresh_age")-
		gfFloat(t, table, "gf_event_age_span")) > 1e-9 {
		t.Error("事件年龄族：陈旧档 - 新鲜档应正好等于线性跨度")
	}
	// 兑现幅度同理：起评点 + 跨度 = 记满分那一档。
	if math.Abs(gfFloat(t, table, "gf_gain_start")+gfFloat(t, table, "gf_gain_span")-
		gfFloat(t, table, "gf_gain_full")) > 1e-9 {
		t.Error("兑现族：起评点 + 线性跨度应正好等于记满分那一档")
	}
	// 传导折扣必须真是打折（开区间 0~1）。
	if d := gfFloat(t, table, "gf_propagation_discount"); d <= 0 || d >= 1 {
		t.Errorf("传导折扣 %v 不在 (0,1)", d)
	}
}

// TestEventAgeDecayFollowsExportedBands 年龄衰减三档（新鲜/线性/陈旧）按导出的数走。
func TestEventAgeDecayFollowsExportedBands(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	fresh := int(gfFloat(t, table, "gf_event_fresh_age"))
	stale := int(gfFloat(t, table, "gf_event_stale_age"))
	maxScore := gfFloat(t, table, "gf_event_max_score")
	minScore := gfFloat(t, table, "gf_event_min_score")
	span := gfFloat(t, table, "gf_event_age_span")
	drop := gfFloat(t, table, "gf_event_age_drop")

	if got := s.eventScore(&shortbase.Data{EventAgeDays: fresh}); got != maxScore {
		t.Errorf("年龄=%d 应为满分 %v，实算 %v", fresh, maxScore, got)
	}
	if got := s.eventScore(&shortbase.Data{EventAgeDays: stale}); got != minScore {
		t.Errorf("年龄=%d 应为保底 %v，实算 %v", stale, minScore, got)
	}
	if got := s.eventScore(&shortbase.Data{EventAgeDays: stale + 3}); got != minScore {
		t.Errorf("超过陈旧档应仍是保底（不许变负），实算 %v", got)
	}
	mid := fresh + 2
	want := maxScore - (float64(mid)-float64(fresh))/span*drop
	if got := s.eventScore(&shortbase.Data{EventAgeDays: mid}); math.Abs(got-want) > 1e-9 {
		t.Errorf("年龄=%d 线性段应为 %v，实算 %v", mid, want, got)
	}
}

// TestGainAndWeaknessBandsFollowExportedValues 兑现幅度与高位转弱两族按导出的档位生效。
func TestGainAndWeaknessBandsFollowExportedValues(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	start := gfFloat(t, table, "gf_gain_start")
	full := gfFloat(t, table, "gf_gain_full")
	span := gfFloat(t, table, "gf_gain_span")
	maxScore := gfFloat(t, table, "gf_gain_max_score")
	eventClose := 100.0

	// 低于起评点：兑现分 0（"利好没被炒起来"不该算派发）。
	if got := s.gainSinceEvent(&shortbase.Data{EventDayClose: eventClose,
		Price: eventClose * (1 + start - 0.01)}); got != 0 {
		t.Errorf("涨幅未起评应为 0，实算 %v", got)
	}
	if got := s.gainSinceEvent(&shortbase.Data{EventDayClose: eventClose,
		Price: eventClose * (1 + start)}); math.Abs(got) > 1e-9 {
		// 判据是 `g < start → 0`，正好在起评点时会落进线性段算出 0（浮点上有 1e-14 量级的尾巴）。
		t.Errorf("正好在起评点应算作 0 分一侧，实算 %v", got)
	}
	if got := s.gainSinceEvent(&shortbase.Data{EventDayClose: eventClose,
		Price: eventClose * (1 + full)}); math.Abs(got-maxScore) > 1e-9 {
		t.Errorf("涨到记满分那一档应为 %v，实算 %v", maxScore, got)
	}
	midGain := start + span/2
	want := (midGain - start) / span * maxScore
	if got := s.gainSinceEvent(&shortbase.Data{EventDayClose: eventClose,
		Price: eventClose * (1 + midGain)}); math.Abs(got-want) > 1e-9 {
		t.Errorf("兑现线性段 %v 应为 %v，实算 %v", midGain, want, got)
	}
	// 事件收盘价为 0 时不能除（Go 直接返回 0，不是"算出一个巨大的数"）。
	if got := s.gainSinceEvent(&shortbase.Data{EventDayClose: 0, Price: 10}); got != 0 {
		t.Errorf("事件收盘缺失应 0 分，实算 %v", got)
	}

	stall := gfFloat(t, table, "gf_stall_pct")
	stallScore := gfFloat(t, table, "gf_stall_score")
	heavy := gfFloat(t, table, "gf_heavy_ratio")
	heavyScore := gfFloat(t, table, "gf_heavy_score")
	ma5Score := gfFloat(t, table, "gf_below_ma5_score")
	shadowRatio := gfFloat(t, table, "gf_shadow_ratio")
	shadowScore := gfFloat(t, table, "gf_shadow_score")
	cap := gfFloat(t, table, "gf_weakness_max_score")

	if got := s.weaknessScore(&shortbase.Data{Last3RangePct: stall - 0.1,
		VolRatio5_20: heavy, BelowMA5: true, UpperShadowPct: shadowRatio}); math.Abs(got-cap) > 1e-9 {
		t.Errorf("四项齐备应封在帽 %v，实算 %v", cap, got)
	}
	if sum := stallScore + heavyScore + ma5Score + shadowScore; math.Abs(sum-cap) > 1e-9 {
		t.Errorf("四项之和应正好等于帽 %v（当前导出表形状），实为 %v", cap, sum)
	}
	// 滞涨判据是严格小于：等于该档不算滞涨，只可能剩破5日与上影两项。
	if got := s.weaknessScore(&shortbase.Data{Last3RangePct: stall,
		VolRatio5_20: heavy}); got != 0 {
		t.Errorf("近3日涨幅=%v（非严格小于）不应记滞涨分，实算 %v", stall, got)
	}
	// 放量滞涨的"放量"只在滞涨分支内部起作用（Go 的形状），单边放量不加分。
	if got := s.weaknessScore(&shortbase.Data{Last3RangePct: stall + 1,
		VolRatio5_20: heavy + 1}); got != 0 {
		t.Errorf("未滞涨时放量不该加分，实算 %v", got)
	}
	if got := s.weaknessScore(&shortbase.Data{Last3RangePct: stall - 0.1,
		VolRatio5_20: heavy}); math.Abs(got-(stallScore+heavyScore)) > 1e-9 {
		t.Errorf("滞涨且放量（含等号）应为 %v，实算 %v", stallScore+heavyScore, got)
	}
}

// TestFreshSurgeWindowAndStepAreExported 防误杀那两枚（看几根、涨多少）必须按导出的数生效。
// gfSurgeMinBars = 窗口 + 1：相邻涨幅要"多看一根"，这是结构而不是第二个阈值。
func TestFreshSurgeWindowAndStepAreExported(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	window := int(gfFloat(t, table, "gf_surge_window"))
	step := gfFloat(t, table, "gf_surge_step_pct")
	if window <= 0 {
		t.Fatalf("窗口必须为正，实为 %d", window)
	}
	if gfSurgeMinBars != window+1 {
		t.Errorf("最少根数 %d 应正好是窗口 %d 加一根（相邻涨幅要多看一眼前一根）",
			gfSurgeMinBars, window)
	}
	n := window + 4
	closes := make([]float64, n)
	for i := range closes {
		closes[i] = 10
	}
	surge := 1 + step // 正好到档（Go 判 >=，含等号）
	kl := make([]data.KLine, n)
	for i := range kl {
		kl[i] = data.KLine{Close: closes[i]}
	}
	// 窗口外（更早一根）来一个涨停：不该判成"主升未止"。
	kl[n-window-2] = data.KLine{Close: closes[n-window-3] * surge}
	if s.freshSurge(&shortbase.Data{KLines: kl}) {
		t.Errorf("窗口 %d 之外的涨幅不应触发防误杀", window)
	}
	// 挪进窗口最后一根：触发。
	kl = make([]data.KLine, n)
	for i := range kl {
		kl[i] = data.KLine{Close: closes[i]}
	}
	kl[n-1] = data.KLine{Close: closes[n-2] * surge}
	if !s.freshSurge(&shortbase.Data{KLines: kl}) {
		t.Errorf("窗口内且涨幅到档（含等号）应触发防误杀")
	}
	// 差一档就不触发（导出的那枚 step 是判据本身）。
	kl[n-1] = data.KLine{Close: closes[n-2] * (1 + step*0.5)}
	if s.freshSurge(&shortbase.Data{KLines: kl}) {
		t.Errorf("涨幅未到档（step=%v 的一半）不应触发防误杀", step)
	}
	// 根数不足那一带：只认导出表里没有的 gfSurgeMinBars，行为是不判。
	if s.freshSurge(&shortbase.Data{KLines: make([]data.KLine, gfSurgeMinBars-1)}) {
		t.Errorf("根数不足 %d 时不应判成主升未止", gfSurgeMinBars)
	}
}
