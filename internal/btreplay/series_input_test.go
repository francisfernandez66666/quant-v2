// series_input_test.go — §REPLAY-INPUTS 2026-11-28（档 1）：回放侧整段装配的接线锁。
//
// 钉的是"接上了没有"和"接错形状没有"，不是打分本身（打分在 strategies/factor 那侧测）：
//
//	① 同一只票跨多日只装配一次（这是 v2 review #1 的数量级诉求，也是内存取向的关键：
//	   按票累积缓存会到 1.4 GB，而 researchd 只有 500M 配额）；
//	② 装配失败的票**不每天重试**（坏票 900 天 × 37 规则 = 三万次无效 SQL）；
//	③ as-of 查不到就是 -1 ⇒ 调用方回落旧路径，绝不"用序列最后一根"顶班（那是前视）；
//	④ Trigger 真的走整段路径（hits 计数动），且 src=nil 时不 panic（既有测试/旧调用方形态）。
//
// English: locks the replay-side wiring — one assembly per stock, no retry storm on bad
// stocks, miss means fall-back (never "use the last bar"), and a nil source stays safe.
package btreplay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategies/factor"
)

// venueDay 第 i 个夹具日的时刻（seed 与断言两处必须用同一个基准，否则日期对不上是夹具毛病）。
// 取 UTC 零点：经 cntime 折算到北京时区是当日 08:00，日历日不变——两处换算结果一致才对得上。
func venueDay(i int) time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i) }

// newSeriesTestFactorStrategy 一条只吃价量的启用规则。
// 刻意不用情绪因子：本用例验的是"整段路径接上了没有"，与哪条腿参与无关。
func newSeriesTestFactorStrategy() *factor.FactorStrategy {
	fs := factor.New()
	fs.SetRules([]*factor.ActiveRule{{
		ID: "fac_series_wire", Name: "接线用例", CandID: 990001,
		Rule: factor.Rule{
			Factors:      []string{"Mom20", "VolRatio5"},
			Weights:      map[string]float64{"Mom20": 0.5, "VolRatio5": 0.5},
			Directions:   map[string]int{"Mom20": 1, "VolRatio5": 1},
			BuyThreshold: 1, // 门槛放到最低：本用例要的是"走到了打分"，不是"真的入场"
		},
	}})
	return fs
}

// seedSeriesDB 造一只票 n 个交易日的日线（够 Assemble 出序列），返回紧凑日期清单。
func seedSeriesDB(t *testing.T, code string, n int) (*store.DB, []string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "series_in.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	days := make([]string, 0, n)
	rows := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		d := cntime.DayCompactOf(venueDay(i))
		days = append(days, d)
		p := float64(10 + i)
		rows = append(rows, map[string]any{
			"ts_code": code, "trade_date": d, "open": p, "high": p + 0.5, "low": p - 0.5,
			"close": p, "pre_close": p - 1, "change": 1, "pct_chg": 1.0,
			"vol": 1000 + float64(i), "amount": (1000 + float64(i)) * p, "source": "baostock",
		})
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), rows); err != nil {
		t.Fatalf("写日线失败: %v", err)
	}
	return db, days
}

func TestSeriesSourceAssemblesOncePerStock(t *testing.T) {
	const code = "600000.SH"
	db, days := seedSeriesDB(t, code, 40)
	start, end := days[0], days[len(days)-1]
	src := newSeriesSource(db, start, end, "")
	if src == nil {
		t.Fatal("db 在位时不应返回 nil")
	}
	// 同一只票反复换入（回放每天注入一次）⇒ 只装配一次
	first := src.useStock(code)
	if first == nil || first.Len() != len(days) {
		t.Fatalf("应装配出 %d 根序列，得 %+v", len(days), first)
	}
	for i := 0; i < 15; i++ {
		if got := src.useStock(code); got != first {
			t.Fatal("同票必须命中同一份序列对象（key 只能是代码，不能混进日期）")
		}
	}
	if src.assemblies != 1 {
		t.Fatalf("同票 16 次调用应只装配 1 次，得 %d", src.assemblies)
	}
	// 换到库里没有的票 ⇒ nil + 计一次 fallback，且**不重复重试**
	if got := src.useStock("999999.SH"); got != nil {
		t.Fatal("无行情票应返回 nil（调用方回落旧路径）")
	}
	if src.assembleFallback != 1 {
		t.Fatalf("首次失败应计 assemble_fallback 一次，得 %d", src.assembleFallback)
	}
	for i := 0; i < 5; i++ {
		src.useStock("999999.SH")
	}
	if src.assembleFallback != 1 || src.assemblies != 2 {
		t.Fatalf("坏票不得每天重试，得 fallback=%d assemblies=%d", src.assembleFallback, src.assemblies)
	}
	// 空 code / nil 接收器都要安全
	if src.useStock("") != nil {
		t.Fatal("空代码应返回 nil")
	}
	var nilSrc *seriesSource
	if nilSrc.useStock(code) != nil || nilSrc.asIndexOf(venueDay(0)) != -1 {
		t.Fatal("nil seriesSource 必须返回 nil/-1 而不是 panic")
	}
}

func TestSeriesSourceAsIndexOfNeverUsesFuture(t *testing.T) {
	const code = "600001.SH"
	db, days := seedSeriesDB(t, code, 30)
	src := newSeriesSource(db, days[0], days[len(days)-1], "")
	if src.useStock(code) == nil {
		t.Fatal("装配失败，用例前提不成立")
	}
	if got := src.asIndexOf(venueDay(10)); got != 10 {
		t.Fatalf("第 11 个交易日应映射到索引 10，得 %d", got)
	}
	if got := src.asIndexOf(venueDay(len(days) + 5)); got != -1 {
		// 判定日在序列之外（区间外/未来）必须 -1 ⇒ 回落旧路径。
		// 若这里返回最后一根索引，等于把区间末尾的读数给中间某天打分（前视）。
		t.Fatalf("序列外的判定日必须返回 -1，得 %d", got)
	}
	// 窗口**之前**的日子也必须 -1，而不是钳到第一根：
	// 钳到序列开头等于"用区间首日的读数给更早那天打分"，与钳到末尾同为前视/错日形态。
	if got := src.asIndexOf(venueDay(0).AddDate(0, 0, -3)); got != -1 {
		t.Fatalf("早于序列起点必须 -1，得 %d", got)
	}
}

func TestRuleEvalAdapterUsesSeriesPathAndSurvivesNilSource(t *testing.T) {
	const code = "600002.SH"
	db, days := seedSeriesDB(t, code, 60)
	src := newSeriesSource(db, days[0], days[len(days)-1], "")

	mk := func() *ruleEvalAdapter {
		fs := newSeriesTestFactorStrategy()
		return &ruleEvalAdapter{name: "fac_test", ruleID: "fac_test", fs: fs, finaCode: code}
	}
	klines := make([]data.KLine, 0, len(days))
	for i, d := range days {
		_ = d
		p := float64(10 + i)
		klines = append(klines, data.KLine{Date: venueDay(i), Open: p, High: p + 0.5,
			Low: p - 0.5, Close: p, Volume: 1000 + float64(i), Amount: (1000 + float64(i)) * p})
	}

	// ① 接上来源 ⇒ 走整段路径，hits 计数动
	ad := mk()
	ad.setSeriesScope(src, code)
	if ad.series == nil {
		t.Fatal("setSeriesScope 后应持有该票序列")
	}
	if _, ok := ad.Trigger(klines, 9.5, 0); !ok {
		t.Log("Trigger 未入场属合法结果（门槛未过），本用例只验走没走整段路径")
	}
	if src.hits == 0 {
		t.Fatal("已装配序列却一次都没走整段路径＝接线没生效（本批目的落空）")
	}

	// ② 没接来源（src=nil）⇒ 回落旧路径且不 panic
	ad2 := mk()
	ad2.setSeriesScope(nil, code)
	if ad2.series != nil {
		t.Fatal("src 为 nil 时 series 必须为空")
	}
	if _, ok := ad2.Trigger(klines, 9.5, 0); ok {
		t.Log("旧路径同样可以入场，这里只验不炸")
	}

	// ③ 换票后必须丢掉上一只票的序列（跨股串台，§B6 同族）
	if got := src.useStock("600003.SH"); got != nil {
		t.Fatalf("换到无数据的票却仍持有上一只票的序列＝串台，得 %+v", got)
	}
}

// TestMarketNoteReportsStockFlowReadout §5.4：报告头必须自己说清"个股资金流这份长期数据到哪了"。
//
// 三个方向都钉住：
//   - 空表 ⇒ day=none、codes=0（**不写"今天"**，也不写覆盖率 100%）；
//   - 有行 ⇒ day 是该数据日、codes 是分子；
//   - 拿不到库路径（dbPath 空）⇒ gate=none，不拿 pass 顶班。
func TestMarketNoteReportsStockFlowReadout(t *testing.T) {
	const code = "600000.SH"
	db, days := seedSeriesDB(t, code, 10)
	start, end := days[0], days[len(days)-1]

	// ① 空表
	empty := newSeriesSource(db, start, end, "")
	if empty == nil {
		t.Fatal("db 在位时不应返回 nil")
	}
	note := empty.marketNote()
	if !strings.Contains(note, "stock_flow day=none") {
		t.Fatalf("空表必须如实写 day=none，得: %s", note)
	}
	if !strings.Contains(note, "gate=none") {
		t.Fatalf("拿不到数据目录时 gate 必须是 none（不拿 pass 顶班），得: %s", note)
	}

	// ② 写一行资金流（数据日取序列最后一天）
	day := days[len(days)-1]
	if _, err := db.UpsertStockMoneyFlow([]store.StockFlowRow{{
		TsCode: code, TradeDate: day, MainNet: 1234, HasMainNet: true,
		MainPct: 0.5, HasMainPct: true, Amount: 246800, HasAmount: true,
		Source: "eastmoney_fflow_relay_seed", FetchedAt: "2026-01-10T16:00:00+08:00",
	}}); err != nil {
		t.Fatalf("写资金流失败: %v", err)
	}
	withRows := newSeriesSource(db, start, end, filepath.Join(t.TempDir(), "trading.db"))
	note = withRows.marketNote()
	if !strings.Contains(note, "stock_flow day="+day) {
		t.Fatalf("报告头应带最新数据日 %s，得: %s", day, note)
	}
	if !strings.Contains(note, "codes=1") {
		t.Fatalf("该日 1 只票有行，报告头应写 codes=1，得: %s", note)
	}
	// 数据目录存在但没有标记 ⇒ 交易日会落 wait/timeout、休市日落 pass，三者都是合法读数，
	// 这里只锁"不是一个空串"（具体钟点判定在 scheduler 侧单独锁）。
	if !strings.Contains(note, "gate=") || strings.HasSuffix(note, "gate=") {
		t.Fatalf("gate 读数不得为空，得: %s", note)
	}
}
