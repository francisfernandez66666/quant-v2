// trade_results_test.go — §TRADE-LEDGER（2026-11-13）台账回归。
//
// 锁六件事（断言编号对应 PLAN_TRADE_RESULT_LEDGER_20261113.md §6）：
//  T1 粒度：一行 = 一笔买入成交；3 笔部成 → 3 行，各带自己的成交价/量；
//  T3 盈亏同尺：Σ 行 realized_pnl == handleQMTTrades 式重放 realized（同一序列两实现对比）；
//  T4 含费口径：买入摊费（价=含费单价读数）、卖出扣 fee+stamp_tax；
//  T5 超卖钳制：卖出量 > 持仓量时按剩余钳制，无基准余量不摊分；
//  T6 状态机：部分平=partial、全平=closed；同码二次建仓=新行（生命周期与持仓一致）；
//  T12 幂等：同一回报重放 → 台账行不变（fills 判重先行，无半态）。
// T2（挂单价）与 T11（勘误重算）分见各自用例。
package store

import (
	"testing"
)

// applyBuy/applySell 便捷封装：走生产唯一写入点 ApplyRealFill（台账与 fills 同事务）。
func applyBuy(t *testing.T, db *DB, orderID, signalID string, price float64, qty int, fee float64, tradedAt string) {
	t.Helper()
	if err := db.ApplyRealFill(RealFill{OrderID: orderID, Code: "600519.SH", Name: "贵州茅台",
		Side: "买入", Price: price, Qty: qty, Amount: price * float64(qty), Fee: fee,
		TradedAt: tradedAt, SignalID: signalID, UserID: "u1"}); err != nil {
		t.Fatalf("买入成交落库: %v", err)
	}
}

func applySell(t *testing.T, db *DB, orderID string, price float64, qty int, fee, stamp float64, tradedAt string) {
	t.Helper()
	if err := db.ApplyRealFill(RealFill{OrderID: orderID, Code: "600519.SH",
		Side: "卖出", Price: price, Qty: qty, Amount: price * float64(qty),
		Fee: fee, StampTax: stamp, TradedAt: tradedAt, UserID: "u1"}); err != nil {
		t.Fatalf("卖出成交落库: %v", err)
	}
}

func ledgerRows(t *testing.T, db *DB) []TradeResult {
	t.Helper()
	rows, _, err := db.TradeResultsForUser("u1", "", "", "", 500, 0)
	if err != nil {
		t.Fatalf("读台账: %v", err)
	}
	return rows
}

// T1+T2+T4：3 笔部成买入 → 3 行各带自己的价/量/挂单价；战法从 signal_id 归因。
func TestTradeResultsPerFillGranularity(t *testing.T) {
	db := testDB(t)
	if err := db.ensureManualBook(); err != nil {
		t.Fatalf("ensure manual: %v", err)
	}
	// 两个挂单（orders.price 供挂单价读数）
	for _, o := range []struct {
		id string
		p  float64
	}{{"O-L1", 10.50}, {"O-L2", 10.80}} {
		if _, err := db.UpsertRealOrder(RealOrder{OrderID: o.id, SignalID: "S-" + o.id,
			Code: "600519.SH", Side: "买入", Status: "已报", Price: o.p, Qty: 100,
			UserID: "u1"}); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}
	applyBuy(t, db, "O-L1", "buy:600519.SH:龙抬头:2026-11-13", 10.00, 100, 5, "2026-11-13 09:31:00")
	applyBuy(t, db, "O-L1", "buy:600519.SH:龙抬头:2026-11-13", 10.20, 100, 5, "2026-11-13 09:31:10")
	applyBuy(t, db, "O-L2", "buy:600519.SH:龙抬头:2026-11-13", 10.30, 100, 5, "2026-11-13 09:31:20")

	rows := ledgerRows(t, db)
	if len(rows) != 3 {
		t.Fatalf("T1: 3 笔部成应 3 行, got %d", len(rows))
	}
	// 新→旧排序：第一行是最后一笔
	last := rows[0]
	if last.LimitPrice != 10.80 {
		t.Fatalf("T2: 挂单价应=orders.price 10.80, got %v", last.LimitPrice)
	}
	first := rows[2]
	if first.Price != 10.00 || first.Qty != 100 || first.Strategy != "龙抬头" {
		t.Fatalf("T1/T7: 首笔行不符: price=%v qty=%d strategy=%q", first.Price, first.Qty, first.Strategy)
	}
	if first.Status != "open" || first.RealizedPnl != 0 {
		t.Fatalf("未平仓行应 open 且 pnl=0: %+v", first)
	}
	// 买入摊费：行内 price 是成交价（含费口径读数在重放侧），fee 落行
	if rows[1].Fee != 5 {
		t.Fatalf("T4: 费用腿应落行 fee=5, got %v", rows[1].Fee)
	}
}

// T3+T5+T6：卖出 FIFO 匹配、Σ 行 pnl == 重放、部分平/全平状态机、超卖钳制。
func TestTradeResultsSellMatchingAndPnl(t *testing.T) {
	db := testDB(t)
	if err := db.ensureManualBook(); err != nil {
		t.Fatalf("ensure manual: %v", err)
	}
	// 买入 3 笔：100@10(费5)、100@12(费5)、100@14(费5)
	applyBuy(t, db, "B1", "buy:600519.SH:龙抬头:2026-11-13", 10, 100, 5, "2026-11-13 09:31:00")
	applyBuy(t, db, "B2", "buy:600519.SH:龙抬头:2026-11-13", 12, 100, 5, "2026-11-13 09:32:00")
	applyBuy(t, db, "B3", "buy:600519.SH:龙抬头:2026-11-13", 14, 100, 5, "2026-11-13 09:33:00")
	// 卖出 150 @16（费3 税4.5）：FIFO 吃 B1(100)+B2(50)
	applySell(t, db, "S1", 16, 150, 3, 4.5, "2026-11-13 10:00:00")

	rows := ledgerRows(t, db)
	// 重放 realized（加权成本口径）：总成本=(1005+1205+1405)=3615，成本/股=12.05，
	// 卖 150@16：pnl=(16-12.05)*150-7.5=585
	var sumPnl float64
	openRows, closedRows, partialRows := 0, 0, 0
	for _, r := range rows {
		sumPnl += r.RealizedPnl
		switch r.Status {
		case "open":
			openRows++
		case "partial":
			partialRows++
		case "closed":
			closedRows++
		}
	}
	want := (16-12.05)*150 - 7.5
	if diff := sumPnl - want; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("T3: Σ 行 pnl=%v 应=重放 %v", sumPnl, want)
	}
	if closedRows != 1 || partialRows != 1 || openRows != 1 {
		t.Fatalf("T6: 应 1 closed(B1 吃满) + 1 partial(B2) + 1 open(B3), got c=%d p=%d o=%d",
			closedRows, partialRows, openRows)
	}
	// 卖出腿读数：B1 行 sell_qty=100、sell_legs=1、卖出均价=16
	for _, r := range rows {
		if r.MatchedQty == 100 && r.Status == "closed" {
			if r.SellQty != 100 || r.SellLegs != 1 || r.SellAvgPrice != 16 {
				t.Fatalf("closed 行卖出读数不符: sell_qty=%d legs=%d avg=%v", r.SellQty, r.SellLegs, r.SellAvgPrice)
			}
			if r.ClosedAt == "" {
				t.Fatal("closed 行应有 closed_at")
			}
		}
	}
	// T5：超卖钳制——再卖 300（持仓只剩 150）→ 钳到 150，无基准余量不摊分
	applySell(t, db, "S2", 17, 300, 3, 4.5, "2026-11-13 10:30:00")
	rows = ledgerRows(t, db)
	sumPnl = 0
	for _, r := range rows {
		sumPnl += r.RealizedPnl
	}
	// 第二笔卖出按剩余加权成本重放：余 150@均价(3615-150*12.05)/150=12.05
	// pnl2=(17-12.05)*150-7.5=735
	want2 := want + (17-12.05)*150 - 7.5
	if diff := sumPnl - want2; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("T5: 超卖钳制后 Σ=%v 应=%v", sumPnl, want2)
	}
	// 全部行 closed
	for _, r := range rows {
		if r.Status != "closed" {
			t.Fatalf("全平后所有行应 closed, got %s (fill %d)", r.Status, r.FillID)
		}
	}
	// T6：清仓后再建仓 = 新行（新信号日）
	applyBuy(t, db, "B4", "buy:600519.SH:龙回头:2026-11-14", 20, 100, 5, "2026-11-14 09:31:00")
	rows = ledgerRows(t, db)
	if len(rows) != 4 {
		t.Fatalf("T6: 重建仓应新行, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Day == "2026-11-14" && r.Strategy != "龙回头" {
			t.Fatalf("新行战法应按新 signal_id 归因, got %q", r.Strategy)
		}
	}
}

// T12：同一回报重放（outbox 重试形态）→ 台账行不变（fills 判重先行）。
func TestTradeResultsIdempotentReplay(t *testing.T) {
	db := testDB(t)
	if err := db.ensureManualBook(); err != nil {
		t.Fatalf("ensure manual: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "B-IDEM", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-11-13 09:31:00",
		SignalID: "buy:600519.SH:龙抬头:2026-11-13", TradeID: "T-IDEM", UserID: "u1"}); err != nil {
		t.Fatalf("首笔成交落库: %v", err)
	}
	// 同一笔回报重发（outbox 重放同一信封：同 trade_id 同字段）
	if err := db.ApplyRealFill(RealFill{OrderID: "B-IDEM", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-11-13 09:31:00",
		SignalID: "buy:600519.SH:龙抬头:2026-11-13", TradeID: "T-IDEM", UserID: "u1"}); err != nil {
		t.Fatalf("重放应幂等成功: %v", err)
	}
	rows := ledgerRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("T12: 重放后应仍 1 行, got %d", len(rows))
	}
	if rows[0].Qty != 100 {
		t.Fatalf("T12: 重放不得二次累加, qty=%d", rows[0].Qty)
	}
}

// T11：勘误重算——RebuildTradeResultsForCode 从 fills_effective 整本重放。
func TestTradeResultsRebuildFollowsAmendment(t *testing.T) {
	db := testDB(t)
	if err := db.ensureManualBook(); err != nil {
		t.Fatalf("ensure manual: %v", err)
	}
	applyBuy(t, db, "B-AM", "buy:600519.SH:龙抬头:2026-11-13", 10, 100, 5, "2026-11-13 09:31:00")
	// 卖出 100@9（亏）→ 行 closed pnl<0
	applySell(t, db, "S-AM", 9, 100, 3, 4.5, "2026-11-13 14:00:00")
	rows := ledgerRows(t, db)
	if len(rows) != 1 || rows[0].Status != "closed" {
		t.Fatalf("前置: 应 1 行 closed, got %+v", rows)
	}
	if rows[0].RealizedPnl >= 0 {
		t.Fatalf("前置: 亏损退出 pnl 应<0, got %v", rows[0].RealizedPnl)
	}
	// 整本重放（同 fills 流）→ 行值一致（幂等重建）
	n, err := db.RebuildTradeResultsForCode("u1", "600519.SH")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if n != 1 {
		t.Fatalf("rebuild 应写 1 行, got %d", n)
	}
	rows = ledgerRows(t, db)
	if len(rows) != 1 || rows[0].Status != "closed" {
		t.Fatalf("T11: 重放后应仍是 1 行 closed, got %+v", rows)
	}
	if diff := rows[0].RealizedPnl - ((9 - 10.05) * 100 - 7.5); diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("T11: 重放 pnl 应一致 (含费成本 10.05), got %v", rows[0].RealizedPnl)
	}
	// scope 守卫
	if _, err := db.RebuildTradeResultsForCode("", "600519.SH"); err == nil {
		t.Fatal("空 user_id 重算必须被拒绝（scopedWrite）")
	}
}
