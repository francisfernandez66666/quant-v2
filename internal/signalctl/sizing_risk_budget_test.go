// sizing_risk_budget_test.go — §DYN-SIZE P5 风险预算守恒上限因子守护测试。
//
// 口径（方案 §2.6 配套，owner 已裁决强制）：止损从 6% 放宽到 9% 时，同一笔金额的单笔最大亏损被
// 悄悄放大 50%，必须同比例缩仓（6/9 = 0.67）。它是**上限类**因子（取 min，不连乘）——与"大盘跌幅"
// 这类权重因子描述的是同一次股灾的两个投影，连乘会把同一个风险罚两次（方案 §5 测试锁）。
//
// English: guard tests for the P5 risk-budget-conservation cap — loosening the stop must shrink the
// position proportionally; it is a cap factor (min-combined, not multiplied).
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// neutralSignal 全维度弃权的信号（ATR/置信度/质量权重都缺 → 权重类恒 1.0），
// 使合成结果只反映上限类因子。
func neutralSignal() combat_agent.Signal {
	return combat_agent.Signal{Code: "600001", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy"}
}

// budgetRegime 启用态 + 指定偏移/基准止损的快照（其余维度全弃权）。
func budgetRegime(offset, baseSL float64) Regime {
	return Regime{
		SizeCfg:           config.SizingConfig{Enabled: boolPtr(true)}.WithDefaults(),
		StopLossOffsetPct: offset,
		BaseStopLossPct:   baseSL,
	}
}

// TestRiskBudgetCapShrinksOnLoosenedStop 放宽 3pt（6%→9%）→ 合成缩放 0.6667。
func TestRiskBudgetCapShrinksOnLoosenedStop(t *testing.T) {
	res := ComputeSizing(budgetRegime(3, 6), neutralSignal())
	if math.Abs(res.Scale-6.0/9.0) > 1e-9 {
		t.Fatalf("6%%→9%% 应缩到 0.6667，得 %.4f（%v）", res.Scale, res.Reasons)
	}
	// 理由是"风险预算×0.67"，可归因。
	found := false
	for _, r := range res.Reasons {
		if len(r) > 0 && r[:len("风险预算")] == "风险预算" {
			found = true
		}
	}
	if !found {
		t.Fatalf("理由应含风险预算因子，得 %v", res.Reasons)
	}
}

// TestRiskBudgetCapAbstains 无偏移 / 收紧 / 基准不可得 / 守恒关闭 → 一律不缩（scale=1.0）。
func TestRiskBudgetCapAbstains(t *testing.T) {
	cases := []struct {
		name   string
		offset float64
		baseSL float64
		mut    func(*Regime)
	}{
		{"无偏移", 0, 6, nil},
		{"收紧不放杠杆", -1, 6, nil},
		{"基准止损不可得", 3, 0, nil},
		{"守恒开关关闭", 3, 6, func(r *Regime) {
			off := false
			r.SizeCfg.SLOffsetRiskBudgetKeep = &off
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := budgetRegime(tc.offset, tc.baseSL)
			if tc.mut != nil {
				tc.mut(&r)
			}
			res := ComputeSizing(r, neutralSignal())
			if math.Abs(res.Scale-1.0) > 1e-9 {
				t.Fatalf("应弃权（scale=1.0），得 %.4f（%v）", res.Scale, res.Reasons)
			}
		})
	}
}

// TestRiskBudgetCapEnabledAgnostic ComputeSizing 本身**与 Enabled 无关**（门槛在调用方
// `applySizing`）——这是刻意设计：P0 影子读数必须在关闭态下照算真实分布（P0 的全部价值就是
// 拿到"改造前会缩到多少"的基线）。本用例把该契约钉住，防有人"顺手"在合成器里加门槛，
// 让关闭态的影子读数退化成一片 1.0。
// English: ComputeSizing is deliberately Enabled-agnostic (the gate lives in applySizing), so the
// shadow readout can compute the real distribution while the feature is off. Pinned here so nobody
// "helpfully" adds a gate that would flatten the shadow baseline to all-1.0.
func TestRiskBudgetCapEnabledAgnostic(t *testing.T) {
	r := budgetRegime(3, 6)
	r.SizeCfg.Enabled = boolPtr(false)
	res := ComputeSizing(r, neutralSignal())
	if math.Abs(res.Scale-6.0/9.0) > 1e-9 {
		t.Fatalf("合成器应忽略 Enabled（调用方门槛），得 %.4f", res.Scale)
	}
}

// TestRiskBudgetCapIsCapNotWeight 上限类语义：风险预算 0.667 与"大盘跌幅"权重因子取 min
// 而非连乘——同一场股灾不得罚两次。
func TestRiskBudgetCapIsCapNotWeight(t *testing.T) {
	r := budgetRegime(3, 6)
	// 大盘 -2% → indexFactor 走 deep 档 0.40（带宽：≥-1% / (-2,-1) / ≤-2）；风险预算 0.667（上限类）。
	r.IndexChangePct = -2
	res := ComputeSizing(r, neutralSignal())
	// 取 min(0.40, 0.667) = 0.40，而不是 0.40×0.667 = 0.267。
	if math.Abs(res.Scale-0.40) > 1e-9 {
		t.Fatalf("上限类应取 min（0.40）而非连乘（0.267），得 %.4f（%v）", res.Scale, res.Reasons)
	}
}
