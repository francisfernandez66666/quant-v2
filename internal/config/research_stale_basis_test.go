// research_stale_basis_test.go §ADJ-BASIS-2 配置键 rules.research.stale_adj_basis_action 的回归：
// 未配置（nil）/空串/未知值一律归一为 "shadow"（缺省绝不 fail-close——停一条在跑的战法是资金行为变更，
// 必须 owner 显式写 disable 才生效），显式 "disable" 才返回 disable。
// English: the stale-adjustment-basis knob defaults to "shadow" for absent/empty/unknown values; only an
// explicit "disable" fail-closes.
package config

import (
	"encoding/json"
	"testing"
)

// sptr 取字符串指针（构造 *string 可选位）。
func sptr(s string) *string { return &s }

// TestStaleAdjBasisActionDefaultsShadow 零值/空串/未知值 → shadow。
func TestStaleAdjBasisActionDefaultsShadow(t *testing.T) {
	var zero ResearchConfig
	if got := zero.StaleAdjBasisOrDefault(); got != "shadow" {
		t.Fatalf("未配置应为 shadow, got %s", got)
	}
	for _, v := range []string{"", "  ", "off", "STOP", "disble"} {
		c := ResearchConfig{StaleAdjBasisActionConfig: sptr(v)}
		if got := c.StaleAdjBasisOrDefault(); got != "shadow" {
			t.Fatalf("取值 %q 应归一为 shadow（未知值不得变成停战法）, got %s", v, got)
		}
	}
}

// TestStaleAdjBasisActionExplicitDisable 显式 disable（含首尾空格）→ disable。
func TestStaleAdjBasisActionExplicitDisable(t *testing.T) {
	c := ResearchConfig{StaleAdjBasisActionConfig: sptr(" disable ")}
	if got := c.StaleAdjBasisOrDefault(); got != "disable" {
		t.Fatalf("显式 disable 应生效, got %s", got)
	}
}

// TestStaleAdjBasisActionJSONPath 键路径必须是 rules.research.stale_adj_basis_action，且往返保真。
func TestStaleAdjBasisActionJSONPath(t *testing.T) {
	var wrapper struct {
		Rules Rules `json:"rules"`
	}
	raw := `{"rules":{"research":{"stale_adj_basis_action":"disable"}}}`
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatal(err)
	}
	if got := wrapper.Rules.Research.StaleAdjBasisOrDefault(); got != "disable" {
		t.Fatalf("按 rules.research.stale_adj_basis_action 解析失败, got %s", got)
	}
	b, err := json.Marshal(wrapper.Rules)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	res, ok := back["research"].(map[string]any)
	if !ok || res["stale_adj_basis_action"] != "disable" {
		t.Fatalf("序列化未回写该键: %s", b)
	}
	// 老配置（整段缺失）解析后仍为 nil ⇒ shadow，不改变既有行为。
	var old Rules
	if err := json.Unmarshal([]byte(`{"emotion_cycle":{}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Research.StaleAdjBasisActionConfig != nil {
		t.Fatal("老配置不应出现合成默认值")
	}
	if got := old.Research.StaleAdjBasisOrDefault(); got != "shadow" {
		t.Fatalf("老配置应为 shadow, got %s", got)
	}
}
