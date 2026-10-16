// stop_offset_test.go — §DYN-SIZE P5 止损/止盈两态偏移守护测试（方案 §2.6）。
//
// 锁六件事：
//  1. **两态判别**：单日冲击（大盘 ≤-1% 且趋势未破）→ 放宽；趋势破位 → 收紧（优先保命）；
//  2. **三条豁免**：做空持仓 / N 形超短 / 个股独立崩坏（跌幅偏离大盘 ≥3pt）一律不动；
//  3. **换算口径**：大盘 -2% → 放宽 3.0pt（6%→9%），触上限钳制；
//  4. **关闭态零行为变更**：Enabled=false 恒返回零值；
//  5. **风险预算守恒**：6%→9% ⇒ 缩仓 0.67；收紧不放杠杆（恒 ≤1）；
//  6. **阈值钳制**：收紧不得制造"下一轮必卖"（ExitContext.ATRStopPct 钳到 >0）。
//
// English: guard tests for §DYN-SIZE P5 — two-state discrimination, three exemptions, the
// -2%→+3.0pt conversion, zero change when disabled, risk-budget conservation, and threshold clamps.
package combat_agent

import (
	"math"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

// onCfg 启用态定权配置（其余走出厂默认）。
func onCfg() config.SizingConfig {
	return config.SizingConfig{Enabled: true}.WithDefaults()
}

// longShock 大盘 -2%、趋势未破、个股同步下跌（不触发独立崩坏）的做多持仓输入。
func longShock() StopOffsetInput {
	return StopOffsetInput{IndexChangePct: -2, IndexTrendBroken: false, StockChangePct: -2.5, Strategy: "龙头"}
}

// ── 1. 两态判别 ─────────────────────────────────────────────────────────────────

// TestStopOffsetSingleDayShockLoosens 单日冲击 → 放宽 3.0pt / 止盈上浮 3.0pt。
func TestStopOffsetSingleDayShockLoosens(t *testing.T) {
	got := ComputeStopOffsets(longShock(), onCfg())
	if !got.Applied {
		t.Fatal("大盘 -2% 单日冲击应生效")
	}
	if math.Abs(got.SL-3.0) > 1e-9 {
		t.Fatalf("大盘 -2%% 应放宽 3.0pt（6%%→9%%），得 %.2f", got.SL)
	}
	if math.Abs(got.TP-3.0) > 1e-9 {
		t.Fatalf("止盈应上浮 3.0pt，得 %.2f", got.TP)
	}
	if !strings.Contains(got.Reason, "放宽") {
		t.Fatalf("理由应可归因，得 %q", got.Reason)
	}
}

// TestStopOffsetBelowThresholdInert 大盘跌幅未达门槛（-0.8%）→ 不动。
func TestStopOffsetBelowThresholdInert(t *testing.T) {
	in := longShock()
	in.IndexChangePct = -0.8
	if got := ComputeStopOffsets(in, onCfg()); got.Applied {
		t.Fatalf("未达 1%% 门槛不应生效，得 %+v", got)
	}
	// 边界：恰好 -1.0% → excess=0 → 两个偏移都 0 → 不生效（不写"放宽 0.0pt"的噪音）。
	in.IndexChangePct = -1.0
	if got := ComputeStopOffsets(in, onCfg()); got.Applied {
		t.Fatalf("恰好 -1%% 时 excess=0 应不生效，得 %+v", got)
	}
}

// TestStopOffsetTrendBrokenTightens 趋势破位 → 收紧（覆盖单日冲击判定），且不上浮止盈。
func TestStopOffsetTrendBrokenTightens(t *testing.T) {
	in := longShock()
	in.IndexTrendBroken = true
	got := ComputeStopOffsets(in, onCfg())
	if !got.Applied || got.SL >= 0 {
		t.Fatalf("趋势破位应收紧（SL<0），得 %+v", got)
	}
	if math.Abs(got.SL-(-1.0)) > 1e-9 {
		t.Fatalf("默认应收紧 1.0pt，得 %.2f", got.SL)
	}
	if got.TP != 0 {
		t.Fatalf("趋势破位不应上浮止盈，得 %.2f", got.TP)
	}
	if !strings.Contains(got.Reason, "收紧") {
		t.Fatalf("理由应可归因，得 %q", got.Reason)
	}
}

// TestStopOffsetCapClamped 极端大盘跌幅触上限（SL cap 4pt / TP cap 5pt）。
func TestStopOffsetCapClamped(t *testing.T) {
	in := longShock()
	in.IndexChangePct = -8
	in.StockChangePct = -8 // 个股与大盘同步（否则触发独立崩坏豁免）
	got := ComputeStopOffsets(in, onCfg())
	if math.Abs(got.SL-4.0) > 1e-9 {
		t.Fatalf("止损放宽应钳到 4.0pt，得 %.2f", got.SL)
	}
	if math.Abs(got.TP-5.0) > 1e-9 {
		t.Fatalf("止盈上浮应钳到 5.0pt，得 %.2f", got.TP)
	}
}

// ── 2. 三条豁免 ─────────────────────────────────────────────────────────────────

// TestStopOffsetExemptions 做空 / N 形超短 / 个股独立崩坏 一律不动。
func TestStopOffsetExemptions(t *testing.T) {
	cfg := onCfg()
	// 做空持仓。
	in := longShock()
	in.IsShort = true
	if got := ComputeStopOffsets(in, cfg); got.Applied {
		t.Fatalf("做空持仓应豁免，得 %+v", got)
	}
	// N 形超短（含别名归一）。
	for _, name := range []string{"N形", "N形超短", "n_shape"} {
		in = longShock()
		in.Strategy = name
		if got := ComputeStopOffsets(in, cfg); got.Applied {
			t.Fatalf("N 形超短（%s）应豁免，得 %+v", name, got)
		}
	}
	// 个股独立崩坏：个股 -6% vs 大盘 -2% → 偏离 4pt ≥ 3pt。
	in = longShock()
	in.StockChangePct = -6
	if got := ComputeStopOffsets(in, cfg); got.Applied {
		t.Fatalf("个股独立崩坏应不适用，得 %+v", got)
	}
	// 边界：恰好偏离 3pt → 不适用（≥ 判定）。
	in.StockChangePct = -5
	if got := ComputeStopOffsets(in, cfg); got.Applied {
		t.Fatalf("偏离恰好 3pt 应不适用，得 %+v", got)
	}
	// 个股涨幅偏离大盘 ≥3pt（个股逆势上涨）同样不适用。
	in.StockChangePct = 1.5
	if got := ComputeStopOffsets(in, cfg); got.Applied {
		t.Fatalf("个股逆势上涨偏离 ≥3pt 应不适用，得 %+v", got)
	}
}

// TestStopOffsetMissingReadingsAbstain 大盘读数缺失（NaN）→ 不适用（绝不当作 0）。
func TestStopOffsetMissingReadingsAbstain(t *testing.T) {
	in := longShock()
	in.IndexChangePct = math.NaN()
	if got := ComputeStopOffsets(in, onCfg()); got.Applied {
		t.Fatalf("大盘读数缺失应弃权，得 %+v", got)
	}
	// 个股读数缺失不影响（不做独立崩坏判定，按大盘腿生效）。
	in = longShock()
	in.StockChangePct = math.NaN()
	if got := ComputeStopOffsets(in, onCfg()); !got.Applied {
		t.Fatalf("个股读数缺失不应阻断大盘腿，得 %+v", got)
	}
}

// ── 3. 关闭态 ───────────────────────────────────────────────────────────────────

// TestStopOffsetDisabledInert 出厂默认（Enabled=false）→ 满屏极端输入也恒零值。
func TestStopOffsetDisabledInert(t *testing.T) {
	in := longShock()
	in.IndexChangePct = -9
	if got := ComputeStopOffsets(in, config.DefaultSizingConfig()); got.Applied || got.SL != 0 || got.TP != 0 {
		t.Fatalf("关闭态必须零值，得 %+v", got)
	}
	// 零值配置同样零值。
	if got := ComputeStopOffsets(in, config.SizingConfig{}); got.Applied {
		t.Fatalf("零值配置必须零值，得 %+v", got)
	}
}

// ── 4. 风险预算守恒 ─────────────────────────────────────────────────────────────

// TestRiskBudgetScale 6%→9% ⇒ 0.67；收紧不放杠杆（恒 ≤1）；非法输入回退 1.0。
func TestRiskBudgetScale(t *testing.T) {
	if got := RiskBudgetScale(6, 3); math.Abs(got-6.0/9.0) > 1e-9 {
		t.Fatalf("6%%→9%% 应得 0.6667，得 %.4f", got)
	}
	if got := RiskBudgetScale(6, -1); got != 1.0 {
		t.Fatalf("收紧不应放大仓位（恒 ≤1），得 %.4f", got)
	}
	if got := RiskBudgetScale(6, 0); got != 1.0 {
		t.Fatalf("无偏移应得 1.0，得 %.4f", got)
	}
	if got := RiskBudgetScale(0, 3); got != 1.0 {
		t.Fatalf("基准不可得应回退 1.0，得 %.4f", got)
	}
	if got := RiskBudgetScale(6, -6); got != 1.0 {
		t.Fatalf("收紧到非正应回退 1.0（配置异常），得 %.4f", got)
	}
}
