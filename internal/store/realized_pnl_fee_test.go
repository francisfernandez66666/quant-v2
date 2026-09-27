// realized_pnl_fee_test.go — §0927AUDIT-D1（2026-09-27 修复批）：日内已实现盈亏必须扣卖出腿费用。
//
// 缺陷原文（全栈字节级 UAT 报告 D1）：TodayRealizedPnl 旧实现只算 (成交价−成本)×量，
// 不扣卖出佣金与印花税；而 /api/qmt/trades 的重放腿自 §F1 起就扣。同一笔卖出两个读数点
// 数字不同——违反 ListFillsByDay（settlement.go）注释里「熔断闸与 trades 必须是同一个数」
// 的不变量声明，且 day_loss 熔断闸看到的亏损系统性偏小（少扣的费用＝低估的亏损），
// 逼近阈值时更晚熔断。
//
// 本用例的数值构造即反证：费用腿取非零值（佣 4 元 + 印花 2.5 元），若实现摘掉扣费腿，
// 读数会恰好等于「仅价差」的 −502.50 而不是 −509.00，用例必红——不需要额外回滚验证。
// 买入腿佣金（5 元）不在此处二次扣：ApplyRealFill 已按 §F1 把它摊进持仓成本，
// CostPrice＝10.005 本身含费，这里同时把该口径钉死。
// English: regression for §0927AUDIT-D1 — the intraday realized P&L reader (feeding the day-loss
// breaker) must deduct the sell-side fee and stamp tax so it matches the trades replay leg; the
// fixture uses non-zero fees, so removing the deduction makes the exact-value assertion go red.
package store

import (
	"path/filepath"
	"testing"
)

// TestTodayRealizedPnlDeductsSellFees 含费买入 → 含费+印花税卖出，锤死熔断闸读数的精确值。
func TestTodayRealizedPnlDeductsSellFees(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "d1.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	const (
		uid  = "u_d1"
		day  = "2026-09-25"
		code = "600001.SH"
	)

	// 买入 1000 股 @10.00，佣金 5 元 → ApplyRealFill 摊入后 CostPrice=(10000+5)/1000=10.005。
	buy := RealFill{OrderID: "D1-B1", Code: code, Name: "测试甲", Side: "买入",
		Price: 10, Qty: 1000, Amount: 10000, TradedAt: day + " 09:35:00",
		TradeID: "D1-TB1", UserID: uid, Fee: 5}
	if err := db.ApplyRealFill(buy); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	p, err := db.RealPositionByCodeForUser(uid, code)
	if err != nil || p.Qty != 1000 {
		t.Fatalf("position after buy: %v %+v", err, p)
	}
	if diff := p.CostPrice - 10.005; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("买入腿佣金应已摊入成本（§F1 口径），期望 CostPrice=10.005, got %.6f", p.CostPrice)
	}

	// 卖出 500 股 @9.00，佣金 4 元 + 印花税 2.5 元（费用腿全部非零，构成天然反证）。
	sell := RealFill{OrderID: "D1-S1", Code: code, Name: "测试甲", Side: "卖出",
		Price: 9, Qty: 500, Amount: 4500, TradedAt: day + " 14:30:00",
		TradeID: "D1-TS1", UserID: uid, Fee: 4, StampTax: 2.5}
	if err := db.ApplyRealFill(sell); err != nil {
		t.Fatalf("seed sell: %v", err)
	}

	pnl, err := db.TodayRealizedPnl(uid, day)
	if err != nil {
		t.Fatalf("pnl: %v", err)
	}
	// 期望：(9.00 − 10.005)×500 − 4 − 2.5 = −502.50 − 6.50 = −509.00。
	const want = -509.00
	if pnl < want-0.01 || pnl > want+0.01 {
		t.Fatalf("日内已实现盈亏应为 %.2f（含卖出费用腿），got %.2f —— 若读数恰为 −502.50 说明扣费腿又被摘了", want, pnl)
	}
	// 方向锁：亏损必须比「仅价差」更深（费用只会让卖出腿更亏，永远不许反向抬数）。
	if pnl >= -502.50 {
		t.Fatalf("费用腿未生效：pnl=%.2f 未低于仅价差口径 −502.50", pnl)
	}
}

// TestTodayRealizedPnlNoSellUnaffected 反向边界：当日没有卖出腿时读数恒 0，
// 买入费用不通过本函数产生任何盈亏扰动（防"把买入费也在这里扣一遍"的过度修复）。
func TestTodayRealizedPnlNoSellUnaffected(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "d1b.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	buy := RealFill{OrderID: "D1-C1", Code: "600002.SH", Name: "测试乙", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-09-25 09:35:00",
		TradeID: "D1-TC1", UserID: "u_d1b", Fee: 5}
	if err := db.ApplyRealFill(buy); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if pnl, err := db.TodayRealizedPnl("u_d1b", "2026-09-25"); err != nil || pnl != 0 {
		t.Fatalf("无卖出腿时读数必须为 0, got %.2f err=%v", pnl, err)
	}
}
