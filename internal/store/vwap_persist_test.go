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

	// §C2-VWAP-READ：成交列表读取路径（fills_effective + ListFillsByDay）也必须带出 VWAP；
	// 此前视图/查询没这四列，页面永远看不到——这条是读取断链的反例锁。
	fills, err := db.ListFillsByDay("u_v", "2026-10-22")
	if err != nil {
		t.Fatalf("ListFillsByDay: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("成交列表应 1 条，got %d", len(fills))
	}
	got := fills[0]
	if got.MarketVWAP != 9.5 || got.VWAPSource != "quote" || got.VWAPAsOf == "" {
		t.Fatalf("成交列表未带出 VWAP: mv=%v src=%q asOf=%q", got.MarketVWAP, got.VWAPSource, got.VWAPAsOf)
	}
	if math.Abs(got.FillVWAPPremiumPct-want) > 1e-9 {
		t.Fatalf("成交列表偏差应为 %.6f, got %.6f", want, got.FillVWAPPremiumPct)
	}
}

// TestRealOrderReadRoundTripAllFields §C2-VWAP-READ：RealOrder 所有字段必须能写进去再原样读回。
// 这是"写侧加列、读侧漏 SELECT"那一类缺陷的通用反例锁——以后新增字段漏接读取即红。
func TestRealOrderReadRoundTripAllFields(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "order_roundtrip.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	want := RealOrder{
		OrderID: "O-ROUND", SignalID: "S-ROUND", Code: "600002.SH", Side: "买入", Status: "已报",
		Price: 12.34, Qty: 300, CreatedAt: "2026-10-26 09:31:00", UserID: "u_round",
		MarketVWAP: 11.99, VWAPSource: "quote", VWAPAsOf: "2026-10-26T09:31:00+08:00",
		SignalVWAPPremiumPct: (12.34 - 11.99) / 11.99 * 100, TimeoutSec: 120,
	}
	if _, err := db.UpsertRealOrder(want); err != nil {
		t.Fatalf("UpsertRealOrder: %v", err)
	}
	orders, err := db.RealOrdersForUser("u_round")
	if err != nil {
		t.Fatalf("RealOrdersForUser: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("订单列表应 1 条，got %d", len(orders))
	}
	got := orders[0]
	if got.OrderID != want.OrderID || got.SignalID != want.SignalID || got.Code != want.Code ||
		got.Side != want.Side || got.Status != want.Status || got.Qty != want.Qty ||
		got.CreatedAt != want.CreatedAt || got.UserID != want.UserID || got.TimeoutSec != want.TimeoutSec {
		t.Fatalf("订单基础字段回读不一致:\nwant=%+v\ngot =%+v", want, got)
	}
	if got.Price != want.Price || got.MarketVWAP != want.MarketVWAP ||
		got.VWAPSource != want.VWAPSource || got.VWAPAsOf != want.VWAPAsOf ||
		math.Abs(got.SignalVWAPPremiumPct-want.SignalVWAPPremiumPct) > 1e-9 {
		t.Fatalf("订单 VWAP 字段回读不一致:\nwant=%+v\ngot =%+v", want, got)
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
