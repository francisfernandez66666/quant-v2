// backfill_stock_moneyflow_gz_test.go — §REPLAY-INPUTS 2026-11-28（档 3）：采集器行为锁。
//
// 这些断言全部针对"静默坏数据"的形态——本批最容易犯的错不是崩，而是写进一张
// 看起来正常、实际深度/分母/日期都错的表：
//
//	① 上游 120 根全落库（不是只落我们 historically 取的 5 根）；
//	② 6 列与 7 列两种报文都能解，缺 f57 时 `amount` 留 NULL 而不是编一个；
//	③ `main_pct=0` 时不得反推出成交额（除零）；
//	④ 重复跑幂等、今天那根不被 seed 覆盖、append 只写今天；
//	⑤ 北交所**不发请求**（上游 rc=100，实测）；
//	⑥ 三类失败里只有 no_data 落 state（中继抖动一旦落 state 就是"永久少一批票"）；
//	⑦ `--mode=full` 必须显式报错，不许静默跑；
//	⑧ 标记无论成败都写（夜间链等的是"有结论"）。
//
// English: locks collector behavior against silently-bad data: full 120-row depth, 6/7-column
// tolerance, no fabricated denominators, idempotency, BSE never requested, only no_data
// persisted, explicit failure for the unsupported mode, and the marker always written.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// stubRelay 起一个假盒子中继：返回 /flow/money 信封，body 是东财原始报文。
// 计数每个 secid 被请求了几次，用于断言"北交所根本没被打"。
func stubRelay(t *testing.T, body string) (*httptest.Server, map[string]int) {
	t.Helper()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flow/money" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sid := r.URL.Query().Get("secid")
		hits[sid]++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "upstream_status": 200, "upstream_body": body, "src": "stub",
		})
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// fflowBody 造东财 daykline 报文：n 根、cols=6 或 7（7 带 f57 净占比）。
func fflowBody(n, cols int, startOffsetDays int) string {
	rows := make([]string, 0, n)
	base := cntime.Now().AddDate(0, 0, -startOffsetDays)
	for i := 0; i < n; i++ {
		d := base.AddDate(0, 0, i).Format("2006-01-02")
		net := float64(1000 * (i + 1))
		line := fmt.Sprintf("%s,%.1f,-1.0,-2.0,-3.0,-4.0", d, net)
		if cols == 7 {
			line += ",2.50"
		}
		rows = append(rows, `"`+line+`"`)
	}
	return `{"rc":0,"data":{"klines":[` + strings.Join(rows, ",") + `]}}`
}

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFetchOneSeedsFullDepthAndWritesRows(t *testing.T) {
	db := newTestDB(t)
	srv, _ := stubRelay(t, fflowBody(120, 7, 200))
	proxy := newProxyForTest(srv.URL)
	n, kind := fetchOne(proxy, db, "seed", "600519.SH", "1.600519")
	if kind != fetchOK {
		t.Fatalf("应成功，得 kind=%v", kind)
	}
	if n != 120 {
		t.Fatalf("seed 模式应落满上游 %d 根（不是历史上的 5 根），得 %d", upstreamDepth, n)
	}
	codes, rowsTotal, maxPer, err := db.StockFlowDepth()
	if err != nil {
		t.Fatalf("深度读数失败: %v", err)
	}
	if codes != 1 || rowsTotal != 120 || maxPer != 120 {
		t.Fatalf("深度读数不符 codes=%d rows=%d max=%d", codes, rowsTotal, maxPer)
	}
	got, _ := db.StockFlowSeries("600519.SH", cntime.DayCompactOf(cntime.Now()), 200)
	if len(got) != 120 {
		t.Fatalf("读回应 120 行，得 %d", len(got))
	}
	if !got[0].HasMainPct || got[0].MainPct != 2.5 {
		t.Fatalf("第 7 列必须落库（成交额分母的唯一来源），得 %+v", got[0])
	}
	if !got[0].HasAmount || got[0].Amount <= 0 {
		t.Fatalf("amount 应由净额/占比反推（1000/0.025=40000），得 %+v", got[0])
	}
}

func TestFetchOneToleratesSixColumnPayloadWithoutFabricatingAmount(t *testing.T) {
	db := newTestDB(t)
	srv, _ := stubRelay(t, fflowBody(8, 6, 100))
	proxy := newProxyForTest(srv.URL)
	if _, kind := fetchOne(proxy, db, "seed", "000001.SZ", "0.000001"); kind != fetchOK {
		t.Fatalf("6 列报文也应成功落库（净额仍在），得 kind=%v", kind)
	}
	got, _ := db.StockFlowSeries("000001.SZ", cntime.DayCompactOf(cntime.Now()), 50)
	if len(got) != 8 {
		t.Fatalf("应落 8 行，得 %d", len(got))
	}
	for _, r := range got {
		if r.HasMainPct || r.HasAmount {
			t.Fatalf("缺 f57 时占比与成交额都必须 NULL，不得编一个数： %+v", r)
		}
		if !r.HasMainNet || r.MainNet == 0 {
			t.Fatalf("净额是有读数的，不该被连带丢弃： %+v", r)
		}
	}
}

func TestFetchOneZeroPctYieldsNullAmount(t *testing.T) {
	db := newTestDB(t)
	body := `{"rc":0,"data":{"klines":["2026-01-05,100.0,-1.0,-2.0,-3.0,-4.0,0.00"]}}`
	srv, _ := stubRelay(t, body)
	proxy := newProxyForTest(srv.URL)
	if _, kind := fetchOne(proxy, db, "seed", "600000.SH", "1.600000"); kind != fetchOK {
		t.Fatalf("应成功: %v", kind)
	}
	got, _ := db.StockFlowSeries("600000.SH", "20991231", 10)
	if len(got) != 1 {
		t.Fatalf("应落 1 行，得 %d", len(got))
	}
	if got[0].HasAmount {
		t.Fatalf("pct=0 除不出成交额，必须 NULL（写 0 会让 Flow5d 分母凭空出现）: %+v", got[0])
	}
	if !got[0].HasMainPct || got[0].MainPct != 0 {
		t.Fatalf("pct=0 是**真读数**，必须 Has=true: %+v", got[0])
	}
}

func TestSeedSkipsTodayAndAppendWritesOnlyToday(t *testing.T) {
	db := newTestDB(t)
	today := cntime.DayCompactOf(cntime.Now())
	body := fmt.Sprintf(`{"rc":0,"data":{"klines":[%q,%q]}}`,
		// 倒数第二根=昨天，最后一根=今天
		"2020-01-02,500.0,-1.0,-2.0,-3.0,-4.0,1.00",
		today[:4]+"-"+today[4:6]+"-"+today[6:]+",900.0,-1.0,-2.0,-3.0,-4.0,3.00")
	srv, _ := stubRelay(t, body)
	proxy := newProxyForTest(srv.URL)

	// seed：今天这根归 append 腿，seed 不许写（两个写口同一天互盖＝两本账）
	if _, kind := fetchOne(proxy, db, "seed", "601398.SH", "1.601398"); kind != fetchOK {
		t.Fatalf("seed 应落非当日行: %v", kind)
	}
	rows, _ := db.StockFlowSeries("601398.SH", "20991231", 10)
	if len(rows) != 1 || rows[0].TradeDate == today {
		t.Fatalf("seed 不得写当日行，得 %+v", rows)
	}
	if rows[0].Source != "eastmoney_fflow_relay_seed" {
		t.Fatalf("seed 来源标识不符: %s", rows[0].Source)
	}

	// append：只写今天
	if _, kind := fetchOne(proxy, db, "append", "601398.SH", "1.601398"); kind != fetchOK {
		t.Fatalf("append 应成功: %v", kind)
	}
	rows, _ = db.StockFlowSeries("601398.SH", "20991231", 10)
	if len(rows) != 2 {
		t.Fatalf("append 后应共 2 行（seed 一行 + 当日一行），得 %d", len(rows))
	}
	last := rows[len(rows)-1]
	if last.TradeDate != today || last.MainNet != 900 || last.Source != "eastmoney_fflow_relay_live" {
		t.Fatalf("当日行形状不符: %+v", last)
	}

	// 幂等：再跑一次 append，行数不变
	before, _, _ := countRows(t, db)
	if _, kind := fetchOne(proxy, db, "append", "601398.SH", "1.601398"); kind != fetchOK {
		t.Fatalf("重复 append 失败: %v", kind)
	}
	after, _, _ := countRows(t, db)
	if before != after {
		t.Fatalf("重复跑行数必须不变（幂等是断点续跑的前提），得 %d -> %d", before, after)
	}
}

func TestAppendFallsBackToNoDataWhenUpstreamLacksToday(t *testing.T) {
	db := newTestDB(t)
	// 上游末根不是今天（还没出数）⇒ append 必须判 no_data，
	// 绝不能"拿末根当今天"写一根旧值——那正是 §静默停更 那类缺陷的入口。
	srv, _ := stubRelay(t, `{"rc":0,"data":{"klines":["2020-01-02,500.0,-1.0,-2.0,-3.0,-4.0,1.00"]}}`)
	proxy := newProxyForTest(srv.URL)
	if _, kind := fetchOne(proxy, db, "append", "600001.SH", "1.600001"); kind != fetchNoData {
		t.Fatalf("上游没有当日行时 append 必须 no_data，得 kind=%v", kind)
	}
	rows, _ := db.StockFlowSeries("600001.SH", "20991231", 10)
	if len(rows) != 0 {
		t.Fatalf("no_data 不得落任何行，得 %+v", rows)
	}
}

func TestRelaySecIDNeverRequestsBSE(t *testing.T) {
	cases := map[string]struct {
		secid string
		isBSE bool
	}{
		"600519.SH": {"1.600519", false},
		"510300.SH": {"1.510300", false}, // 沪市基金段（9 前缀）走 .SH 分支
		"000001.SZ": {"0.000001", false},
		"300750.SZ": {"0.300750", false},
		"430047.BJ": {"", true},
		"830799.BJ": {"", true},
		"920819.BJ": {"", true}, // 920 段必须先于 9→.SH 判定（exchange.go 的唯一口径）
		"600519":    {"1.600519", false},
	}
	for code, want := range cases {
		got, isBSE := relaySecID(code)
		if isBSE != want.isBSE || got != want.secid {
			t.Fatalf("relaySecID(%q)=(%q,%v)，期望 (%q,%v)", code, got, isBSE, want.secid, want.isBSE)
		}
	}
	// 关键负锁：绝不允许出现 "0.430047" 这种"把北交所当深市"的 secid
	// ——现成 data.secID() 正是这么错的（market.go:667 只认 6/5 前缀）。
	if sid, _ := relaySecID("430047"); sid == "0.430047" || sid != "" {
		t.Fatalf("北交所代码不得被拼成 secid，得 %q", sid)
	}
}

func TestCollectTargetsExcludesBSEAndCountsIt(t *testing.T) {
	db := newTestDB(t)
	rows := []map[string]any{
		{"ts_code": "600000.SH", "name": "浦发"},
		{"ts_code": "000001.SZ", "name": "平安"},
		{"ts_code": "430047.BJ", "name": "北A"},
		{"ts_code": "920819.BJ", "name": "北B"},
	}
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), rows); err != nil {
		t.Fatalf("写 stocks 失败: %v", err)
	}
	codes, skipped, err := collectTargets(db, "")
	if err != nil {
		t.Fatalf("取清单失败: %v", err)
	}
	if skipped != 2 {
		t.Fatalf("应跳过 2 只北交所，得 %d", skipped)
	}
	for _, c := range codes {
		if strings.HasSuffix(c, ".BJ") {
			t.Fatalf("北交所混进了采集清单: %s", c)
		}
	}
	if len(codes) != 2 {
		t.Fatalf("应剩 2 只可采，得 %v", codes)
	}
}

func TestValidateModeRejectsFull(t *testing.T) {
	if err := validateMode("seed"); err != nil {
		t.Fatalf("seed 合法: %v", err)
	}
	if err := validateMode("append"); err != nil {
		t.Fatalf("append 合法: %v", err)
	}
	err := validateMode("full")
	if err == nil {
		t.Fatal("full 必须显式报错——静默跑'全历史'等于骗自己说上游有全历史")
	}
	if !strings.Contains(err.Error(), "未实现") {
		t.Fatalf("报错必须点名未实现，得 %v", err)
	}
	if err := validateMode("whatever"); err == nil {
		t.Fatal("未知模式必须报错")
	}
}

func TestStateOnlyPersistsNoData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	sf, err := loadState(path)
	if err != nil {
		t.Fatalf("初始状态读取失败: %v", err)
	}
	sf.mark("600111.SH")
	if !sf.has("600111.SH") || sf.has("600222.SH") {
		t.Fatal("mark/has 判定不符")
	}
	if err := flushState(path, sf); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}
	again, err := loadState(path)
	if err != nil {
		t.Fatalf("复读失败: %v", err)
	}
	if !again.has("600111.SH") {
		t.Fatal("no_data 必须跨进程保留（上游明确没有这只，别每天重打）")
	}
	// nil 状态与空路径都必须安全（未启用 --state 时的形态）
	var nilState *stateFile
	if nilState.has("x") {
		t.Fatal("nil 状态不应判定为已完成")
	}
	if err := flushState("", sf); err != nil {
		t.Fatalf("空路径应直接返回，得 %v", err)
	}
}

func TestMarkerAlwaysWrittenAndShaped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "moneyflow_run.json")
	mk := runMarker{Day: "20261127", Mode: "append", Status: "relay_unavailable",
		CodesTotal: 5000, CodesOK: 12, CodesError: 20, CoveragePct: 0.24, Note: "aborted"}
	if err := writeMarker(path, mk); err != nil {
		t.Fatalf("写标记失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("复读失败: %v", err)
	}
	var got runMarker
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("标记不是合法 JSON（半截写会让夜间链判成读不到）: %v", err)
	}
	// 键名是跨侧契约：scheduler 读侧按这些键判定，改名必须两侧同批。
	for _, key := range []string{"day", "mode", "status", "codes_total", "codes_ok",
		"codes_no_data", "codes_error", "skipped_bse", "rows_written", "coverage_pct",
		"elapsed_sec", "started_at", "finished_at", "note"} {
		if !strings.Contains(string(b), `"`+key+`"`) {
			t.Fatalf("标记缺少键 %s（读侧会判成读不到）", key)
		}
	}
	if got.Status != "relay_unavailable" || got.Day != "20261127" {
		t.Fatalf("标记内容不符: %+v", got)
	}
}

// TestRunRoundNonTradingDayWritesConclusiveMarker 钉住"休市日也要有结论"这条腿。
//
// 判据**两侧都断**（写死"今天必须休市"的用例会随日期漂红，这是本仓的老坑）：
//
//	休市日 ⇒ status=not_trading_day 且返回 nil（不许打上游、不许记成 failed）；
//	交易日 ⇒ status 绝不能是 not_trading_day（否则当天整条腿被误判为"日历没开"而永不落库）。
//
// 这条分支排在凭证与开库之前，所以夹具什么都不用准备——**这正是它要在最前面的理由**：
// 休市日连库都不必打开，更不该因为 token 文件缺失把"日历没开"记成"采集失败"。
func TestRunRoundNonTradingDayWritesConclusiveMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "moneyflow_run.json")
	err := runRound("append", filepath.Join(dir, "no_such.db"), "", filepath.Join(dir, "no_token"),
		"", "", "", marker, 4, 2, "")
	trading := data.IsTradingDay(cntime.Now())
	b, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("标记没落下（夜间链等的是「这一轮有结论」，缺失会被拖到兜底时限）: %v", rerr)
	}
	var mk runMarker
	if json.Unmarshal(b, &mk) != nil {
		t.Fatalf("标记不是合法 JSON: %s", b)
	}
	if !trading {
		if mk.Status != "not_trading_day" {
			t.Fatalf("休市日状态应为 not_trading_day，得 %q（记成 failed 会把日历问题读成中继故障）", mk.Status)
		}
		if err != nil {
			t.Fatalf("休市日不该返回错误（今天没数不是故障）: %v", err)
		}
		return
	}
	if mk.Status == "not_trading_day" {
		t.Fatalf("今天是交易日却判成休市（日历尺用错了时区/形状？）: %+v", mk)
	}
}

func TestSeedFloatSharesSkipsMissingAndNonPositive(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "precalc.json")
	doc := map[string]any{
		"day": "20261127",
		"precalc": map[string]map[string]any{
			"600519.SH": {"float_shares": 1.256e9},
			"000001.SZ": {"float_shares": 0},      // 坏值：不写
			"300750.SZ": {"pe_ttm": 20.0},         // 缺字段：不写
			"601398":    {"float_shares": 2.8e10}, // 无后缀：应补成 .SH
		},
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	n, err := seedFloatShares(db, path)
	if err != nil {
		t.Fatalf("种子灌入失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("只应写 2 行（坏值与缺字段都不写），得 %d", n)
	}
	if v, ok := db.FloatShareOn("600519.SH", "20261127"); !ok || v != 1.256e9 {
		t.Fatalf("600519 股本未命中: %v %v", v, ok)
	}
	if _, ok := db.FloatShareOn("601398.SH", "20261127"); !ok {
		t.Fatal("无后缀代码必须经唯一分类表补成 .SH 后入库")
	}
	if _, ok := db.FloatShareOn("000001.SZ", "20261127"); ok {
		t.Fatal("float_share=0 不得入库（缺就不写，写 0 会让占流通市值除出天文数字）")
	}
	if _, ok := db.FloatShareOn("300750.SZ", "20261127"); ok {
		t.Fatal("缺 float_shares 字段的票不得入库")
	}
	// 全空 precalc 必须报错，而不是"成功写 0 行"
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"day":"20261127","precalc":{"600000.SH":{"pe":1}}}`), 0o644); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	if _, err := seedFloatShares(db, bad); err == nil {
		t.Fatal("一条股本都没有时必须报错")
	}
}

func TestFetchOneRelayErrorWhenEnvelopeFails(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "upstream reset"})
	}))
	t.Cleanup(srv.Close)
	proxy := newProxyForTest(srv.URL)
	if _, kind := fetchOne(proxy, db, "seed", "600004.SH", "1.600004"); kind != fetchRelayError {
		t.Fatalf("ok=false 必须是 relay_error（不落 state，明天再来），得 %v", kind)
	}
	rows, _ := db.StockFlowSeries("600004.SH", "20991231", 5)
	if len(rows) != 0 {
		t.Fatalf("失败不得落任何行，得 %+v", rows)
	}
}

// newProxyForTest 用桩服务构造**真实的中继客户端**（不造替身：
// 替身会绕过信封解包/状态码/钥匙校验这一整层，而那层正是现网出过事的地方）。
// 钥匙只在本测试进程内出现，绝不落盘、绝不打进日志。
func newProxyForTest(url string) *data.FlowProxy {
	p := data.NewFlowProxy(url, "stub-token-not-a-real-credential")
	if p == nil {
		panic("测试桩未构造出中继客户端")
	}
	return p
}

// countRows 数一下资金流表总行数（幂等断言用）。
func countRows(t *testing.T, db *store.DB) (int, int, int) {
	t.Helper()
	codes, rows, maxPer, err := db.StockFlowDepth()
	if err != nil {
		t.Fatalf("行数读数失败: %v", err)
	}
	return rows, codes, maxPer
}
