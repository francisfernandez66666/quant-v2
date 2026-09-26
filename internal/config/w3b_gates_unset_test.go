// w3b_gates_unset_test.go — §0926E2E-4B（2026-09-26 三波）「六闸默认关 → 缺配置健康度信号」
// 行为用例：钉 DisabledGates / GatesConfigUnset 的判定语义。
// 裁决口径（选项B）：默认关**不改**（出厂开闸会在存量账号上产生新拒单，风险>收益），
// 但"一条都没配"必须可见——本文件即那条可见信号的机器锁。
package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// mustUnsetFalse 小工具：把 *bool 指向 false（跌停闸"显式停用"形态）。
func mustUnsetFalse() *bool { f := false; return &f }

func TestW3bZeroConfigIsFlaggedUnset(t *testing.T) {
	var rg RiskGateConfig // 零值=一条风控配置都没写过
	if !rg.GatesConfigUnset() {
		t.Fatal("零值配置必须判『缺配置』——默认关六闸全关正是缺陷4的现网形态")
	}
	want := []string{"day_loss", "concentration", "stale_quote", "limit_up_block_buy", "max_order_amount", "cross_check"}
	got := rg.DisabledGates()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("缺配置时关闭闸清单=%v，期望六闸全列 %v", got, want)
	}
	// 反直觉但关键：跌停追卖闸未配置=常开（§A5），不得混进"关闭"清单（否则告警文案说谎）。
	for _, k := range got {
		if k == "limit_down_block_sell" {
			t.Fatal("跌停闸未配置时按 §A5 常开，不得计入关闭清单")
		}
	}
	// AnyEnabled 在零值下仍为 true（A5 常开闸在位）——"至少一闸开"≠"配过配置"，
	// 这正是不能用 any_enabled 代替本信号的原因（假绿反证）。
	if !rg.AnyEnabled() {
		t.Fatal("零值下 AnyEnabled 应为 true（§A5 常开），本用例前提被改动需重审")
	}
}

func TestW3bOneGateOnClearsUnset(t *testing.T) {
	rg := RiskGateConfig{MaxOrderAmount: 20000}
	if rg.GatesConfigUnset() {
		t.Fatal("只要配开任一默认关闸就不再是『一条没配』——告警必须熄（不误伤已配账号）")
	}
	want := []string{"day_loss", "concentration", "stale_quote", "limit_up_block_buy", "cross_check"}
	if got := rg.DisabledGates(); !reflect.DeepEqual(got, want) {
		t.Fatalf("半配状态关闭清单=%v，期望其余五闸 %v", got, want)
	}
}

func TestW3bExplicitLimitDownOffIsCountedClosed(t *testing.T) {
	// 六闸里开一个（清缺配置信号），同时跌停闸被显式配 false——它必须出现在"关闭"清单里，
	// 否则"我自己关掉了防守"这个事实仍然不可见。
	rg := RiskGateConfig{StaleQuoteMs: 3000, LimitDownBlockSell: mustUnsetFalse()}
	if rg.GatesConfigUnset() {
		t.Fatal("stale_quote 已配开，不应再报缺配置")
	}
	got := rg.DisabledGates()
	found := false
	for _, k := range got {
		if k == "limit_down_block_sell" {
			found = true
		}
	}
	if !found {
		t.Fatalf("显式停用的跌停闸必须计入关闭清单，实得 %v", got)
	}
}

func TestW3bDisabledGatesJSONFriendlyOrdering(t *testing.T) {
	// DisabledGates 返回非 nil 空切片（前端直接 .length 不怕 null——§F-5 null 崩页先例）。
	rg := RiskGateConfig{DayLossLimitPct: 3, SingleStockValuePct: 30, StaleQuoteMs: 3000,
		LimitUpBlockBuy: true, MaxOrderAmount: 20000, CrossCheckPct: 2}
	if rg.GatesConfigUnset() {
		t.Fatal("全配开状态不得报缺配置（反证：闸不误伤常态）")
	}
	b, err := json.Marshal(rg.DisabledGates())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("全配开时清单应为 JSON 空数组（不能是 null），实得 %s", b)
	}
}
