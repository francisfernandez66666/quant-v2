// cancel_state_20261028_test.go — §CANCEL-STATE（2026-10-28）存储层状态机用例。
//
// 本仓踩过的坑（2026-10-28 600362）：「撤单请求被客户端受理」被当成「交易所已撤」写进订单行，
// 结果实际成交既没有 fills 也没有持仓，委托行永远停在"已撤"。存储层是这次事故的**落点**——
// 上游说什么状态，库里就存什么状态。所以状态迁移矩阵必须在这里钉死：
//
//	已报/部成 --(发出撤单请求)--> 撤单中 --(交易所确认)--> 已撤/部撤/已成
//	已撤 --(有成交证据)--> 部成/已成      （纠错：只有成交事实能推翻撤单终态）
//	撤单中 --(§CANCEL-REJECT)--> 已报/部成 （唯一允许回退的显式事件）
//
// English: store-level status machine legs for the 撤单中 state.
package store

import (
	"testing"
)

// seedCancelOrder 落一笔在途委托行。
func seedCancelOrder(t *testing.T, db *DB, orderID, signalID, status string) {
	t.Helper()
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: orderID, SignalID: signalID, Code: "600362.SH", Side: "买入",
		Status: status, Price: 36.56, Qty: 400,
		CreatedAt: "2026-10-28T14:57:48+08:00", UserID: "u1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestMarkRealOrderCancelRequested §CANCEL-STATE：撤单请求 → 「撤单中」+ 留痕。
func TestMarkRealOrderCancelRequested(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-1", "SIG-CS-1", "已报")

	ok, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-1", "manual", "手动撤单")
	if err != nil || !ok {
		t.Fatalf("撤单请求应命中在途行: ok=%v err=%v", ok, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if len(os) != 1 {
		t.Fatalf("应只有 1 行, got %d", len(os))
	}
	if os[0].Status != "撤单中" {
		t.Fatalf("状态应为 撤单中, got %s", os[0].Status)
	}
	if os[0].CancelRequestedAt == "" || os[0].CancelSource != "manual" {
		t.Fatalf("撤单留痕应齐备: at=%q source=%q", os[0].CancelRequestedAt, os[0].CancelSource)
	}
}

// TestMarkRealOrderCancelRequestedSkipsTerminal §CANCEL-STATE：终态行不得被改回「撤单中」
// （已撤/已成是交易所事实，撤单请求改不动它）。
func TestMarkRealOrderCancelRequestedSkipsTerminal(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-2", "SIG-CS-2", "已成")

	ok, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-2", "manual", "手动撤单")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if ok {
		t.Fatal("已成行不应被撤单请求命中")
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已成" {
		t.Fatalf("终态不得被改写, got %s", os[0].Status)
	}
}

// TestCancelPendingAdvancesToExchangeTerminal §CANCEL-STATE：「撤单中」收到交易所终态 → 推进终态。
func TestCancelPendingAdvancesToExchangeTerminal(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-3", "SIG-CS-3", "已报")
	if _, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-3", "manual", "手动撤单"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// 交易所确认撤成（网关快照/回报）
	rep := RealOrder{OrderID: "GW-CS-3", SignalID: "SIG-CS-3", Code: "600362.SH", Side: "买入",
		Status: "已撤", Price: 36.56, Qty: 400,
		CreatedAt: "2026-10-28T14:58:10+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportAdvanced {
		t.Fatalf("撤单终态应推进: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已撤" {
		t.Fatalf("撤单中收到确认后应为 已撤, got %s", os[0].Status)
	}
}

// TestCancelPendingRevertsOnExplicitReject §CANCEL-STATE：撤单被交易所**明确拒绝**
// （§CANCEL-REJECT 事件）→ 退回在途态。没有这个显式事件，任何迟到快照都不许回退。
func TestCancelPendingRevertsOnExplicitReject(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-4", "SIG-CS-4", "已报")
	if _, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-4", "manual", "手动撤单"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// 拒绝事件带 §CANCEL-REJECT 前缀 + 拒因
	rep := RealOrder{OrderID: "GW-CS-4", SignalID: "SIG-CS-4", Code: "600362.SH", Side: "买入",
		Status: "已报", Price: 36.56, Qty: 400, Reason: "§CANCEL-REJECT 收盘集合竞价不接受撤单",
		CreatedAt: "2026-10-28T14:58:10+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportAdvanced {
		t.Fatalf("显式拒绝应允许回退: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已报" {
		t.Fatalf("撤单被拒后应退回 已报, got %s", os[0].Status)
	}
	if os[0].CancelReason == "" {
		t.Fatal("撤单被拒应记录拒因")
	}
}

// TestCancelPendingDoesNotRevertWithoutRejectEvent §CANCEL-STATE 反证：没有 §CANCEL-REJECT 事件时，
// 撤单中**不得**被迟到的"已报"快照回退（否则重复撤单风暴 + 状态乱跳）。
func TestCancelPendingDoesNotRevertWithoutRejectEvent(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-5", "SIG-CS-5", "已报")
	if _, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-5", "manual", "手动撤单"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	rep := RealOrder{OrderID: "GW-CS-5", SignalID: "SIG-CS-5", Code: "600362.SH", Side: "买入",
		Status: "已报", Price: 36.56, Qty: 400,
		CreatedAt: "2026-10-28T14:58:10+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportIgnored {
		t.Fatalf("无拒绝事件的迟到快照应 no-op: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "撤单中" {
		t.Fatalf("撤单中不得被无事件的快照回退, got %s", os[0].Status)
	}
}

// TestCancelledTerminalCorrectedByFillEvidence §CANCEL-STATE 纠错①：本地已是「已撤」终态，
// 但**存在成交证据**（fills 有行）时允许纠正为「已成」——只有成交事实能推翻撤单终态。
// 这正是 600362 的形态：15:00 集合竞价成交、15:00:03 撤单被拒、本地误标已撤。
func TestCancelledTerminalCorrectedByFillEvidence(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-6", "SIG-CS-6", "已撤")
	// 成交证据：fills 落一行，指向同一委托/信号
	if err := db.ApplyRealFill(RealFill{
		Code: "600362.SH", Side: "买入", Price: 36.56, Qty: 400, Amount: 14624,
		OrderID: "GW-CS-6", SignalID: "SIG-CS-6", TradedAt: "2026-10-28T15:00:00+08:00",
		UserID: "u1",
	}); err != nil {
		t.Fatalf("apply fill: %v", err)
	}
	rep := RealOrder{OrderID: "GW-CS-6", SignalID: "SIG-CS-6", Code: "600362.SH", Side: "买入",
		Status: "已成", Price: 36.56, Qty: 400,
		CreatedAt: "2026-10-28T15:00:10+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportAdvanced {
		t.Fatalf("有成交证据时已撤应可纠正: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已成" {
		t.Fatalf("有成交证据的已撤应纠正为 已成, got %s", os[0].Status)
	}
}

// TestCancelledTerminalNotCorrectedWithoutFillEvidence §CANCEL-STATE 纠错① 反证：
// 没有成交证据时，「已撤」不得被"已成"推翻（否则一次错误回报就能抹掉真实撤成记录）。
func TestCancelledTerminalNotCorrectedWithoutFillEvidence(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-7", "SIG-CS-7", "已撤")
	rep := RealOrder{OrderID: "GW-CS-7", SignalID: "SIG-CS-7", Code: "600362.SH", Side: "买入",
		Status: "已成", Price: 36.56, Qty: 400,
		CreatedAt: "2026-10-28T15:00:10+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportIgnored {
		t.Fatalf("无成交证据的已撤不得被纠正: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已撤" {
		t.Fatalf("已撤应保持, got %s", os[0].Status)
	}
}

// TestCancelPendingCountsAsInFlight §CANCEL-STATE：「撤单中」必须仍算在途（占资金/占额度）。
// 把它从冻结账里剔掉＝低估敞口：撤单未确认前交易所仍可能成交。
func TestCancelPendingCountsAsInFlight(t *testing.T) {
	db := newTestDB(t)
	seedCancelOrder(t, db, "GW-CS-8", "SIG-CS-8", "已报")
	if _, err := db.MarkRealOrderCancelRequested("u1", "GW-CS-8", "manual", "手动撤单"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	pend, err := db.PendingBuys("u1", "2026-10-28")
	if err != nil {
		t.Fatalf("pending buys: %v", err)
	}
	if len(pend) != 1 {
		t.Fatalf("撤单中应仍计入待买（占资金）, got %d 笔", len(pend))
	}
	frozen, err := db.LocalBuyFrozen("u1", "2026-10-28")
	if err != nil {
		t.Fatalf("frozen: %v", err)
	}
	if frozen <= 0 {
		t.Fatalf("撤单中应仍计入冻结资金, got %.2f", frozen)
	}
}
