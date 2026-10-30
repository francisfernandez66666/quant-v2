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

// TestPendingBuysExposeCancelAge §P1-1：在途买入必须带撤单时刻，卡单统计能算出滞留时长。
// 种的时间一律从"现在"往前推：写死 09:31:53 这种钟面在午夜后跑等于把 cancel_requested_at
// 种到未来，time.Since 得负数、判龄归 0（10-31 00:44 实测就是这个假失败）。
func TestPendingBuysExposeCancelAge(t *testing.T) {
	db := openTestDB(t)
	uid := "u_p1"
	now := time.Now()
	day := now.Format("2006-01-02")
	created := now.Add(-120 * time.Second)
	cancelAt := now.Add(-60 * time.Second)
	if created.Format("2006-01-02") != day {
		// 跨零点的极小窗口：退回今日 0 点之后，保证 substr(created_at,1,10) 仍命中本日的 day
		created = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 1, 0, time.Local)
		cancelAt = now
	}
	createdStr := created.Format("2006-01-02 15:04:05")
	cancelStr := cancelAt.Format("2006-01-02 15:04:05")
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "seq:1", SignalID: "buy:300502:fac_150:20261030", Code: "300502.SZ",
		Side: "买入", Status: "撤单中", Price: 195.01, Qty: 100,
		CreatedAt: createdStr, UserID: uid,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE orders SET cancel_requested_at=? WHERE signal_id=? AND user_id=?`,
		cancelStr, "buy:300502:fac_150:20261030", uid); err != nil {
		t.Fatalf("set cancel time: %v", err)
	}
	pend, err := db.PendingBuys(uid, day)
	if err != nil || len(pend) != 1 {
		t.Fatalf("PendingBuys: n=%d err=%v", len(pend), err)
	}
	if pend[0].CancelRequestedAt != cancelStr {
		t.Fatalf("在途单必须带撤单时刻, got %q", pend[0].CancelRequestedAt)
	}
	n, age, err := db.CountStuckCancelBuys(uid, day)
	if err != nil || n != 1 {
		t.Fatalf("卡单统计: n=%d err=%v", n, err)
	}
	// 判龄必须对得上种下去的那 60 秒（不是"恒正"这种废话断言）：判的是"用的是撤单时刻、
	// 而且真按秒算"，写死成 >0 的话种到未来也能蒙绿。
	if age < 55*time.Second || age > 5*time.Minute {
		t.Fatalf("卡单滞留时长应与种下的撤单时刻一致（期望 ≈60s）, got %v", age)
	}
}

// TestUpdateRealOrderWritesBrokerID §P1-5：回填必须把真实委托号写进审计列。
func TestUpdateRealOrderWritesBrokerID(t *testing.T) {
	db := openTestDB(t)
	uid := "u_p5"
	sid := "SIG-P5"
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "pend:" + sid, SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID(uid, sid, "xt123", "已报"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var broker string
	if err := db.db.QueryRow(`SELECT COALESCE(broker_order_id,'') FROM orders WHERE signal_id=? AND user_id=?`, sid, uid).Scan(&broker); err != nil {
		t.Fatalf("read broker id: %v", err)
	}
	if broker != "xt123" {
		t.Fatalf("broker_order_id 应为 xt123, got %q", broker)
	}
	// 撞键路径也要落审计号
	sid2 := "SIG-P5B"
	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "pend:" + sid2, SignalID: sid2, Code: "600001.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: uid,
	}); err != nil {
		t.Fatalf("seed2: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID(uid, sid2, "xt123", "部成"); err != nil {
		t.Fatalf("collision backfill 不应上抛: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COALESCE(broker_order_id,'') FROM orders WHERE signal_id=? AND user_id=?`, sid2, uid).Scan(&broker); err != nil {
		t.Fatalf("read broker id2: %v", err)
	}
	if broker != "xt123" {
		t.Fatalf("撞键时真实号仍须落审计列, got %q", broker)
	}
	if st, _ := db.RealOrderStatusForUser(uid, sid2); st != "部成" {
		t.Fatalf("撞键时状态仍须落库, got %q", st)
	}
}
