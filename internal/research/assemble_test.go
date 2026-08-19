// 装配端到端测试：临时库 + 手工插入行情/财务，验证点对时对齐与 SUE 接线。
// English: Assembly end-to-end test: temp DB + manually inserted quotes/financials, verifying point-in-time alignment and SUE wiring.
package research

import (
	"math"
	"testing"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// dailyCols/dailyRows 插入单日行情（hfq 恒等因子，原始价=复权价）。
// English: dailyCols/dailyRows inserts a single day of quotes (hfq identity factor, raw price = adjusted price).
func dailyRows(ts, date string, close float64, vol float64) map[string]any {
	return map[string]any{
		"ts_code": ts, "trade_date": date, "open": close - 0.1,
		"high": close + 0.5, "low": close - 0.2, "close": close,
		"vol": vol, "amount": vol * close * 100,
	}
}

// seedDB 建临时库并插入单只股票（000001.SZ）的行情/财务数据。
// 行情：5 个交易日，close 10→14；daily_basic 覆盖 4 根（缺 20210105）；
// 财务 2020 Q1-Q4 与 2021 Q1：ann_date 全部晚于行情区间（最早 2020-04-30 仍早于
// 区间起点？否——2020 年报告 ann 在 2020，但 2021 区间 [20210104,20210108] 内
// 最新已披露为 2020Q4(ann=20210430 越界) → 用 2020Q3(ann=20201031)。验证点对时跳步。
// English: seedDB creates a temp DB and inserts quotes/financials for a single stock (000001.SZ).
// Quotes: 5 trading days, close 10→14; daily_basic covers 4 bars (missing 20210105).
// Financials 2020 Q1-Q4 and 2021 Q1: all ann_dates fall outside the quote window (earliest 2020-04-30 is still
// before the window start? No — 2020 reports are announced in 2020, but within the 2021 window [20210104,20210108]
// the latest disclosed is 2020Q4 (ann=20210430 out of range) → so 2020Q3 (ann=20201031) is used. Verifies point-in-time skipping.
func seedDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dates := []string{"20210104", "20210105", "20210106", "20210107", "20210108"}
	closes := []float64{10, 11, 12, 13, 14}

	dailyCols := []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	var dailyRowsList []map[string]any
	for i, d := range dates {
		dailyRowsList = append(dailyRowsList, dailyRows("000001.SZ", d, closes[i], 1000))
	}
	if _, err := db.InsertRows("daily", dailyCols, dailyRowsList); err != nil {
		t.Fatalf("插入 daily 失败: %v", err)
	}

	adjCols := []string{"ts_code", "trade_date", "adj_factor"}
	var adjRows []map[string]any
	for _, d := range dates {
		adjRows = append(adjRows, map[string]any{"ts_code": "000001.SZ", "trade_date": d, "adj_factor": 1.0})
	}
	if _, err := db.InsertRows("adj_factor", adjCols, adjRows); err != nil {
		t.Fatalf("插入 adj_factor 失败: %v", err)
	}

	basicCols := []string{"ts_code", "trade_date", "turnover_rate", "volume_ratio", "pe_ttm",
		"pb", "ps_ttm", "pcf_ttm", "dv_ttm", "total_share", "total_mv", "circ_mv", "is_st"}
	var basicRows []map[string]any
	// 覆盖 0、2、3、4 四根（缺第 1 根 20210105）
	// English: Covers bars 0, 2, 3, 4 (missing the 1st bar 20210105).
	for _, i := range []int{0, 2, 3, 4} {
		basicRows = append(basicRows, map[string]any{
			"ts_code": "000001.SZ", "trade_date": dates[i], "turnover_rate": 1.0,
			"volume_ratio": 1.1, "pe_ttm": 12.0 + float64(i), "pb": 1.2, "ps_ttm": 2.0,
			"pcf_ttm": 3.0, "dv_ttm": 0.5, "total_share": 1e9, "total_mv": 1e6,
			"circ_mv": 8e5, "is_st": 0,
		})
	}
	if _, err := db.InsertRows("daily_basic", basicCols, basicRows); err != nil {
		t.Fatalf("插入 daily_basic 失败: %v", err)
	}

	// 财务 fina_indicator：ann_date 交错，验证点对时取"ann ≤ 当日 的最新报告"
	// English: Financials fina_indicator: interleaved ann_dates, verifying point-in-time picks the latest report with ann ≤ current day.
	finaCols := []string{"ts_code", "end_date", "ann_date", "roe", "grossprofit_margin",
		"netprofit_margin", "debt_to_assets", "yoy_or", "yoy_net_profit"}
	finaRows := []map[string]any{
		{"ts_code": "000001.SZ", "end_date": "20200331", "ann_date": "20200430", "roe": 2.0, "grossprofit_margin": 30.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": nil},
		{"ts_code": "000001.SZ", "end_date": "20200630", "ann_date": "20200731", "roe": 4.0, "grossprofit_margin": 32.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 10.0},
		{"ts_code": "000001.SZ", "end_date": "20200930", "ann_date": "20201031", "roe": 6.0, "grossprofit_margin": 33.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 11.0},
		{"ts_code": "000001.SZ", "end_date": "20201231", "ann_date": "20210430", "roe": 8.0, "grossprofit_margin": 34.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 12.0},
		{"ts_code": "000001.SZ", "end_date": "20210331", "ann_date": "20210430", "roe": 2.5, "grossprofit_margin": 31.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 5.0},
	}
	if _, err := db.InsertRows("fina_indicator", finaCols, finaRows); err != nil {
		t.Fatalf("插入 fina_indicator 失败: %v", err)
	}

	// 利润表：2020 Q1-Q4 + 2021 Q1 累计归母净利（单季 100/200/300/400 / 110）
	// English: Income statement: 2020 Q1-Q4 + 2021 Q1 cumulative net profit attributable to parent (single-quarter 100/200/300/400 / 110).
	incomeCols := []string{"ts_code", "end_date", "n_income_attr_p", "revenue"}
	incomeRows := []map[string]any{
		{"ts_code": "000001.SZ", "end_date": "20200331", "n_income_attr_p": 100.0, "revenue": 500.0},
		{"ts_code": "000001.SZ", "end_date": "20200630", "n_income_attr_p": 300.0, "revenue": 1100.0},
		{"ts_code": "000001.SZ", "end_date": "20200930", "n_income_attr_p": 600.0, "revenue": 1800.0},
		{"ts_code": "000001.SZ", "end_date": "20201231", "n_income_attr_p": 1000.0, "revenue": 2500.0},
		{"ts_code": "000001.SZ", "end_date": "20210331", "n_income_attr_p": 110.0, "revenue": 520.0},
	}
	if _, err := db.InsertRows("income", incomeCols, incomeRows); err != nil {
		t.Fatalf("插入 income 失败: %v", err)
	}
	return db
}

func TestAssemblePointInTime(t *testing.T) {
	db := seedDB(t)
	s, err := Assemble(db, "000001.SZ", "20210104", "20210108")
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if s.Len() != 5 {
		t.Fatalf("期望 5 根，得 %d", s.Len())
	}
	// 收盘 hfq 递增
	// English: Close hfq is increasing.
	if s.CloseHfq[4] != 14 {
		t.Fatalf("期望最后一根收盘 14，得 %v", s.CloseHfq[4])
	}
	// 点对时：区间内最新已披露 = 2020Q3（ann=20201031），2020Q4 ann=20210430 越界
	// English: Point-in-time: latest disclosed within window = 2020Q3 (ann=20201031); 2020Q4 ann=20210430 is out of range.
	for i := range s.Dates {
		if s.Roe[i] != 6.0 {
			t.Fatalf("日期 %s 期望 ROE=6（2020Q3），得 %v", s.Dates[i], s.Roe[i])
		}
		if s.GrossMargin[i] != 33.0 {
			t.Fatalf("日期 %s 期望毛利率 33，得 %v", s.Dates[i], s.GrossMargin[i])
		}
	}
	// SUE：区间内最新报告 2020Q3（end 20200930）单季净利同比 = (300-400)/400 = -0.25
	// English: SUE: latest report in window 2020Q3 (end 20200930) single-quarter NI YoY = (300-400)/400 = -0.25.
	for i := range s.Dates {
		if math.Abs(s.SingleQuarterNIYoy[i]+0.25) > 1e-9 {
			t.Fatalf("日期 %s 期望 SUE=-0.25，得 %v", s.Dates[i], s.SingleQuarterNIYoy[i])
		}
	}
	// daily_basic 缺第 2 根（20210105）→ PeTTM NaN；其余有值
	// English: daily_basic is missing the 2nd bar (20210105) → PeTTM is NaN; others have values.
	if !isNaN(s.PeTTM[1]) {
		t.Fatalf("20210105 期望 PeTTM NaN（缺 daily_basic），得 %v", s.PeTTM[1])
	}
	if s.PeTTM[0] != 12 || s.PeTTM[4] != 16 {
		t.Fatalf("PeTTM 期望 12/16，得 %v/%v", s.PeTTM[0], s.PeTTM[4])
	}
	if s.PcfTTM[0] != 3.0 || s.TotalShare[0] != 1e9 {
		t.Fatalf("PcfTTM/TotalShare 装配错误: %v/%v", s.PcfTTM[0], s.TotalShare[0])
	}
}

func TestAssembleMissingSeries(t *testing.T) {
	db := seedDB(t)
	if _, err := Assemble(db, "000001.SZ", "20230101", "20231231"); err == nil {
		t.Fatal("空区间期望报错")
	}
	if _, err := Assemble(db, "600000.SH", "20210104", "20210108"); err == nil {
		t.Fatal("无此股期望报错")
	}
}

// TestReportEndToEnd 全链路冒烟：2 只股票 → 面板 → Summarize → JSON/HTML 渲染。
// English: TestReportEndToEnd full-chain smoke test: 2 stocks → panels → Summarize → JSON/HTML rendering.
func TestReportEndToEnd(t *testing.T) {
	db := seedDB(t)
	// 第二只股票 600000.SH：closes 20→24，其余字段复用（财务同构但 ROE 更高）
	// English: Second stock 600000.SH: closes 20→24, other fields reused (isomorphic financials but higher ROE).
	dates := []string{"20210104", "20210105", "20210106", "20210107", "20210108"}
	closes := []float64{20, 21, 22, 23, 24}
	var dailyRowsList []map[string]any
	for i, d := range dates {
		dailyRowsList = append(dailyRowsList, dailyRows("600000.SH", d, closes[i], 2000))
	}
	if _, err := db.InsertRows("daily",
		[]string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"},
		dailyRowsList); err != nil {
		t.Fatalf("插入 daily(600000) 失败: %v", err)
	}
	var adjRows []map[string]any
	for _, d := range dates {
		adjRows = append(adjRows, map[string]any{"ts_code": "600000.SH", "trade_date": d, "adj_factor": 1.0})
	}
	if _, err := db.InsertRows("adj_factor",
		[]string{"ts_code", "trade_date", "adj_factor"}, adjRows); err != nil {
		t.Fatalf("插入 adj_factor(600000) 失败: %v", err)
	}
	var basicRows []map[string]any
	for _, i := range []int{0, 2, 3, 4} {
		basicRows = append(basicRows, map[string]any{
			"ts_code": "600000.SH", "trade_date": dates[i], "turnover_rate": 1.0,
			"volume_ratio": 1.1, "pe_ttm": 8.0 + float64(i), "pb": 1.1, "ps_ttm": 1.0,
			"pcf_ttm": 2.0, "dv_ttm": 0.7, "total_share": 2e9, "total_mv": 2e6,
			"circ_mv": 1.5e6, "is_st": 0,
		})
	}
	if _, err := db.InsertRows("daily_basic",
		[]string{"ts_code", "trade_date", "turnover_rate", "volume_ratio", "pe_ttm",
			"pb", "ps_ttm", "pcf_ttm", "dv_ttm", "total_share", "total_mv", "circ_mv", "is_st"},
		basicRows); err != nil {
		t.Fatalf("插入 daily_basic(600000) 失败: %v", err)
	}
	// 财务复用同构数据（ROE 不同）
	// English: Financials reuse isomorphic data (different ROE).
	finaCols := []string{"ts_code", "end_date", "ann_date", "roe", "grossprofit_margin",
		"netprofit_margin", "debt_to_assets", "yoy_or", "yoy_net_profit"}
	finaRows := []map[string]any{
		{"ts_code": "600000.SH", "end_date": "20200331", "ann_date": "20200430", "roe": 3.0, "grossprofit_margin": 30.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": nil},
		{"ts_code": "600000.SH", "end_date": "20200630", "ann_date": "20200731", "roe": 5.0, "grossprofit_margin": 32.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 10.0},
		{"ts_code": "600000.SH", "end_date": "20200930", "ann_date": "20201031", "roe": 9.0, "grossprofit_margin": 33.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 11.0},
		{"ts_code": "600000.SH", "end_date": "20201231", "ann_date": "20210430", "roe": 12.0, "grossprofit_margin": 34.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 12.0},
		{"ts_code": "600000.SH", "end_date": "20210331", "ann_date": "20210430", "roe": 3.5, "grossprofit_margin": 31.0, "netprofit_margin": 10.0, "debt_to_assets": 40.0, "yoy_or": 5.0, "yoy_net_profit": 5.0},
	}
	if _, err := db.InsertRows("fina_indicator", finaCols, finaRows); err != nil {
		t.Fatalf("插入 fina_indicator(600000) 失败: %v", err)
	}

	defs := factor.All()
	panels, err := BuildPanels(db, []string{"000001.SZ", "600000.SH"}, "20210104", "20210108", defs)
	if err != nil {
		t.Fatalf("构建面板失败: %v", err)
	}
	if len(panels) != 2 {
		t.Fatalf("期望 2 面板，得 %d", len(panels))
	}
	var reports []*FactorReport
	for _, d := range defs {
		reports = append(reports, Summarize(panels, d, "20210104", "20210108", 1, 2, 2))
	}
	js, err := JSONReport(reports)
	if err != nil {
		t.Fatalf("JSON 渲染失败: %v", err)
	}
	if len(js) == 0 || !bytesContains(js, []byte("LnMktCap")) {
		t.Fatal("JSON 缺少因子数据")
	}
	html, err := HTMLReport(reports)
	if err != nil {
		t.Fatalf("HTML 渲染失败: %v", err)
	}
	for _, want := range []string{"多因子验证报告", "LnMktCap", "净利同比"} {
		if !bytesContains(html, []byte(want)) {
			t.Fatalf("HTML 缺少 %q", want)
		}
	}
}

func bytesContains(b, sub []byte) bool {
	return len(sub) == 0 || (len(b) >= len(sub) && indexBytes(b, sub) >= 0)
}

func indexBytes(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}

// TestBuildPanelFactor 面板构建应能算出规模因子（依赖 TotalShare 与 CloseRaw）。
// English: TestBuildPanelFactor panel construction should compute the size factor (depends on TotalShare and CloseRaw).
func TestBuildPanelFactor(t *testing.T) {
	db := seedDB(t)
	ln, ok := factor.Get("LnMktCap")
	if !ok {
		t.Fatal("缺少 LnMktCap 因子")
	}
	p, err := BuildPanel(db, "000001.SZ", "20210104", "20210108", []factor.Def{ln})
	if err != nil {
		t.Fatalf("构建面板失败: %v", err)
	}
	if isNaN(p.Factors["LnMktCap"][0]) {
		t.Fatal("LnMktCap 期望有值")
	}
}
