// vwap_persist_test.go — §C2 订单/成交 VWAP 落库锁。
//
// 成交回报本身不带市场 VWAP，必须从下单时落库的 orders 行继承；找不到就保持未采集，
// 不能拿成交价冒充 VWAP。偏差只在 VWAP>0 时计算。
package store

import (
	"math"
	"path/filepath"
	"testing"
)

func TestFillInheritsOrderVWAP(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "vwap.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.UpsertRealOrder(RealOrder{
		OrderID: "O-V1", SignalID: "S-V1", Code: "600000.SH", Side: "买入", Status: "已报",
		Price: 10, Qty: 100, CreatedAt: "2026-10-22 09:31:00", UserID: "u_v",
		MarketVWAP: 9.5, VWAPSource: "quote", VWAPAsOf: "2026-10-22T09:31:00+08:00",
		SignalVWAPPremiumPct: (10 - 9.5) / 9.5 * 100,
	}); err != nil {
		t.Fatalf("UpsertRealOrder: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{
		OrderID: "O-V1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000,
		TradedAt: "2026-10-22 09:32:00", SignalID: "S-V1", UserID: "u_v",
	}); err != nil {
		t.Fatalf("ApplyRealFill: %v", err)
	}
	var mv, prem float64
	var src, asOf string
	if err := db.db.QueryRow(`SELECT market_vwap, fill_vwap_premium_pct, vwap_source, vwap_as_of
		FROM fills WHERE order_id='O-V1'`).Scan(&mv, &prem, &src, &asOf); err != nil {
		t.Fatalf("读 fills: %v", err)
	}
	if mv != 9.5 || src != "quote" || asOf == "" {
		t.Fatalf("成交未继承下单 VWAP: mv=%v src=%q asOf=%q", mv, src, asOf)
	}
	want := (10 - 9.5) / 9.5 * 100
	if math.Abs(prem-want) > 1e-9 {
		t.Fatalf("偏差应为 %.6f, got %.6f", want, prem)
	}
}

func TestFillWithoutOrderVWAPStaysEmpty(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "vwap2.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyRealFill(RealFill{
		OrderID: "O-NO-VWAP", Code: "600001.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000,
		TradedAt: "2026-10-22 09:33:00", SignalID: "S-V2", UserID: "u_v",
	}); err != nil {
		t.Fatalf("ApplyRealFill: %v", err)
	}
	var mv, prem float64
	if err := db.db.QueryRow(`SELECT market_vwap, fill_vwap_premium_pct FROM fills WHERE order_id='O-NO-VWAP'`).Scan(&mv, &prem); err != nil {
		t.Fatalf("读 fills: %v", err)
	}
	if mv != 0 || prem != 0 {
		t.Fatalf("无下单 VWAP 时不得伪造: mv=%v prem=%v", mv, prem)
	}
}
