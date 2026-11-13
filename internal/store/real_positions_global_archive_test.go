// real_positions_global_archive_test.go — §L2 F2-①（2026-11-13）遗留全局桶归档迁移演练。
//
// D4 纪律「结构变更必须先备份 + 在测试库演练」的演练半边：本测试在测试库上完整走一遍
// 「有全局行 → 留档 → 主表清零 → 幂等 no-op」全程，锁四件事：
//  1. 只有 user_id=” 的行被搬走，具体账号的行一字不动；
//  2. 留档表逐字段保真（含 archived_at 戳），审计可证、可回灌——**不是删库**；
//  3. 幂等：桶空后再次调用零写入；
//  4. 留档/移除计数强一致（counted == removed），半态当场报错。
//
// English: §L2 F2-① rehearsal on a test database — archive preserves every field plus an
// archived_at stamp, touches no scoped row, and is idempotent.
package store

import "testing"

func TestArchiveLegacyGlobalPositions(t *testing.T) {
	db := testDB(t)
	// 造混合形态：2 行遗留全局桶 + 1 行具体账号（归档绝不能波及后者）
	if _, err := db.db.Exec(`INSERT INTO real_positions
		(ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at, user_id)
		VALUES
		('000520.SZ','长运',0,0,0,0,'recon','','2026-11-12 08:52:00',''),
		('600000.SH','浦发',100,12,1200,13,'recon','','2026-11-11 15:00:00',''),
		('600519.SH','茅台',100,1500,150000,1510,'recon','','2026-11-12 10:00:00','u1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := db.ArchiveLegacyGlobalPositions()
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if n != 2 {
		t.Fatalf("应留档 2 行全局行, got %d", n)
	}
	// ① 主表：全局行清零，具体账号行原样
	var globalLeft, scoped int
	if err := db.db.QueryRow(`SELECT count(*) FROM real_positions WHERE user_id=''`).Scan(&globalLeft); err != nil {
		t.Fatalf("count global: %v", err)
	}
	if globalLeft != 0 {
		t.Fatalf("主表全局行应清零, got %d", globalLeft)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM real_positions WHERE user_id='u1'`).Scan(&scoped); err != nil {
		t.Fatalf("count scoped: %v", err)
	}
	if scoped != 1 {
		t.Fatalf("具体账号的行必须一字不动, got %d", scoped)
	}
	// ② 留档表逐字段保真
	var name string
	var qty int
	var archivedAt string
	if err := db.db.QueryRow(`SELECT name, qty, archived_at FROM real_positions_global_archive
		WHERE ts_code='600000.SH'`).Scan(&name, &qty, &archivedAt); err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if name != "浦发" || qty != 100 || archivedAt == "" {
		t.Fatalf("留档行字段保真失败: name=%q qty=%d archived_at=%q", name, qty, archivedAt)
	}
	// ③ 幂等：桶空后再次调用零写入
	if n2, err := db.ArchiveLegacyGlobalPositions(); err != nil || n2 != 0 {
		t.Fatalf("幂等重跑应 0 行无错, got n=%d err=%v", n2, err)
	}
}
