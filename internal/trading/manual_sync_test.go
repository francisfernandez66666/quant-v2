package trading

// §MANUAL-SYNC（2026-10-28）：例行自动撤单/收盘清场不得碰手动委托。
// 手动单在券商 App 里下的（signal_id=ext:<委托号>），超时被系统撤掉属于越权误杀；
// 紧急停止（HaltAll）仍会连它们一起撤，这是两条不同的路径，必须分别守住。

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestSweepOrdersSkipsManualOrders 超龄手动单保持原状，系统单照常被撤。
func TestSweepOrdersSkipsManualOrders(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 120
	cfg.CloseSweepAt = -1 // 隔离收盘清单，只验超时撤单
	ctrl := NewController(guardServer(), db, "u_ms", cfg, nil)

	old := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	seed := []store.RealOrder{
		{OrderID: "GW-MS-1", SignalID: "buy:600000:fac_1:20261028", Code: "600000.SH",
			Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_ms"},
		{OrderID: "12345", SignalID: "ext:12345", Code: "000001.SZ",
			Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_ms"},
	}
	for _, o := range seed {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	res := ctrl.SweepOrders(time.Now())
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if res.Cancelled != 1 {
		t.Fatalf("应只撤系统单 1 笔, got %+v", res)
	}
	orders, _ := db.RealOrdersForUser("u_ms")
	got := map[string]string{}
	for _, o := range orders {
		got[o.SignalID] = o.Status
	}
	if got["buy:600000:fac_1:20261028"] != "撤单中" {
		// §CANCEL-STATE：撤单请求已发出 ≠ 交易所已撤。本地先进「撤单中」，
		// 终态由回报/快照推进（见 store 层 status 迁移用例）。
		t.Fatalf("系统单应进入撤单中, got %s", got["buy:600000:fac_1:20261028"])
	}
	if got["ext:12345"] != "已报" {
		t.Fatalf("手动单不得被例行自动撤单误杀, got %s", got["ext:12345"])
	}
}

// TestSnapshotExposesCashAt §MANUAL-SYNC：/api/qmt/state 必须带券商资金快照时刻。
// 为什么锁它：资金/持仓已改 10s 一采，前端若只有「过期」横幅，用户无法区分
// 「金额没变」与「快照没更新」——这两件事的处理方式完全不同（后者要查桥/网关链路）。
func TestSnapshotExposesCashAt(t *testing.T) {
	db := testDB(t)
	updated := "2026-10-28 10:00:00"
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_ms_at", AvailableCash: 1234.5, FrozenCash: 0,
		TotalAsset: 5678, MarketValue: 4443.5, UpdatedAt: updated,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	ctrl := NewController(guardServer(), db, "u_ms_at", config.DefaultQMTConfig(), nil)
	snap := ctrl.Snapshot()
	if snap.CashAt != updated {
		t.Fatalf("cash_at 应透出最近快照时刻 %s, got %q", updated, snap.CashAt)
	}
	if snap.Cash != 1234.5 {
		t.Fatalf("cash 应等于快照可用资金, got %v", snap.Cash)
	}
}
