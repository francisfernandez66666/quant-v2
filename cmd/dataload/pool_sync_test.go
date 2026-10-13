// pool_sync_test.go — §POOL-STAMP／§POOL-SNAPSHOT 盘口同步链（拉取→校验→整日替换）行为锁。
//
// 与 internal/store/ths_pool_replace_test.go 的分工：那份验"替换与拦停的库面语义"，
// 本份验**同步链真的走这些口**——上游回包怎么折成行、两道守卫各在什么读数下出手、
// 以及只读探针与同步链是否共用同一份行构造（探针判"same"而同步判"放行"＝两条腿在比两个东西）。
//
// English: sync-chain level locks for the pool snapshot/stamp guards, driven by an httptest
// hithink server so the payload shape (including data.timestamp) is exercised end to end.
package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// beijingMS 把某个北京自然日的中午换成毫秒时间戳（池子回包里的 data.timestamp 用）。
func beijingMS(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 12, 0, 0, 0, time.FixedZone("CST", 8*3600)).UnixMilli()
}

// poolItemJSON 一条池子条目的原始 JSON（字段名即上游契约，改名要同步改这里）。
func poolItemJSON(code, name string, price float64, cnt int, maxSeal float64) string {
	return fmt.Sprintf(`{"thscode":%q,"name":%q,"last_price":%v,"price_change_ratio_pct":10.01,`+
		`"limit_up_time":"09:35","continue_day_cnt":%d,"continue_day_text":"2天2板",`+
		`"seal_money":10000000,"max_seal_money":%v,"open_times":1,"turnover_ratio_pct":1.5,"turnover":500000000}`,
		code, name, price, cnt, maxSeal)
}

// stubHithink 起一个假上游：三池各自的条目与时间戳由参数决定，返回可直接喂给同步链的客户端。
// 复位 HithinkBaseURL 用 t.Cleanup（与 internal/data/risk_pools_test.go 同一 harness 形状）。
func stubHithink(t *testing.T, upDay int64, items map[string][]string) *data.HithinkClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/a-share/special-data/")
		stamp := upDay
		list := items[name]
		if list == nil {
			list = []string{}
		}
		body := fmt.Sprintf(`{"code":0,"message":"success","data":{"timestamp":%d,`+
			`"pagination":{"total":%d,"pages":1,"size":200,"page":1},"item":[%s]}}`,
			stamp, len(list), strings.Join(list, ","))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := data.HithinkBaseURL
	data.HithinkBaseURL = srv.URL
	t.Cleanup(func() { data.HithinkBaseURL = old })
	t.Setenv(data.HithinkAPIKeyEnv, "test-key")
	c, err := data.NewHithinkClient()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newPoolTestDB 临时库（表骨架由 store.Open 建表链给出）。
func newPoolTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pool-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// captureLog 把标准库 log 的重定向收进缓冲区（同步链的读数只有落进日志才算交付，
// 断言"函数返回了正确的值"不足以锁住那三行 pool= 读数）。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// TestSyncPoolsReplacesWholeDay 同步链真的走整日替换：库里该日有两只，本次回包一只 ⇒ 落库后只剩一只。
// 这是缺陷本体（旧写法 INSERT OR REPLACE 留着已出池的那只），所以两侧都要读数：deleted=2 written=1。
func TestSyncPoolsReplacesWholeDay(t *testing.T) {
	db := newPoolTestDB(t)
	const day = "20261009"
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: day, TsCode: "600001.SH", Name: "甲", Price: 10, MaxSealMoney: 1e8},
		{TradeDate: day, TsCode: "600002.SH", Name: "乙", Price: 20, MaxSealMoney: 2e8}}); err != nil {
		t.Fatal(err)
	}
	client := stubHithink(t, beijingMS(2026, 10, 9), map[string][]string{
		"limit-up-pool":    {poolItemJSON("600001.SH", "甲", 10.5, 1, 1.2e8)},
		"limit-down-pool":  {poolItemJSON("600699.SH", "跌停丙", 3, 0, 1e7)},
		"limit-break-pool": {poolItemJSON("600777.SH", "炸板丁", 8, 0, 5e7)},
	})
	buf := captureLog(t)
	res, err := syncPoolsForDate(client, db, day, defaultPoolSyncOpts())
	if err != nil {
		t.Fatalf("同步应成功: %v", err)
	}
	if res.Deleted != 2 || res.Written != 1 {
		t.Fatalf("整日替换读数应为 deleted=2 written=1, got %+v", res)
	}
	rows, err := db.LimitUpsOnDate(day)
	if err != nil || len(rows) != 1 || rows[0].Price != 10.5 {
		t.Fatalf("已出池的那只必须从这一天消失且新值落库, got %+v err=%v", rows, err)
	}
	text := buf.String()
	for _, want := range []string{
		"pool=limit_up requested=20261009", "payload_day=20261009 match=yes rows=1",
		"pool=limit_down", "pool=break_pool",
		"replaced trade_date=20261009 deleted=2 written=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志缺少读数 %q：\n%s", want, text)
		}
	}
}

// TestSyncPoolsPhantomDayBlockedAndEscaped 影子日两道：默认拦停（库里那一天仍 0 行、
// 错误点名 shadow_of），--allow-shadow 放行后真落库。反向必须同轮验，
// 否则"拦停"可能只是"这一趟根本没写到那张表"。
func TestSyncPoolsPhantomDayBlockedAndEscaped(t *testing.T) {
	db := newPoolTestDB(t)
	const prev, day = "20261008", "20261009"
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: prev, TsCode: "600001.SH", Name: "甲", Price: 10, PctChg: 10.01,
			FirstSealTime: "09:35", ContinueCnt: 1, MaxSealMoney: 1e8},
		{TradeDate: prev, TsCode: "600002.SH", Name: "乙", Price: 20, PctChg: 10.01,
			FirstSealTime: "09:35", ContinueCnt: 2, MaxSealMoney: 2e8}}); err != nil {
		t.Fatal(err)
	}
	// 上游把 10-08 的池子原样兜给 10-09（名称带空格＝上游真实形态之一，签名要能归一）。
	phantom := []string{poolItemJSON("600001.SH", "甲", 10, 1, 1e8), poolItemJSON("600002.SH", "乙", 20, 2, 2e8)}
	client := stubHithink(t, beijingMS(2026, 10, 9), map[string][]string{
		"limit-up-pool": phantom, "limit-down-pool": phantom, "limit-break-pool": phantom})
	buf := captureLog(t)

	_, err := syncPoolsForDate(client, db, day, defaultPoolSyncOpts())
	if err == nil || !strings.Contains(err.Error(), "幻影日拦停") ||
		!strings.Contains(err.Error(), "shadow_of=20261008") {
		t.Fatalf("影子日必须拦停并点名对照日, got %v", err)
	}
	if cnt, _ := db.LimitUpCountOnDate(day); cnt != 0 {
		t.Fatalf("拦停后 20261009 必须仍是 0 行（不许把上一日写成今天）, got %d", cnt)
	}
	if cnt, _ := db.LimitUpCountOnDate(prev); cnt != 2 {
		t.Fatalf("拦停不得动上一交易日, got %d", cnt)
	}
	if !strings.Contains(buf.String(), "shadow_of=20261008 shadow=same") {
		t.Fatalf("拦停必须有影子日读数留痕：\n%s", buf.String())
	}

	// 逃生口：人工确认真有其事后允许落库，且库里确实出现这一天。
	if _, err := syncPoolsForDate(client, db, day, poolSyncOpts{allowShadow: true}); err != nil {
		t.Fatalf("--allow-shadow 应放行: %v", err)
	}
	if cnt, _ := db.LimitUpCountOnDate(day); cnt != 2 {
		t.Fatalf("放行后应落库 2 行, got %d", cnt)
	}
	if !strings.Contains(buf.String(), "--allow-shadow 生效") {
		t.Fatal("放行也必须留一行读数（放行是决定，不是静默）")
	}
}

// TestSyncPoolsStampThreeStates 回包时间戳三态：yes/no/unknown。
// unknown（上游没给时间戳或越界）绝不许折成 yes 或 no——它是"这条证据不存在"。
func TestSyncPoolsStampThreeStates(t *testing.T) {
	day := "20261012" // 与 cases[0] 的 beijingMS(2026,10,12) 同一天＝match=yes 的正对照
	cases := []struct {
		name      string
		stampMS   int64
		wantMatch string
		wantErr   bool
	}{
		{"时间戳就是请求日", beijingMS(2026, 10, 12), "yes", false},
		{"时间戳是别的一天", beijingMS(2026, 10, 9), "no", true},
		{"上游没给时间戳", 0, "unknown", false},
		{"时间戳越界（不像日期）", 1, "unknown", false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newPoolTestDB(t)
			// 每个子用例用不同代码，避免上一子用例的落库把本用例判成影子日。
			code := fmt.Sprintf("60001%d.SH", i)
			items := []string{poolItemJSON(code, "甲", 10+float64(i), 1, 1e8)}
			client := stubHithink(t, c.stampMS, map[string][]string{
				"limit-up-pool": items, "limit-down-pool": items, "limit-break-pool": items})
			buf := captureLog(t)
			_, err := syncPoolsForDate(client, db, day, poolSyncOpts{stampEnforce: true})
			text := buf.String()
			if !strings.Contains(text, "match="+c.wantMatch) {
				t.Fatalf("应读到 match=%s：\n%s", c.wantMatch, text)
			}
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "日期同源判红") {
					t.Fatalf("--enforce-stamp 下时间戳不符应判失败, got %v", err)
				}
				if cnt, _ := db.LimitUpCountOnDate(day); cnt != 0 {
					t.Fatalf("判红不得落库, got %d", cnt)
				}
				return
			}
			if err != nil {
				t.Fatalf("该形态不该拦停（时间戳只记读数）: %v", err)
			}
			if cnt, _ := db.LimitUpCountOnDate(day); cnt != 1 {
				t.Fatalf("unknown/yes 形态应正常落库, got %d", cnt)
			}
		})
	}
}

// TestSyncPoolsEmptyPayloadNeverErasesDay 空回包：整日替换被 ErrPoolSnapshotEmpty 拦停，
// 库里那一天原有数据必须在位（把"上游没给"写成"这一天没有涨停"是最坏形态）。
func TestSyncPoolsEmptyPayloadNeverErasesDay(t *testing.T) {
	db := newPoolTestDB(t)
	const day = "20261009"
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: day, TsCode: "600001.SH", Name: "甲", Price: 10}}); err != nil {
		t.Fatal(err)
	}
	client := stubHithink(t, beijingMS(2026, 10, 9), map[string][]string{})
	if _, err := syncPoolsForDate(client, db, day, defaultPoolSyncOpts()); err == nil {
		t.Fatal("空回包必须报错")
	}
	if cnt, _ := db.LimitUpCountOnDate(day); cnt != 1 {
		t.Fatalf("空回包不得擦掉当日数据, got %d", cnt)
	}
}

// TestPoolProbeMatchesSyncDecision 探针与同步链共用同一份行构造：同一包数据，
// 探针读出 shadow_state=same，同步链就必须是拦停——两腿比的是同一个东西，
// 否则"探针绿、现网写进幻影日"完全可能同时发生。
func TestPoolProbeMatchesSyncDecision(t *testing.T) {
	db := newPoolTestDB(t)
	const prev = "20261008"
	if _, err := db.UpsertThsLimitUps([]store.ThsLimitUpRow{
		{TradeDate: prev, TsCode: "600001.SH", Name: "甲", Price: 10, PctChg: 10.01,
			FirstSealTime: "09:35", ContinueCnt: 1, MaxSealMoney: 1e8}}); err != nil {
		t.Fatal(err)
	}
	items := []string{poolItemJSON("600001.SH", "甲", 10, 1, 1e8)}
	client := stubHithink(t, beijingMS(2026, 10, 12), map[string][]string{
		"limit-up-pool": items, "limit-down-pool": items, "limit-break-pool": items})
	row, err := probeOnePool(client, db, "limit-up", "20261012")
	if err != nil {
		t.Fatal(err)
	}
	if row.ShadowState != "same" || row.ShadowOf != prev || row.Match != "yes" || row.Rows != 1 {
		t.Fatalf("探针应读出 shadow_state=same/shadow_of=20261008/match=yes, got %+v", row)
	}
	if !strings.Contains(row.String(), "shadow_state=same") {
		t.Fatalf("探针行读数必须带 shadow_state：\n%s", row.String())
	}
	// 同一包喂给同步链 ⇒ 必须拦停（两腿结论一致）。
	if _, err := syncPoolsForDate(client, db, "20261012", defaultPoolSyncOpts()); err == nil {
		t.Fatal("探针判 same 时同步链必须拦停")
	}
	// 跌停池尚无行签名：显式 not_supported，不能被读成"判过了、没问题"。
	dn, err := probeOnePool(client, db, "limit-down", "20261012")
	if err != nil || dn.ShadowState != "not_supported" {
		t.Fatalf("未建签名的池要写明 not_supported, got %+v err=%v", dn, err)
	}
}

// TestPoolProbeUnknownMatch 探针的 unknown 三态（时间戳缺失/越界）与写库侧同形。
func TestPoolProbeUnknownMatch(t *testing.T) {
	db := newPoolTestDB(t)
	client := stubHithink(t, 0, map[string][]string{"limit-up-pool": {poolItemJSON("600001.SH", "甲", 10, 1, 1e8)}})
	row, err := probeOnePool(client, db, "limit-up", "20261012")
	if err != nil {
		t.Fatal(err)
	}
	if row.Match != "unknown" || row.PayloadDay != "" || row.ShadowState != "no_earlier_day" {
		t.Fatalf("无时间戳且无上一日时应 unknown/no_earlier_day, got %+v", row)
	}
	if !strings.Contains(row.String(), "payload_day=<none>") {
		t.Fatalf("空值也要可见（键不能消失）：\n%s", row.String())
	}
}

// TestSyncPoolsEmptySimplePoolKeepsDay 跌停/炸板空回包：涨停池照常落库、整轮不算失败、
// 该日旧行一条不少，并留下一行 skipped_replace 读数。
// 反向（不许删）由同用例的后半段验：库里预置一条炸板旧行，空回包后必须还在。
func TestSyncPoolsEmptySimplePoolKeepsDay(t *testing.T) {
	db := newPoolTestDB(t)
	const day = "20261009"
	if _, err := db.UpsertThsSimplePool("ths_break_pool_daily", day, map[string]store.ThsPoolSimple{
		"600777.SH": {Name: "旧炸板", Price: 7}}); err != nil {
		t.Fatal(err)
	}
	client := stubHithink(t, beijingMS(2026, 10, 9), map[string][]string{
		"limit-up-pool": {poolItemJSON("600001.SH", "甲", 10, 1, 1e8)},
	})
	buf := captureLog(t)
	res, err := syncPoolsForDate(client, db, day, defaultPoolSyncOpts())
	if err != nil {
		t.Fatalf("跌停/炸板空回包不得把整轮判失败: %v", err)
	}
	if res.Written != 1 {
		t.Fatalf("涨停池应照常落库, got %+v", res)
	}
	if n, _ := db.SimplePoolCountOnDate("ths_break_pool_daily", day); n != 1 {
		t.Fatalf("空回包不得删掉炸板旧行, got %d", n)
	}
	text := buf.String()
	// 两行分开断言：同一行里 requested 与 payload_day 之间还夹着 payload_ms=，
	// 把整行当一条子串写会把真存在的读数判成不存在（断言自伤）。
	if !strings.Contains(text, "pool=break_pool empty_payload=skipped_replace") ||
		!strings.Contains(text, "pool=break_pool requested=20261009") ||
		!strings.Contains(text, "payload_day=20261009 match=yes rows=0") {
		t.Fatalf("空池既要留 rows=0 读数，也要留 skipped_replace 说明：\n%s", text)
	}
}

// TestNewPoolStampReadoutKeepsEveryKey 读数行的形状：所有键都在，payload_day 为空打 <none>。
func TestNewPoolStampReadoutKeepsEveryKey(t *testing.T) {
	line := newPoolStampReadout("limit_up", "20261012", data.HithinkPool{
		Items: []data.HithinkLimitUpItem{{ThsCode: "600001.SH"}}, TimestampMs: 0}).String()
	for _, k := range []string{"pool=limit_up", "requested=20261012", "payload_ms=0",
		"payload_day=<none>", "match=unknown", "rows=1"} {
		if !strings.Contains(line, k) {
			t.Fatalf("读数缺键 %q：\n%s", k, line)
		}
	}
}
