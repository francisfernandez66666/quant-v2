// pnl_offset_test.go — §E1 盈亏单轨：纸面「清零」校准记录入库的存储层单测。
// 锁三件事：① 当前生效值=最新一行（append-only，不覆盖历史）；② 空表按 0（从未校准）
// 而不是报错；③ 遗留全局行（user_id=''）能被任意账号读到（与 real_account 兜底同姿势）。
// English: §E1 store tests — latest-wins offset semantics, empty-table = 0, legacy global row fallback.
package store

import "testing"

// TestPnlOffsetLatestWins 追加两条校准，生效值取最新；历史行保留（留痕语义）。
func TestPnlOffsetLatestWins(t *testing.T) {
	db := testDB(t)
	// 空表：无校准 → 0 且无错误（不是"读数不可得"）。
	if off, err := db.LatestPnlOffset("u1"); err != nil || off != 0 {
		t.Fatalf("空表应 (0,nil), got (%v,%v)", off, err)
	}
	r1, err := db.AddPnlOffset(PnlOffsetRecord{UserID: "u1", Offset: 120.5, Note: "首次清零"})
	if err != nil || r1.ID == 0 {
		t.Fatalf("首条入账失败: id=%d err=%v", r1.ID, err)
	}
	if _, err := db.AddPnlOffset(PnlOffsetRecord{UserID: "u1", Offset: 80.25, Note: "再次校准"}); err != nil {
		t.Fatalf("第二条入账失败: %v", err)
	}
	off, err := db.LatestPnlOffset("u1")
	if err != nil || off != 80.25 {
		t.Fatalf("生效值应为最新一行 80.25, got (%v,%v)", off, err)
	}
	// 留痕等值锁：表内 2 行（append-only，后一条不覆盖前一条）。
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM pnl_offset_history`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("留痕应 2 行（不覆盖），got n=%d err=%v", n, err)
	}
}

// TestPnlOffsetLegacyGlobalRow 遗留全局行（user_id=''）对任意账号可见——
// 与 GetRealAccount 的「user_id = ? OR user_id = ''」兜底同款，避免收编前的手工账凭空失效。
func TestPnlOffsetLegacyGlobalRow(t *testing.T) {
	db := testDB(t)
	if _, err := db.AddPnlOffset(PnlOffsetRecord{UserID: "", Offset: 66, Note: "全局行"}); err != nil {
		t.Fatalf("全局行入账失败: %v", err)
	}
	if off, err := db.LatestPnlOffset("someone"); err != nil || off != 66 {
		t.Fatalf("全局行应对任意账号生效 (66,nil), got (%v,%v)", off, err)
	}
}
