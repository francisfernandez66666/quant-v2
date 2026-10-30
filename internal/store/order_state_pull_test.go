// order_state_pull_test.go — §ORDER-STATE-PULL（2026-10-30）账本层回归锁。
//
// 钉住两件事：
//  1. 网关判定终态后，该 signal_id 当日不可再被「零成交已撤」规则放行重发；
//  2. 委托号回填与状态写入解耦——跨日号复用撞主键时，状态仍必须落库。
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestFinalizeTerminalSignalBlocksResend 终态收口必须同时置重发禁令。
func TestFinalizeTerminalSignalBlocksResend(t *testing.T) {
	db := openTestDB(t)
	uid := "u_t"
	sid := "buy:300502:fac_150:20261030"
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "pend:" + sid, SignalID: sid, Code: "300502.SZ", Side: "买入",
		Status: "撤单中", Price: 195.01, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 基线：撤单中本身不可重放
	if ok, _ := db.ResetFailedRealOrder(uid, sid); ok {
		t.Fatal("撤单中不应可重放")
	}
	if err := db.FinalizeTerminalSignal(uid, sid, "已撤", "§ORDER-STATE-PULL 测试"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "已撤" {
		t.Fatalf("状态应为已撤, got %q", st)
	}
	blocked, err := db.IsOrderRetryBlocked(uid, sid)
	if err != nil || !blocked {
		t.Fatalf("必须置重发禁令, blocked=%v err=%v", blocked, err)
	}
	// 零成交已撤在无禁令时本可重放——禁令必须把它挡住
	if ok, _ := db.ResetFailedRealOrder(uid, sid); ok {
		t.Fatal("终态收口后不得再放行重发")
	}
}

// TestFinalizeTerminalSignalKeepsLocalTerminal 本地已是终态时不改写状态，只落禁令。
func TestFinalizeTerminalSignalKeepsLocalTerminal(t *testing.T) {
	db := openTestDB(t)
	uid := "u_t2"
	sid := "SIG-T2"
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "pend:" + sid, SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: "已成", Price: 10, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.FinalizeTerminalSignal(uid, sid, "已撤", "reason"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "已成" {
		t.Fatalf("本地终态不得被覆盖, got %q", st)
	}
}

// TestUpdateRealOrderBackfillKeepsStatusOnIDCollision 号撞主键时状态仍落库。
func TestUpdateRealOrderBackfillKeepsStatusOnIDCollision(t *testing.T) {
	db := openTestDB(t)
	uid := "u_bf"
	sid := "SIG-BF"
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "pend:" + sid, SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	// 历史行占着要回填的委托号（跨日复用）
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "xt1", SignalID: "SIG-OLD", Code: "301226.SZ", Side: "买入",
		Status: "已撤", Price: 1, Qty: 100,
		CreatedAt: "2026-09-22 10:00:00", UserID: uid,
	}); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID(uid, sid, "xt1", "部成"); err != nil {
		t.Fatalf("撞键不得上抛（状态已落库）: %v", err)
	}
	if st, _ := db.RealOrderStatusForUser(uid, sid); st != "部成" {
		t.Fatalf("状态必须落库, got %q", st)
	}
	rows, err := db.RealOrdersForUser(uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.SignalID == sid && r.OrderID == "xt1" {
			t.Fatal("撞键时不得抢占历史行的委托号")
		}
	}
}
