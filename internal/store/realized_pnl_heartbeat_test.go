// realized_pnl_heartbeat_test.go — §0929HB-2 存储层读数用例（口径与设计理由见 realized_pnl_heartbeat.go）。
//
// 这里只管存储层那两把尺子的**边界**，不重复 trading 包那三支成对判据（A 有亏损 / B 有卖出恒 0 /
// C 无卖出，见 internal/trading/heartbeat_test.go）：
//  1. 日界口径：CountSellFillsByDay 必须按 traded_at 前 10 位归日，跨日成交不得串数
//     （熔断闸吃"昨天的亏损"这类串日事故本仓锤实过，同一把尺子在心跳侧再钉一次）；
//  2. 账号口径：另一账号的卖出不得计入本账号读数（多账号隔离，与 TodayRealizedPnl 同锁法）；
//  3. 方向口径：买入腿不得被数成卖出；
//  4. 容差边界：|pnl|<0.005 才算"没有数"，一分钱以上的真实盈亏必须放过去
//     （否则 §0927AUDIT-D1 修好的那条含费口径会被心跳天天误报成"账断了"）。
//
// 生效视图（fills_effective）这一层不在此重复构造：§FILL-AMEND 已有专测，且本函数的两个读数
// （笔数、盈亏）按构造就是同一张视图——刻意不再造第三条"勘误后卖出"用例，避免同族断言双写。
// English: storage-layer boundary tests (day/user/side boundaries + the half-cent tolerance); the
// matched triple lives in internal/trading/heartbeat_test.go and the effective-view property is
// already locked by the §FILL-AMEND suite, so it is not re-implemented here.
package store

import (
	"math"
	"path/filepath"
	"testing"
)

// hbDB 开临时库（用例结束自动关闭）。
func hbDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "hb_store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestCountSellFillsDayAndUserBoundary 日界 + 账号 + 方向三道边界各配一支反证：
// 任何一道失守，读数都会从期望的 1 变成 2/3/4（等值断言，不做"至少 1 笔"的单向锁）。
func TestCountSellFillsDayAndUserBoundary(t *testing.T) {
	db := hbDB(t)
	const (
		uid    = "u_hb_s"
		other  = "u_hb_other"
		today  = "2026-09-29"
		yester = "2026-09-28"
	)
	seed := []RealFill{
		{OrderID: "1", Code: "600010.SH", Side: "卖出", Price: 9, Qty: 100, Amount: 900,
			TradedAt: today + " 10:00:00", TradeID: "S1", UserID: uid},
		{OrderID: "2", Code: "600011.SH", Side: "卖出", Price: 9, Qty: 100, Amount: 900,
			TradedAt: yester + " 10:00:00", TradeID: "S2", UserID: uid},
		{OrderID: "3", Code: "600012.SH", Side: "卖出", Price: 9, Qty: 100, Amount: 900,
			TradedAt: today + " 11:00:00", TradeID: "S3", UserID: other},
		{OrderID: "4", Code: "600013.SH", Side: "买入", Price: 9, Qty: 100, Amount: 900,
			TradedAt: today + " 12:00:00", TradeID: "B4", UserID: uid},
	}
	for _, f := range seed {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("seed %s: %v", f.OrderID, err)
		}
	}
	n, err := db.CountSellFillsByDay(uid, today)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("当日卖出笔数应等值 1（跨日/他人/买入腿都不得计入），got %d", n)
	}
}

// TestRealizedPnlHeartbeatToleranceBoundary 容差边界：恰好 0 判可疑、一分钱以上不判。
// 构造用"成本不可知的孤儿卖出"（TodayRealizedPnl 对这类 fail-open 不计入 ⇒ 恒 0），
// 再用同库补一笔可算出盈亏的卖出把它抬出容差带。
func TestRealizedPnlHeartbeatToleranceBoundary(t *testing.T) {
	db := hbDB(t)
	const (
		uid = "u_hb_tol"
		day = "2026-09-29"
	)
	// 孤儿卖出：无持仓、当日无买入 ⇒ 成本不可知 ⇒ 已实现盈亏恒 0（正是心跳要抓的形态）。
	if err := db.ApplyRealFill(RealFill{OrderID: "T-S1", Code: "600020.SH", Side: "卖出", Price: 9, Qty: 300,
		Amount: 2700, TradedAt: day + " 14:00:00", TradeID: "T-TS1", UserID: uid, Fee: 3, StampTax: 1.35}); err != nil {
		t.Fatalf("seed orphan sell: %v", err)
	}
	h, err := db.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		t.Fatalf("heartbeat orphan: %v", err)
	}
	if h.SellFills != 1 || h.RealizedPnl != 0 || !h.Suspicious {
		t.Fatalf("有卖出且盈亏恒 0 必须判可疑，got %+v", h)
	}

	// 同库另票补一对买/卖，使当日盈亏抬到容差带之外（|pnl|≥0.005）⇒ 不得判可疑。
	const code2 = "600021.SH"
	if err := db.ApplyRealFill(RealFill{OrderID: "T-B2", Code: code2, Side: "买入", Price: 10, Qty: 1000,
		Amount: 10000, TradedAt: day + " 09:30:00", TradeID: "T-TB2", UserID: uid, Fee: 5}); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "T-S2", Code: code2, Side: "卖出", Price: 11, Qty: 500,
		Amount: 5500, TradedAt: day + " 14:05:00", TradeID: "T-TS2", UserID: uid, Fee: 4, StampTax: 2.75}); err != nil {
		t.Fatalf("seed sell: %v", err)
	}
	h2, err := db.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		t.Fatalf("heartbeat pair: %v", err)
	}
	if math.Abs(h2.RealizedPnl) < realizedPnlZeroEpsilon {
		t.Fatalf("含费口径下这对买卖的盈亏不该落在容差带内（got %.6f）", h2.RealizedPnl)
	}
	if h2.Suspicious {
		t.Fatalf("盈亏非 0 却判可疑 ⇒ 心跳会把 §0927AUDIT-D1 修好的正常账天天报成故障：%+v", h2)
	}
	if h2.SellFills != 2 {
		t.Fatalf("两笔卖出都要计入笔数（got %d）", h2.SellFills)
	}
}
