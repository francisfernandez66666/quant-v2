// §H1-MG（2026-09-22 PM 修复批）已撤零成交卖单可重放的存储层两极锁。
// 缺陷原文：UpsertRealOrder 纯 INSERT OR IGNORE 按 signal_id 判重不看状态、ResetFailedRealOrder
// 只放行「发送失败」、scoring_loop 把 duplicate 当目标达成静默返回——保护性卖单被撤且一股未卖时，
// 同幂等键整天 duplicate，当日止损卖出猝死（H-4 同日上午 603468 手动卖出后账本冻结是另一腿，
// 本条是"撤单零成交"这条独立腿）。
// 两极：已撤+零成交 → 同键可重放（重置回已报+换新 pend 占位号）；已撤+有成交 → 仍不可重放
// （部分成交已占额度，剩余仓位由下一轮评分以新键接管）。
// English: §H1-MG storage-level two-pole lock — a cancelled sell with zero fills becomes replayable
// under the same idempotency key; a cancelled sell WITH fills stays non-replayable.
package store

import "testing"

// m12Status 读回委托行状态（测试内小工具，避免各断言重复展开查询）。
func m12Order(t *testing.T, db *DB, uid, sid string) RealOrder {
	t.Helper()
	for _, o := range mustOrders(t, db, uid) {
		if o.SignalID == sid {
			return o
		}
	}
	t.Fatalf("orders 无 signal_id=%s", sid)
	return RealOrder{}
}

func mustOrders(t *testing.T, db *DB, uid string) []RealOrder {
	t.Helper()
	os, err := db.RealOrdersForUser(uid)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	return os
}

func TestResetCancelledZeroFillReplayable(t *testing.T) {
	db := testDB(t)
	// 极一：已撤+零成交 → 可重放。走完整生命周期：报出→回填网关真实单号→撤单。
	o := RealOrder{OrderID: "pend:H1A", SignalID: "H1-A", Code: "600000.SH", Side: "卖出",
		Status: "已报", Price: 10, Qty: 900, CreatedAt: "2026-09-22T09:35:00+08:00", UserID: "u_h1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID("u_h1", "H1-A", "GW-H1A", "已报"); err != nil {
		t.Fatalf("backfill gw id: %v", err)
	}
	if ok, err := db.AdvanceRealOrderStatus("u_h1", "H1-A", "已撤"); err != nil || !ok {
		t.Fatalf("advance to 已撤: ok=%v err=%v", ok, err)
	}
	// 旧口径这里返回 false（唯一放行态是"发送失败"）→ 同键整天猝死；新口径必须 true。
	if ok, err := db.ResetFailedRealOrder("u_h1", "H1-A"); err != nil || !ok {
		t.Fatalf("已撤零成交应可重放, ok=%v err=%v", ok, err)
	}
	got := m12Order(t, db, "u_h1", "H1-A")
	if got.Status != "已报" {
		t.Fatalf("重放应重置为已报, got %s", got.Status)
	}
	// §H1-MG 秩守卫不打架：真实旧单号必须被换成 pend 占位号——晚到的 GW-H1A 迟到回报
	// （状态推进按 order_id 反查）再也不能命中这行、把重放轮次的"已报"覆盖回"已撤"。
	if got.OrderID != "pend:H1-A:1" {
		t.Fatalf("重放须换新占位单号（旧网关单号离场），got %s", got.OrderID)
	}

	// 极二：已撤+有成交 → 仍不可重放（fills 前缀命中即视为有成交）。
	o2 := o
	o2.OrderID, o2.SignalID, o2.CreatedAt = "pend:H1B", "H1-B", "2026-09-22T09:36:00+08:00"
	if _, err := db.UpsertRealOrder(o2); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID("u_h1", "H1-B", "GW-H1B", "部成"); err != nil {
		t.Fatalf("backfill b: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO fills (order_id, code, side, price, qty, amount, traded_at, signal_id, user_id)
		VALUES ('GW-H1B','600000.SH','卖出',10,300,3000,'2026-09-22T09:37:00+08:00','H1-B','u_h1')`); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	if ok, err := db.AdvanceRealOrderStatus("u_h1", "H1-B", "已撤"); err != nil || !ok {
		t.Fatalf("advance b: ok=%v err=%v", ok, err)
	}
	if ok, err := db.ResetFailedRealOrder("u_h1", "H1-B"); err != nil || ok {
		t.Fatalf("已撤有成交不得重放（部分成交已占额度）, ok=%v err=%v", ok, err)
	}
	if got := m12Order(t, db, "u_h1", "H1-B"); got.Status != "已撤" {
		t.Fatalf("不可重放行状态必须原样保留, got %s", got.Status)
	}

	// 边界：他人/他号成交不得污染判定之外的行，但前缀匹配与本包 SumFilledQty 口径一致——
	// fills 里挂同前缀 signal_id 即算有成交（保守侧：宁可当日不重放，不可重复下卖单）。
	if ok, err := db.ResetFailedRealOrder("other", "H1-A"); err != nil || ok {
		t.Fatalf("跨账号不得借道重置他号委托, ok=%v err=%v", ok, err)
	}
}

// TestResetSendFailedStillReplayable §GAP2-W1 原语义回归：发送失败放行腿不受 §H1-MG 扩集影响。
func TestResetSendFailedStillReplayable(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:H1C", SignalID: "H1-C", Code: "600000.SH", Side: "卖出",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-22T09:38:00+08:00", UserID: "u_h1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.MarkRealOrderSendFailed("u_h1", "H1-C"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if ok, err := db.ResetFailedRealOrder("u_h1", "H1-C"); err != nil || !ok {
		t.Fatalf("发送失败仍须可重放, ok=%v err=%v", ok, err)
	}
}
