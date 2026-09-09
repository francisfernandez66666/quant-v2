// settlement_test.go — §WS-B 交割单三方对账的 store 层测试：
// 按日成交拉取、费用字段、对账差异持久化（upsert）、补记幂等。
// English: §WS-B store-layer settlement tests — per-day fills, fee fields, diff persistence,
// backfill idempotency.
package store

import (
	"testing"
	"time"
)

// TestSettlementFillsAndFee §WS-B：ListFillsByDay 按日拉取、Fee/StampTax/Serial 落库。
func TestSettlementFillsAndFee(t *testing.T) {
	db := testDB(t)
	now := time.Now().Format("2006-01-02")
	if err := db.ApplyRealFill(RealFill{OrderID: "S1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: now + " 09:35:00", SignalID: "SIG-A",
		UserID: "u1", Fee: 2.5, StampTax: 0, Serial: "SER-1"}); err != nil {
		t.Fatalf("apply fill: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "S2", Code: "600000.SH", Side: "卖出",
		Price: 11, Qty: 50, Amount: 550, TradedAt: now + " 14:00:00", SignalID: "SIG-B",
		UserID: "u1", Fee: 1.5, StampTax: 0.275, Serial: "SER-2"}); err != nil {
		t.Fatalf("apply fill2: %v", err)
	}
	fills, err := db.ListFillsByDay("u1", now)
	if err != nil || len(fills) != 2 {
		t.Fatalf("应按日拉到 2 条, got %d err=%v", len(fills), err)
	}
	// 费用/印花税/流水号已落库
	if fills[0].Fee != 2.5 || fills[0].Serial != "SER-1" {
		t.Fatalf("买入费用/流水号异常: %+v", fills[0])
	}
	if fills[1].StampTax != 0.275 || fills[1].Serial != "SER-2" {
		t.Fatalf("卖出印花税异常: %+v", fills[1])
	}
	// 账号隔离
	if o, _ := db.ListFillsByDay("u2", now); len(o) != 0 {
		t.Fatalf("u2 不应看到 u1 的成交")
	}
}

// TestSettlementDiffPersist §WS-B：差异 upsert（同 (user,day) 覆盖）与列表查询。
func TestSettlementDiffPersist(t *testing.T) {
	db := testDB(t)
	d1 := SettlementDiff{Day: "2026-09-08", MissingInLocal: []string{"a"}, FeeDiff: 1.5, CashDiff: 2, Mode: "report_only"}
	if err := db.SaveSettlementDiff("u1", d1); err != nil {
		t.Fatalf("save: %v", err)
	}
	d2 := SettlementDiff{Day: "2026-09-08", MissingInLocal: []string{"a", "b"}, FeeDiff: 9.9, Mode: "sync_fills"}
	if err := db.SaveSettlementDiff("u1", d2); err != nil {
		t.Fatalf("save overwrite: %v", err)
	}
	diffs, err := db.ListSettlementDiffs(10)
	if err != nil || len(diffs) != 1 {
		t.Fatalf("同 (user,day) 应只有 1 条（覆盖）, got %d err=%v", len(diffs), err)
	}
	if diffs[0].FeeDiff != 9.9 || diffs[0].Mode != "sync_fills" {
		t.Fatalf("覆盖后差异异常: %+v", diffs[0])
	}
}

// TestSettlementBackfillIdempotent §WS-B：sync_fills 补记同一成交二次不重复（复用幂等键）。
func TestSettlementBackfillIdempotent(t *testing.T) {
	db := testDB(t)
	f := RealFill{OrderID: "BF-1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-09-08 09:30:00", UserID: "u1", Serial: "BF-SER"}
	if err := db.ApplySettlementFill(f); err != nil {
		t.Fatalf("backfill1: %v", err)
	}
	if err := db.ApplySettlementFill(f); err != nil {
		t.Fatalf("backfill2 应幂等: %v", err)
	}
	fills, _ := db.ListFillsByDay("u1", "2026-09-08")
	if len(fills) != 1 {
		t.Fatalf("补记应幂等（1 条）, got %d", len(fills))
	}
	p, _ := db.RealPositionByCodeForUser("u1", "600000.SH")
	if p.Qty != 100 {
		t.Fatalf("持仓应 100, got %d", p.Qty)
	}
}
