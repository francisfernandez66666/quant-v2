// manual_snapshot_test.go — §L2/§L1（2026-11-13）手动账每日快照回归。
//
// 锁三件事：
//  1. SnapshotManualPositions 幂等覆盖当日、账户行（ts_code=”）单独存资金、
//     空 user_id 一律拒绝（账户级鉴权不许有"全局桶"写入口）；
//  2. RestoreManualPositionsFromSnapshot 按 day 一键回灌（T6.5），asof 由调用方传入；
//  3. LatestManualSnapshot 读回最近一天并正确拆分持仓行/账户行（§L1 只读展示数据源）。
//
// English: §L2 daily manual-book snapshot regression — idempotent capture, one-command
// restore, and the display-only latest-snapshot read.
package store

import "testing"

func TestManualSnapshotCaptureRestoreLatest(t *testing.T) {
	db := testDB(t)
	// manual_* 表是惰性建表（ensureManualBook），直插种子前先确保结构存在
	if err := db.ensureManualBook(); err != nil {
		t.Fatalf("ensure manual book: %v", err)
	}
	day := "2026-11-13"

	// 造手动账：2 行持仓 + 账户行
	if _, err := db.db.Exec(`INSERT INTO manual_positions (ts_code, name, qty, cost_price, source, asof, updated_at, user_id, taken_over_at)
		VALUES ('002584.SZ','苏大维格',1000,9.60,'manual','2026-11-13 09:15:00','','u1',''),
		       ('002738.SZ','中矿资源',200,60.00,'manual','2026-11-13 09:15:00','','u1','')`); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO manual_account (user_id, available_cash, total_asset, cash_filled, source, asof, updated_at, taken_over_at)
		VALUES ('u1',61316,121000,1,'manual','2026-11-13 09:15:00','','')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// ① 快照落表
	n, err := db.SnapshotManualPositions("u1", day)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if n != 2 {
		t.Fatalf("快照应含 2 行持仓, got %d", n)
	}
	// 幂等：同日再拍不报错且行数不变
	if _, err := db.SnapshotManualPositions("u1", day); err != nil {
		t.Fatalf("snapshot idempotent: %v", err)
	}

	// ③ LatestManualSnapshot 读回：持仓 2 行 + 账户行拆成现金
	v, ok, err := db.LatestManualSnapshot("u1")
	if err != nil || !ok {
		t.Fatalf("latest snapshot: ok=%v err=%v", ok, err)
	}
	if v.Day != day || len(v.Positions) != 2 {
		t.Fatalf("latest snapshot 应为当日 2 行持仓: day=%s n=%d", v.Day, len(v.Positions))
	}
	if v.Cash != 61316 || v.AsOf != "2026-11-13 09:15:00" {
		t.Fatalf("账户行应拆成现金 61316 + asof, got cash=%v asof=%q", v.Cash, v.AsOf)
	}
	if v.Positions[0].TsCode != "002584.SZ" || v.Positions[0].Qty != 1000 || v.Positions[0].CostPrice != 9.60 {
		t.Fatalf("快照持仓行不符: %+v", v.Positions[0])
	}

	// ② 模拟跨日事故：清掉 manual_* 后按快照一键回灌
	if _, err := db.db.Exec(`DELETE FROM manual_positions WHERE user_id='u1'`); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	got, err := db.RestoreManualPositionsFromSnapshot("u1", day, "2026-11-14 09:00:00")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got != 2 {
		t.Fatalf("restore 应回灌 2 行, got %d", got)
	}
	// 回灌后的 asof 用调用方传入的当日时刻（避免恢复出来的账立刻跨日失效）
	view, err := db.ManualBookViewFor("u1", "2026-11-14")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if len(view.Positions) != 2 || !view.PositionsUsable || !view.CashUsable {
		t.Fatalf("恢复后当日应可用: n=%d posUsable=%v cashUsable=%v", len(view.Positions), view.PositionsUsable, view.CashUsable)
	}
	if view.Cash.Account.AvailableCash != 61316 {
		t.Fatalf("恢复后现金应 61316, got %v", view.Cash.Account.AvailableCash)
	}
}

func TestManualSnapshotScopeGuards(t *testing.T) {
	db := testDB(t)
	// 空 user_id 一律拒绝（写口）
	if _, err := db.SnapshotManualPositions("", "2026-11-13"); err == nil {
		t.Fatal("空 user_id 快照必须被拒绝（scopedWrite）")
	}
	if _, err := db.RestoreManualPositionsFromSnapshot("", "2026-11-13", "x"); err == nil {
		t.Fatal("空 user_id 恢复必须被拒绝（scopedWrite）")
	}
	// 空 user_id 读口同样拒绝
	if _, _, err := db.LatestManualSnapshot(""); err == nil {
		t.Fatal("空 user_id 快照读口必须被拒绝（scopedRead）")
	}
	// 无快照：ok=false 而不是报错
	if _, ok, _ := db.LatestManualSnapshot("u_nobody"); ok {
		t.Fatal("无快照账号应返回 ok=false")
	}
}
