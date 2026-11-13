// §ANCHOR-FIRST（2026-10-24 裁决二「止盈止损点和动态加减仓按首次买入价做锚点」）实盘账锚点列测试。
// 2026-11-13 owner 裁决 3 改口径：柜台快照**可回填缺失锚点**（NULL/0，带 counter_snapshot 溯源），
// 但**绝不回拉**已存在的非零风险线；"唯一写入点"从 ApplyRealFill 放宽为「ApplyRealFill 建行 +
// 对账快照仅缺失回填」两个受控写入点。
//
// 钉死的语义（W2 影子层的写入冻结面）：
//   - anchor_price 的生产写入点是 ApplyRealFill 的建仓建行（含费每股成本，与 cost_price 同源同值）；
//   - 加仓摊薄 cost_price，**绝不**摊薄 anchor_price；
//   - 柜台对账快照（两个 upsert 口）仅在本地锚点 NULL/0 时回填，且回填必带 anchor_source=
//     'counter_snapshot'（可证来源纪律）；本地已有非零锚点时快照任何取值都碰不到它；
//   - 存量行 NULL 读回 0＝「锚点未知」，不是「锚点等于 0」也不是「锚点等于成本」。
//
// English: the live-book anchor column. The first fill creates it; add-ons dilute cost but
// never the anchor; a broker snapshot may FILL a missing anchor (with counter_snapshot
// provenance) but can never pull back an existing non-zero one; NULL reads as 0 = "unknown",
// which is not the same statement as "equal to cost".
package store

import (
	"database/sql"
	"testing"
)

// TestAnchorFrozenAcrossAddAndReduce 建仓写锚 → 加仓只摊成本 → 减仓两者都不动。
// 高位加仓是裁决二要修的形态：100@10 后再买 100@20，成本被抬到 15，风险线若跟着成本走，
// 止损线被推到 15 附近（离首买价更远）；锚点必须仍停在 10.05（含费）。
func TestAnchorFrozenAcrossAddAndReduce(t *testing.T) {
	db := testDB(t)
	if err := db.ApplyRealFill(RealFill{OrderID: "A-1", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-10-24 09:31:00", UserID: "u1"}); err != nil {
		t.Fatalf("建仓: %v", err)
	}
	p, err := db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if p.CostPrice != 10.05 || p.AnchorPrice != 10.05 {
		t.Fatalf("建仓锚点须与含费成本同值 10.05/10.05, got cost=%v anchor=%v", p.CostPrice, p.AnchorPrice)
	}
	// 高位加仓：成本摊到 (1005+2005)/200=15.05，锚点必须原地不动
	if err := db.ApplyRealFill(RealFill{OrderID: "A-2", Code: "600519.SH", Side: "买入",
		Price: 20, Qty: 100, Amount: 2000, Fee: 5, TradedAt: "2026-10-24 10:00:00", UserID: "u1"}); err != nil {
		t.Fatalf("加仓: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.CostPrice != 15.05 {
		t.Fatalf("加仓成本应摊薄为 15.05, got %v", p.CostPrice)
	} else if p.AnchorPrice != 10.05 {
		t.Fatalf("§ANCHOR-FIRST 加仓不得改写锚点, got %v", p.AnchorPrice)
	}
	// 减仓：两个基准都不动（成本与锚点只在卖出时随数量摊销）
	if err := db.ApplyRealFill(RealFill{OrderID: "A-3", Code: "600519.SH", Side: "卖出",
		Price: 18, Qty: 100, Amount: 1800, TradedAt: "2026-10-24 14:00:00", UserID: "u1"}); err != nil {
		t.Fatalf("减仓: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.Qty != 100 || p.AnchorPrice != 10.05 {
		t.Fatalf("减仓后锚点必须原样, got qty=%d anchor=%v", p.Qty, p.AnchorPrice)
	}
}

// TestAnchorSurvivesBrokerReconcile 柜台对账不得回拉本地已有锚点（2026-11-13 改口径后仍钉死的一半）。
// 锤实形态：本地锚点已是建仓那笔的 10.05 时，快照里 anchor_price 为 0（网关不认识字段）、
// 99（脏值）、7（更低值）都碰不到它，anchor_source 也保持首买来源不被洗成 counter_snapshot。
// English: once the anchor exists, a broker snapshot carrying 0, a dirty value, or a lower
// price cannot touch it, and the provenance stays first-buy.
func TestAnchorSurvivesBrokerReconcile(t *testing.T) {
	db := testDB(t)
	if err := db.ApplyRealFill(RealFill{OrderID: "R-1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-10-24 09:31:00", UserID: "u1"}); err != nil {
		t.Fatalf("建仓: %v", err)
	}
	for i, snap := range []float64{0, 99, 7} {
		if _, err := db.UpsertRealPositions([]RealPosition{
			{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 12, Amount: 1200, HighestPrice: 13, AnchorPrice: snap, UserID: "u1"},
		}); err != nil {
			t.Fatalf("对账 #%d: %v", i+1, err)
		}
		p, err := db.RealPositionByCodeForUser("u1", "600000.SH")
		if err != nil {
			t.Fatalf("读回 #%d: %v", i+1, err)
		}
		if p.AnchorPrice != 10.05 {
			t.Fatalf("§ANCHOR-FIRST 对账快照(anchor=%v)不得改写本地锚点, got %v", snap, p.AnchorPrice)
		}
		if p.AnchorSource != AnchorSourceFirstBuyOrder {
			t.Fatalf("§ANCHOR-FIRST 快照不得洗掉首买溯源, got %q", p.AnchorSource)
		}
	}
	// 定向口径（生产主路径）同锁：ReconcilePositionsForUser 也必须碰不到锚点
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 12, Amount: 1200, HighestPrice: 13, AnchorPrice: 7},
	}); err != nil {
		t.Fatalf("定向对账: %v", err)
	}
	if p, _ := db.RealPositionByCodeForUser("u1", "600000.SH"); p.AnchorPrice != 10.05 {
		t.Fatalf("§ANCHOR-FIRST 定向对账不得改写本地锚点, got %v", p.AnchorPrice)
	}
}

// TestAnchorUnknownOnLegacyRow 存量行（列 NULL）读回 0＝锚点未知，四口一致。
// 反证：COALESCE 漏在任一口上，那一口会直接 SQL 报错或扫空整行——那才是"未回填"被写成"崩"。
func TestAnchorUnknownOnLegacyRow(t *testing.T) {
	db := testDB(t)
	// 绕过 ApplyRealFill 造一行"改造前"的存量持仓（不写 anchor_price ⇒ NULL）
	if _, err := db.db.Exec(`INSERT INTO real_positions
		(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
		VALUES ('300750.SZ','宁德',200,18,3600,19,'N形','SIG-OLD','2026-10-23 15:00:00','u1')`); err != nil {
		t.Fatalf("造存量行: %v", err)
	}
	byCode, err := db.RealPositionByCode("300750.SZ")
	if err != nil {
		t.Fatalf("ByCode: %v", err)
	}
	if byCode.AnchorPrice != 0 {
		t.Fatalf("存量行锚点须读回 0（未知）, got %v", byCode.AnchorPrice)
	}
	if byCode.CostPrice != 18 {
		t.Fatalf("存量行成本仍应正常读回, got %v", byCode.CostPrice)
	}
	all, err := db.RealPositions()
	if err != nil || len(all) != 1 {
		t.Fatalf("RealPositions 应读到 1 行, n=%d err=%v", len(all), err)
	}
	if all[0].AnchorPrice != 0 {
		t.Fatalf("§ANCHOR-FIRST RealPositions 口未回填锚点须为 0, got %v", all[0].AnchorPrice)
	}
	byUser, err := db.RealPositionsForUser("u1")
	if err != nil || len(byUser) != 1 {
		t.Fatalf("RealPositionsForUser 应读到 1 行, n=%d err=%v", len(byUser), err)
	}
	if byUser[0].AnchorPrice != 0 {
		t.Fatalf("§ANCHOR-FIRST ForUser 口未回填锚点须为 0, got %v", byUser[0].AnchorPrice)
	}
	// 清仓后重建：锚点重新取新仓首买价（生命周期与持仓一致，同裁决状态 Prune 语义）
	if err := db.ApplyRealFill(RealFill{OrderID: "R-9", Code: "300750.SZ", Side: "卖出",
		Price: 20, Qty: 200, Amount: 4000, TradedAt: "2026-10-24 14:57:00", UserID: "u1"}); err != nil {
		t.Fatalf("清仓: %v", err)
	}
	if _, err := db.RealPositionByCode("300750.SZ"); err != sql.ErrNoRows {
		t.Fatalf("清仓后应删行, err=%v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "R-10", Code: "300750.SZ", Side: "买入",
		Price: 25, Qty: 100, Amount: 2500, Fee: 5, TradedAt: "2026-10-26 09:31:00", UserID: "u1"}); err != nil {
		t.Fatalf("重新建仓: %v", err)
	}
	if p, _ := db.RealPositionByCode("300750.SZ"); p.AnchorPrice != 25.05 {
		t.Fatalf("重新入场锚点须从零起算=新首买价 25.05, got %v", p.AnchorPrice)
	}
}

// TestAnchorBackfilledFromCounterSnapshotOnlyWhenMissing 2026-11-13 owner 裁决 3 的正向锁：
// 本地锚点缺失（NULL/0）时，柜台快照**必须**回填首买锚点与最高价，且回填带
// anchor_source='counter_snapshot' 溯源（防"无标签锚点"混进风险线）；
// 回填后快照再送更低的值不得回拉（与 TestAnchorSurvivesBrokerReconcile 闭环）。
// 根因背景：旧链路 uplink 根本不发 cost/anchor，云端落库 cost=0、锚点恒「—」，
// 现字段链已补齐（broker_reads 列别名 + uplink 四字段），这里锁住云侧消费端。
// English: when the local anchor is missing, a counter snapshot MUST back-fill it with
// counter_snapshot provenance; afterwards lower/dirty snapshot values cannot pull it back.
func TestAnchorBackfilledFromCounterSnapshotOnlyWhenMissing(t *testing.T) {
	db := testDB(t)
	// 造一行锚点缺失的存量持仓（改造前形态：锚点 NULL）
	if _, err := db.db.Exec(`INSERT INTO real_positions
		(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
		VALUES ('002584.SZ','苏大维格',1000,9.60,9600,9.80,'recon','SIG-BS','2026-11-13 09:15:00','u1')`); err != nil {
		t.Fatalf("造存量行: %v", err)
	}
	// 柜台快照带锚点 8.32 + 期间最高 9.80 → 必须回填并打 counter_snapshot 溯源
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "002584.SZ", Name: "苏大维格", Qty: 1000, CostPrice: 9.60, Amount: 9600,
			HighestPrice: 9.80, AnchorPrice: 8.32, UserID: "u1"},
	}); err != nil {
		t.Fatalf("对账回填: %v", err)
	}
	p, err := db.RealPositionByCodeForUser("u1", "002584.SZ")
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if p.AnchorPrice != 8.32 {
		t.Fatalf("缺失锚点应被柜台快照回填为 8.32, got %v", p.AnchorPrice)
	}
	if p.AnchorSource != "counter_snapshot" {
		t.Fatalf("回填锚点必须带 counter_snapshot 溯源, got %q", p.AnchorSource)
	}
	if p.HighestPrice != 9.80 {
		t.Fatalf("期间最高价应随快照落值, got %v", p.HighestPrice)
	}
	// 回填后：快照送更低价/0/脏值都不得回拉，溯源不得被洗
	for i, snap := range []float64{7, 0, 99} {
		if _, err := db.UpsertRealPositions([]RealPosition{
			{TsCode: "002584.SZ", Name: "苏大维格", Qty: 1000, CostPrice: 9.60, Amount: 9600,
				HighestPrice: 9.80, AnchorPrice: snap, UserID: "u1"},
		}); err != nil {
			t.Fatalf("回拉对账 #%d: %v", i+1, err)
		}
		if p, _ = db.RealPositionByCodeForUser("u1", "002584.SZ"); p.AnchorPrice != 8.32 || p.AnchorSource != "counter_snapshot" {
			t.Fatalf("回填后快照(anchor=%v)不得回拉/洗溯源: anchor=%v source=%q", snap, p.AnchorPrice, p.AnchorSource)
		}
	}
	// 定向口径（生产主路径 ReconcilePositionsForUser）同锁：本地无行时新建行也带锚点+溯源
	if _, err := db.ReconcilePositionsForUser("u2", []RealPosition{
		{TsCode: "002738.SZ", Name: "中矿资源", Qty: 200, CostPrice: 60.00, Amount: 12000,
			HighestPrice: 61.00, AnchorPrice: 58.50},
	}, true); err != nil {
		t.Fatalf("定向对账: %v", err)
	}
	p2, err := db.RealPositionByCodeForUser("u2", "002738.SZ")
	if err != nil {
		t.Fatalf("定向读回: %v", err)
	}
	if p2.AnchorPrice != 58.50 || p2.AnchorSource != "counter_snapshot" {
		t.Fatalf("定向对账新建行应回填锚点 58.50 + counter_snapshot, got %v/%q", p2.AnchorPrice, p2.AnchorSource)
	}
}
