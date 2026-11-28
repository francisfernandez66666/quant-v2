// replay_inputs_test.go — §REPLAY-INPUTS 2026-11-28：四张新表读写的行为锁。
//
// 钉的是四件在钱路径数据上最容易错的事：
//  1. **NULL ≠ 0**：资金流列缺失必须读成 Has*=false，而不是"值为 0 的一行"。
//     把"没有读数"读成 0，在净流入语义里就是"中性"，方向可能整档错（§FLOW 那批的同型坑）。
//  2. **幂等**：重复回填行数不涨（INSERT OR REPLACE + 复合主键）。
//  3. **序**：StockFlowSeries 返回升序（最旧在前）。给成倒序时窗口会整体错位，
//     而"取到了 5 行"这个数照样对——静态与计数都看不出，只有断言第一行日期能抓住。
//  4. **日尺**：写入边界接受两种形状、库里只留紧凑一种；账龄用日期差而不是字符串差。
//
// English: behavior locks for the four new tables — NULL is not zero, upserts are idempotent,
// the series comes back ascending, and date shapes are normalized at the write boundary only.
package store

import (
	"testing"
)

func TestStockMoneyFlowRoundtripKeepsNullApartFromZero(t *testing.T) {
	db := testDB(t)
	rows := []StockFlowRow{
		{TsCode: "600519.SH", TradeDate: "2026-11-25", MainNet: 100, HasMainNet: true,
			MainPct: 1.5, HasMainPct: true, Amount: 6666.7, HasAmount: true,
			Source: "eastmoney_fflow_relay_seed", FetchedAt: "2026-11-25 15:40:00"},
		// 第二行：上游给了 0（真读数），第三行：上游没给（NULL）。两者必须读得出来不一样。
		{TsCode: "600519.SH", TradeDate: "20261126", MainNet: 0, HasMainNet: true,
			MainPct: 0, HasMainPct: true, Amount: 0, HasAmount: false,
			Source: "eastmoney_fflow_relay_live", FetchedAt: "2026-11-26 15:40:00"},
		{TsCode: "600519.SH", TradeDate: "20261127", HasMainNet: false, HasMainPct: false,
			HasAmount: false, Source: "eastmoney_fflow_relay_live", FetchedAt: "2026-11-27 15:40:00"},
	}
	if n, err := db.UpsertStockMoneyFlow(rows); err != nil || n != 3 {
		t.Fatalf("三行应全落库，得 n=%d err=%v", n, err)
	}

	got, err := db.StockFlowSeries("600519.SH", "20261127", 5)
	if err != nil {
		t.Fatalf("读序列失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应读到 3 行，得 %d", len(got))
	}
	// 序：升序（最旧在前），否则 5 日窗口会整体错位而计数看不出来。
	if got[0].TradeDate != "20261125" || got[2].TradeDate != "20261127" {
		t.Fatalf("必须升序返回，得 %s..%s", got[0].TradeDate, got[2].TradeDate)
	}
	if !got[0].HasMainPct || got[0].MainPct != 1.5 {
		t.Fatalf("第一行 pct 应为 1.5，得 %+v", got[0])
	}
	if got[1].MainPct != 0 || !got[1].HasMainPct {
		t.Fatalf("第二行 pct=0 是**真读数**，必须 Has=true，得 %+v", got[1])
	}
	// pct=0 时成交额推不出来 ⇒ amount 必须是 NULL（Has=false），
	// 而同一行的 main_net=0 / main_pct=0 是**真读数**（Has=true）——两种"0"在同一行里并存，
	// 这正是本表不存比率、只存原子量的理由。
	if got[1].HasAmount {
		t.Fatalf("pct=0 时 amount 不可由上游反推，必须留 NULL，得 %+v", got[1])
	}
	if got[2].HasMainNet || got[2].HasMainPct || got[2].HasAmount {
		t.Fatalf("第三行三列都缺，必须全是 Has=false（绝不折成 0），得 %+v", got[2])
	}
}

func TestStockMoneyFlowIdempotentAndRejectsBadShape(t *testing.T) {
	db := testDB(t)
	rows := []StockFlowRow{{TsCode: "000001.SZ", TradeDate: "20261127", MainNet: 1, HasMainNet: true,
		Source: "eastmoney_fflow_relay_seed", FetchedAt: "x"}}
	if _, err := db.UpsertStockMoneyFlow(rows); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 重复回填：行数不涨（幂等是断点续跑的前提）
	if _, err := db.UpsertStockMoneyFlow(rows); err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	codes, rowsTotal, maxPerCode, err := db.StockFlowDepth()
	if err != nil {
		t.Fatalf("深度读数失败: %v", err)
	}
	if codes != 1 || rowsTotal != 1 || maxPerCode != 1 {
		t.Fatalf("重复回填后必须仍是 1 票 1 行，得 codes=%d rows=%d max=%d", codes, rowsTotal, maxPerCode)
	}
	// 日尺不认识：整批拒绝（不是跳过坏行——跳过会让覆盖率分子从此对不上）
	if _, err := db.UpsertStockMoneyFlow([]StockFlowRow{{TsCode: "000001.SZ", TradeDate: "2026/11/27"}}); err == nil {
		t.Fatal("斜杠日期必须整批拒绝")
	}
	if _, err := db.UpsertStockMoneyFlow([]StockFlowRow{{TsCode: "", TradeDate: "20261127"}}); err == nil {
		t.Fatal("空 ts_code 必须拒绝")
	}
}

func TestStockFlowSeriesRespectsEndDayAndLimit(t *testing.T) {
	db := testDB(t)
	rows := make([]StockFlowRow, 0, 8)
	for _, d := range []string{"20261118", "20261119", "20261120", "20261123", "20261124", "20261125", "20261126", "20261127"} {
		rows = append(rows, StockFlowRow{TsCode: "300750.SZ", TradeDate: d, MainNet: 1, HasMainNet: true,
			Source: "s", FetchedAt: "f"})
	}
	if _, err := db.UpsertStockMoneyFlow(rows); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}
	got, err := db.StockFlowSeries("300750.SZ", "20261125", 5)
	if err != nil {
		t.Fatalf("读序列失败: %v", err)
	}
	// 只取 ≤endDay 的最近 5 行，且不得包含未来日（前视）
	if len(got) != 5 {
		t.Fatalf("应取 5 行，得 %d", len(got))
	}
	// 库里 18/19/20/23/24/25 六行 ≤ 判定日，取最近 5 行 ⇒ 19..25（不是从 18 起）
	if got[0].TradeDate != "20261119" || got[4].TradeDate != "20261125" {
		t.Fatalf("窗口应为 20261119..20261125，得 %s..%s", got[0].TradeDate, got[4].TradeDate)
	}
	for _, r := range got {
		if r.TradeDate > "20261125" {
			t.Fatalf("读到判定日之后的行＝前视: %s", r.TradeDate)
		}
	}
}

func TestFloatShareDailyRejectsNonPositive(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertFloatShareDaily([]FloatShareRow{{TsCode: "600000.SH", TradeDate: "20261127",
		Share: 0, Source: "precalc_seed_v1"}}); err == nil {
		t.Fatal("float_share<=0 必须拒绝（缺股本就不写这一行，写 0 会让占流通市值除出天文数字）")
	}
	if _, err := db.UpsertFloatShareDaily([]FloatShareRow{{TsCode: "600000.SH", TradeDate: "2026-11-27",
		Share: 1.2e9, Source: "precalc_seed_v1"}}); err != nil {
		t.Fatalf("横线日期应在写边界归一后落库: %v", err)
	}
	// 只认当日精确命中，不许拿上一日顶班
	if v, ok := db.FloatShareOn("600000.SH", "20261127"); !ok || v != 1.2e9 {
		t.Fatalf("当日应命中 1.2e9，得 v=%v ok=%v", v, ok)
	}
	if _, ok := db.FloatShareOn("600000.SH", "20261126"); ok {
		t.Fatal("缺日必须 ok=false——股本会变，拿别的日子的值顶班等于换一个分母")
	}
	if _, ok := db.FloatShareOn("999999.SH", "20261127"); ok {
		t.Fatal("不存在的票必须 ok=false")
	}
}

func TestSectorMapAsOfAgeWindow(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertSectorMapDay("2026-11-24", map[string]string{"600519": "白酒", "300750": "电池"}, nil); err != nil {
		t.Fatalf("存档失败: %v", err)
	}
	// 空表拒绝（半张/零张表比没表更坏）
	if _, err := db.UpsertSectorMapDay("20261125", map[string]string{}, nil); err == nil {
		t.Fatal("空映射必须拒绝落库")
	}
	// 全空板块名也拒绝
	if _, err := db.UpsertSectorMapDay("20261125", map[string]string{"600519": "  "}, nil); err == nil {
		t.Fatal("板块名全空必须拒绝")
	}
	got, day, ok := db.SectorMapAsOf("20261125", 5)
	if !ok || day != "20261124" || got["600519"] != "白酒" {
		t.Fatalf("账龄 1 天应命中 20261124 那份，得 day=%s ok=%v map=%v", day, ok, got)
	}
	if _, _, ok := db.SectorMapAsOf("20261220", 5); ok {
		t.Fatal("账龄超窗必须整表弃权，不许拿旧快照冒充当日")
	}
	if _, _, ok := db.SectorMapAsOf("20261101", 5); ok {
		t.Fatal("判定日之前没有快照时必须弃权（不得向后借未来那份＝前视）")
	}
	if _, _, ok := db.SectorMapAsOf("坏日期", 5); ok {
		t.Fatal("日尺不认识必须弃权")
	}
}

func TestLeaderRankDayAndMissing(t *testing.T) {
	db := testDB(t)
	rows := []LeaderRankRow{
		{TsCode: "600519.SH", TradeDate: "20261127", Sector: "白酒", PctChg: 2.1, HasPctChg: true, GainRank: 0.98, HasRank: true},
		{TsCode: "600000.SH", TradeDate: "20261127", Sector: "银行", HasPctChg: false, HasRank: false},
	}
	if n, err := db.UpsertLeaderRankDay(rows); err != nil || n != 2 {
		t.Fatalf("两行应落库，得 n=%d err=%v", n, err)
	}
	if v, ok := db.LeaderRankOn("600519.SH", "2026-11-27"); !ok || v != 0.98 {
		t.Fatalf("分位应命中 0.98，得 v=%v ok=%v", v, ok)
	}
	// 缺值行必须 ok=false——0 在"分位"语义里是"同板块最弱"，方向相反的一条真读数
	if _, ok := db.LeaderRankOn("600000.SH", "20261127"); ok {
		t.Fatal("gain_rank 为 NULL 的行不得读出读数")
	}
	if _, ok := db.LeaderRankOn("600000.SH", "20261120"); ok {
		t.Fatal("无该行时必须 ok=false")
	}
}

func TestIndexDailyCoverageAndMaxFlowDay(t *testing.T) {
	db := testDB(t)
	// 空库：末行日必须是空串（"读不到"），不能是某个假日期或 1970
	if d, err := db.MaxStockFlowDay(); err != nil || d != "" {
		t.Fatalf("空表末行日应为空串，得 d=%q err=%v", d, err)
	}
	if n, err := db.CountStockFlowCodes("20261127"); err != nil || n != 0 {
		t.Fatalf("空表覆盖数应为 0，得 n=%d err=%v", n, err)
	}
	if _, err := db.UpsertStockMoneyFlow([]StockFlowRow{{TsCode: "000001.SZ", TradeDate: "20261127",
		HasMainNet: true, Source: "s", FetchedAt: "f"}}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if d, _ := db.MaxStockFlowDay(); d != "20261127" {
		t.Fatalf("末行日应为 20261127，得 %q", d)
	}
	if n, _ := db.CountStockFlowCodes("20261127"); n != 1 {
		t.Fatalf("覆盖数应为 1，得 %d", n)
	}
	cov, err := db.IndexDailyCoverage()
	if err != nil {
		t.Fatalf("覆盖率读数失败: %v", err)
	}
	if len(cov) != 0 {
		t.Fatalf("新库 index_daily 应为空，得 %v", cov)
	}
}
