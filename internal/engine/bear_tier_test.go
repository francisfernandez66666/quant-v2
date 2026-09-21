// 本文件：§D1 护栏4/5（利空验证分级）的纯逻辑单测——双源交集判级（classifyBearTier）、
// 失败保级合并（mergeBearTier）、消费侧查级与超龄降级（bearTierFor）。
// 全程不依赖真实行情源（同花顺/东财），确定性断言。
// English: Pure unit tests for the §D1 guardrail 4/5 bearish verification tier: dual-source
// intersection classification, keep-old-on-failure merge, and consumer lookup with TTL downgrade.
// No live THS/EastMoney dependency.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/signalctl"
)

// TestClassifyBearTier 双源交集=dual；仅 THS/仅东财命中=single；某源 nil 只能 single。
func TestClassifyBearTier(t *testing.T) {
	ths := map[string]bool{"600001": true, "600002": true}
	em := map[string]bool{"600002": true, "600003": true}
	got := classifyBearTier(ths, em)
	if got["600001"] != signalctl.BearVerifiedSingle {
		t.Fatalf("仅同花顺命中应为 single, got %q", got["600001"])
	}
	if got["600002"] != signalctl.BearVerifiedDual {
		t.Fatalf("双源交集应为 dual, got %q", got["600002"])
	}
	if got["600003"] != signalctl.BearVerifiedSingle {
		t.Fatalf("仅东财命中应为 single, got %q", got["600003"])
	}
	if len(got) != 3 {
		t.Fatalf("应覆盖两源并集 3 只, got %d", len(got))
	}
	// 东财不可用（nil 集合）：全部只能 single，绝不凭空给硬清资格
	onlyThs := classifyBearTier(ths, nil)
	for code, tier := range onlyThs {
		if tier != signalctl.BearVerifiedSingle {
			t.Fatalf("第二源缺失时 %s 不得判 dual", code)
		}
	}
}

// TestMergeBearTier keepOld=false 整表替换（事件消退自然出表）；keepOld=true 保留旧条目仅覆盖成功项
// （护栏5：取数失败不得把已验证 dual 悄悄降格/清除）。
func TestMergeBearTier(t *testing.T) {
	now := time.Now()
	old := map[string]bearTierEntry{
		"600001": {verified: signalctl.BearVerifiedDual, at: now.Add(-time.Minute)},
		"600002": {verified: signalctl.BearVerifiedSingle, at: now.Add(-time.Minute)},
	}
	updated := map[string]bearTierEntry{
		"600002": {verified: signalctl.BearVerifiedDual, at: now},
		"600003": {verified: signalctl.BearVerifiedSingle, at: now},
	}

	replaced := mergeBearTier(old, updated, false)
	if _, ok := replaced["600001"]; ok {
		t.Fatal("整表替换时消失事件的旧条目应出表")
	}
	if replaced["600002"].verified != signalctl.BearVerifiedDual || len(replaced) != 2 {
		t.Fatalf("整表替换结果异常: %+v", replaced)
	}

	merged := mergeBearTier(old, updated, true)
	if merged["600001"].verified != signalctl.BearVerifiedDual {
		t.Fatal("存在取数失败板块时，旧 dual 条目必须保留（缺数据不否定利空）")
	}
	if merged["600002"].verified != signalctl.BearVerifiedDual || merged["600003"].verified != signalctl.BearVerifiedSingle {
		t.Fatalf("合并应覆盖本轮成功算出的条目: %+v", merged)
	}
	if len(merged) != 3 {
		t.Fatalf("合并结果应为 3 条, got %d", len(merged))
	}
}

// TestBearTierFor 消费侧口径：无记录/空等级/超 TTL 一律 single（只失去硬清资格，不误判"无利空"）；
// 新鲜 dual 原样给出。
func TestBearTierFor(t *testing.T) {
	now := time.Now()
	e := &Engine{bearTier: map[string]bearTierEntry{
		"600001": {verified: signalctl.BearVerifiedDual, at: now},
		"600002": {verified: signalctl.BearVerifiedDual, at: now.Add(-bearTierTTL - time.Minute)}, // 超龄
		"600003": {verified: "", at: now},                                                         // 空等级
	}}
	if got := e.bearTierFor("600001"); got != signalctl.BearVerifiedDual {
		t.Fatalf("新鲜 dual 应原样给出, got %q", got)
	}
	if got := e.bearTierFor("600002"); got != signalctl.BearVerifiedSingle {
		t.Fatalf("超龄等级应降为 single, got %q", got)
	}
	if got := e.bearTierFor("600003"); got != signalctl.BearVerifiedSingle {
		t.Fatalf("空等级应按 single, got %q", got)
	}
	if got := e.bearTierFor("600999"); got != signalctl.BearVerifiedSingle {
		t.Fatalf("无记录应按 single（不是无利空）, got %q", got)
	}
	// nil 表（流水线尚无利空板块事件）不得 panic
	e2 := &Engine{}
	if got := e2.bearTierFor("600001"); got != signalctl.BearVerifiedSingle {
		t.Fatalf("nil 表应按 single, got %q", got)
	}
}
