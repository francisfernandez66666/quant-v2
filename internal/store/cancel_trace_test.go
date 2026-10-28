// 撤单留痕竞态回归（§FIX-20261028-F14，2026-10-29）。
//
// 缺陷本体：MarkRealOrderCancelRequested 的 UPDATE 带 `status IN ('已报','部成',…)` 守卫，
// 撤单请求发出后若终态回报先到（快速受理/秒撤柜台、sweep 批量），CAS 落空、三列永远为空。
// 本地 UAT 实测 8 条真撤单行的 cancel_requested_at/source/reason 全空。
//
// 本文件锁三件事：
//  1. 行已被推成终态，SetCancelTrace 仍能把留痕写进去（且不改状态）；
//  2. 已有留痕不被后来的 source 覆盖（**空串**场景——COALESCE 在这里不顶用，必须 CASE WHEN）；
//  3. 行不存在时返回 (false,nil)，不 panic。
//
// English: the cancel-trace write must survive a fast terminal report and must never overwrite
// an existing trace (empty strings, not NULLs, are the storage default).
package store

import "testing"

func seedOrder(t *testing.T, db *DB, orderID, status, userID string) {
	t.Helper()
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: orderID, SignalID: "sig-" + orderID, Code: "600519.SH", Side: "买入",
		Status: status, Price: 1500, Qty: 100, CreatedAt: "2026-10-29T09:31:00+08:00", UserID: userID,
	}); err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func traceOf(t *testing.T, db *DB, userID, orderID string) RealOrder {
	t.Helper()
	// 注意：RealOrdersForUser("") 只回遗留全局行（user_id=''），这里必须按账号查。
	rows, err := db.RealOrdersForUser(userID)
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	for _, r := range rows {
		if r.OrderID == orderID {
			return r
		}
	}
	t.Fatalf("订单 %s 不存在", orderID)
	return RealOrder{}
}

// TestSetCancelTraceSurvivesFastTerminal 终态行也要能补留痕，且状态不被改回。
func TestSetCancelTraceSurvivesFastTerminal(t *testing.T) {
	db := testDB(t)
	seedOrder(t, db, "GW-F14-A", "已报", "uA")
	// 模拟回报抢跑：先推成终态
	if _, err := db.AdvanceRealOrderStatus("uA", "sig-GW-F14-A", "已撤"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	ok, err := db.SetCancelTrace("uA", "GW-F14-A", "manual", "手动撤单请求已发出")
	if err != nil || !ok {
		t.Fatalf("SetCancelTrace 应成功: ok=%v err=%v", ok, err)
	}
	r := traceOf(t, db, "uA", "GW-F14-A")
	if r.Status != "已撤" {
		t.Fatalf("留痕不得改状态，实到 %q", r.Status)
	}
	if r.CancelRequestedAt == "" || r.CancelSource != "manual" || r.CancelReason == "" {
		t.Fatalf("终态行的留痕三列应被补齐，实到 %+v", r)
	}
}

// TestSetCancelTraceDoesNotOverwrite 已有留痕不被覆盖（专锁空串/COALESCE 的坑）。
func TestSetCancelTraceDoesNotOverwrite(t *testing.T) {
	db := testDB(t)
	seedOrder(t, db, "GW-F14-B", "已报", "uA")
	if _, err := db.SetCancelTrace("uA", "GW-F14-B", "manual", "手动撤单"); err != nil {
		t.Fatalf("first trace: %v", err)
	}
	first := traceOf(t, db, "uA", "GW-F14-B")
	// 第二次来自网关兜底：三列都已有值 ⇒ 必须原样保留
	if _, err := db.SetCancelTrace("uA", "GW-F14-B", "gateway", "网关回报终态"); err != nil {
		t.Fatalf("second trace: %v", err)
	}
	second := traceOf(t, db, "uA", "GW-F14-B")
	if second.CancelSource != "manual" {
		t.Fatalf("已有来源不得被覆盖：want manual, got %q（COALESCE 处理不了空串，必须 CASE WHEN）", second.CancelSource)
	}
	if second.CancelRequestedAt != first.CancelRequestedAt {
		t.Fatalf("已有请求时刻不得被改写：%q → %q", first.CancelRequestedAt, second.CancelRequestedAt)
	}
}

// TestSetCancelTraceMissingRow 行不存在 ⇒ (false,nil)。
func TestSetCancelTraceMissingRow(t *testing.T) {
	db := testDB(t)
	ok, err := db.SetCancelTrace("uA", "GW-NOT-EXIST", "manual", "x")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if ok {
		t.Fatal("不存在的行应返回 false")
	}
}
