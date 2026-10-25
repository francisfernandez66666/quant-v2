// sizing_flow_test.go — §FLOW-FACTOR / §SIZING-EVO 配置契约测试
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.4/§2.5/§2.11）。
//
// 钉死三类语义：
//  1. 出厂默认档位值（数值口径与文档逐字一致，改动必须走文档修订）；
//  2. 指针三态：nil=开/显式 false=关、Evo nil=关/显式 true=开（"没配过"≠"显式关"）；
//  3. WithDefaults 合并：用户键覆盖默认键，未写键继承出厂值；空 RatioBasis 回落 float_mcap。
package config

import "testing"

// TestFlowFactorDefaults 出厂默认档位与文档 §2.4/§2.5 逐字对齐。
// English: factory band values match the spec verbatim.
func TestFlowFactorDefaults(t *testing.T) {
	d := DefaultFlowFactorConfig()
	if d.RatioBasis != "float_mcap" {
		t.Fatalf("RatioBasis = %q, want float_mcap", d.RatioBasis)
	}
	// 5 日主因子：强入 1.10 / 轻入 1.05 / 中性 1.00 / 轻出 0.85 / 强出 0.70
	want5D := map[string]float64{
		FlowBandStrongIn: 1.10, FlowBandMildIn: 1.05, FlowBandNeutral: 1.00,
		FlowBandMildOut: 0.85, FlowBandStrongOut: 0.70,
	}
	for k, v := range want5D {
		if d.Buy5DScales[k] != v {
			t.Errorf("Buy5DScales[%s] = %v, want %v", k, d.Buy5DScales[k], v)
		}
	}
	// 当日辅因子幅度减半：1.05/1.025/1.00/0.95/0.90
	wantToday := map[string]float64{
		FlowBandStrongIn: 1.05, FlowBandMildIn: 1.025, FlowBandNeutral: 1.00,
		FlowBandMildOut: 0.95, FlowBandStrongOut: 0.90,
	}
	for k, v := range wantToday {
		if d.BuyTodayScales[k] != v {
			t.Errorf("BuyTodayScales[%s] = %v, want %v", k, d.BuyTodayScales[k], v)
		}
	}
	// 卖出侧权重=买入偏离折半：强出买 0.70→卖 0.85（即 +0.15/2 的中和不足），轻出 0.95
	if d.SellScales[FlowBandStrongOut] != 0.85 || d.SellScales[FlowBandMildOut] != 0.95 {
		t.Errorf("SellScales = %v, want strong_out 0.85 / mild_out 0.95", d.SellScales)
	}
	// 分档阈值（%）：强入 3 / 轻入 1 / 轻出 -1 / 强出 -3
	for k, v := range map[string]float64{"strong_in_pct": 3, "mild_in_pct": 1, "mild_out_pct": -1, "strong_out_pct": -3} {
		if d.Buy5DBands[k] != v {
			t.Errorf("Buy5DBands[%s] = %v, want %v", k, d.Buy5DBands[k], v)
		}
	}
}

// TestFlowFactorPointerTriState 指针三态：nil=开、显式 false=关；SellEnabled 可独立关。
// English: nil = on, explicit false = off; the sell switch is independent.
func TestFlowFactorPointerTriState(t *testing.T) {
	var zero SizingConfig // 全零：两个指针都是 nil
	if !zero.FlowFactorOn() {
		t.Error("FlowFactorOn: nil should mean on")
	}
	if !zero.FlowSellOn() {
		t.Error("FlowSellOn: nil should mean on")
	}
	off := false
	s := SizingConfig{FlowFactor: FlowFactorConfig{Enabled: &off, SellEnabled: &off}}
	if s.FlowFactorOn() {
		t.Error("FlowFactorOn: explicit false must stay off")
	}
	if s.FlowSellOn() {
		t.Error("FlowSellOn: explicit false must stay off")
	}
	// 只关卖出侧，买入侧仍开
	s2 := SizingConfig{FlowFactor: FlowFactorConfig{SellEnabled: &off}}
	if !s2.FlowFactorOn() || s2.FlowSellOn() {
		t.Error("sell-only off must keep the buy side on")
	}
}

// TestFlowFactorWithDefaultsMerge 合并语义：用户键覆盖默认键、未写键继承出厂值。
// English: user keys override, unwritten keys inherit the factory values.
func TestFlowFactorWithDefaultsMerge(t *testing.T) {
	in := FlowFactorConfig{
		RatioBasis: "amount",
		Buy5DScales: map[string]float64{
			FlowBandStrongIn: 1.25, // 覆盖
		},
	}
	out := in.WithDefaults()
	if out.RatioBasis != "amount" {
		t.Errorf("explicit RatioBasis=amount lost: %q", out.RatioBasis)
	}
	if out.Buy5DScales[FlowBandStrongIn] != 1.25 {
		t.Errorf("user override lost: %v", out.Buy5DScales[FlowBandStrongIn])
	}
	if out.Buy5DScales[FlowBandNeutral] != 1.00 {
		t.Errorf("unwritten key should inherit factory 1.00, got %v", out.Buy5DScales[FlowBandNeutral])
	}
	if len(out.BuyTodayScales) != 5 {
		t.Errorf("untouched table should inherit all 5 keys, got %d", len(out.BuyTodayScales))
	}
	// 空 RatioBasis 回落 float_mcap
	out2 := FlowFactorConfig{}.WithDefaults()
	if out2.RatioBasis != "float_mcap" {
		t.Errorf("empty RatioBasis should fall back to float_mcap, got %q", out2.RatioBasis)
	}
	// 未识别值同样回落（访问器口径）
	out3 := FlowFactorConfig{RatioBasis: "weird"}.WithDefaults()
	if got := (SizingConfig{FlowFactor: out3}).FlowRatioBasisOf(); got != "float_mcap" {
		t.Errorf("FlowRatioBasisOf(weird) = %q, want float_mcap", got)
	}
	// 原映射不被就地修改（值接收+mergeScales 拷贝）
	if in.Buy5DScales[FlowBandNeutral] != 0 {
		t.Error("WithDefaults must not write into the caller's map")
	}
}

// TestSizingWithDefaultsWiresFlowAndEvo SizingConfig.WithDefaults 必须串联两个子配置。
// English: the parent WithDefaults must wire both sub-configs through.
func TestSizingWithDefaultsWiresFlowAndEvo(t *testing.T) {
	// 空 SizingConfig 走一遍 WithDefaults：FlowFactor 应拿到全套出厂档位，Evo 补运行参数。
	out := SizingConfig{}.WithDefaults()
	if out.FlowFactor.Buy5DScales[FlowBandStrongIn] != 1.10 {
		t.Errorf("FlowFactor not wired: %v", out.FlowFactor.Buy5DScales)
	}
	if out.FlowFactor.RatioBasis != "float_mcap" {
		t.Errorf("FlowFactor.RatioBasis = %q", out.FlowFactor.RatioBasis)
	}
	if out.Evo.Schedule != "weekly" || out.Evo.MinSamples != 200 || out.Evo.ObservationDays != 20 {
		t.Errorf("Evo not wired: %+v", out.Evo)
	}
	// 用户显式覆盖要活过合并
	on := true
	out2 := SizingConfig{FlowFactor: FlowFactorConfig{Enabled: &on}, Evo: EvoConfig{Enabled: &on}}.WithDefaults()
	if !out2.FlowFactorOn() {
		t.Error("explicit flow enable lost")
	}
	if !out2.EvoOn() {
		t.Error("explicit evo enable lost")
	}
}

// TestEvoDefaultsAndTriState Evo 缺省关、显式才开；AutoPromote nil=false。
// English: evo defaults off; only an explicit true enables it.
func TestEvoDefaultsAndTriState(t *testing.T) {
	var zero SizingConfig
	if zero.EvoOn() {
		t.Error("EvoOn: nil must mean OFF (unlike the factors)")
	}
	if zero.EvoAutoPromoteOn() {
		t.Error("EvoAutoPromoteOn: nil must mean false")
	}
	on := true
	e := SizingConfig{Evo: EvoConfig{Enabled: &on, AutoPromote: &on}}
	if !e.EvoOn() || !e.EvoAutoPromoteOn() {
		t.Error("explicit evo enable must stick")
	}
	off := false
	e2 := SizingConfig{Evo: EvoConfig{Enabled: &off}}
	if e2.EvoOn() {
		t.Error("explicit evo disable must stick")
	}
}

// TestEvoSearchBoundsOverride 搜索围栏覆盖：显式写入的键原样保留（sizing_evo 侧只允许
// 在默认围栏内收紧）；未写的键=内置默认（nil 映射）。
// English: explicit search-bound overrides survive; unwritten keys use the built-in rails.
func TestEvoSearchBoundsOverride(t *testing.T) {
	in := EvoConfig{SearchBounds: map[string][]float64{
		"flow_factor.buy_5d_scales.strong_in": {1.0, 1.08},
	}}
	out := in.WithDefaults()
	if got := out.SearchBounds["flow_factor.buy_5d_scales.strong_in"]; len(got) != 2 || got[0] != 1.0 || got[1] != 1.08 {
		t.Errorf("SearchBounds override lost: %v", out.SearchBounds)
	}
	// WithDefaults 不注入默认围栏（nil=用 sizing_evo 内置 rails），也不就地改调用方映射。
	if len(EvoConfig{}.WithDefaults().SearchBounds) != 0 {
		t.Error("default SearchBounds must stay empty (built-in rails)")
	}
	if in.SearchBounds["flow_factor.buy_5d_scales.strong_in"][1] != 1.08 {
		t.Error("WithDefaults must not mutate the caller's map")
	}
}
