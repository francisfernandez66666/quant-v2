// sizing_gate_test.go — §N6-RISK（2026-10-21）风险档权重收束与广度缺失降权的定点测试。
//
// 三条 owner 裁决：
//  1. Red 不默认硬拦，用 tier_scales.Red=0.30 收束；只有 red_block_new_buy=true 才整笔拒绝；
//  2. 广度读数缺失（NaN）不再弃权，按 breadth_missing_scale=0.90 小幅降权，恢复即回真实档；
//  3. 减权只作用一次，不与状态档叠加（避免同一风险双罚）。
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func testSizingCfg() config.SizingConfig {
	return config.DefaultSizingConfig().WithDefaults()
}

// TestTierFactorRedWeight Red 默认 0.30、可配覆盖、空档中性 1.0。
func TestTierFactorRedWeight(t *testing.T) {
	cfg := testSizingCfg()
	if got := tierFactor(combat_agent.RiskTierRed, cfg); math.Abs(got-0.30) > 1e-9 {
		t.Fatalf("Red 默认权重应为 0.30, got %.3f", got)
	}
	if got := tierFactor("", cfg); got != 1.0 {
		t.Fatalf("空档应中性 1.0, got %.3f", got)
	}
	cfg.TierScales["Red"] = 0.5
	if got := tierFactor(combat_agent.RiskTierRed, cfg); got != 0.5 {
		t.Fatalf("配置覆盖应生效, got %.3f", got)
	}
}

// TestSizingHardBlockRedOnlyWithSwitch 默认不硬拦；开关打开才拒。
func TestSizingHardBlockRedOnlyWithSwitch(t *testing.T) {
	cfg := testSizingCfg()
	sig := combat_agent.Signal{Action: "buy", Confidence: 0.95}
	if down, _ := sizingHardBlock(Regime{RiskTier: combat_agent.RiskTierRed}, sig, cfg); down {
		t.Fatal("red_block_new_buy=false 时 Red 不得硬拦（应由权重收束）")
	}
	if down, why := sizingHardBlock(Regime{RiskTier: combat_agent.RiskTierRed, RedBlockNewBuy: true}, sig, cfg); !down {
		t.Fatalf("red_block_new_buy=true 时应硬拦, why=%q", why)
	}
}

// TestBreadthFactorMissingHaircut NaN=0.90 降权；真实读数按档；0 视为全跌。
func TestBreadthFactorMissingHaircut(t *testing.T) {
	cfg := testSizingCfg()
	if got := breadthFactor(math.NaN(), cfg); math.Abs(got-0.90) > 1e-9 {
		t.Fatalf("广度缺失应 ×0.90, got %.3f", got)
	}
	if got := breadthFactor(0.20, cfg); got != 0.70 {
		t.Fatalf("广度 0.20 应 ×0.70, got %.3f", got)
	}
	if got := breadthFactor(0.40, cfg); got != 0.85 {
		t.Fatalf("广度 0.40 应 ×0.85, got %.3f", got)
	}
	if got := breadthFactor(0.60, cfg); got != 1.0 {
		t.Fatalf("广度 0.60 应 ×1.0, got %.3f", got)
	}
	if got := breadthFactor(0, cfg); got != 0 {
		t.Fatalf("广度 0（零值/无法区分）应弃权返回 0, got %.3f", got)
	}
}

// TestBreadthMissingScaleConfigurable 配置覆盖生效。
func TestBreadthMissingScaleConfigurable(t *testing.T) {
	cfg := testSizingCfg()
	cfg.BreadthMissingScale = 0.8
	if got := breadthFactor(math.NaN(), cfg); got != 0.8 {
		t.Fatalf("breadth_missing_scale 覆盖应生效, got %.3f", got)
	}
}
