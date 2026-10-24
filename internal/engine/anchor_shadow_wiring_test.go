// anchor_shadow_wiring_test.go — §ANCHOR-FIRST W2：影子对照表**落地面**的行为锁。
//
// signalctl 那边锁的是纯函数（两个基准各算一遍）；这里锁的是引擎侧真正会出事的五件事：
//
//	① 首轮真落盘（"有代码无调度"是本项目的缺陷级名词，必须证明 recordAnchorShadow 能把文件写出来）；
//	② 同一 (账本 × 北京日) 只记首轮，第二个账本进**同一日**而不是新开一天；
//	③ 进程重启后盘上已有当日读数 → 不重写、且把当日标记补齐（不再每轮读盘）；
//	④ 落盘失败**不打当日标记**（下一轮重试，宁吵不哑），空表绝不落盘；
//	⑤ 真链路（生产轮次）落位：从 store.RealPosition 的锚点列经 runSellUnifiedJudge 一路到对照
//	   文件——这张表必须真的会在引擎轮次里自己长出来；锚点未回填的持仓要记成"未回填"，
//	   不能折叠成"没有差异"（那会让回填前的表读起来像"切硬不影响任何持仓"）。
//
// English: the engine-side behavior locks for the W2 shadow artifact — the first sample really
// lands, one sample per (book × Beijing day), a restart neither rewrites nor re-reads, a failed
// write retries next round, an empty table never lands, and the live round (RealPosition →
// runSellUnifiedJudge → file) actually produces the artifact with unknown anchors kept as unknown.
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// asRow 造一笔影子对照行：anchor>0 时按锚点已知处理，anchor=0 时是"未回填"形态。
// English: builds one shadow row; anchor 0 is the un-backfilled shape.
func asRow(code string, cost, anchor, high, cur float64, book string) signalctl.AnchorShadowRow {
	return signalctl.CompareAnchor(signalctl.SellInput{
		Code: code, EntryPrice: cost, AnchorPrice: anchor, HighPrice: high, CurPrice: cur,
	}, signalctl.Policy{}, book)
}

func asLiveRows() []signalctl.AnchorShadowRow {
	return []signalctl.AnchorShadowRow{
		asRow("600519.SH", 15.05, 10.05, 14, 14, "live"), // 高位加仓：线位移动 + 判线翻转
		asRow("000001.SZ", 10, 10, 10, 10, "live"),       // 未加仓：两基准重合
	}
}

func readAnchorShadow(t *testing.T, path string) anchorShadowFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读对照文件: %v", err)
	}
	var doc anchorShadowFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析对照文件: %v", err)
	}
	return doc
}

// TestAnchorShadowFirstSampleLandsAndDedups 首轮落盘、当日去重、第二账本并入同日。
// English: first sample lands, same-day same-book is deduped, a second book joins the same day.
func TestAnchorShadowFirstSampleLandsAndDedups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor_shadow.json")
	e := &Engine{anchorShadowPath: path}
	now := time.Now()

	e.recordAnchorShadow("acct1", "live", asLiveRows(), now)
	doc := readAnchorShadow(t, path)
	if len(doc.Days) != 1 || len(doc.Days[0].Books) != 1 {
		t.Fatalf("首轮应恰好一日一账本: %+v", doc.Days)
	}
	entry := doc.Days[0].Books["live"]
	if entry == nil || entry.Summary.Rows != 2 || len(entry.Rows) != 2 {
		t.Fatalf("live 账本读数缺失或行数不符: %+v", entry)
	}
	// 总账与明细同批：翻转数必须真等于明细里的翻转行数，不是各自算一遍。
	liveShift, liveDiv, liveUnknown := 0, 0, 0
	for _, r := range entry.Rows {
		if r.StopPriceByAnchor != r.StopPriceByCost {
			liveShift++
		}
		if r.LineChanged {
			liveDiv++
		}
		if !r.AnchorKnown {
			liveUnknown++
		}
	}
	if entry.Summary.Shifted != liveShift || entry.Summary.Diverged != liveDiv || entry.Summary.Unknown != liveUnknown {
		t.Fatalf("总账(%d/%d/%d)与明细(%d/%d/%d)不同批", entry.Summary.Shifted, entry.Summary.Diverged, entry.Summary.Unknown, liveShift, liveDiv, liveUnknown)
	}
	if entry.Summary.Day != doc.Days[0].Day {
		t.Fatalf("日键必须来自北京日历: 明细 %q vs 文件 %q", entry.Summary.Day, doc.Days[0].Day)
	}
	if doc.Account != "acct1" || doc.KeepDays != anchorShadowKeepDays {
		t.Fatalf("账号/保留天数落位异常: %+v", doc)
	}

	// 同日同账本再次调用（内容更"新"）：绝不改写当日首轮读数。
	e.recordAnchorShadow("acct1", "live", []signalctl.AnchorShadowRow{asRow("600000.SH", 20, 8, 20, 20, "live")}, now.Add(5*time.Second))
	doc2 := readAnchorShadow(t, path)
	if len(doc2.Days) != 1 || len(doc2.Days[0].Books) != 1 || doc2.Days[0].Books["live"].Summary.Rows != 2 {
		t.Fatalf("当日该账本只记首轮, got %+v", doc2.Days)
	}
	if doc2.Days[0].Books["live"].Rows[0].Code != "600519.SH" {
		t.Fatalf("首轮读数被后续轮覆盖: %+v", doc2.Days[0].Books["live"].Rows)
	}

	// 同日第二个账本：并入同一天的 Books，而不是新开一天。
	e.recordAnchorShadow("acct1", "paper", []signalctl.AnchorShadowRow{asRow("300750.SZ", 12, 12, 12, 12, "paper")}, now.Add(10*time.Second))
	doc3 := readAnchorShadow(t, path)
	if len(doc3.Days) != 1 || len(doc3.Days[0].Books) != 2 {
		t.Fatalf("第二账本应并入同日: days=%d books=%d", len(doc3.Days), len(doc3.Days[0].Books))
	}
	if doc3.Days[0].Books["paper"].Summary.ByBook["paper"] != 1 {
		t.Fatalf("分账本计数应按 book 参数打标: %+v", doc3.Days[0].Books["paper"].Summary.ByBook)
	}
}

// TestAnchorShadowRestartDoesNotRewriteOrReread 进程重启后盘上已有当日读数：内容逐字节不变，
// 且当日标记被补齐（后续轮不再读盘）。
// English: after a restart, an existing same-day reading is left byte-identical and the day is
// marked so later rounds stop reading the disk.
func TestAnchorShadowRestartDoesNotRewriteOrReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor_shadow.json")
	now := time.Now()
	(&Engine{anchorShadowPath: path}).recordAnchorShadow("acct1", "live", asLiveRows(), now)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读基线: %v", err)
	}

	e2 := &Engine{anchorShadowPath: path}
	e2.recordAnchorShadow("acct1", "live", asLiveRows(), now.Add(time.Minute))
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("复读: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("重启后当日读数被改写")
	}
	if !e2.anchorShadowSampled["live|"+cntime.DayOf(now)] {
		t.Fatal("盘上已有当日读数时必须补齐当日标记，否则 5 秒轮每轮都读盘")
	}
}

// TestAnchorShadowWriteFailureRetries 落盘失败不得打当日标记：下一轮（路径恢复后）必须真写进去。
// English: a failed write must not mark the day — the next round has to land it.
func TestAnchorShadowWriteFailureRetries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	e := &Engine{anchorShadowPath: dir} // 目标是目录：AtomicWrite 必然失败
	e.recordAnchorShadow("acct1", "live", asLiveRows(), now)
	if e.anchorShadowSampled["live|"+cntime.DayOf(now)] {
		t.Fatal("写失败却打了当日标记＝这一天的证据永久丢失")
	}
	good := filepath.Join(dir, "anchor_shadow.json")
	e.anchorShadowPath = good
	e.recordAnchorShadow("acct1", "live", asLiveRows(), now.Add(5*time.Second))
	if _, err := os.ReadFile(good); err != nil {
		t.Fatalf("失败后下一轮未重试落盘: %v", err)
	}
	if doc := readAnchorShadow(t, good); len(doc.Days) != 1 || doc.Days[0].Books["live"].Summary.Rows != 2 {
		t.Fatalf("重试后的读数不完整: %+v", doc.Days)
	}
}

// TestAnchorShadowEmptyTableNeverLands 空表不落盘："0 行"分不清今天全空仓与持仓快照还没到，
// 两种含义在 Q10 签认材料里正好相反，所以宁可缺行。
// English: an empty table never lands — "0 rows" cannot tell flat-today from snapshot-not-in.
func TestAnchorShadowEmptyTableNeverLands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor_shadow.json")
	e := &Engine{anchorShadowPath: path}
	e.recordAnchorShadow("acct1", "live", nil, time.Now())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("空表不应落盘, err=%v", err)
	}
	if len(e.anchorShadowSampled) != 0 {
		t.Fatalf("空表不应占用当日名额: %v", e.anchorShadowSampled)
	}
	// 空表之后的首轮非空读数必须照常落盘（没被"先来的空轮"顶掉）。
	e.recordAnchorShadow("acct1", "live", asLiveRows(), time.Now())
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("空表轮顶掉了首非空轮: %v", err)
	}
}

// TestAnchorShadowKeepDaysTrim 按日裁剪保新：超过保留天数时截掉最旧的一天，最新一天必在表内。
// English: day trimming keeps the newest samples.
func TestAnchorShadowKeepDaysTrim(t *testing.T) {
	doc := &anchorShadowFile{KeepDays: anchorShadowKeepDays}
	first := time.Now().AddDate(0, 0, -anchorShadowKeepDays).UTC()
	for i := 0; i <= anchorShadowKeepDays; i++ {
		day := cntime.DayOf(first.AddDate(0, 0, i))
		if !doc.setBookEntry(day, "live", &anchorShadowBookEntry{Summary: signalctl.AnchorShadowSummary{Day: day}}) {
			t.Fatalf("日 %s 重复", day)
		}
	}
	if len(doc.Days) != anchorShadowKeepDays {
		t.Fatalf("保留天数应为 %d, got %d", anchorShadowKeepDays, len(doc.Days))
	}
	lastDay := cntime.DayOf(first.AddDate(0, 0, anchorShadowKeepDays))
	if doc.Days[len(doc.Days)-1].Day != lastDay {
		t.Fatalf("最新一天被截掉了: %q vs %q", doc.Days[len(doc.Days)-1].Day, lastDay)
	}
	if _, ok := doc.Days[0].Books["live"]; !ok {
		t.Fatal("裁剪后仍应是完整日条目")
	}
}

// TestAnchorShadowLiveRoundProducesArtifact W2 的调度证明：一张对照表必须能从生产轮次里自己长出来。
// 链路 = store.RealPosition.anchor_price → sellProbeRow.AnchorPrice → signalctl.CompareAnchor →
// recordAnchorShadow → <acctDir>/anchor_shadow.json。任一环节没接线，本用例即红
// （"有代码无调度"是本项目反复出现的缺陷级名词，这条就是它的反证）。
// English: the scheduling proof — the artifact must be produced by a real live round, and an
// un-backfilled anchor must show up as unknown rather than as "no divergence".
func TestAnchorShadowLiveRoundProducesArtifact(t *testing.T) {
	// 甲：高位加仓的标准形态（成本被抬到 15.05、锚点仍是首买价 10.05）。
	pathA := filepath.Join(t.TempDir(), "anchor_shadow.json")
	eA := shadowTestEnv(t, "shadow")
	eA.anchorShadowPath = pathA
	eA.runSellUnifiedJudge("u_1",
		[]store.RealPosition{{TsCode: "600519.SH", Name: "锚点链路", Qty: 200, CostPrice: 15.05, AnchorPrice: 10.05, HighestPrice: 14}},
		nil, map[string]*data.StockInfo{"600519": {Price: 14}}, nil, nil, nil, eA.sellUnifiedModeEngine())
	docA := readAnchorShadow(t, pathA)
	if len(docA.Days) != 1 {
		t.Fatalf("生产轮次应产出一日读数: %+v", docA.Days)
	}
	entryA := docA.Days[0].Books["live"]
	if entryA == nil || len(entryA.Rows) != 1 {
		t.Fatalf("live 账本读数缺行: %+v", docA.Days[0].Books)
	}
	rA := entryA.Rows[0]
	if rA.CostPrice != 15.05 || rA.AnchorPrice != 10.05 || !rA.AnchorKnown {
		t.Fatalf("锚点没走通实盘装配: %+v", rA)
	}
	if entryA.Summary.Shifted != 1 || entryA.Summary.Diverged != 1 || entryA.Summary.Unknown != 0 {
		t.Fatalf("总账应为 线位移动1/翻转1/未回填0: %+v", entryA.Summary)
	}

	// 乙：存量未回填（anchor=0）——必须登记成"未回填"，绝不允许折叠成"与成本相同、无差异"。
	pathB := filepath.Join(t.TempDir(), "anchor_shadow.json")
	eB := shadowTestEnv(t, "shadow")
	eB.anchorShadowPath = pathB
	eB.runSellUnifiedJudge("u_1",
		[]store.RealPosition{{TsCode: "600520.SH", Name: "存量未回填", Qty: 100, CostPrice: 15.05, HighestPrice: 14}},
		nil, map[string]*data.StockInfo{"600520": {Price: 14}}, nil, nil, nil, eB.sellUnifiedModeEngine())
	docB := readAnchorShadow(t, pathB)
	entryB := docB.Days[0].Books["live"]
	if entryB == nil || len(entryB.Rows) != 1 {
		t.Fatalf("未回填持仓也应进表（否则回填进度无从统计）: %+v", docB.Days)
	}
	if entryB.Rows[0].AnchorKnown || entryB.Rows[0].StopPriceByAnchor != 0 {
		t.Fatalf("未回填行的锚点侧必须是「算不出来」而不是抄成本: %+v", entryB.Rows[0])
	}
	if entryB.Summary.Unknown != 1 || entryB.Summary.Diverged != 0 {
		t.Fatalf("未回填计数必须单列（1），差异不得被算成 0 行差异=无影响: %+v", entryB.Summary)
	}
}
