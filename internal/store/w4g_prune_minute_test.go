// w4g_prune_minute_test.go — §0926E2E-17B store.PruneMinuteBars 的边界锁。
//  1. 严格早于 cutoff 才删：cutoff 当日 00:00:00 起的行必须留存（保留窗口按"整天"算）；
//  2. 只裁目标 scale：日后若加 15/30/60 分钟周期，5 的裁剪不得波及别的 scale；
//  3. cutoff 形态非法直接拒执行——字典序下坏前缀（如 "2026/01/01"）会误删大片，宁可不动。
// English: boundary locks for PruneMinuteBars — strictly-older rows only, per-scale, and
// malformed cutoff rejection (lexicographic compare would mass-delete with a bad prefix).
package store

import "testing"

func TestW4GPruneMinuteBarsBoundary(t *testing.T) {
	db := newMinuteDB(t)
	if _, err := db.UpsertMinuteBars(mkBars("600000.SH", "2026-03-29", 48)); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if _, err := db.UpsertMinuteBars(mkBars("600000.SH", "2026-03-30", 48)); err != nil {
		t.Fatalf("seed boundary: %v", err)
	}
	// 另一周期同旧 ts：不得被 scale=5 的裁剪波及。
	old := mkBars("600001.SH", "2025-01-05", 10)
	for i := range old {
		old[i].Scale = 15
	}
	if _, err := db.UpsertMinuteBars(old); err != nil {
		t.Fatalf("seed scale15: %v", err)
	}
	deleted, err := db.PruneMinuteBars(5, "2026-03-30")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 48 {
		t.Fatalf("只应删 scale=5 且早于 2026-03-30 的 48 行，得到 %d", deleted)
	}
	if bars, _ := db.MinuteBarsByDay("600000.SH", 5, "2026-03-30"); len(bars) != 48 {
		t.Fatalf("cutoff 当日行必须留存（整天口径），得到 %d", len(bars))
	}
	if bars, _ := db.MinuteBarsByDay("600000.SH", 5, "2026-03-29"); len(bars) != 0 {
		t.Fatalf("cutoff 前一日应清空，得到 %d", len(bars))
	}
	st15, err := db.MinuteTableStats(15)
	if err != nil {
		t.Fatalf("stats15: %v", err)
	}
	if st15.Rows != 10 {
		t.Fatalf("scale=15 不应被 scale=5 裁剪波及，得到 %d 行", st15.Rows)
	}
}

func TestW4GPruneMinuteBarsRejectsMalformedCutoff(t *testing.T) {
	db := newMinuteDB(t)
	if _, err := db.UpsertMinuteBars(mkBars("600000.SH", "2026-03-29", 48)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, bad := range []string{"", "20260330", "2026/03-30", "202-03-30"} {
		if _, err := db.PruneMinuteBars(5, bad); err == nil {
			t.Fatalf("非法 cutoff %q 必须拒执行（字典序误删风险）", bad)
		}
	}
	st, err := db.MinuteTableStats(5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Rows != 48 {
		t.Fatalf("拒执行后行数不得变化，得到 %d", st.Rows)
	}
}
