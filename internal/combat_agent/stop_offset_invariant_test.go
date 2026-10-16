// stop_offset_invariant_test.go — §DYN-SIZE §5「不变量」门禁锁。
//
// 锁的内容（方案 §5 原文）：
//
//	深破兜底 / 日内熔断 / 跌停追卖闸 / 持仓上限 **在偏移生效时行为不变**。
//
// 其中"深破兜底"是唯一由本包自己负责的一条，也是最容易在实现里被悄悄架空的一条：
// 放宽止损 5%→8% 之后，如果深破线仍按**冻结的 5%**算（-10%），那么"放宽"刚生效就被深破线
// 反噬——双重放宽变成双重收紧，owner 的诉求被架空。故深破线必须以**有效止损**为基准（-16%）。
//
// 本文件用同一组输入跑两条腿，把"偏移关/开"的差异钉死：
//
//	腿 A（Enabled=false，偏移关）：有效止损 == 冻结止损，pnl -12% 触发**硬止损**（深破 -10%）
//	腿 B（Enabled=true，偏移开）：有效止损 5%+3pt=8%，pnl -12% 只触发**减仓**（未达深破 -16%）
//
// 两条腿的差异**必须存在**——若相同，说明深破线没跟着有效止损走（本锁失败）。
//
// English: the §5 invariant lock for the deep-breach backstop. Loosening the stop from 5% to 8%
// must also move the deep-breach line from -10% to -16%; otherwise the loosening is immediately
// undone by the backstop. The same inputs are run with the offset off and on, and the two legs
// MUST differ — if they agree, the backstop is still keyed on the frozen stop.
package combat_agent

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// TestStopOffsetDeepBreachFollowsEffectiveStop §5 不变量：深破兜底以有效止损为基准。
func TestStopOffsetDeepBreachFollowsEffectiveStop(t *testing.T) {
	const (
		cost    = 10.0
		stopPct = 5.0 // 冻结止损 5%
		// 现价 8.80 → 盈亏 -12%：
		//   冻结基准：-12% ≤ -2×5% = -10% ⇒ 深破 ⇒ 硬止损
		//   有效基准：-12% > -2×8% = -16% ⇒ 未深破 ⇒ 减仓
		price = 8.80
	)
	// 大盘 -2%（趋势未破 ⇒ 单日冲击），个股涨跌 0% → 与大盘差 2pt < 3pt（不触发独立崩坏豁免）。
	// 战法用「龙头」：N 形超短会被豁免，用它测不出偏移。
	quotes := map[string]*data.StockInfo{
		"600000": {Code: "600000", Name: "浦发银行", Price: price, ChangePct: 0},
	}

	// ── 腿 A：偏移关（Enabled=false）⇒ 有效止损 == 冻结止损 ⇒ 硬止损 ──
	aOff, rptOff, mOff := newAlertTestRig(t)
	rptOff.LogSignal("pos-off", "600000", "浦发银行", "做多", "龙头", cost, 15.0, stopPct)
	off := config.DefaultSizingConfig() // Enabled=false（出厂默认）
	aOff.SetMarketStopContext(off, -2.0, false)
	alertsOff := aOff.CheckPositionAlerts(rptOff, mOff, quotes, nil)
	if len(alertsOff) != 1 {
		t.Fatalf("腿 A：应产出 1 条提醒, got %d", len(alertsOff))
	}
	if alertsOff[0].AlertType != "止损" {
		t.Fatalf("腿 A（偏移关，深破基准 -10%%）：-12%% 应为硬止损, got %s", alertsOff[0].AlertType)
	}

	// ── 腿 B：偏移开（Enabled=true）⇒ 有效止损 8% ⇒ 未深破 ⇒ 减仓 ──
	aOn, rptOn, mOn := newAlertTestRig(t)
	rptOn.LogSignal("pos-on", "600000", "浦发银行", "做多", "龙头", cost, 15.0, stopPct)
	on := config.DefaultSizingConfig()
	on.Enabled = true
	on.Shadow = nil // 非影子：偏移真生效（ShadowOn 只影响 signalctl 的下发，不影响本处判定）
	aOn.SetMarketStopContext(on, -2.0, false)
	alertsOn := aOn.CheckPositionAlerts(rptOn, mOn, quotes, nil)
	if len(alertsOn) != 1 {
		t.Fatalf("腿 B：应产出 1 条提醒, got %d", len(alertsOn))
	}
	if alertsOn[0].AlertType != "减仓" {
		t.Fatalf("腿 B（偏移开，有效止损 8%%、深破基准 -16%%）：-12%% 应为减仓, got %s（理由=%s）",
			alertsOn[0].AlertType, alertsOn[0].Reason)
	}
	if alertsOn[0].Action != "卖出" {
		t.Fatalf("腿 B：减仓 Action 应为卖出, got %s", alertsOn[0].Action)
	}

	// ── 反证：两条腿的结论必须不同，否则本锁什么都没验证 ──
	if alertsOff[0].AlertType == alertsOn[0].AlertType {
		t.Fatalf("偏移关/开得到相同结论（%s）——深破兜底没跟着有效止损走，§5 不变量被架空",
			alertsOff[0].AlertType)
	}
}

// TestStopOffsetDoesNotMutateFrozenStop §5 不变量：偏移绝不改写开仓冻结的 pos.StopLossPct。
// 冻结值一旦被改，后续每轮都在"上一次修正"之上再修正，偏移会累积漂移。
func TestStopOffsetDoesNotMutateFrozenStop(t *testing.T) {
	const stopPct = 5.0
	a, rpt, m := newAlertTestRig(t)
	rpt.LogSignal("pos-frozen", "600000", "浦发银行", "做多", "龙头", 10.0, 15.0, stopPct)
	cfg := config.DefaultSizingConfig()
	cfg.Enabled = true
	a.SetMarketStopContext(cfg, -3.0, false) // 大盘 -3% ⇒ 放宽已顶到 SLOffsetCap

	quotes := map[string]*data.StockInfo{
		"600000": {Code: "600000", Name: "浦发银行", Price: 9.00, ChangePct: 0},
	}
	// 连跑三轮：偏移若被写回冻结值，第二轮就会在"已放宽"的基准上再放宽
	for i := 0; i < 3; i++ {
		a.CheckPositionAlerts(rpt, m, quotes, nil)
	}
	// 冻结值必须原封不动（读回开仓记录本身）
	e := rpt.FindBySignalID("pos-frozen")
	if e == nil {
		t.Fatal("未取到持仓记录，用例失去观测点")
	}
	if e.StopLossPct != stopPct {
		t.Fatalf("三轮后冻结止损被改写：%.2f（应恒为 %.2f）——偏移会累积漂移", e.StopLossPct, stopPct)
	}
}
