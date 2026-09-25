// 本文件锁 §B4-PIT（owner 裁决 2026-09-26「幸存者偏差开关：开，默认打开」）的股票池裁决面：
//  1. 缺省即时点：不写 PointInTime 也要按"起始日已上市且未退市"建池——退市样本必须在 Start
//     之前被剔、Start 之后才退市的票必须留在池内（这正是消除幸存者偏差的方向）；
//  2. 显式关闭是唯一的旧口径出口，且 poolNote 必须把"幸存者偏差在体"说出来；
//  3. 元数据零覆盖（dataload meta-dates 没跑）时的降级必须**可见**：退回旧池可以，静默不行。
//
// 缺陷背景：UniverseAt/--with-delisted/PointInTime 三件套自 §WS-D D-2 起零个非测试调用方，
// 夜间回放/扫参全部跑在"今天在市的票"上回测历史——防线停在测试里（[[feedback]] 家族）。
// English: locks the §B4-PIT universe behavior — PIT by default, opt-out only via an explicit
// false (loudly labeled), and a zero-coverage metadata table degrades loudly, never silently.
package btreplay

import (
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// pitDB 建一只最小研究库：三只票覆盖"一直在市 / Start 后退市 / Start 时未上市"三档。
// dates=false 时 list_date/delist_date 全空（模拟 §B4-META 回填前的现网形态）。
func pitDB(t *testing.T, withDates bool) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mk := func(ts, name, ld, dd string) map[string]any {
		if !withDates {
			ld, dd = "", ""
		}
		return map[string]any{"ts_code": ts, "name": name, "list_date": ld, "delist_date": dd}
	}
	rows := []map[string]any{
		mk("600000.SH", "一直在市", "20180101", ""),
		mk("600625.SH", "后退市", "20170101", "20220630"),
		mk("600999.SH", "晚上市", "20240101", ""),
	}
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), rows); err != nil {
		t.Fatalf("seed stocks: %v", err)
	}
	return db
}

// runPool 跑一轮最小回放（单内置战法不受库门约束），返回池规模与 poolNote 读数。
func runPool(t *testing.T, o *Options) (int, string) {
	t.Helper()
	if o.DataDir == "" {
		o.DataDir = t.TempDir()
	}
	if o.Strategy == "" {
		o.Strategy = "double_bump"
	}
	sums, _, count, err := o.collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	_ = sums
	return count, o.poolNote
}

func TestPoolPITDefaultOnIncludesDelisted(t *testing.T) {
	db := pitDB(t, true)
	// 缺省（PointInTime=nil）+ Start=2021：600000（在市）与 600625（2022 才退市）都必须在池内
	o := &Options{DB: db, Start: "20210101", End: "20211231"}
	count, note := runPool(t, o)
	if count != 2 {
		t.Fatalf("§B4-PIT 缺省应建时点池 2 只（含 Start 后退市样本），实得 %d", count)
	}
	if !strings.Contains(note, "池口径=时点") {
		t.Fatalf("时点池必须带 poolNote 读数，实得 %q", note)
	}
	// Start=2023：600625 已退市被剔、600999 未上市不进——两头的时点语义都要钉住
	o2 := &Options{DB: db, Start: "20230101", End: "20231231"}
	if count2, _ := runPool(t, o2); count2 != 1 {
		t.Fatalf("Start=2023 时点池应只剩 600000，实得 %d", count2)
	}
}

func TestPoolPITExplicitOffIsLabeled(t *testing.T) {
	db := pitDB(t, true)
	off := false
	o := &Options{DB: db, Start: "20210101", End: "20211231", PointInTime: &off}
	count, note := runPool(t, o)
	if count != 3 { // 旧口径：今天在市的名单全量（含 2022 退市票，因为它今天仍在 stocks 表里）
		t.Fatalf("显式关闭应回全量 StockCodes()=3，实得 %d", count)
	}
	if !strings.Contains(note, "显式关闭") || !strings.Contains(note, "幸存者偏差") {
		t.Fatalf("显式关闭必须把偏差声明抬进 poolNote，实得 %q", note)
	}
}

func TestPoolPITNoMetadataDegradesLoudly(t *testing.T) {
	db := pitDB(t, false) // 元数据全空＝dataload §B4-META 未回填的现网形态
	o := &Options{DB: db, Start: "20210101", End: "20211231"}
	count, note := runPool(t, o)
	if count != 3 {
		t.Fatalf("覆盖为 0 时应退回全量池（可用但带偏差），实得 %d", count)
	}
	// 关键断言是"可见"：降级读数必须点名成因（未回填）与后果（偏差在体），缺一不可
	if !strings.Contains(note, "降级") || !strings.Contains(note, "meta-dates") || !strings.Contains(note, "幸存者偏差在体") {
		t.Fatalf("零覆盖降级必须点名成因与后果，实得 %q", note)
	}
}

func TestPoolPITExplicitCodesListKeepsAuthorOnHook(t *testing.T) {
	db := pitDB(t, true)
	o := &Options{DB: db, Start: "20210101", End: "20211231", Codes: []string{"600000.SH", "600625.SH"}}
	count, note := runPool(t, o)
	if count != 2 {
		t.Fatalf("显式清单应原样生效，实得 %d", count)
	}
	if !strings.Contains(note, "显式清单") || !strings.Contains(note, "不接管") {
		t.Fatalf("显式清单时 PIT 必须声明不代办时点裁决，实得 %q", note)
	}
}
