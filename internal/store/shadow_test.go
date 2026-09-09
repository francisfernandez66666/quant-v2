// shadow_test.go — §WS-G shadow_orders 存储层单测：幂等插入 + 按日查询。
// English: §WS-G shadow_orders store tests — idempotent insert + per-day query.
package store

import "testing"

// TestInsertShadowOrderIdempotent 同 signal_id 幂等：第二次返回 existed，不重复计。
func TestInsertShadowOrderIdempotent(t *testing.T) {
	db := testDB(t)
	o := ShadowOrder{
		SignalID: "buy:600000.SH:n_shape:2026-09-08", Code: "600000.SH", Name: "浦发",
		Strategy: "N形", StrategyID: "n_shape", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, UserID: "u_shadow", CreatedAt: "2026-09-08 09:35:00",
	}
	existed, err := db.InsertShadowOrder(o)
	if err != nil || existed {
		t.Fatalf("首插应成功 existed=false, got %v err=%v", existed, err)
	}
	existed, err = db.InsertShadowOrder(o)
	if err != nil || !existed {
		t.Fatalf("重复应 existed=true, got %v err=%v", existed, err)
	}
	rows, _ := db.ShadowOrdersForDay("u_shadow", "2026-09-08", 10)
	if len(rows) != 1 {
		t.Fatalf("应只 1 条, got %d", len(rows))
	}
}

// TestShadowOrdersForDay 按日过滤 + 跨日隔离 + 全局行可见。
func TestShadowOrdersForDay(t *testing.T) {
	db := testDB(t)
	if _, err := db.InsertShadowOrder(ShadowOrder{SignalID: "s1", Code: "600000.SH", Side: "买入",
		CreatedAt: "2026-09-08 09:30:00", UserID: "u_shadow"}); err != nil {
		t.Fatalf("s1: %v", err)
	}
	if _, err := db.InsertShadowOrder(ShadowOrder{SignalID: "s2", Code: "600000.SH", Side: "买入",
		CreatedAt: "2026-09-09 09:30:00", UserID: "u_shadow"}); err != nil {
		t.Fatalf("s2: %v", err)
	}
	if _, err := db.InsertShadowOrder(ShadowOrder{SignalID: "s3", Code: "600000.SH", Side: "买入",
		CreatedAt: "2026-09-08 10:00:00"}); err != nil { // 全局行（无 user_id）
		t.Fatalf("s3: %v", err)
	}
	rows, err := db.ShadowOrdersForDay("u_shadow", "2026-09-08", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("09-08 应 2 条（含全局行）, got %d err=%v", len(rows), err)
	}
	// 最新在前：s3(10:00) 应在 s1(09:30) 之前
	if rows[0].SignalID != "s3" || rows[1].SignalID != "s1" {
		t.Fatalf("应最新在前, got %+v", rows)
	}
}
