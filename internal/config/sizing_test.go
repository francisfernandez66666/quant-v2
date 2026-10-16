package config

import (
	"encoding/json"
	"testing"
)

// TestSizingConfigDefaultsSafe §DYN-SIZE：出厂默认必须是"安全侧"——总开关关、影子开。
func TestSizingConfigDefaultsSafe(t *testing.T) {
	d := DefaultSizingConfig()
	if d.Enabled {
		t.Fatalf("出厂默认必须 Enabled=false（零行为变更）")
	}
	if !d.ShadowOn() {
		t.Fatalf("出厂默认必须 Shadow=true（只留痕）")
	}
	if !d.RiskBudgetKeepOn() {
		t.Fatalf("放宽止损默认必须强制同比例缩仓（owner 裁决）")
	}
	if d.HolidayBlockNewOn() {
		t.Fatalf("长假禁开仓默认必须关（owner 风险偏好项）")
	}
	if d.Floor != 0.15 || d.Cap != 1.15 {
		t.Fatalf("地板/上限默认应为 0.15/1.15，got %v/%v", d.Floor, d.Cap)
	}
	if d.SellBoostLevel != "L2" {
		t.Fatalf("卖出加权默认档应为 L2，got %q", d.SellBoostLevel)
	}
}

// TestSizingConfigZeroValueWithDefaults 零值（存量配置缺键）经 WithDefaults 后等价于出厂默认。
func TestSizingConfigZeroValueWithDefaults(t *testing.T) {
	var zero SizingConfig
	got := zero.WithDefaults()
	want := DefaultSizingConfig()
	if got.Floor != want.Floor || got.Cap != want.Cap || got.KellyLambda != want.KellyLambda {
		t.Fatalf("零值补齐后应与出厂默认一致，got %+v", got)
	}
	if got.StoplossGuardCount != want.StoplossGuardCount {
		t.Fatalf("连续止损阈值应补齐为 %d，got %d", want.StoplossGuardCount, got.StoplossGuardCount)
	}
	if len(got.HolidayScales) != len(want.HolidayScales) {
		t.Fatalf("节前分级表应补齐，got %v", got.HolidayScales)
	}
	if !got.ShadowOn() {
		t.Fatalf("零值 Shadow 应视为 true")
	}
	if !got.RiskBudgetKeepOn() {
		t.Fatalf("零值 SLOffsetRiskBudgetKeep 应视为 true")
	}
}

// TestSizingConfigPartialOverride 部分覆盖：用户只改关心的键，其余走默认；默认表不被污染。
func TestSizingConfigPartialOverride(t *testing.T) {
	base := DefaultSizingConfig()
	baseLen := len(base.HolidayScales)

	in := SizingConfig{
		Enabled:       true,
		Floor:         0.25,
		HolidayScales: map[string]float64{"weekend": 0.50},
	}
	got := in.WithDefaults()
	if got.Floor != 0.25 {
		t.Fatalf("显式 Floor 应保留，got %v", got.Floor)
	}
	if got.HolidayScales["weekend"] != 0.50 {
		t.Fatalf("显式覆盖应生效，got %v", got.HolidayScales["weekend"])
	}
	if got.HolidayScales["long1"] != 0.40 {
		t.Fatalf("未覆盖的键应保留默认 0.40，got %v", got.HolidayScales["long1"])
	}
	// 不得污染出厂默认表（WithDefaults 必须返回新映射）。
	if len(DefaultSizingConfig().HolidayScales) != baseLen {
		t.Fatalf("WithDefaults 污染了出厂默认映射")
	}
	if DefaultSizingConfig().HolidayScales["weekend"] != 0.85 {
		t.Fatalf("出厂默认 weekend 应恒为 0.85，got %v", DefaultSizingConfig().HolidayScales["weekend"])
	}
}

// TestSizingConfigJSONRoundTrip JSON 键与零迁移：旧配置无 sizing 段 → 零值 → 安全默认。
func TestSizingConfigJSONRoundTrip(t *testing.T) {
	// 旧配置文件（无 sizing 段）反序列化后必须落到安全默认。
	var rules Rules
	if err := json.Unmarshal([]byte(`{"position":{"max_total_position_pct":0}}`), &rules); err != nil {
		t.Fatalf("反序列化旧配置失败: %v", err)
	}
	if rules.Sizing.Enabled {
		t.Fatalf("旧配置无 sizing 段时应为 Enabled=false")
	}
	got := rules.Sizing.WithDefaults()
	if got.Floor != 0.15 || got.Cap != 1.15 {
		t.Fatalf("旧配置补齐后应为出厂默认，got floor=%v cap=%v", got.Floor, got.Cap)
	}

	// 显式配置可往返。
	in := SizingConfig{Enabled: true, Shadow: boolPtr(false), Floor: 0.2, Cap: 1.3, SellBoostLevel: "L3"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var out SizingConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if !out.Enabled || out.ShadowOn() || out.Floor != 0.2 || out.Cap != 1.3 || out.SellBoostLevel != "L3" {
		t.Fatalf("往返不一致: %+v (raw=%s)", out, raw)
	}
}

// TestNormalizeLegacyTotalPosPct §DYN-SIZE P6：既有死字段 PositionConfig.MaxTotalPositionPct
// 归一化为比例。两种历史写法（0.8 与 80）都必须落到同一个 0.8——否则 80 会让
// `totalPosCap` 的 limit/max(limit,risk) 恒等于 1.0（限额大到永不约束，接了个寂寞）。
func TestNormalizeLegacyTotalPosPct(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},     // 未设置 → 0（调用方回落 SizingConfig 自己的默认 0.80）
		{-1, 0},    // 非法 → 0
		{0.5, 0.5}, // 比例写法原样
		{0.8, 0.8},
		{1, 1},     // 边界：恰好 1（满仓上限）仍按比例
		{80, 0.8},  // 百分数写法 → ÷100
		{100, 1.0}, // 100% → 1.0
		{120, 1.2}, // 超出区间仍按百分数归一（>1 即百分数约定），由调用方钳制
	}
	for _, c := range cases {
		if got := NormalizeLegacyTotalPosPct(c.in); got != c.want {
			t.Errorf("NormalizeLegacyTotalPosPct(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
