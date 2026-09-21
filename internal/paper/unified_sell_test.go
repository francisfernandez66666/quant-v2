// unified_sell_test.go — §SELLPOINT-UNIFY P3 模拟盘并轨的 paper 侧行为锁。
//
// 钉死三件事：
//
//	① ApplyUnifiedSell 是切闸后自动卖出的唯一入口：close 全平（收益回池）、trim 半仓且
//	   每码每日一次去重（复用 autoSellLocked 的 trimDone/T+1 全套守卫）；
//	② 资金安全守卫与旧链同口径——AutoSell=false、price≤0、action 非法、未持仓一律不成交；
//	③ T+1 当日拒单留痕不假成交（GAP1.9 语义经统一入口原样保留）。
//
// English: P3 paper-side locks — ApplyUnifiedSell is the sole auto-sell exit (full close /
// once-daily half trim reusing autoSellLocked guards); safety guards unchanged; T+1 rejects
// stay audited, never fake-filled.
package paper

import (
	"strings"
	"testing"
)

// TestApplyUnifiedSellClose ① close 处置全平：持仓清空、现金回池、订单留痕带裁决原因。
func TestApplyUnifiedSellClose(t *testing.T) {
	e := sellTestEngine(t, true)
	buyFill(t, e)
	t1Ready(e) // 越过 T+1：模拟次日

	if !e.ApplyUnifiedSell("300001", "close", 11, "[统一裁决] 窗结算无做多信号") {
		t.Fatal("close 处置应执行")
	}
	if _, held := e.positions["300001"]; held {
		t.Fatal("close 后应无持仓")
	}
	last := e.orders[len(e.orders)-1]
	if last.Side != "sell" || last.Status != "filled" || last.Qty != 1000 {
		t.Fatalf("close 应留 1000 股 filled 卖单: %+v", last)
	}
	if !strings.Contains(last.Reason, "[统一裁决]") {
		t.Fatalf("订单留痕应携带裁决原因，得 %q", last.Reason)
	}
	// 重复处置（状态机下轮重放/双轮叠加）→ 未持仓 false，不产生第二笔卖出
	before := len(e.orders)
	if e.ApplyUnifiedSell("300001", "close", 11, "[统一裁决] 重放") {
		t.Fatal("已平仓的重复处置应返回 false")
	}
	if len(e.orders) != before {
		t.Fatalf("重复处置不得新增订单: %d → %d", before, len(e.orders))
	}
}

// TestApplyUnifiedSellTrimOnceDaily ① trim 处置半仓（整手）且当日去重：首轮 1000→500，
// 同日重放不再减。
func TestApplyUnifiedSellTrimOnceDaily(t *testing.T) {
	e := sellTestEngine(t, true)
	buyFill(t, e)
	t1Ready(e)

	if !e.ApplyUnifiedSell("300001", "trim", 10.5, "[统一裁决] 移动止盈减半") {
		t.Fatal("trim 处置应执行")
	}
	if p := e.positions["300001"]; p == nil || p.Qty != 500 {
		t.Fatalf("trim 应剩 500 股, got %+v", e.positions["300001"])
	}
	// 同日重放：trimDone 去重，持仓不变（入口仍返回 true=账本在持，但无新成交）
	before := len(e.orders)
	e.ApplyUnifiedSell("300001", "trim", 10.5, "[统一裁决] 重放")
	if len(e.orders) != before {
		t.Fatalf("同日二次 trim 应被去重: %d → %d", before, len(e.orders))
	}
	if p := e.positions["300001"]; p == nil || p.Qty != 500 {
		t.Fatalf("去重后持仓应不变, got %+v", e.positions["300001"])
	}
}

// TestApplyUnifiedSellGuards ② 守卫负锁：AutoSell 关 / 无效价 / 非法 action / 未持仓
// 全部返回 false 且不动账。
func TestApplyUnifiedSellGuards(t *testing.T) {
	// AutoSell=false
	e := sellTestEngine(t, false)
	buyFill(t, e)
	t1Ready(e)
	if e.ApplyUnifiedSell("300001", "close", 9, "[统一裁决] 止损") {
		t.Fatal("AutoSell 关闭时不得执行处置")
	}
	if p := e.positions["300001"]; p == nil || p.Qty != 1000 {
		t.Fatal("AutoSell 关闭时持仓不得变动")
	}
	// 有效引擎上的三类非法输入
	e2 := sellTestEngine(t, true)
	buyFill(t, e2)
	t1Ready(e2)
	if e2.ApplyUnifiedSell("300001", "close", 0, "无效价") {
		t.Fatal("price≤0 不得执行（宁可不卖不错价记账）")
	}
	if e2.ApplyUnifiedSell("300001", "halt", 10, "非法动作") {
		t.Fatal("非法 action 不得执行")
	}
	if e2.ApplyUnifiedSell("999999", "close", 10, "未持仓") {
		t.Fatal("未持仓 code 不得执行")
	}
	if p := e2.positions["300001"]; p == nil || p.Qty != 1000 {
		t.Fatal("负例后持仓应原样保留")
	}
}

// TestApplyUnifiedSellT1RejectedNotFakeFilled ③ T+1 当日：处置到达但卖出被拒——
// 只留 rejected 卖单，不产生假成交（GAP1.9 语义经统一入口原样保留）。
func TestApplyUnifiedSellT1RejectedNotFakeFilled(t *testing.T) {
	e := sellTestEngine(t, true)
	buyFill(t, e) // 当日买入，未 t1Ready → T+1 锁定
	tradesBefore := len(e.trades)
	if !e.ApplyUnifiedSell("300001", "close", 11, "[统一裁决] 窗结算") {
		t.Fatal("持仓在账，入口应受理（成交与否由 autoSellLocked 判定）")
	}
	if p := e.positions["300001"]; p == nil || p.Qty != 1000 {
		t.Fatal("T+1 拦截后持仓应保留")
	}
	if len(e.trades) != tradesBefore {
		t.Fatalf("被拒处置不得产生成交记录: %d → %d", tradesBefore, len(e.trades))
	}
	last := e.orders[len(e.orders)-1]
	if last.Status != "rejected" || last.Side != "sell" {
		t.Fatalf("被拒处置应留 rejected 卖单: %+v", last)
	}
}

// TestSellProbesSnapshot 探针：仅返回 Qty>0 的持仓，成本价/名称随账本快照。
func TestSellProbesSnapshot(t *testing.T) {
	e := sellTestEngine(t, true)
	if len(e.SellProbes()) != 0 {
		t.Fatal("空账本探针应为 0")
	}
	buyFill(t, e)
	probes := e.SellProbes()
	if len(probes) != 1 || probes[0].Code != "300001" || probes[0].EntryPrice != 10 || probes[0].Qty != 1000 {
		t.Fatalf("探针快照不符: %+v", probes)
	}
}
