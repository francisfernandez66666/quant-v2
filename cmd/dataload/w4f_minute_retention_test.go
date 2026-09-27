// w4f_minute_retention_test.go — §0926E2E-17B 分钟表保留策略定点测试。
// 锁四件事：
//  1. 日增模式成功后按 --keep-days 裁剪（cutoff 之前的旧行删、窗口内行与当日新行留）；
//  2. 回填模式**不裁剪**（回填自己喂进来的就是历史窗口，边灌边删等于自残）；
//  3. keep-days=0 关闭裁剪（显式口径，不是"忘了配"造成的静默全删）；
//  4. 裁剪失败不翻转本轮落库成功（卫生问题 ≠ 装载失败），锚点行仍出门。
//
// store.PruneMinuteBars 自身边界（严格早于 cutoff、形态非法拒删）在 internal/store 侧另有锁。
// English: §0926E2E-17B minute-bar retention tests — incremental runs prune, backfill never
// prunes, keep-days=0 disables, prune errors don't flip sync success; store-level boundary
// (strictly-older rows, malformed cutoff rejection) is pinned in internal/store.
package main

import (
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// w4fSeedDay 往分钟表直写某票某日的 48 根（复用 store 测试同款口径，day 形如 "2025-01-05"）。
func w4fSeedDay(t *testing.T, db *store.DB, tsCode, day string) {
	t.Helper()
	bars := make([]store.MinuteBar, 0, 48)
	for i := 0; i < 48; i++ {
		total := 9*60 + 35 + 5*i
		ts := fmt.Sprintf("%s %02d:%02d:00", day, total/60, total%60)
		p := 10.0 + float64(i)*0.01
		bars = append(bars, store.MinuteBar{TsCode: tsCode, Scale: 5, Ts: ts,
			Open: p, High: p, Low: p, Close: p, Vol: 1000, Amount: p * 1000})
	}
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("seed %s: %v", day, err)
	}
}

func w4fStatsAll(t *testing.T, db *store.DB) store.MinuteStats {
	t.Helper()
	st, err := db.MinuteTableStats(5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	return st
}

// TestW4FIncrementalPrunesOldRows 日增成功 → 裁剪 cutoff 前旧行，保留窗口内与本轮新行。
func TestW4FIncrementalPrunesOldRows(t *testing.T) {
	db := newSyncDB(t)
	w4fSeedDay(t, db, "600000.SH", "2025-01-05") // 远久（>180 天，应删）
	w4fSeedDay(t, db, "600000.SH", "2026-08-01") // 窗口内（<180 天，应留）
	codes := writeCodesFile(t, "600001.SH")
	o := minuteSyncOpts{Scale: 5, Count: 10, CodesFile: codes, Incremental: true, MaxFailPct: 10, KeepDays: 180}
	// now 固定在 2026-09-26（北京）：cutoff = 2026-03-30。
	now := time.Date(2026, 9, 26, 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	stub := &stubFetcher{count: 10, missing: map[string]bool{}}
	written, err := runMinuteSync(db, stub, o, now)
	if err != nil {
		t.Fatalf("日增: %v", err)
	}
	if written == 0 {
		t.Fatal("日增应有写入")
	}
	st := w4fStatsAll(t, db)
	old, err := db.MinuteBarsByDay("600000.SH", 5, "2025-01-05")
	if err != nil {
		t.Fatalf("查旧日: %v", err)
	}
	if len(old) != 0 {
		t.Fatalf("cutoff 前旧行应被裁剪，仍剩 %d 根", len(old))
	}
	keep, err := db.MinuteBarsByDay("600000.SH", 5, "2026-08-01")
	if err != nil {
		t.Fatalf("查窗口内日: %v", err)
	}
	if len(keep) != 48 {
		t.Fatalf("保留窗口的 2026-08-01 应完整留存 48 根，得到 %d", len(keep))
	}
	if st.Rows != 48+10 {
		t.Fatalf("裁剪后总行数应为 48(留存)+10(本轮)=%d，得到 %d", 58, st.Rows)
	}
}

// TestW4FBackfillNeverPrunes 反证：回填模式即便带 keep-days 也不得裁剪。
func TestW4FBackfillNeverPrunes(t *testing.T) {
	db := newSyncDB(t)
	w4fSeedDay(t, db, "600000.SH", "2025-01-05")
	codes := writeCodesFile(t, "600001.SH")
	o := minuteSyncOpts{Scale: 5, Count: 10, CodesFile: codes, Incremental: false, MaxFailPct: 10, KeepDays: 180}
	now := time.Date(2026, 9, 26, 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	stub := &stubFetcher{count: 10, missing: map[string]bool{}}
	if _, err := runMinuteSync(db, stub, o, now); err != nil {
		t.Fatalf("回填: %v", err)
	}
	old, err := db.MinuteBarsByDay("600000.SH", 5, "2025-01-05")
	if err != nil {
		t.Fatalf("查旧日: %v", err)
	}
	if len(old) != 48 {
		t.Fatalf("回填模式不得裁剪旧行，得到 %d 根（应 48）", len(old))
	}
}

// TestW4FKeepDaysZeroDisablesPrune keep-days=0 = 显式关闭裁剪。
func TestW4FKeepDaysZeroDisablesPrune(t *testing.T) {
	db := newSyncDB(t)
	w4fSeedDay(t, db, "600000.SH", "2025-01-05")
	codes := writeCodesFile(t, "600001.SH")
	o := minuteSyncOpts{Scale: 5, Count: 10, CodesFile: codes, Incremental: true, MaxFailPct: 10, KeepDays: 0}
	now := time.Date(2026, 9, 26, 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	stub := &stubFetcher{count: 10, missing: map[string]bool{}}
	if _, err := runMinuteSync(db, stub, o, now); err != nil {
		t.Fatalf("日增: %v", err)
	}
	old, err := db.MinuteBarsByDay("600000.SH", 5, "2025-01-05")
	if err != nil {
		t.Fatalf("查旧日: %v", err)
	}
	if len(old) != 48 {
		t.Fatalf("keep-days=0 应关闭裁剪，得到 %d 根（应 48）", len(old))
	}
}

// TestW4FParseMinuteFlagsDefaultKeepDays 命令行缺省：--keep-days 默认 180（与文件门口径一致）。
func TestW4FParseMinuteFlagsDefaultKeepDays(t *testing.T) {
	o, err := parseMinuteFlags([]string{"--incremental"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.KeepDays != 180 {
		t.Fatalf("keep-days 缺省应为 180，得到 %d", o.KeepDays)
	}
	o2, err := parseMinuteFlags([]string{"--keep-days", "0"})
	if err != nil {
		t.Fatalf("parse2: %v", err)
	}
	if o2.KeepDays != 0 {
		t.Fatalf("--keep-days 0 应透传，得到 %d", o2.KeepDays)
	}
}
