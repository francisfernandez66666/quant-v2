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

// TestTwoWritePoesDontClobber §FLOW-MONEY：资金流四列专用写口与量比三列专用写口互不覆盖。
// 先写量比→再写资金流→量比仍在；再反向一次。全字段 Upsert 若被误用会互相盖 NULL（方案 B 红线）。
func TestTwoWritePoesDontClobber(t *testing.T) {
	db := testDB(t)
	day := "2026-11-19"
	rY, rM, vDelta := 1.3, 1.1, 1.0
	if err := db.UpsertFlowVolumeOnly(FlowFactorDailyRow{
		TradeDate: day, RYest: &rY, RMa5: &rM, VolSurgeDelta: &vDelta, Source: "index_em:000001.SH_intraday",
	}); err != nil {
		t.Fatal(err)
	}
	mPct, mDelta := 0.8, 0.5
	if err := db.UpsertFlowMoneyOnly(FlowFactorDailyRow{
		TradeDate: day, MarketFlowPct: &mPct, FlowMarketDelta: &mDelta, Source: "eastmoney_fflow",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetFlowFactorDaily(day)
	if err != nil || row == nil {
		t.Fatalf("读回失败: %v", err)
	}
	if row.RYest == nil || *row.RYest != rY || row.VolSurgeDelta == nil || *row.VolSurgeDelta != vDelta {
		t.Fatalf("资金流写口不得盖量比三列, got %+v", row)
	}
	if row.FlowMarketDelta == nil || *row.FlowMarketDelta != mDelta {
		t.Fatalf("资金流列应已写入, got %+v", row)
	}
	// 反向：再写量比，资金流列仍在。
	rY2 := 1.4
	if err := db.UpsertFlowVolumeOnly(FlowFactorDailyRow{TradeDate: day, RYest: &rY2}); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetFlowFactorDaily(day)
	if row.FlowMarketDelta == nil || *row.FlowMarketDelta != mDelta {
		t.Fatalf("量比写口不得盖资金流列, got %+v", row)
	}
}

// TestSectorFlowDailyRoundtrip §FLOW-MONEY 板块表：UPSERT 幂等 + 读回一致。
func TestSectorFlowDailyRoundtrip(t *testing.T) {
	db := testDB(t)
	day, sector := "2026-11-19", "航空机场"
	if err := db.UpsertSectorFlowDaily(SectorFlowDailyRow{
		TradeDate: day, Sector: sector, NetInflow: 79545088, Amount: 3299859059, Delta: 0.7,
	}); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSectorFlowDaily(day, sector)
	if err != nil || row == nil || row.Delta != 0.7 || row.Amount != 3299859059 {
		t.Fatalf("板块读数应一致, got %+v err=%v", row, err)
	}
	// 覆盖写（同日同板块）幂等更新。
	if err := db.UpsertSectorFlowDaily(SectorFlowDailyRow{
		TradeDate: day, Sector: sector, NetInflow: 1, Amount: 10, Delta: -0.2,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetSectorFlowDaily(day, sector)
	if row == nil || row.Delta != -0.2 {
		t.Fatalf("覆盖写应生效, got %+v", row)
	}
	// 无行 → (nil, nil)。
	if row, err := db.GetSectorFlowDaily(day, "不存在板块"); err != nil || row != nil {
		t.Fatalf("无行应 (nil,nil), got %+v err=%v", row, err)
	}
}
