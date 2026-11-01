// subscription_audit_test.go — §SUBSCRIPTION（2026-11-01 追加裁决 15 的 L3）开关翻转必须留审计。
//
// "谁在什么时候把哪个键从关拧到开"必须是可回溯的：否则过两天问"行情源什么时候变成券商的"，
// 账上答不出来。DiffRules 是递归 JSON 对比（不是字段白名单），所以新键**自动**进 diff——
// 这条测试锁的就是这个"自动"：将来有人把 diff 改成白名单枚举而漏掉新键，这里当场红。
package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// rulesWithSwitches 生成一份带两个申购开关的账号规则文档（before=false / after=true 的翻转形态）。
func rulesWithSwitches(t *testing.T, recognize, guard bool) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"qmt": map[string]any{
			"enabled":                   true,
			"subscription_recognize":    recognize,
			"subscription_cancel_guard": guard,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestDiffRulesSurfacesSubscriptionSwitchFlip(t *testing.T) {
	before := rulesWithSwitches(t, false, false)
	after := rulesWithSwitches(t, true, false)
	diff, err := DiffRules(before, after)
	if err != nil {
		t.Fatalf("DiffRules: %v", err)
	}
	if !strings.Contains(diff, "qmt.subscription_recognize") {
		t.Fatalf("开关翻转没进审计 diff（白名单化回归？）: %q", diff)
	}
	if strings.Contains(diff, "subscription_cancel_guard") {
		t.Fatalf("没改动的键也被记成变更（噪声审计）: %q", diff)
	}
	if diff == "(无变更)" {
		t.Fatal("翻开关却报无变更")
	}
}

func TestDiffRulesStillHidesSecretPathsAfterNewKeys(t *testing.T) {
	// 同一条规则文档里新增了两个可读键，敏感路径的"只报长度不报值"不许被顺手带坏
	pb, _ := json.Marshal(map[string]any{"qmt": map[string]any{"token": "old-secret-value-1234", "subscription_recognize": false}})
	pa, _ := json.Marshal(map[string]any{"qmt": map[string]any{"token": "new-secret-value-9999", "subscription_recognize": true}})
	diff, err := DiffRules(pb, pa)
	if err != nil {
		t.Fatalf("DiffRules: %v", err)
	}
	if strings.Contains(diff, "old-secret-value") || strings.Contains(diff, "new-secret-value") {
		t.Fatalf("令牌原值进了审计 diff（§0929CFG-SECRET 回潮）: %q", diff)
	}
	if !strings.Contains(diff, "token") || !strings.Contains(diff, "字") {
		t.Fatalf("敏感路径应只报长度: %q", diff)
	}
	if !strings.Contains(diff, "subscription_recognize") {
		t.Fatalf("可读开关应照常出现在 diff 里: %q", diff)
	}
}
