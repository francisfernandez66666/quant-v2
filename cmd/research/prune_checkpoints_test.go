// 文件职责：research 子命令 prune-stale-checkpoints 的行为测试（CLI 层）。
//
// 这里刻意用**真实口径位常量** research.AdjBasisMarker 与真实形态的 resume_key
// （df| / dp| / pfac-dedup: 前缀 + |adj= 后缀），把"CLI 判据 == 研究链写入口径位"
// 钉住；store 包内的 PruneStaleCheckpoints 语义测试（含在跑门、cutoff 严格早于、
// 命名空间统计）见 internal/store/research_ckpts_prune_test.go。
//
// 只在临时库里跑，绝不碰生产研究库。
package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// 真实形态的断点键：改前（不含口径位）与改后（含当前口径位）。
const (
	legDF   = "df|20230801|20260901|h5|ms10|w60|Mom20|a1b2c3d4e5|x"
	curDF   = legDF + research.AdjBasisMarker
	legDP   = "dp|20230801|20260901|h5|mt3|5.00|70|a1b2c3d4e5"
	curDP   = legDP + research.AdjBasisMarker
	legPFIX = "pfac-dedup:20230801:20260901"
	curPFIX = legPFIX + research.AdjBasisMarker
)

// openPruneTestDB 打开临时真库（走完整迁移路径），返回句柄。
func openPruneTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "prune_cli.db"))
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seed 按指定时刻写入一行断点。
func seed(t *testing.T, db *store.DB, key, stage, at string) {
	t.Helper()
	if err := db.PutWindowCkptAt(key, stage, "20240102", "20240331", `{"rows":1}`, at); err != nil {
		t.Fatalf("写断点失败: %v", err)
	}
}

// countCkpts 表行数。
func countCkpts(t *testing.T, db *store.DB) int {
	t.Helper()
	rows, err := db.QueryRows(`SELECT resume_key FROM research_ckpts`)
	if err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	return len(rows)
}

// captureStdout 抓一段函数输出（验证运维输出不外泄 resume_key 载荷）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("建管道失败: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读输出失败: %v", err)
	}
	_ = r.Close()
	return string(out)
}

// TestPruneSubcommandDefaultsToDryRun 缺省（无 --apply）只报统计：行数一分不减。
func TestPruneSubcommandDefaultsToDryRun(t *testing.T) {
	db := openPruneTestDB(t)
	seed(t, db, legDF, "pre", "2026-09-01 03:00:00")
	seed(t, db, legDP, "pattern", "2026-09-01 03:05:00")
	seed(t, db, legPFIX, "dedup", "2026-09-01 03:10:00")
	seed(t, db, curDF, "pre", "2026-09-23 02:00:00")
	seed(t, db, curDP, "pattern", "2026-09-23 03:00:00")
	before := countCkpts(t, db)

	out := captureStdout(t, func() { cmdPruneStaleCheckpoints(db, nil) })

	if got := countCkpts(t, db); got != before {
		t.Fatalf("缺省必须是 dry-run，行数却 %d → %d", before, got)
	}
	if !strings.Contains(out, "DRY-RUN") {
		t.Fatalf("输出未标明 dry-run 模式: %s", out)
	}
	if !strings.Contains(out, "判定旧口径=3") {
		t.Fatalf("应报出 3 行可删（旧口径死重），输出: %s", out)
	}
	for _, ns := range []string{"df|", "dp|", "pfac-dedup:"} {
		if !strings.Contains(out, ns) {
			t.Fatalf("输出缺少命名空间 %q 的统计行: %s", ns, out)
		}
	}
	// 运维输出只给命名空间前缀与计数，绝不打印 resume_key 全文（键内含策略参数）。
	if strings.Contains(out, "Mom20") || strings.Contains(out, legDP) {
		t.Fatalf("输出泄漏 resume_key 载荷: %s", out)
	}
}

// TestPruneSubcommandApplyDeletesOnlyStale --apply 时按真实口径位删除旧口径行，
// 新口径行原样保留，且末尾给出释放规模。
func TestPruneSubcommandApplyDeletesOnlyStale(t *testing.T) {
	db := openPruneTestDB(t)
	seed(t, db, legDF, "pre", "2026-09-01 03:00:00")
	seed(t, db, legDF, "gen", "2026-09-01 03:00:00")
	seed(t, db, legDP, "pattern", "2026-09-01 03:05:00")
	seed(t, db, curDF, "pre", "2026-09-23 02:00:00")
	seed(t, db, curDP, "pattern", "2026-09-23 03:00:00")
	seed(t, db, curPFIX, "dedup", "2026-09-23 03:00:00")
	before := countCkpts(t, db)

	out := captureStdout(t, func() { cmdPruneStaleCheckpoints(db, []string{"--apply"}) })

	if !strings.Contains(out, "删除 3 行") {
		t.Fatalf("--apply 应删除 3 行旧口径断点，输出: %s", out)
	}
	if got := countCkpts(t, db); got != before-3 {
		t.Fatalf("剩余行数应为 %d，实得 %d", before-3, got)
	}
	keys := map[string]int{}
	rows, err := db.QueryRows(`SELECT resume_key FROM research_ckpts`)
	if err != nil {
		t.Fatalf("读回剩余断点失败: %v", err)
	}
	for _, r := range rows {
		k, _ := r["resume_key"].(string)
		keys[k]++
	}
	for _, k := range []string{curDF, curDP, curPFIX} {
		if keys[k] == 0 {
			t.Fatalf("含当前口径位的断点被误删: %s", k)
		}
	}
	if keys[legDF] != 0 || keys[legDP] != 0 {
		t.Fatalf("旧口径死重行未被清理（df=%d dp=%d）", keys[legDF], keys[legDP])
	}
	// 幂等：再 --apply 一次没有可删的，行数不再变
	again := countCkpts(t, db)
	out2 := captureStdout(t, func() { cmdPruneStaleCheckpoints(db, []string{"--apply"}) })
	if !strings.Contains(out2, "未删除任何行") {
		t.Fatalf("二次 --apply 应无行可删，输出: %s", out2)
	}
	if got := countCkpts(t, db); got != again {
		t.Fatalf("二次 --apply 改动了数据：%d → %d", again, got)
	}
}

// TestPruneRefusedWhileDiscoveryInFlight 在跑门：队列里有未终结的发现类任务时
// 即便 --apply 也不得删任何行（该分支在 CLI 入口会转成非零退出码，故这里走内部函数）。
func TestPruneRefusedWhileDiscoveryInFlight(t *testing.T) {
	db := openPruneTestDB(t)
	seed(t, db, legDF, "pre", "2026-09-01 03:00:00")
	seed(t, db, curDF, "pre", "2026-09-23 03:00:00")
	if _, err := db.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskDiscoverFactors, Status: store.TaskRunning, Payload: "{}",
	}); err != nil {
		t.Fatalf("入队在跑任务失败: %v", err)
	}
	before := countCkpts(t, db)

	rep, err := pruneStaleCheckpoints(db, research.AdjBasisMarker, true)
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	if len(rep.Blocked) == 0 {
		t.Fatal("未检出在跑的发现类任务")
	}
	if rep.DeletedRows != 0 {
		t.Fatalf("在跑时必须拒绝删除，实得 deleted=%d", rep.DeletedRows)
	}
	if got := countCkpts(t, db); got != before {
		t.Fatalf("在跑门被绕过：%d → %d", before, got)
	}
}

// TestPruneRefusedWithoutNewBasisRows 库里一条含口径位断点都没有：即便 --apply 也不删。
func TestPruneRefusedWithoutNewBasisRows(t *testing.T) {
	db := openPruneTestDB(t)
	seed(t, db, legDF, "pre", "2026-09-01 03:00:00")
	seed(t, db, legDP, "pattern", "2026-09-01 03:00:00")
	before := countCkpts(t, db)

	rep, err := pruneStaleCheckpoints(db, research.AdjBasisMarker, true)
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	if !rep.NoBaseline || rep.DeletedRows != 0 {
		t.Fatalf("无新口径基线时应拒绝删除：%+v", rep)
	}
	if got := countCkpts(t, db); got != before {
		t.Fatalf("无基线时改动了数据：%d → %d", before, got)
	}
}

// TestAdjBasisMarkerShape 口径位常量必须保持 "|adj=<版本>" 形态：
// prune 判据与断点键生成共用这一处定义（键生成侧契约见 internal/research）。
func TestAdjBasisMarkerShape(t *testing.T) {
	if !strings.HasPrefix(research.AdjBasisMarker, "|adj=") {
		t.Fatalf("AdjBasisMarker 形态异常: %q", research.AdjBasisMarker)
	}
	if !strings.Contains(research.AdjBasisMarker, research.AdjBaselineVersion) {
		t.Fatalf("AdjBasisMarker 未包含版本号常量: %q", research.AdjBasisMarker)
	}
}
