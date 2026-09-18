// risk_gates_test.go — 单日买入笔数「已成交」口径（CountBuyFilledOrdersByDay）回归。
//
// 2026-09-18 修正：单日买入笔数纪律闸原先数 orders 表里「今日已报」的委托数，报单即占额度——
// 一笔被券商废掉、或挂在委托簿上没成交的报单同样吃掉一天的买入额度，用户会因为一堆没成交的
// 报单被锁死买入权。改为只数 fills（柜台回报的客观成交事实）后，这里的用例钉住计数键语义：
// 同一委托的多次部分成交算 1 笔；卖出/非当日/他人账号不计。
//
// English: regression for the "filled, not submitted" daily buy-count metric — partial fills of one
// order collapse to one; sells, other days and other accounts are excluded.
package store

import (
	"path/filepath"
	"testing"
)

// newRiskTestDB 建临时实盘账本。
func newRiskTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rg.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fill 便捷构造一笔成交。
func fill(orderID, serial, code, side, tradedAt string) RealFill {
	return RealFill{
		OrderID: orderID, Serial: serial, Code: code, Side: side,
		Price: 10, Qty: 100, Amount: 1000, UserID: "u_rg", TradedAt: tradedAt,
	}
}

// TestCountBuyFilledOrdersByDay 计数口径：仅当日买入成交，且同一委托去重。
func TestCountBuyFilledOrdersByDay(t *testing.T) {
	db := newRiskTestDB(t)
	// 他账号的一笔买入：必须在计数之外
	other := fill("O5", "", "600005.SH", "买入", "2026-09-18 11:00:00")
	other.UserID = "u_other"
	fills := []RealFill{
		// 同一委托 O1 的两笔部分成交 → 1 笔
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:00"),
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:05"),
		// 另一委托 → 第 2 笔
		fill("O2", "", "600002.SH", "买入", "2026-09-18 10:00:00"),
		// 卖出不计
		fill("O3", "", "600003.SH", "卖出", "2026-09-18 10:30:00"),
		// 非当日不计
		fill("O4", "", "600004.SH", "买入", "2026-09-17 10:00:00"),
		// 他账号不计
		other,
		// 无 order_id：回落券商交割流水号去重（同流水号两行 → 1 笔）
		fill("", "S1", "600006.SH", "买入", "2026-09-18 13:00:00"),
		fill("", "S1", "600006.SH", "买入", "2026-09-18 13:00:01"),
		// 无 order_id 无流水号：各自算一笔（宁可多计也不把多笔成交并成 1 笔而少计额度）
		fill("", "", "600007.SH", "买入", "2026-09-18 14:00:00"),
		fill("", "", "600008.SH", "买入", "2026-09-18 14:01:00"),
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	// 期望：O1(2 行→1) + O2 + S1(2 行→1) + 2 行无键 = 5 笔
	got, err := db.CountBuyFilledOrdersByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("CountBuyFilledOrdersByDay: %v", err)
	}
	if got != 5 {
		t.Fatalf("今日已成交买入笔数应为 5（O1 两笔部分成交算 1）got=%d", got)
	}
	// 非当日/他账号口径为空
	if got, err = db.CountBuyFilledOrdersByDay("u_rg", "2026-09-17"); err != nil || got != 1 {
		t.Fatalf("9-17 应为 1 笔, got %d err=%v", got, err)
	}
	if got, err = db.CountBuyFilledOrdersByDay("u_none", "2026-09-18"); err != nil || got != 0 {
		t.Fatalf("无成交账号应为 0, got %d err=%v", got, err)
	}
}

// TestSumBuyFilledAmountByDay 预算闸冻结账的「已成交」半边：Σ 当日买入成交金额
// （amount 优先，旧数据缺失时回落 price×qty），卖出/非当日/他人账号不计。
// 与 LocalBuyFrozen（orders 状态派生的「在途冻结」半边）合起来才是完整占用。
func TestSumBuyFilledAmountByDay(t *testing.T) {
	db := newRiskTestDB(t)
	other := fill("O5", "", "600005.SH", "买入", "2026-09-18 11:00:00")
	other.UserID = "u_other"
	fills := []RealFill{
		// 正常 amount 行：按 amount 计
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:00"),
		// 同一委托的另一笔部分成交：金额照加（金额口径不去重，笔数口径才去重）
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:05"),
		// 旧数据 amount=0：回落 price×qty = 10×100 = 1000
		func() RealFill {
			f := fill("O2", "", "600002.SH", "买入", "2026-09-18 10:00:00")
			f.Amount = 0
			return f
		}(),
		// 卖出 / 非当日 / 他账号不计
		fill("O3", "", "600003.SH", "卖出", "2026-09-18 10:30:00"),
		fill("O4", "", "600004.SH", "买入", "2026-09-17 10:00:00"),
		other,
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	got, err := db.SumBuyFilledAmountByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("SumBuyFilledAmountByDay: %v", err)
	}
	if got != 3000 {
		t.Fatalf("今日已成交买入金额应为 3000（1000×2 + 回落1000）got=%.0f", got)
	}
	if got, err = db.SumBuyFilledAmountByDay("u_rg", "2026-09-17"); err != nil || got != 1000 {
		t.Fatalf("9-17 应为 1000, got %.0f err=%v", got, err)
	}
	if got, err = db.SumBuyFilledAmountByDay("u_none", "2026-09-18"); err != nil || got != 0 {
		t.Fatalf("无成交账号应为 0, got %.0f err=%v", got, err)
	}
}

// TestCountBuyFilledOrdersByDayIgnoresUnfilledOrders 「报单不占额度」的数据层对照：
// 只有 orders 行（还没成交）时计数恒为 0——旧口径在这里会数出 N 笔。
func TestCountBuyFilledOrdersByDayIgnoresUnfilledOrders(t *testing.T) {
	db := newRiskTestDB(t)
	for i, id := range []string{"SIG-A", "SIG-B"} {
		if _, err := db.UpsertRealOrder(RealOrder{
			OrderID: "GW-" + id, SignalID: id, Code: "600000.SH", Side: "买入",
			Status: "已报", Price: 10, Qty: 100, UserID: "u_rg",
			CreatedAt: "2026-09-18T09:30:0" + string(rune('0'+i)) + "+08:00",
		}); err != nil {
			t.Fatalf("UpsertRealOrder: %v", err)
		}
	}
	got, err := db.CountBuyFilledOrdersByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("CountBuyFilledOrdersByDay: %v", err)
	}
	if got != 0 {
		t.Fatalf("2 笔已报未成交的报单不应占用笔数额度, got %d", got)
	}
}
