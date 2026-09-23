// 文件职责：PruneStaleCheckpoints（§ADJ-BASIS 旧口径断点清理）的真库测试。
//
// 覆盖 owner 要求的四条：
//
//	① 缺省 dry-run 只报统计、一行都不删；
//	② --apply 只删"键不含当前口径位且写入时刻严格早于最后一次新口径写入"的行；
//	③ 有跑到一半的新口径任务（research_tasks 未终结的 discover_*）时拒绝删除；
//	④ 库里一条含口径位断点都没有时整轮不删。
//
// 本包不能 import internal/research（research 已 import store，会成环），因此这里用
// 合成的口径位常量 testPruneMarker：store 侧逻辑与具体版本串无关，真串（research.AdjBasisMarker）
// 与真实键形态的联调放在 cmd/research 的子命令测试里钉。
package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// testPruneMarker 测试用口径位（合成值，与内部逻辑解耦）。
const testPruneMarker = "|adj=zz-test-basis-1"

const (
	// 改前旧键（无口径位）与当前新键（含口径位）。
	legacyDFKey = "df|20230801|20260901|h5|ms10|w60|Mom20|a1b2c3d4e5|x"
	basisDFKey  = legacyDFKey + testPruneMarker
	legacyDPKey = "dp|20230801|20260901|h5|mt3|5.00|70|a1b2c3d4e5"
	basisDPKey  = legacyDPKey + testPruneMarker
	legacyPFKey = "pfac-dedup:20230801:20260901"
	basisPFKey  = legacyPFKey + testPruneMarker
	otherKey    = "zz|unknown-prefix|no-basis-tag"

	// cutoff 之前又写过一次的旧键（真死重，可删）
	legacyBeforeCutoff = "df|20200101|20201231|h5|ms10|w60|Mom20|ffffffff10|x"
	// 与最后一次新口径写入同秒的旧键（跑到一半的旧任务，严格早于判据 → 保留）
	legacySameSecond = "df|20210101|20211231|h5|ms10|w60|Mom20|eeeeeeee10|x"
	// cutoff 之后仍在写的旧键（宁可留死重也不误删 → 保留）
	legacyAfterCutoff = "df|20220101|20221231|h5|ms10|w60|Mom20|dddddddd10|x"
)

// newCkptPruneDB 打开一个临时真库（走完整 store.Open 迁移路径）。
func newCkptPruneDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// put 按指定写入时刻落一行断点（created_at 即 prune 判据读到的时刻）。
func put(t *testing.T, db *DB, key, stage, at string) {
	t.Helper()
	if err := db.PutWindowCkptAt(key, stage, "20240102", "20240331", `{"payload":"`+strings.Repeat("x", 64)+`"}`, at); err != nil {
		t.Fatalf("写断点失败 %s/%s: %v", key, stage, err)
	}
}

// ckptRowCount 读表行数（断言"没删任何东西"用）。
func ckptRowCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM research_ckpts`).Scan(&n); err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	return n
}

// hasKey 判断某键是否仍在表内。
func hasKey(t *testing.T, db *DB, key string) bool {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM research_ckpts WHERE resume_key=?`, key).Scan(&n); err != nil {
		t.Fatalf("查询 %s 失败: %v", key, err)
	}
	return n > 0
}

// seedPruneFixture 铺一个"改前 + 改后"混合时间线（共 11 行，其中 6 行为可删死重）：
//
//	2026-09-01 旧键 5 行（df| 两阶段 + dp| + pfac-dedup: + 未知前缀）→ 可删
//	2026-09-23 02:30/03:00/03:00 新口径 3 行（cutoff = 03:00:00）→ 永久保留
//	2026-09-23 01:00 旧键 1 行（早于 cutoff）→ 可删
//	2026-09-23 03:00 旧键 1 行（与 cutoff 同秒，跑到一半）→ 保留
//	2026-09-23 04:00 旧键 1 行（晚于 cutoff）→ 保留
func seedPruneFixture(t *testing.T, db *DB) {
	t.Helper()
	put(t, db, legacyDFKey, "pre", "2026-09-01 03:00:00")
	put(t, db, legacyDFKey, "gen", "2026-09-01 03:00:00")
	put(t, db, legacyDPKey, "pattern", "2026-09-01 03:10:00")
	put(t, db, legacyPFKey, "dedup", "2026-09-01 03:20:00")
	put(t, db, otherKey, "stage", "2026-09-01 03:30:00")
	// 新口径基线（三条，cutoff 取最晚的 03:00:00）
	put(t, db, basisDFKey, "pre", "2026-09-23 02:30:00")
	put(t, db, basisDPKey, "pattern", "2026-09-23 03:00:00")
	put(t, db, basisPFKey, "dedup", "2026-09-23 03:00:00")
	// 旧键但在 cutoff 之前又写了一次（真·旧口径残留）→ 仍可删
	put(t, db, legacyBeforeCutoff, "pre", "2026-09-23 01:00:00")
	// 旧键、与 cutoff 同秒写入：判据用严格早于，故保留（跑到一半不被删）
	put(t, db, legacySameSecond, "pre", "2026-09-23 03:00:00")
	// 旧键、cutoff 之后写入：保留（可能仍是旧任务在跑，宁可留死重不误删）
	put(t, db, legacyAfterCutoff, "pre", "2026-09-23 04:00:00")
}

// nsOf 在报告里找命名空间统计。
func nsOf(t *testing.T, rep *CkptPruneReport, ns string) CkptPruneNamespace {
	t.Helper()
	for _, g := range rep.Namespaces {
		if g.Namespace == ns {
			return g
		}
	}
	t.Fatalf("报告缺少命名空间 %q：%+v", ns, rep.Namespaces)
	return CkptPruneNamespace{}
}

// TestPruneStaleCheckpointsDryRun ①缺省 dry-run：报出规模，但行数一分不减。
func TestPruneStaleCheckpointsDryRun(t *testing.T) {
	db := newCkptPruneDB(t)
	seedPruneFixture(t, db)
	before := ckptRowCount(t, db)

	rep, err := db.PruneStaleCheckpoints(testPruneMarker, false)
	if err != nil {
		t.Fatalf("dry-run 失败: %v", err)
	}
	if rep.Apply {
		t.Fatal("Apply=false 却报成 apply 模式")
	}
	if rep.DeletedRows != 0 {
		t.Fatalf("dry-run 不应删除任何行，deleted=%d", rep.DeletedRows)
	}
	if got := ckptRowCount(t, db); got != before {
		t.Fatalf("dry-run 改动了数据：行数 %d → %d", before, got)
	}
	// 规模核对：11 行总量、3 行含口径位、6 行 stale（含 unknown 前缀 1 行）
	if rep.TotalRows != before || before != 11 {
		t.Fatalf("总行数统计异常 total=%d 实际=%d（铺数据应为 11）", rep.TotalRows, before)
	}
	if rep.MarkedRows != 3 {
		t.Fatalf("含口径位行数应为 3，实得 %d", rep.MarkedRows)
	}
	if rep.StaleRows != 6 {
		t.Fatalf("stale 行数应为 6（5 条 9/1 旧行 + 1 条 cutoff 前旧行），实得 %d", rep.StaleRows)
	}
	if rep.CutoffAt != "2026-09-23 03:00:00" {
		t.Fatalf("cutoff 应为最后一次新口径写入时刻 2026-09-23 03:00:00，实得 %q", rep.CutoffAt)
	}
	if rep.StaleBytes <= 0 {
		t.Fatalf("stale payload 字节应为正，实得 %d", rep.StaleBytes)
	}
	if n := nsOf(t, rep, "df|"); n.Rows != 6 || n.Stale != 3 {
		t.Fatalf("df| 命名空间统计异常：%+v", n)
	}
	if n := nsOf(t, rep, "dp|"); n.Rows != 2 || n.Stale != 1 {
		t.Fatalf("dp| 命名空间统计异常：%+v", n)
	}
	if n := nsOf(t, rep, "pfac-dedup:"); n.Rows != 2 || n.Stale != 1 {
		t.Fatalf("pfac-dedup: 命名空间统计异常：%+v", n)
	}
	if n := nsOf(t, rep, "other"); n.Rows != 1 || n.Stale != 1 {
		t.Fatalf("other 命名空间统计异常：%+v", n)
	}
	// 报告与日志里都不得出现 resume_key 全文之外的东西——这里检查前缀桶名不带键体。
	for _, g := range rep.Namespaces {
		if strings.Contains(g.Namespace, "Mom20") {
			t.Fatalf("命名空间桶名疑似泄漏 resume_key 载荷: %q", g.Namespace)
		}
	}
}

// TestPruneStaleCheckpointsApply ②--apply 只删 stale 行，新口径行与 cutoff 前后旧行保留。
func TestPruneStaleCheckpointsApply(t *testing.T) {
	db := newCkptPruneDB(t)
	seedPruneFixture(t, db)
	before := ckptRowCount(t, db)

	rep, err := db.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	if rep.DeletedRows != rep.StaleRows || rep.DeletedRows != 6 {
		t.Fatalf("删除行数应为 6（=stale 数），实得 deleted=%d stale=%d", rep.DeletedRows, rep.StaleRows)
	}
	if got := ckptRowCount(t, db); got != before-6 {
		t.Fatalf("表内剩余行数应为 %d，实得 %d", before-6, got)
	}
	// 含口径位的三行必须原样保留
	for _, k := range []string{basisDFKey, basisDPKey, basisPFKey} {
		if !hasKey(t, db, k) {
			t.Fatalf("新口径断点被误删: %s", k)
		}
	}
	// cutoff 当秒与之后的旧键行必须保留（在跑/未定型保护）
	if !hasKey(t, db, legacySameSecond) {
		t.Fatal("与 cutoff 同秒写入的旧口径行被误删（严格早于判据失效）")
	}
	if !hasKey(t, db, legacyAfterCutoff) {
		t.Fatal("cutoff 之后写入的旧口径行被误删")
	}
	// 9/1 那批纯死重应已全部消失
	for _, k := range []string{legacyDFKey, legacyDPKey, legacyPFKey, otherKey, legacyBeforeCutoff} {
		if hasKey(t, db, k) {
			t.Fatalf("旧口径死重行未被删除: %s", k)
		}
	}
	// 幂等：再跑一次没有可删的
	rep2, err := db.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("二次 apply 失败: %v", err)
	}
	if rep2.StaleRows != 0 || rep2.DeletedRows != 0 {
		t.Fatalf("清理应幂等，实得 stale=%d deleted=%d", rep2.StaleRows, rep2.DeletedRows)
	}
	if got := ckptRowCount(t, db); got != before-6 {
		t.Fatalf("二次 apply 改动了数据：剩余 %d 行（应为 %d）", got, before-6)
	}
}

// TestPruneStaleCheckpointsInFlightGuard ③在跑门：有未终结的发现类任务时拒绝删除。
func TestPruneStaleCheckpointsInFlightGuard(t *testing.T) {
	db := newCkptPruneDB(t)
	seedPruneFixture(t, db)
	before := ckptRowCount(t, db)

	// 当前口径的夜间发现任务正在跑（run-task 领取后 status=running）
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDiscoverFactors, Status: TaskRunning, Payload: "{}"})
	if err != nil {
		t.Fatalf("入队任务失败: %v", err)
	}
	rep, err := db.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	if len(rep.Blocked) == 0 {
		t.Fatal("未检出在跑任务，在跑门形同虚设")
	}
	if !strings.Contains(rep.Blocked[0], "discover_factors") {
		t.Fatalf("在跑门描述应含任务类型，实得 %q", rep.Blocked[0])
	}
	if rep.DeletedRows != 0 {
		t.Fatalf("在跑时必须拒绝删除，实得 deleted=%d", rep.DeletedRows)
	}
	if got := ckptRowCount(t, db); got != before {
		t.Fatalf("在跑门被绕过：行数 %d → %d", before, got)
	}

	// 任务终结（done）后同样的 --apply 就能删
	if err := db.UpdateTaskRunState(id, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("置任务 done 失败: %v", err)
	}
	rep2, err := db.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("任务终结后 apply 失败: %v", err)
	}
	if len(rep2.Blocked) != 0 || rep2.DeletedRows != 6 {
		t.Fatalf("任务终结后应完成清理，实得 blocked=%v deleted=%d", rep2.Blocked, rep2.DeletedRows)
	}
}

// TestPruneStaleCheckpointsNoBaseline ④库里没有任何含口径位断点：整轮不删。
func TestPruneStaleCheckpointsNoBaseline(t *testing.T) {
	db := newCkptPruneDB(t)
	// 只有旧口径行（新基线还没落库）
	put(t, db, legacyDFKey, "pre", "2026-09-01 03:00:00")
	put(t, db, legacyDPKey, "pattern", "2026-09-01 03:00:00")
	before := ckptRowCount(t, db)

	rep, err := db.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	if !rep.NoBaseline {
		t.Fatal("无新口径基线时未置 NoBaseline 标志")
	}
	if rep.DeletedRows != 0 || rep.StaleRows != 0 {
		t.Fatalf("无基线时不应统计/删除任何行，实得 stale=%d deleted=%d", rep.StaleRows, rep.DeletedRows)
	}
	if got := ckptRowCount(t, db); got != before {
		t.Fatalf("无基线时改动了数据：%d → %d", before, got)
	}

	// 空库同样安全（表存在但无行）
	empty := newCkptPruneDB(t)
	rep2, err := empty.PruneStaleCheckpoints(testPruneMarker, true)
	if err != nil {
		t.Fatalf("空库 apply 失败: %v", err)
	}
	if !rep2.NoBaseline || rep2.TotalRows != 0 || rep2.DeletedRows != 0 {
		t.Fatalf("空库应直接拒绝且零删除：%+v", rep2)
	}
}

// TestPruneStaleCheckpointsMarkerFormat 口径位必须显式传入且形态受控：
// 传空串/历史残串一律拒绝，避免把当前基线判成旧口径而误删。
func TestPruneStaleCheckpointsMarkerFormat(t *testing.T) {
	db := newCkptPruneDB(t)
	seedPruneFixture(t, db)
	before := ckptRowCount(t, db)
	for _, bad := range []string{"", "hfq-forward-fill-1", "adj=xx"} {
		if _, err := db.PruneStaleCheckpoints(bad, true); err == nil {
			t.Fatalf("口径位 %q 应被拒绝（缺 |adj= 前缀）", bad)
		}
	}
	if got := ckptRowCount(t, db); got != before {
		t.Fatalf("非法口径位却改动了数据：%d → %d", before, got)
	}
}
