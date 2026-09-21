// advice_sellpoint_test.go — §P4 缺陷3 标签映射收口 + 敞口 B 废除的回归锁
// （docs/BUGFIX_SELLPOINT_FALSEPOSITIVE_20260921.md §三.3/§三.4）。
//
// 钉死四件事：
//
//	① 做空模式（AlertType=做空）按结构化 SellLevel 定帽：减仓级绝不再落 default 顶「止盈」；
//	② default 分支按盈亏符号：亏损票（超期离场「提示」等）永不叫「止盈」；
//	③ 利空归因路走结构化 AlertType=利空抛售（清仓级语义保留），不再依赖 reason 子串；
//	④ 废除「reason 含 利空/抛售 子串 → 止损(高)」——否定句式"该票无利空迹象"不得被升级。
//
// English: P4 label-mapping locks — short-mode badges from the structured SellLevel, the P/L-sign
// default, structured bearish attribution, and removal of the reason-substring 止损 heuristic.
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/store"
)

// spAdviceInput 亏损持仓（成本10/现价9.3 → −7%）的建议输入。
func spAdviceInput() AdviceInput {
	return AdviceInput{Positions: []store.RealPosition{
		{TsCode: "600580.SH", Name: "卧龙电驱", Qty: 100, CostPrice: 10},
	}}
}

// spFromSignal 以统一输入跑一条 fromSignal（现价 9.3，亏损 −7%）。
func spFromSignal(t *testing.T, sig combat_agent.Signal) *PositionAdvice {
	t.Helper()
	sig.Code = "600580"
	if sig.Price <= 0 {
		sig.Price = 9.3
	}
	pa := fromSignal(sig, spAdviceInput(), time.Now(), "")
	if pa == nil {
		t.Fatal("fromSignal 不应返回 nil")
	}
	return pa
}

// TestFromSignalShortModeUsesSellLevel ①：做空模式减仓级提醒 → Action=减仓（旧实现落 default 顶「止盈」帽）。
func TestFromSignalShortModeUsesSellLevel(t *testing.T) {
	cases := []struct {
		level      string
		wantAction string
		forbidden  string
	}{
		{"减仓", "减仓", "止盈"},
		{"清仓", "止损", "止盈"}, // 亏损票清仓级 → 止损高
		{"提示", "减仓", "止盈"}, // 提示级亏损票按符号落 default 口径
	}
	for _, c := range cases {
		pa := spFromSignal(t, combat_agent.Signal{
			AlertType: "做空", Direction: "做空", Action: "卖出",
			SellLevel: c.level, Reason: "卖点等级:" + c.level + "; 放量下跌派发迹象",
		})
		if pa.Action != c.wantAction {
			t.Errorf("做空/SellLevel=%s 应 %s, got %s", c.level, c.wantAction, pa.Action)
		}
		if pa.Action == c.forbidden {
			t.Errorf("做空/SellLevel=%s 不得顶「%s」帽", c.level, c.forbidden)
		}
	}
	// 盈利票清仓级仍是止盈高（符号守卫只保护亏损票）
	gp := AdviceInput{Positions: []store.RealPosition{{TsCode: "600580.SH", Name: "卧龙电驱", Qty: 100, CostPrice: 8}}}
	sig := combat_agent.Signal{Code: "600580", AlertType: "做空", SellLevel: "清仓", Price: 9.3}
	pa := fromSignal(sig, gp, time.Now(), "")
	if pa == nil || pa.Action != "止盈" {
		t.Fatalf("盈利票做空清仓级应为止盈/高, got %+v", pa)
	}
}

// TestFromSignalDefaultNeverTakeProfitOnLoss ②：超期离场（提示）等未知 AlertType 亏损票不得叫止盈。
func TestFromSignalDefaultNeverTakeProfitOnLoss(t *testing.T) {
	for _, alert := range []string{"提示", "持仓超期离场", "系统性风险", "预期差"} {
		pa := spFromSignal(t, combat_agent.Signal{AlertType: alert, Direction: "提醒", Reason: "超期强制离场"})
		if pa.Action == "止盈" {
			t.Errorf("AlertType=%s 亏损票（−7%%）不得顶「止盈」帽, got %+v", alert, pa)
		}
		if pa.Action != "减仓" {
			t.Errorf("AlertType=%s 亏损票 default 应减仓/中, got %s/%s", alert, pa.Action, pa.Level)
		}
	}
}

// TestFromSignalBearishStructured ③：利空抛售走结构化 AlertType——亏损票仍是止损高（FIX#13 语义）。
func TestFromSignalBearishStructured(t *testing.T) {
	pa := spFromSignal(t, combat_agent.Signal{
		AlertType: "利空抛售", Direction: "提醒", Action: "卖出",
		Reason: "利空归因: 板块双源验证利空 → 建议尽快抛售该持仓以规避风险",
	})
	if pa.Action != "止损" || pa.Level != "高" {
		t.Fatalf("利空抛售亏损票应为止损/高（结构化承接）, got %s/%s", pa.Action, pa.Level)
	}
}

// TestFromSignalBearishSubstringAbolished ④：子串映射已废除——reason 仅含"利空/抛售"子串
// 但 AlertType 是普通提示级时，不得被升级为止损高；否定句式"该票无利空迹象"同锁。
func TestFromSignalBearishSubstringAbolished(t *testing.T) {
	for _, reason := range []string{
		"放量下跌: 折算日量=1.9倍均量, 有资金派发迹象（该票无利空迹象）",
		"消息面提及抛售传闻但未证实",
	} {
		pa := spFromSignal(t, combat_agent.Signal{AlertType: "提示", Direction: "提醒", Action: "关注", Reason: reason})
		if pa.Action == "止损" && pa.Level == "高" {
			t.Errorf("利空/抛售子串不得再强制升级止损高: reason=%q got %+v", reason, pa)
		}
	}
}
