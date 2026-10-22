// sizing_account_test.go — §A0 账号级仓位配置的读/写/回退锁。
//
// 五条锁：
//  1. 账号专用键优先，且 A/B 两账号互不影响；
//  2. 无专用键时回退账号旧 rules.sizing，再回退全局；
//  3. 旧字段 rules.position.max_total_position_pct 在 WithDefaults 之前桥接（80→0.80）；
//  4. 稀疏 merge 只改 patch 键，未出现的兄弟键保留；
//  5. 持久化失败如实返回 error（不静默成功）。
//
// English: account-scoped sizing locks — dedicated key isolation, legacy fallback order,
// pre-default legacy cap bridge, sparse merge sibling retention, and honest persistence errors.
package config

import (
	"encoding/json"
	"errors"
	"testing"
)

// memKV 最小 KVStore 实现，避免 config 测试依赖 auth 包。
// English: minimal in-memory KVStore so the config package tests stay dependency-light.
type memKV struct {
	data map[string]map[string]string
	fail bool
}

func newMemKV() *memKV { return &memKV{data: map[string]map[string]string{}} }

func (m *memKV) SetConfig(userID, key, value string) error {
	if m.fail {
		return errors.New("disk full")
	}
	if m.data[userID] == nil {
		m.data[userID] = map[string]string{}
	}
	m.data[userID][key] = value
	return nil
}

func (m *memKV) GetConfig(userID, key string) (string, bool) {
	v, ok := m.data[userID][key]
	return v, ok
}

func newSizingTestManager(t *testing.T, rules *Rules) (*Manager, *memKV) {
	t.Helper()
	m := NewManagerWithRules(rules)
	kv := newMemKV()
	m.SetStore(kv)
	return m, kv
}

// TestSizingConfigForIsolatesAccounts 专用键按账号隔离，A 开启不影响 B/全局。
func TestSizingConfigForIsolatesAccounts(t *testing.T) {
	m, _ := newSizingTestManager(t, &Rules{})
	if _, err := m.MergeSizingConfigFor("A", map[string]json.RawMessage{
		"enabled": json.RawMessage(`true`),
		"shadow":  json.RawMessage(`false`),
	}); err != nil {
		t.Fatalf("写 A: %v", err)
	}
	if got, _ := m.GetSizingConfigFor("A"); !got.Enabled || got.ShadowOn() {
		t.Fatalf("A 应为 enabled=true shadow=false, got %+v", got)
	}
	if got, _ := m.GetSizingConfigFor("B"); got.Enabled || !got.ShadowOn() {
		t.Fatalf("B 不应被 A 污染: %+v", got)
	}
	if m.GetSizingConfig().Enabled {
		t.Fatal("账号写入不应改变全局 rules.sizing")
	}
}

// TestSizingConfigForFallbackOrder 专用键 → 账号旧 sizing → 全局。
func TestSizingConfigForFallbackOrder(t *testing.T) {
	global := &Rules{Sizing: SizingConfig{Enabled: true, SellBoostLevel: "L3"}}
	m, kv := newSizingTestManager(t, global)

	// 账号旧 rules.sizing 存在时优先于全局。
	acctRules, _ := json.Marshal(&Rules{Sizing: SizingConfig{Enabled: false, SellBoostLevel: "L1"}})
	kv.data["u"] = map[string]string{perUserKey: string(acctRules)}
	if got, _ := m.GetSizingConfigFor("u"); got.Enabled || got.SellBoostLevelOf() != "L1" {
		t.Fatalf("应回退账号旧 sizing, got %+v", got)
	}
	// 专用键出现后压过账号旧 sizing。
	if _, err := m.MergeSizingConfigFor("u", map[string]json.RawMessage{
		"enabled":          json.RawMessage(`true`),
		"sell_boost_level": json.RawMessage(`"L2"`),
	}); err != nil {
		t.Fatalf("写专用键: %v", err)
	}
	if got, _ := m.GetSizingConfigFor("u"); !got.Enabled || got.SellBoostLevelOf() != "L2" {
		t.Fatalf("专用键应优先, got %+v", got)
	}
}

// TestSizingConfigLegacyTotalPosBridge 旧 position 字段必须在 WithDefaults 之前桥接。
func TestSizingConfigLegacyTotalPosBridge(t *testing.T) {
	// 只有账号旧 position 字段：80 → 0.80。
	m, kv := newSizingTestManager(t, &Rules{})
	acctRules, _ := json.Marshal(&Rules{Position: PositionConfig{MaxTotalPositionPct: 80}})
	kv.data["u"] = map[string]string{perUserKey: string(acctRules)}
	if got, _ := m.GetSizingConfigFor("u"); got.MaxTotalPositionPct != 0.80 {
		t.Fatalf("账号旧字段 80 应归一为 0.80, got %v", got.MaxTotalPositionPct)
	}
	// 专用键显式 0.5 优先。
	if _, err := m.MergeSizingConfigFor("u", map[string]json.RawMessage{
		"max_total_position_pct": json.RawMessage(`0.5`),
	}); err != nil {
		t.Fatalf("写专用键: %v", err)
	}
	if got, _ := m.GetSizingConfigFor("u"); got.MaxTotalPositionPct != 0.5 {
		t.Fatalf("专用键 0.5 应优先, got %v", got.MaxTotalPositionPct)
	}
}

// TestMergeSizingConfigForKeepsSiblings 稀疏 merge 不得把未出现的键抹成零值。
func TestMergeSizingConfigForKeepsSiblings(t *testing.T) {
	m, _ := newSizingTestManager(t, &Rules{})
	if _, err := m.MergeSizingConfigFor("u", map[string]json.RawMessage{
		"enabled": json.RawMessage(`true`),
		"floor":   json.RawMessage(`0.2`),
	}); err != nil {
		t.Fatalf("首写: %v", err)
	}
	if _, err := m.MergeSizingConfigFor("u", map[string]json.RawMessage{
		"cap": json.RawMessage(`1.5`),
	}); err != nil {
		t.Fatalf("二次写: %v", err)
	}
	got, _ := m.GetSizingConfigFor("u")
	if !got.Enabled || got.Floor != 0.2 || got.Cap != 1.5 {
		t.Fatalf("兄弟键被抹掉: enabled=%v floor=%v cap=%v", got.Enabled, got.Floor, got.Cap)
	}
}

// TestMergeSizingConfigForPersistenceError 写失败必须返回 error，不能回 ok。
func TestMergeSizingConfigForPersistenceError(t *testing.T) {
	m, kv := newSizingTestManager(t, &Rules{})
	kv.fail = true
	if _, err := m.MergeSizingConfigFor("u", map[string]json.RawMessage{
		"enabled": json.RawMessage(`true`),
	}); err == nil {
		t.Fatal("持久化失败仍返回 nil")
	}
}
