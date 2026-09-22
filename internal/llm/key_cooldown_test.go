// key_cooldown_test.go — §DEADGAUGE（2026-09-23 傍晚批收尾）KeysInCooldown 口径锁。
// 规则 llm_cooldown（量规 llm_cooldown_count，p2「LLM 冷却数超阈值 >2」）自 09-15 注册以来
// 无赋值点 = 永不触发。KeysInCooldown 是它的新数据源，判据必须与 pickKey 逐字一致：
// 槽位 coolUntil 严格大于当下才算冷却中；单 key 池恒 0（markKeyStatus 对单 key 不标记）。
//
// English: §DEADGAUGE locks for KeysInCooldown — the predicate the llm_cooldown alert rule needed.
// Must agree with pickKey (slot coolUntil strictly in the future counts) and stay 0 for single-key pools.
package llm

import (
	"testing"
	"time"
)

// TestKeysInCooldownCountsLiveSlots 多 key 池：401 进长冷却、429 按 Retry-After、200 不标记。
func TestKeysInCooldownCountsLiveSlots(t *testing.T) {
	c := New(Config{APIKeys: []string{"K0", "K1", "K2", "K3"}})
	if got := c.KeysInCooldown(); got != 0 {
		t.Fatalf("初始无冷却, got %d", got)
	}
	c.markKeyStatus("K0", 401, 0)             // 鉴权失败：长冷却
	c.markKeyStatus("K1", 429, 5*time.Second) // 限流：按 Retry-After
	c.markKeyStatus("K2", 200, 0)             // 正常响应不标记
	c.markKeyStatus("K3", 418, 0)             // 非冷却类状态不标记
	if got := c.KeysInCooldown(); got != 2 {
		t.Fatalf("401+429 两条应在冷却, got %d", got)
	}
	// 冷却到期后自动归零（与 pickKey 同一判据：coolUntil <= now 即可再用）
	for i := range c.keyCoolUntil {
		c.keyCoolUntil[i].Store(time.Now().Add(-time.Second).Unix())
	}
	if got := c.KeysInCooldown(); got != 0 {
		t.Fatalf("过期后应为 0, got %d", got)
	}
}

// TestKeysInCooldownSingleKeyPoolAlwaysZero 单 key 池恒 0：markKeyStatus 明确不标记（没有可回避的
// 备用），因此量规不会把"只有一个 key 且它坏了"误报成"池子在冷却"——那类故障由降级/预算规则负责。
func TestKeysInCooldownSingleKeyPoolAlwaysZero(t *testing.T) {
	c := New(Config{APIKey: "only"})
	c.markKeyStatus("only", 401, 0)
	if got := c.KeysInCooldown(); got != 0 {
		t.Fatalf("单 key 池恒 0, got %d", got)
	}
	if n := c.KeyCount(); n != 1 {
		t.Fatalf("KeyCount 基线被改动, got %d", n)
	}
}
