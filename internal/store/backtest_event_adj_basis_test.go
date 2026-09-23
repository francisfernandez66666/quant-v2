// 文件职责：§ADJ-BASIS（2026-09-23）backtest_event_results「复权口径位进主键」迁移 + 读写侧
// 口径过滤的真库回归。锁死六件事：
//
//	① 旧库三列主键重建为四列，且**一行不丢、一行不改**（旧行带 adj_basis='' 过表）；
//	② 改前 '' 行与当前口径行可以在同一三元组上并存（这正是"只加列+建 UNIQUE 索引"做不到、
//	   必须重建主键的原因——旧写法会撞 `UNIQUE constraint failed: ... (1555)`）；
//	③ 行数守恒守卫：任何会塌行的旧表一律中止重建，原表分毫不动 + ERROR 日志，应用照常启动；
//	④ 同口径重写=覆盖，不产生第二行；
//	⑤ 读写侧永不把 '' 行当当前结果（含情绪矩阵聚合）；口径位未装配时读恒未命中、写直接拒绝；
//	⑥ 迁移幂等：反复 Open 不重复搬表、不改数据、不残留 *_new。
//
// 本包不能 import internal/research（research 已 import store，会成环），因此这里用合成的
// 口径位常量 testAdjBasis：store 侧逻辑与具体版本串无关；真串（research.AdjBaselineVersion）
// 与调用方的联动由 internal/backtest / internal/server 侧的测试钉住。
// English: real-DB regression for the basis-in-primary-key rebuild of backtest_event_results —
// row conservation, coexistence of pre-basis (empty) and current-basis rows, the abort guard,
// replace-not-duplicate upserts, readers that never surface legacy rows, and idempotency.
package store

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// testAdjBasis 当前口径位（合成值）；testAdjBasisOther 另一个口径位（并存对照用）。
	testAdjBasis      = "zz-basis-current-1"
	testAdjBasisOther = "zz-basis-other-2"
	// legacyBasis 改前旧证据行的哨兵值：空串。
	legacyBasis = ""
)

// 旧表形态：三列主键（口径进键之前的生产形态）。
const legacyEventResultsDDL = `CREATE TABLE backtest_event_results (
	candidate_id INTEGER NOT NULL,
	event_date TEXT NOT NULL,
	industry TEXT NOT NULL,
	result_json TEXT NOT NULL,
	rule_fp TEXT DEFAULT '',
	PRIMARY KEY (candidate_id, event_date, industry)
)`

// 旧表变体：键列不声明 NOT NULL（模拟被手工改过/更早一代的库——SQLite 下复合主键允许 NULL，
// 且 NULL 之间互不相等，于是同一三元组能塞进两行，正是守卫要拦的塌行场景）。
const legacyEventResultsNullableDDL = `CREATE TABLE backtest_event_results (
	candidate_id INTEGER,
	event_date TEXT,
	industry TEXT,
	result_json TEXT,
	rule_fp TEXT,
	PRIMARY KEY (candidate_id, event_date, industry)
)`

// legacyRow 一条改前断点行（ind 可为 nil=键列为 NULL）。
type legacyRow struct {
	cand int64
	date string
	ind  any
	js   string
	fp   string
}

// seedLegacyEventDB 造一个"口径进键之前"的库：先走完整 Open 建库，再把
// backtest_event_results 换回旧形态并灌入 rows，返回（库路径, 迁移前的全表逐行快照）。
// 下次 Open 即触发重建迁移，快照用来证明"一行不丢、一行不改"。
func seedLegacyEventDB(t *testing.T, ddl string, rows []legacyRow) (string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "basis.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open(建库): %v", err)
	}
	if _, err := d.db.Exec(`DROP TABLE backtest_event_results`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(ddl); err != nil {
		t.Fatalf("建旧形态表: %v", err)
	}
	for _, r := range rows {
		if _, err := d.db.Exec(`INSERT INTO backtest_event_results (candidate_id, event_date, industry, result_json, rule_fp)
			VALUES (?,?,?,?,?)`, r.cand, r.date, r.ind, r.js, r.fp); err != nil {
			t.Fatalf("灌旧行 %+v: %v", r, err)
		}
	}
	before := dumpEventRows(t, d)
	if len(before) != len(rows) {
		t.Fatalf("旧库灌数异常: %d != %d", len(before), len(rows))
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return path, before
}

// openWithLog 打开库并捕获这段时间的日志（守卫中止的 ERROR 要能在日志里看到）。
func openWithLog(t *testing.T, path string) (*DB, string) {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, buf.String()
}

// dumpEventRows 全表逐行快照（含/不含 adj_basis 由表实际形态决定），用于"一行不丢、一行不改"比对。
func dumpEventRows(t *testing.T, d *DB) []string {
	t.Helper()
	hasBasis, err := d.hasColumn("backtest_event_results", "adj_basis")
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT candidate_id, COALESCE(event_date,'<NULL>'), COALESCE(industry,'<NULL>'),
			COALESCE(rule_fp,'<NULL>'), COALESCE(result_json,'<NULL>')`
	if hasBasis {
		q += `, COALESCE(adj_basis,'<NULL>')`
	}
	q += ` FROM backtest_event_results ORDER BY 1,2,3,4`
	rows, err := d.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		if hasBasis {
			var cid int64
			var date, ind, fp, js, basis string
			if err := rows.Scan(&cid, &date, &ind, &fp, &js, &basis); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%s", cid, date, ind, fp, js, basis))
			continue
		}
		var cid int64
		var date, ind, fp, js string
		if err := rows.Scan(&cid, &date, &ind, &fp, &js); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s", cid, date, ind, fp, js))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// countRows SELECT COUNT(*)（本表）。
func countRows(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_event_results`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEventResultsMigrationCarriesLegacyRows ① 旧行原样过表、主键升级为四列、无 *_new 残留。
func TestEventResultsMigrationCarriesLegacyRows(t *testing.T) {
	seed := []legacyRow{
		{1, "20260105", "半导体", `{"date":"20260105","mean_excess":{"5":2.0}}`, "fp-a"},
		{1, "20260106", "券商", `{"date":"20260106","mean_excess":{"5":1.0}}`, "fp-a"},
		{2, "20260105", "白酒", `{"date":"20260105","mean_excess":{"1":-1.0}}`, ``},
	}
	path, before := seedLegacyEventDB(t, legacyEventResultsDDL, seed)
	if len(before) != 3 {
		t.Fatalf("旧库行数=%d，期望 3", len(before))
	}
	d, logs := openWithLog(t, path)
	if d.EventBasisDegraded() {
		t.Fatalf("迁移被判降级（不该）: %s", logs)
	}
	if !strings.Contains(logs, "migrate backtest_event_results PK") {
		t.Errorf("重建未打日志: %s", logs)
	}
	pk, err := d.tableHasPKColumns("backtest_event_results")
	if err != nil {
		t.Fatal(err)
	}
	if !pkColumnsEqual(pk, eventResultsPKTarget) {
		t.Fatalf("主键未升级: %v", pk)
	}
	if n := countRows(t, d); n != 3 {
		t.Fatalf("迁移后行数=%d，期望 3（一行不丢）", n)
	}
	// 逐行等价：旧行 = 新行前 5 段 + 末尾 '' 哨兵
	got := dumpEventRows(t, d)
	for i, want := range before {
		if got[i] != want+"|"+legacyBasis {
			t.Fatalf("第 %d 行被改动:\n 旧 %q\n 新 %q", i, want, got[i])
		}
	}
	// 旧行一律带 '' 哨兵 ⇒ 当前口径下计数为 0（改前行不得被当作现成结果）
	if n, _ := d.CountBacktestEventResults(1, testAdjBasis); n != 0 {
		t.Fatalf("旧行应全部落在 adj_basis='' 上，当前口径计数=%d", n)
	}
	var left int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='backtest_event_results_new'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("重建后残留 backtest_event_results_new 表")
	}
	// ⑥ 再开一次：不动表、不改数据
	d2, logs2 := openWithLog(t, path)
	if d2.EventBasisDegraded() {
		t.Fatalf("二次 Open 被判降级: %s", logs2)
	}
	if strings.Contains(logs2, "migrate backtest_event_results PK") {
		t.Errorf("二次 Open 重复搬表（幂等失效）: %s", logs2)
	}
	if got2 := dumpEventRows(t, d2); strings.Join(got2, "\n") != strings.Join(got, "\n") {
		t.Errorf("二次 Open 改动了数据:\n%v\n%v", got, got2)
	}
	d2.Close()
	d.Close()
}

// TestEventResultsLegacyAndCurrentBasisCoexist ② 同三元组的改前行与当前口径行并存——
// 这就是"只 ALTER 加列 + 建四列 UNIQUE 索引"办不到的那一步（旧表级主键会把新口径行拦死）。
func TestEventResultsLegacyAndCurrentBasisCoexist(t *testing.T) {
	path, _ := seedLegacyEventDB(t, legacyEventResultsDDL, []legacyRow{
		{7, "20260202", "半导体", `{"pre":true}`, "fp"},
	})
	d, logs := openWithLog(t, path)
	defer d.Close()
	if d.EventBasisDegraded() {
		t.Fatalf("迁移被降级: %s", logs)
	}
	if err := d.UpsertBacktestEventResult(7, "20260202", "半导体", "fp", testAdjBasis, `{"post":true}`); err != nil {
		t.Fatalf("新口径行写入失败（1555 复现）: %v", err)
	}
	if n := countRows(t, d); n != 2 {
		t.Fatalf("期望改前/改后两行并存，得 %d", n)
	}
	js, ok, err := d.GetBacktestEventResult(7, "20260202", "半导体", "fp", testAdjBasis)
	if err != nil || !ok || js != `{"post":true}` {
		t.Fatalf("当前口径读到的不是新行: ok=%v js=%s err=%v", ok, js, err)
	}
	// 改前行仍在库里（证据不删），但没有读者能拿到它
	var legacy string
	if err := d.db.QueryRow(`SELECT result_json FROM backtest_event_results WHERE adj_basis=''`).Scan(&legacy); err != nil {
		t.Fatalf("改前证据行丢失: %v", err)
	}
	if legacy != `{"pre":true}` {
		t.Fatalf("改前证据行被改写: %s", legacy)
	}
}

// TestEventResultsMigrationAbortsOnRowCollapse ③ 行数守恒守卫：会塌行的旧表 ⇒ 中止、
// 原表未动、ERROR 入日志、应用照常启动，且读写侧转入保守模式。
func TestEventResultsMigrationAbortsOnRowCollapse(t *testing.T) {
	// 两行键列同为 NULL：SQLite 视作互不相等（旧表装得下），投影到 adj_basis='' 后会塌成一行
	path, _ := seedLegacyEventDB(t, legacyEventResultsNullableDDL, []legacyRow{
		{9, "20260303", nil, `{"a":1}`, "fp"},
		{9, "20260303", nil, `{"b":2}`, "fp"},
		{9, "20260304", "白酒", `{"c":3}`, "fp"},
	})
	d, logs := openWithLog(t, path)
	defer d.Close()
	if !d.EventBasisDegraded() {
		t.Fatalf("守卫未生效：塌行库被放过了\n日志: %s", logs)
	}
	// 守卫算术要看得见：总行数 3 vs 投影键去重后 2（两行 NULL 行业键塌成一行）
	if !strings.Contains(logs, "ERROR") || !strings.Contains(logs, "行数守恒预检不通过") ||
		!strings.Contains(logs, "COUNT(*)=3") || !strings.Contains(logs, "去重后=2") {
		t.Fatalf("中止未打含守卫算术的 ERROR 日志: %s", logs)
	}
	// 原表分毫不动：仍是旧三列主键、三行俱在、内容不变
	pk, err := d.tableHasPKColumns("backtest_event_results")
	if err != nil {
		t.Fatal(err)
	}
	if !pkColumnsEqual(pk, eventResultsPKLegacy) {
		t.Fatalf("守卫中止后主键被改动: %v", pk)
	}
	if n := countRows(t, d); n != 3 {
		t.Fatalf("守卫中止后行数=%d，期望 3（原表未动）", n)
	}
	if has, _ := d.hasColumn("backtest_event_results", "adj_basis"); has {
		t.Fatalf("守卫中止后不该给原表加列")
	}
	var left int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='backtest_event_results_new'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("中止后残留 *_new 半截表")
	}
	// 保守回退：写拒绝、读恒未命中（即便表里物理上有这一行），聚合直接报错
	if err := d.UpsertBacktestEventResult(9, "20260304", "白酒", "fp", testAdjBasis, `{"x":1}`); err == nil {
		t.Fatalf("降级模式下写缓存居然成功了")
	}
	if _, ok, err := d.GetBacktestEventResult(9, "20260304", "白酒", "fp", testAdjBasis); err != nil || ok {
		t.Fatalf("降级模式下读缓存命中了（应恒未命中→重算）: ok=%v err=%v", ok, err)
	}
	if n, _ := d.CountBacktestEventResults(9, testAdjBasis); n != 0 {
		t.Fatalf("降级模式下计数应为 0，得 %d", n)
	}
	if _, err := d.ListEmotionStrategyMatrix(map[string]string{}, nil, testAdjBasis); err == nil {
		t.Fatalf("降级模式下情绪矩阵不该照常发布")
	}
	// 再开一次：依旧中止（不半途转正），原表依旧未动
	d2, logs2 := openWithLog(t, path)
	defer d2.Close()
	if !d2.EventBasisDegraded() || countRows(t, d2) != 3 {
		t.Fatalf("二次 Open 后降级状态/原表异常: degraded=%v logs=%s", d2.EventBasisDegraded(), logs2)
	}
}

// TestEventResultsUnknownPKShapeDegradesWithoutTouching 未知主键形态（被手工改过/无主键）
// 一律不重建：置降级 + ERROR，表原封不动。
func TestEventResultsUnknownPKShapeDegradesWithoutTouching(t *testing.T) {
	path, _ := seedLegacyEventDB(t, `CREATE TABLE backtest_event_results (
		candidate_id INTEGER, event_date TEXT, industry TEXT, result_json TEXT, rule_fp TEXT)`,
		[]legacyRow{{5, "20260404", "半导体", `{"k":1}`, "fp"}})
	d, logs := openWithLog(t, path)
	defer d.Close()
	if !d.EventBasisDegraded() {
		t.Fatalf("未知主键形态未触发降级: %s", logs)
	}
	if !strings.Contains(logs, "主键形态非预期") {
		t.Fatalf("未见未知形态 ERROR 说明: %s", logs)
	}
	if n := countRows(t, d); n != 1 {
		t.Fatalf("原表被改动，行数=%d", n)
	}
}

// TestEventResultsSameBasisReplacesNotDuplicates ④ 同口径重写覆盖、不同口径并存。
func TestEventResultsSameBasisReplacesNotDuplicates(t *testing.T) {
	d := testDB(t)
	for _, js := range []string{`{"v":1}`, `{"v":2}`, `{"v":3}`} {
		if err := d.UpsertBacktestEventResult(3, "20260505", "半导体", "fp-new", testAdjBasis, js); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpsertBacktestEventResult(3, "20260505", "半导体", "fp-new", testAdjBasisOther, `{"other":true}`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, d); n != 2 {
		t.Fatalf("同口径重写应覆盖成 1 行 + 另一口径 1 行 = 2 行，得 %d", n)
	}
	js, ok, err := d.GetBacktestEventResult(3, "20260505", "半导体", "fp-new", testAdjBasis)
	if err != nil || !ok || js != `{"v":3}` {
		t.Fatalf("同口径重写未覆盖: ok=%v js=%s err=%v", ok, js, err)
	}
	if n, _ := d.CountBacktestEventResults(3, testAdjBasis); n != 1 {
		t.Fatalf("当前口径计数=%d，期望 1", n)
	}
	// 规则指纹变更仍按旧语义覆盖（同口径同三元组只留最新一行）
	if err := d.UpsertBacktestEventResult(3, "20260505", "半导体", "fp-old", testAdjBasis, `{"fp":"old"}`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, d); n != 2 {
		t.Fatalf("改指纹应覆盖同一行，得 %d 行", n)
	}
}

// TestEventResultsReadersNeverSurfaceLegacyRows ⑤ 空串哨兵行在任何当前口径读取下都不得出现：
// 单点读取、计数、情绪矩阵三条路径全覆盖；口径位未装配（空串）时读未命中、写与聚合直接拒绝。
func TestEventResultsReadersNeverSurfaceLegacyRows(t *testing.T) {
	d := testDB(t)
	cid, err := d.SaveCandidate(&Candidate{Kind: "factor", Factors: "[]", Weights: "{}", Reason: "口径测试"})
	if err != nil {
		t.Fatal(err)
	}
	insertLegacy := func(date, industry, js string) {
		t.Helper()
		if _, err := d.db.Exec(`INSERT INTO backtest_event_results (candidate_id, event_date, industry, adj_basis, rule_fp, result_json)
			VALUES (?,?,?,?,'fp',?)`, cid, date, industry, legacyBasis, js); err != nil {
			t.Fatal(err)
		}
	}
	// 一条改前 '' 行 + 同三元组的当前口径行 + 一条只有改前行的三元组
	insertLegacy("20260606", "半导体", `{"legacy":true}`)
	if err := d.UpsertBacktestEventResult(cid, "20260606", "半导体", "fp", testAdjBasis, `{"legacy":false}`); err != nil {
		t.Fatal(err)
	}
	insertLegacy("20260607", "白酒", `{"legacy":true}`)
	if n := countRows(t, d); n != 3 {
		t.Fatalf("夹具行数=%d，期望 3", n)
	}
	// 单点读取：命中当前口径行；只有改前行的三元组读不到任何行
	if js, ok, _ := d.GetBacktestEventResult(cid, "20260606", "半导体", "fp", testAdjBasis); !ok || js != `{"legacy":false}` {
		t.Fatalf("当前口径读到了不该读的内容: ok=%v js=%s", ok, js)
	}
	if _, ok, _ := d.GetBacktestEventResult(cid, "20260607", "白酒", "fp", testAdjBasis); ok {
		t.Fatalf("改前 '' 行被当成当前结果返回了")
	}
	// 别的口径同样读不到当前口径行（键真的含口径位）
	if _, ok, _ := d.GetBacktestEventResult(cid, "20260606", "半导体", "fp", testAdjBasisOther); ok {
		t.Fatalf("跨口径读取命中了")
	}
	// 计数只算当前口径
	if n, _ := d.CountBacktestEventResults(cid, testAdjBasis); n != 1 {
		t.Fatalf("当前口径计数=%d，期望 1（'' 行不得计入）", n)
	}
	// 情绪矩阵：只聚合当前口径那一条事件（两条改前行都不进桶）
	rows, err := d.ListEmotionStrategyMatrix(map[string]string{"20260606": "高潮", "20260607": "退潮"}, nil, testAdjBasis)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CandidateID != cid || rows[0].TotalEvents != 1 {
		t.Fatalf("矩阵混装了改前行: %+v", rows)
	}
	if len(rows[0].Cells) != 1 || rows[0].Cells[0].Phase != "高潮" {
		t.Fatalf("矩阵桶异常: %+v", rows[0].Cells)
	}
	// 矩阵确实排除了非当前口径行（2 条）——上面已 WARN，这里直接查库自证
	var excluded int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_event_results WHERE adj_basis <> ?`, testAdjBasis).Scan(&excluded); err != nil {
		t.Fatal(err)
	}
	if excluded != 2 {
		t.Fatalf("被口径过滤掉的旧证据行数=%d，期望 2", excluded)
	}
	// 口径位未装配（空串）：读恒未命中、写与聚合一律拒绝
	if _, ok, err := d.GetBacktestEventResult(cid, "20260606", "半导体", "fp", ""); err != nil || ok {
		t.Fatalf("空口径位不该命中: ok=%v err=%v", ok, err)
	}
	if err := d.UpsertBacktestEventResult(cid, "20260608", "半导体", "fp", "", `{}`); err == nil {
		t.Fatalf("空口径位写入不该被允许")
	}
	if _, err := d.ListEmotionStrategyMatrix(map[string]string{}, nil, ""); err == nil {
		t.Fatalf("空口径位聚合不该被允许")
	}
}

// TestEventResultsFreshDBNeedsNoRebuild 新建库建表语句本身就是目标四列主键（迁移空转）。
func TestEventResultsFreshDBNeedsNoRebuild(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	d := testDB(t)
	pk, err := d.tableHasPKColumns("backtest_event_results")
	if err != nil {
		t.Fatal(err)
	}
	if !pkColumnsEqual(pk, eventResultsPKTarget) {
		t.Fatalf("新建库主键形态非目标: %v", pk)
	}
	if d.EventBasisDegraded() {
		t.Fatalf("新建库不该降级")
	}
	if strings.Contains(buf.String(), "migrate backtest_event_results PK") {
		t.Fatalf("新建库触发了重建（应空转）: %s", buf.String())
	}
}
