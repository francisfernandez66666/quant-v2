// 实盘账本（AUTO_TRADING_PLAN M1）存取测试：全量对账 upsert、成交回报应用（建仓/加仓/减仓/清仓）、幂等下单。
package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestUpsertRealPositions 验证全量对账：upsert 覆盖 + 移除已清仓行 + highest_price 单调不回退。
func TestUpsertRealPositions(t *testing.T) {
	db := testDB(t)
	base := []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 300, CostPrice: 10, Amount: 3000, HighestPrice: 11, Strategy: "N形", SignalID: "SIG1"},
		{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 50, Amount: 5000, HighestPrice: 52},
	}
	if n, err := db.UpsertRealPositions(base); err != nil || n != 2 {
		t.Fatalf("first upsert: n=%d err=%v", n, err)
	}
	// 第二推：600000 数量变化 + 最高价降级（应保留旧最高价）；000001 消失（应删除）
	second := []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 400, CostPrice: 10.5, Amount: 4200, HighestPrice: 10.2},
	}
	if n, err := db.UpsertRealPositions(second); err != nil || n != 1 {
		t.Fatalf("second upsert: n=%d err=%v", n, err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil {
		t.Fatalf("by code: %v", err)
	}
	if p.Qty != 400 || p.CostPrice != 10.5 {
		t.Fatalf("upsert 覆盖失败: %+v", p)
	}
	if p.HighestPrice != 11 {
		t.Fatalf("highest_price 应保留旧峰值 11，got %v", p.HighestPrice)
	}
	if _, err := db.RealPositionByCode("000001.SZ"); err != sql.ErrNoRows {
		t.Fatalf("000001 应被移除，err=%v", err)
	}
}

// TestApplyRealFill 验证成交回报驱动：买入建仓 → 加仓加权成本 → 减仓 → 清仓删除 + fills 落库。
func TestApplyRealFill(t *testing.T) {
	db := testDB(t)
	// 建仓买入
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-08-20 09:35:00", SignalID: "SIG1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil || p.Qty != 100 || p.CostPrice != 10 || p.HighestPrice != 10 {
		t.Fatalf("open 异常: %+v err=%v", p, err)
	}
	// 加仓买入：加权成本应变为 (1000+12*100)/200=11
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "买入", Price: 12, Qty: 100, Amount: 1200, TradedAt: "2026-08-20 10:00:00"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Qty != 200 || p.CostPrice != 11 || p.HighestPrice != 12 {
		t.Fatalf("加仓加权成本异常: %+v", p)
	}
	// 减仓 50
	if err := db.ApplyRealFill(RealFill{OrderID: "O3", Code: "600000.SH", Side: "卖出", Price: 13, Qty: 50, Amount: 650, TradedAt: "2026-08-20 11:00:00"}); err != nil {
		t.Fatalf("reduce: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Qty != 150 || p.Amount != 1650 {
		t.Fatalf("减仓异常: %+v", p)
	}
	// 清仓
	if err := db.ApplyRealFill(RealFill{OrderID: "O4", Code: "600000.SH", Side: "卖出", Price: 13, Qty: 150, Amount: 1950, TradedAt: "2026-08-20 14:00:00"}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := db.RealPositionByCode("600000.SH"); err != sql.ErrNoRows {
		t.Fatalf("清仓后应删除，err=%v", err)
	}
	fills, err := db.RealFills()
	if err != nil || len(fills) != 4 {
		t.Fatalf("fills 应 4 条，got %d err=%v", len(fills), err)
	}
}

// TestUpsertRealOrderIdempotent 验证同一 signal_id 重复下单被幂等拦截。
func TestUpsertRealOrderIdempotent(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "GW1", SignalID: "SIG1", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-20 09:30:00"}
	existed, err := db.UpsertRealOrder(o)
	if err != nil || existed {
		t.Fatalf("first order: existed=%v err=%v", existed, err)
	}
	existed, err = db.UpsertRealOrder(o)
	if err != nil || !existed {
		t.Fatalf("duplicate signal_id 应幂等返回 existed=true, got %v err=%v", existed, err)
	}
	orders, err := db.RealOrders()
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders 应 1 条，got %d err=%v", len(orders), err)
	}
}

// TestApplyRealFillEdge 成交回报边界：超卖钳制清仓、卖空仓不报错、清仓后再买入重建仓。
// English: fill edge cases — over-sell clamps to close, selling a non-existent position is a no-op,
// and a buy after close rebuilds a fresh position.
func TestApplyRealFillEdge(t *testing.T) {
	db := testDB(t)
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000, TradedAt: "t1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 超卖：卖 150（持仓仅 100）→ 钳制为 0 → 删除持仓行，不报错
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "卖出", Price: 11, Qty: 150, Amount: 1650, TradedAt: "t2"}); err != nil {
		t.Fatalf("over-sell should not error: %v", err)
	}
	if _, err := db.RealPositionByCode("600000.SH"); err != sql.ErrNoRows {
		t.Fatalf("over-sell 后应清仓删除，err=%v", err)
	}
	// 卖空仓：不报错、不建行
	if err := db.ApplyRealFill(RealFill{OrderID: "O3", Code: "000001.SZ", Side: "卖出", Price: 5, Qty: 100, Amount: 500, TradedAt: "t3"}); err != nil {
		t.Fatalf("sell non-existent should not error: %v", err)
	}
	if _, err := db.RealPositionByCode("000001.SZ"); err != sql.ErrNoRows {
		t.Fatalf("卖空仓不应建行，err=%v", err)
	}
	// 清仓后再买入：重建仓，成本/最高价按新价
	if err := db.ApplyRealFill(RealFill{OrderID: "O4", Code: "600000.SH", Side: "买入", Price: 15, Qty: 200, Amount: 3000, TradedAt: "t4"}); err != nil {
		t.Fatalf("re-entry: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil || p.Qty != 200 || p.CostPrice != 15 || p.HighestPrice != 15 {
		t.Fatalf("重建仓异常: %+v err=%v", p, err)
	}
	fills, _ := db.RealFills()
	if len(fills) != 4 {
		t.Fatalf("fills 应 4 条，got %d", len(fills))
	}
}

// TestRealPositionsForUser §GAP1.10 回归：持仓按归属账号过滤；
// 遗留全局行（user_id=”）对所有人可见；UpsertRealPositions 写入 user_id。
func TestRealPositionsForUser(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Name: "A", Qty: 100, UserID: "u_boss"},
		{TsCode: "000001.SZ", Name: "B", Qty: 200}, // 遗留全局行
	}); err != nil {
		t.Fatal(err)
	}
	boss, err := db.RealPositionsForUser("u_boss")
	if err != nil || len(boss) != 2 {
		t.Fatalf("归属账号应看到 自己的+遗留行 = 2, got %d err=%v", len(boss), err)
	}
	other, err := db.RealPositionsForUser("u_other")
	if err != nil || len(other) != 1 {
		t.Fatalf("其他账号应只看到遗留全局行 1 条, got %d err=%v", len(other), err)
	}
	if other[0].TsCode != "000001.SZ" {
		t.Fatalf("其他账号可见的应为遗留行, got %s", other[0].TsCode)
	}
	// 对账重写带 user_id → 归属更新
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "000001.SZ", Name: "B", Qty: 200, UserID: "u_boss"},
	}); err != nil {
		t.Fatal(err)
	}
	other2, _ := db.RealPositionsForUser("u_other")
	if len(other2) != 0 {
		t.Fatalf("遗留行归属更新后其他账号应不可见, got %d", len(other2))
	}
}

// TestApplyRealFillIdempotent §W3-b 回归：同一笔回报（同 order_id/traded_at/price/qty）重复投递时，
// 幂等唯一键命中 → 整体 no-op，持仓数量不被二次累加（根除 outbox 重试双倍记账）。
func TestApplyRealFillIdempotent(t *testing.T) {
	db := testDB(t)
	f := RealFill{OrderID: "O-IDEM", Code: "600519.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-08-26T10:00:00+08:00", SignalID: "S1", UserID: "u_x"}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("first fill: %v", err)
	}
	p, _ := db.RealPositionByCode("600519.SH")
	if p.Qty != 100 {
		t.Fatalf("首笔后持仓应 100, got %d", p.Qty)
	}
	// 同一笔重放：唯一键冲突 → 幂等成功（err==nil）且持仓不变
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("duplicate fill 应幂等成功而非报错: %v", err)
	}
	p, _ = db.RealPositionByCode("600519.SH")
	if p.Qty != 100 {
		t.Fatalf("重放后持仓仍应 100, got %d", p.Qty)
	}
	// 真正的新成交（不同回报时间戳）正常累加
	f2 := f
	f2.TradedAt = "2026-08-26T10:01:00+08:00"
	if err := db.ApplyRealFill(f2); err != nil {
		t.Fatalf("second distinct fill: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.Qty != 200 {
		t.Fatalf("新成交应累加到 200, got %d", p.Qty)
	}
}

// TestSchemaMigrationP01P02 §P0-1/P0-2 旧库主键/唯一约束迁移：
// 模拟只含单 ts_code 主键的 real_positions 和单 signal_id 唯一的 orders，
// Open 后应自动重建为 (ts_code, user_id) 与 (user_id, signal_id)。
func TestSchemaMigrationP01P02(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "migrate.db")
	rawDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// 创建旧 schema
	if _, err := rawDB.Exec(`
		CREATE TABLE real_positions (
			ts_code TEXT PRIMARY KEY,
			name TEXT DEFAULT '',
			qty INTEGER NOT NULL DEFAULT 0,
			cost_price REAL NOT NULL DEFAULT 0,
			amount REAL NOT NULL DEFAULT 0,
			highest_price REAL NOT NULL DEFAULT 0,
			strategy TEXT DEFAULT '',
			signal_id TEXT DEFAULT '',
			updated_at TEXT NOT NULL,
			user_id TEXT DEFAULT ''
		);
		CREATE TABLE orders (
			order_id TEXT PRIMARY KEY,
			signal_id TEXT UNIQUE,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			status TEXT NOT NULL,
			price REAL,
			qty INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			user_id TEXT DEFAULT ''
		);
	`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	rawDB.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open after migration: %v", err)
	}
	defer db.Close()

	// 迁移后应支持多账号同股票
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Qty: 100, UserID: "u1"},
		{TsCode: "600000.SH", Qty: 200, UserID: "u2"},
	}); err != nil {
		t.Fatalf("multi-tenant positions upsert: %v", err)
	}
	all, _ := db.RealPositions()
	if len(all) != 2 {
		t.Fatalf("应存在 2 条持仓, got %d", len(all))
	}

	// 迁移后应支持同 signal_id 不同账号
	if _, err := db.UpsertRealOrder(RealOrder{OrderID: "O1", SignalID: "SIG-X", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-28T10:00:00+08:00", UserID: "u1"}); err != nil {
		t.Fatalf("u1 order: %v", err)
	}
	if _, err := db.UpsertRealOrder(RealOrder{OrderID: "O2", SignalID: "SIG-X", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-28T10:00:00+08:00", UserID: "u2"}); err != nil {
		t.Fatalf("u2 order: %v", err)
	}
	orders, _ := db.RealOrders()
	if len(orders) != 2 {
		t.Fatalf("应存在 2 条委托, got %d", len(orders))
	}
}
