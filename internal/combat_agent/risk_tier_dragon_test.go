// risk_tier_dragon_test.go — §N6-RISK（2026-10-21）龙头信号接入统一风险闸门的回归。
//
// 背景：龙头识别走 ScanLimitUp，此前直接产出 buy、绕过 applyRiskTier；现网 Red 日仍有
// 8 笔置信度 0.70~0.75 的龙头买入。owner 裁决：龙头豁免 0.90/0.92 置信度降级（量纲不同，
// 与 §N-RELAX 的 N 形同理），改由仓位权重收束；red_block_new_buy 应急阀仍可整笔拦截。
//
// English: regressions for routing dragon signals through the unified gate — dragons skip the
// confidence bar (different scale) but still obey the emergency new-buy block.
package combat_agent

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

func dragonBuy() Signal {
	return Signal{
		Code: "002724", Name: "龙头样本", Strategy: "龙头识别", StrategyType: "dragon",
		Direction: "做多", Action: "buy", Confidence: 0.73,
	}
}

func strategyBuy(conf float64) Signal {
	return Signal{
		Code: "300967", Name: "策略样本", Strategy: "因子战法#130", StrategyType: "fac_130",
		Direction: "做多", Action: "buy", Confidence: conf,
	}
}

// TestRiskRegimeDragonExempt 龙头低置信在 Red 日不被降级；策略信号同档位仍被降级。
func TestRiskRegimeDragonExempt(t *testing.T) {
	a := New(nil)
	a.strategyCfg = &config.StrategyConfig{}
	a.riskTier = RiskTierRed

	out := a.applyRiskRegime([]Signal{dragonBuy(), strategyBuy(0.80)})
	if out[0].Action != "buy" {
		t.Fatalf("龙头应豁免置信度降级（该由仓位权重收束），got action=%q reason=%q", out[0].Action, out[0].Reason)
	}
	if out[1].Action != "watch" {
		t.Fatalf("策略买入 0.80 < Red 0.92 应降级 watch, got %q", out[1].Action)
	}
}

// TestRiskRegimeRedBlockNewBuy 应急阀打开时龙头买入也被降级（豁免只针对置信度门槛）。
func TestRiskRegimeRedBlockNewBuy(t *testing.T) {
	a := New(nil)
	on := true
	a.strategyCfg = &config.StrategyConfig{MacroGate: config.MacroGateConfig{RedBlockNewBuy: &on}}
	a.riskTier = RiskTierRed

	out := a.applyRiskRegime([]Signal{dragonBuy()})
	if out[0].Action != "watch" {
		t.Fatalf("red_block_new_buy=true 时龙头买入也应降级 watch, got %q", out[0].Action)
	}
	if !strings.Contains(out[0].Reason, "禁止新开仓") {
		t.Fatalf("降级理由应点名禁止新开仓, got %q", out[0].Reason)
	}
}

// TestRedBlockNewBuyDefaultOff 未配置时开关为 false（默认走权重收束）。
func TestRedBlockNewBuyDefaultOff(t *testing.T) {
	a := New(nil)
	a.strategyCfg = &config.StrategyConfig{}
	if a.RedBlockNewBuyOn() {
		t.Fatal("red_block_new_buy 未配置时应默认 false")
	}
}
