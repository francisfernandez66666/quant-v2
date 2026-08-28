// E1 宏观利空门控测试：交割日信号降级/拦截逻辑。
// 覆盖：非 N 低置信买入降级、高置信放行、N 形拦截、动量 watch 拦截、门控关闭不生效。
// English: E1 macro bearish gate tests: settlement-day signal downgrade/blocking logic. Covers: non-N low-confidence buys downgraded, high-confidence passes, N-shape blocked, momentum watch blocked, and gate disabled has no effect.
package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// testSignal 构造一条测试信号。
// English: testSignal builds a test signal.
func testSignal(strategyType string, action string, conf float64) Signal {
	return Signal{Strategy: strategyType, Action: action, Confidence: conf}
}

// TestApplyMacroGateLowConfBuy 交割日：低置信买入信号降级为 watch。
// English: TestApplyMacroGateLowConfBuy on settlement day: low-confidence buy signals are downgraded to watch.
func TestApplyMacroGateLowConfBuy(t *testing.T) {
	cfg := config.MacroGateConfig{Enabled: true, MinConfidence: 0.85}
	sigs := []Signal{
		testSignal(string(strategy.SignalDragon), "buy", 0.7),
	}
	out := applyMacroGate(sigs, true, cfg)
	if out[0].Action != "watch" {
		t.Fatalf("低置信买入应降级为 watch，实际=%s", out[0].Action)
	}
	if out[0].Reason == "" || !containsStr(out[0].Reason, "宏观利空") {
		t.Fatalf("理由应含宏观利空标注，实际=%s", out[0].Reason)
	}
}

// TestApplyMacroGateHighConfBuy 交割日：高置信（特别高质量）买入信号放行。
// English: TestApplyMacroGateHighConfBuy on settlement day: high-confidence (exceptional quality) buy signals pass through.
func TestApplyMacroGateHighConfBuy(t *testing.T) {
	cfg := config.MacroGateConfig{Enabled: true, MinConfidence: 0.85}
	sigs := []Signal{
		testSignal(string(strategy.SignalDragon), "buy", 0.9),
	}
	out := applyMacroGate(sigs, true, cfg)
	if out[0].Action != "buy" {
		t.Fatalf("高置信买入应放行（特别高质量信号），实际=%s", out[0].Action)
	}
}

// TestApplyMacroGateNShape 交割日：N 形超短买入一律拦截为 watch。
// English: TestApplyMacroGateNShape on settlement day: N-shape ultra-short buys are always blocked to watch.
func TestApplyMacroGateNShape(t *testing.T) {
	cfg := config.MacroGateConfig{Enabled: true}
	sigs := []Signal{
		testSignal(string(strategy.SignalNShape), "buy", 0.95), // 即使高置信也拦截
	}
	out := applyMacroGate(sigs, true, cfg)
	if out[0].Action != "watch" {
		t.Fatalf("N 形超短应拦截为 watch，实际=%s", out[0].Action)
	}
}

// TestApplyMacroGateMomentumWatch 交割日：动量 watch 观察信号被拦截剔除。
// English: TestApplyMacroGateMomentumWatch on settlement day: momentum watch signals are blocked and removed.
func TestApplyMacroGateMomentumWatch(t *testing.T) {
	cfg := config.MacroGateConfig{Enabled: true}
	sigs := []Signal{
		{Strategy: "动量", Action: "watch", Confidence: 0.6, Reason: "动量60 量价齐升"},
	}
	out := applyMacroGate(sigs, true, cfg)
	if len(out) != 0 {
		t.Fatalf("动量 watch 应被拦截剔除，实际剩余 %d 条信号", len(out))
	}
}

// TestApplyMacroGateMomentumWatchExplicitPass 显式关闭 BlockMomentum 时动量 watch 放行。
// English: TestApplyMacroGateMomentumWatchExplicitPass: momentum watch passes when BlockMomentum is explicitly false.
func TestApplyMacroGateMomentumWatchExplicitPass(t *testing.T) {
	falseVal := false
	cfg := config.MacroGateConfig{Enabled: true, BlockMomentum: &falseVal}
	sigs := []Signal{
		{Strategy: "动量", Action: "watch", Confidence: 0.6, Reason: "动量60 量价齐升"},
	}
	out := applyMacroGate(sigs, true, cfg)
	if len(out) != 1 || out[0].Action != "watch" {
		t.Fatalf("显式关闭 BlockMomentum 时动量 watch 应放行，实际=%+v", out)
	}
}

// TestApplyMacroGateNShapeExplicitPass 显式关闭 BlockNShape 时 N 形买入放行。
// English: TestApplyMacroGateNShapeExplicitPass: N-shape buy passes when BlockNShape is explicitly false.
func TestApplyMacroGateNShapeExplicitPass(t *testing.T) {
	falseVal := false
	cfg := config.MacroGateConfig{Enabled: true, BlockNShape: &falseVal}
	sigs := []Signal{
		testSignal(string(strategy.SignalNShape), "buy", 0.95),
	}
	out := applyMacroGate(sigs, true, cfg)
	if len(out) != 1 || out[0].Action != "buy" {
		t.Fatalf("显式关闭 BlockNShape 时 N 形买入应放行，实际=%+v", out)
	}
}

// TestApplyMacroGateInactive 门控关闭或未命中交割日时行为不变。
// English: TestApplyMacroGateInactive behavior is unchanged when the gate is disabled or settlement day is not hit.
func TestApplyMacroGateInactive(t *testing.T) {
	cfg := config.MacroGateConfig{Enabled: true}
	sigs := []Signal{
		testSignal(string(strategy.SignalDragon), "buy", 0.5),
	}
	out := applyMacroGate(sigs, false, cfg)
	if out[0].Action != "buy" {
		t.Fatalf("门控未激活时行为不变，实际=%s", out[0].Action)
	}
}

// TestMacroGateLevels 级别匹配：contract 命中，其他级别（如 nfp）不命中。
// English: TestMacroGateLevels level matching: contract matches, other levels (e.g. nfp) do not.
func TestMacroGateLevels(t *testing.T) {
	if !hasGateTriggerLevel([]data.MacroEvent{{Level: "contract"}}, nil) {
		t.Fatal("contract 应命中门控")
	}
	if hasGateTriggerLevel([]data.MacroEvent{{Level: "nfp"}}, nil) {
		t.Fatal("nfp 不应命中默认门控（默认只拦截 contract）")
	}
	if !hasGateTriggerLevel([]data.MacroEvent{{Level: "nfp"}}, []string{"nfp"}) {
		t.Fatal("显式配置 nfp 级别时应命中")
	}
}

// containsStr 判断字符串包含子串。
// English: containsStr reports whether a string contains a substring.
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestMacroEventsNow 交割日检测：2026 年 3 月 20 日（周五）应为交割日附近影响期。
// English: TestMacroEventsNow settlement-day detection: March 20, 2026 (Friday) should be within the settlement-day impact window.
func TestMacroEventsNow(t *testing.T) {
	// 2026-03-20 是周五（每月第三个周五交割日）；影响期 Duration=2 天（3/18~3/22）
	// English: 2026-03-20 is a Friday (settlement day is the third Friday of each month); impact window Duration=2 days (3/18~3/22).
	events := macroEventsAt(time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC))
	if !hasGateTriggerLevel(events, macroGateLevels) {
		t.Fatalf("2026-03-20 应为交割日影响期，实际事件=%+v", events)
	}
	// 非交割日（如 2026-03-10，月中）不应命中
	// English: A non-settlement day (e.g. 2026-03-10, mid-month) should not match.
	events2 := macroEventsAt(time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC))
	if hasGateTriggerLevel(events2, macroGateLevels) {
		t.Fatalf("2026-03-10 不应为交割日影响期，实际事件=%+v", events2)
	}
}
