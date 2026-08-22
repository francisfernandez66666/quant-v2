package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/strategy"
)

func TestRuleExitOverridesLookupAndExit(t *testing.T) {
	// 注册表：因子 fac_1（显示名 因子战法#1）止盈5%/持仓7天；形态 pat_9 仅持仓 3 天
	SetRuleExitOverrides(
		[]research.AppliedFactorEntry{{
			ID: "fac_1", Name: "因子战法#1", Enabled: true,
			ExitTrailPct: 5, ExitMaxHoldDays: 7,
		}},
		[]research.AppliedPatternEntry{{
			ID: "pat_9", Name: "形态战法#9", Enabled: true,
			ExitMaxHoldDays: 3,
		}},
	)
	t.Cleanup(func() { SetRuleExitOverrides(nil, nil) })

	// 按 ID / 显示名（含大小写与空白）均可命中
	if ov := ruleExitParamsFor("fac_1"); ov == nil || ov.trailPct != 5 || ov.holdDays != 7 {
		t.Fatalf("ID 命中失败: %+v", ov)
	}
	if ov := ruleExitParamsFor(" 因子战法#1 "); ov == nil || ov.trailPct != 5 {
		t.Fatalf("显示名命中失败: %+v", ov)
	}
	if ov := ruleExitParamsFor("pat_9"); ov == nil || ov.holdDays != 3 || ov.trailPct != 0 {
		t.Fatalf("形态命中失败: %+v", ov)
	}
	if ruleExitParamsFor("double_bump") != nil || ruleExitParamsFor("") != nil {
		t.Fatal("未注册/空串不应命中")
	}

	// genericTrailingExitWith：覆盖生效——成本100，高点110 后回落到 104.4（-5.09% ≤ -5%）
	now := time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC)
	ctx := &strategy.ExitContext{
		CostPrice: 100,
		CurPrice:  104.4,
		EntryAt:   "2026-08-20",
		EntryMeta: map[string]float64{"highest_price": 110},
		Now:       now,
	}
	res := genericTrailingExitWith(ctx, now, 5, 30)
	if res == nil || res.Reason != "回撤止损(移动止盈)" {
		t.Fatalf("自定义止盈未触发: %+v", res)
	}
	// 默认 8% 阈值下同价不应触发
	if res2 := genericTrailingExit(ctx, now); res2 != nil {
		t.Fatalf("默认阈值不应触发: %+v", res2)
	}
	// 超期覆盖：pat_9 持仓 3 天即离场（EntryAt=19 日 → 22 日恰 3 天）
	ctx2 := &strategy.ExitContext{CostPrice: 100, CurPrice: 101, EntryAt: "2026-08-19",
		EntryMeta: map[string]float64{}, Now: now}
	if r := genericTrailingExitWith(ctx2, now, 8, 3); r == nil || r.Reason != "持仓超期离场" {
		t.Fatalf("超期覆盖失效: %+v", r)
	}
}

func TestSetRuleExitOverridesDisabledCleared(t *testing.T) {
	SetRuleExitOverrides([]research.AppliedFactorEntry{{ID: "fac_2", Name: "因子战法#2", Enabled: false,
		ExitTrailPct: 10}}, nil)
	if ruleExitParamsFor("fac_2") != nil {
		t.Fatal("停用条目不应入表")
	}
	// 全量重建语义：新列表不含旧键 → 旧覆盖清除
	SetRuleExitOverrides([]research.AppliedFactorEntry{{ID: "fac_3", Name: "因子战法#3", Enabled: true,
		ExitMaxHoldDays: 9}}, nil)
	if ruleExitParamsFor("fac_2") != nil {
		t.Fatal("重建后旧键应被清除")
	}
	if ruleExitParamsFor("因子战法#3") == nil {
		t.Fatal("新键应生效")
	}
	SetRuleExitOverrides(nil, nil)
}
