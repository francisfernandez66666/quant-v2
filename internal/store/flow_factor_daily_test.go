// flow_factor_daily_test.go — §FLOW-FACTOR-DAILY 按日滚动留痕锁。
// 三条：① 同日 UPSERT 覆盖（不累积多行）；② 按日取数互不串（跨日不复活）；③ NaN 弃权存 NULL。
package store

import (
	"math"
	"testing"
)

func ptrF(v float64) *float64 { return &v }

// TestFlowFactorDailyUpsert 同日重复写覆盖为最新一轮（主键 trade_date）。
func TestFlowFactorDailyUpsert(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertFlowFactorDaily(FlowFactorDailyRow{
		TradeDate: "2026-11-19", RYest: ptrF(1.5), RMa5: ptrF(1.4), Source: "qmt_native",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFlowFactorDaily(FlowFactorDailyRow{
		TradeDate: "2026-11-19", RYest: ptrF(1.1), RMa5: ptrF(1.0), Source: "qmt_native",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListFlowFactorDaily("2026-11-19", "2026-11-19")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("同日应只有一行（UPSERT 覆盖），实际 %d", len(rows))
	}
	if rows[0].RYest == nil || math.Abs(*rows[0].RYest-1.1) > 1e-9 {
		t.Fatalf("应保留最新一轮读数 1.1，实际 %+v", rows[0].RYest)
	}
}

// TestFlowFactorDailyDayKeyed 按日隔离：昨日行不进今日读面。
func TestFlowFactorDailyDayKeyed(t *testing.T) {
	db := testDB(t)
	_ = db.UpsertFlowFactorDaily(FlowFactorDailyRow{TradeDate: "2026-11-18", RYest: ptrF(2.0)})
	_ = db.UpsertFlowFactorDaily(FlowFactorDailyRow{TradeDate: "2026-11-19", RYest: ptrF(1.5)})
	today, err := db.GetFlowFactorDaily("2026-11-19")
	if err != nil || today == nil || today.RYest == nil || math.Abs(*today.RYest-1.5) > 1e-9 {
		t.Fatalf("今日读面应只拿今日行，实际 %+v err=%v", today, err)
	}
	rows, _ := db.ListFlowFactorDaily("2026-11-19", "2026-11-19")
	if len(rows) != 1 || rows[0].TradeDate != "2026-11-19" {
		t.Fatalf("按日区间应只返回今日，实际 %+v", rows)
	}
}

// TestFlowFactorDailyNaNAbstain NaN 弃权存 NULL，读回 nil（绝不当 0）。
func TestFlowFactorDailyNaNAbstain(t *testing.T) {
	db := testDB(t)
	nan := math.NaN()
	_ = db.UpsertFlowFactorDaily(FlowFactorDailyRow{
		TradeDate: "2026-11-19", RYest: &nan, RMa5: ptrF(1.0), FlowSectorDelta: &nan,
	})
	r, err := db.GetFlowFactorDaily("2026-11-19")
	if err != nil || r == nil {
		t.Fatalf("读到空: %v", err)
	}
	if r.RYest != nil {
		t.Fatalf("NaN 应存 NULL 读回 nil，实际 %+v", *r.RYest)
	}
	if r.RMa5 == nil || math.Abs(*r.RMa5-1.0) > 1e-9 {
		t.Fatalf("非 NaN 应保留，实际 %+v", r.RMa5)
	}
}
