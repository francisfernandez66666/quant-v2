// degraded_source_load_test.go — §P2-F（2026-10-06 修复批 波 5）降级行**落库侧**行为锁。
//
// 这一族缺陷的完整链条是：sidecar 兜底腿拿不出某列 → 写成空串/伪造 "1" → Go 的 F() 把空串
// 折成 0 → 0 在行情语义里是真实读数（平盘/停牌/非 ST）→ 库里把"断源日"长成"平盘日"，
// 而且事后从数据本身看不出这行是兜底来的。
// 线格式侧的锁在 cmd/pydata/tests/test_degraded_source.py（K1b/K1c/K1d/K1e/K1f/K4/K5），
// 本文件锁的是"CSV 进了库以后长什么样"（K1 家族）——两侧都锁住才闭环：
//
//	K1a 降级行整行落库（不再因 tradestatus 读不出来就被当停牌跳掉），source 列如实记录来源；
//	K1b 降级行的涨跌/估值/ST 落成 NULL，而不是 0（0＝平盘/确定非 ST，是伪造读数）；
//	K1c 同批里 tradestatus 真读到 0 的行仍按停牌跳过（缺测与读数 0 必须能分别走到两条路）；
//	K1d 主链路（baostock）行照常落真实涨跌读数，来源值可被读侧 LIKE 判据圈出；
//	K1e 老 sidecar 不带 source 列（缺列世代）不报错，按主链路盖章，读侧不因"没这列"判成降级；
//	K1f ST 态未知时停板价照写（±10% 偏松方向），取向是"缺停板价会让回测护栏整体失效"。
//
// English: load-side behavior locks for §P2-F degraded daily rows (NULL instead of fabricated 0,
// source column recorded, suspended-vs-missing disambiguated, old sidecar without the column).
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// 库内 daily.trade_date 是紧凑形（YYYYMMDD）：装载层用 normDate 把 sidecar 的 ISO 去横线。
// 断言里的日期必须与写入口径同源，否则查询恒 0 行、锁会"绿着什么都没锁"（§CRLF 同族教训）。
const (
	dayPrimary = "20260918" // 主链路（baostock）日
	daySina    = "20260921" // 新浪降级日
	dayEm      = "20260922" // 东财降级日
	dayHalt    = "20260923" // 真停牌日（不得落库）
)

// klineHeader 与 sidecar 的 _fields_with_source() 逐列一致（19 列）。
// 这里刻意把列序抄成生产侧的口径而不是随便排：列序错了这份夹具就测不到真实的错位形态。
const klineHeader = "date,code,open,high,low,close,preclose,volume,amount,adjustflag," +
	"turn,tradestatus,pctChg,peTTM,pbMRQ,psTTM,pcfNcfTTM,isST,source"

// bsSidecar 起一个只服务 /kline 与 /adjust_factor 的 sidecar mock，行内容由调用方给出。
func bsSidecar(t *testing.T, klineRows []string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/kline", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, klineHeader)
		for _, r := range klineRows {
			fmt.Fprintln(w, r)
		}
	})
	mux.HandleFunc("/adjust_factor", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "code,dividOperateDate,backAdjustFactor,adjustFactor")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func openDB(t *testing.T) *store.DB {
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mustLoad 跑装载并把错误直接判红（装载层返回 error 的形态不该被测试吞掉）。
// 返回值是装载层的"四表合计行数"（daily + daily_basic + stk_limit + adj_factor），
// 所以断言单表根数必须另走 countRows，别拿合计当根数用。
func mustLoad(t *testing.T, db *store.DB, url, code, start, end string) int {
	t.Helper()
	n, err := bsLoadStockTables(db, data.NewBaostockClient(url), code, start, end)
	if err != nil {
		t.Fatalf("bsLoadStockTables: %v", err)
	}
	return n
}

// countRows 数一张表的行数（extra 为可选的 WHERE 片段，不含 WHERE 关键字）。
func countRows(t *testing.T, db *store.DB, table, extra string) int64 {
	t.Helper()
	q := "SELECT COUNT(*) AS n FROM " + table
	if extra != "" {
		q += " WHERE " + extra
	}
	rows, err := db.QueryRows(q)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	n := toCount(rows)
	if n < 0 {
		t.Fatalf("count %s 取不到数值：%v", table, rows)
	}
	return n
}

func queryCell(t *testing.T, db *store.DB, query string, args ...any) any {
	t.Helper()
	rows, err := db.QueryRows(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if len(rows) != 1 {
		t.Fatalf("query %q 期望恰好 1 行，实得 %d（行数本身就是判据：行没了/多写了都算红）", query, len(rows))
	}
	for _, v := range rows[0] {
		return v
	}
	t.Fatalf("query %q 返回空列", query)
	return nil
}

func dailyCell(t *testing.T, db *store.DB, col, code, date string) any {
	t.Helper()
	return queryCell(t, db, "SELECT "+col+" FROM daily WHERE ts_code=? AND trade_date=?", code, date)
}

func basisCell(t *testing.T, db *store.DB, col, code, date string) any {
	t.Helper()
	return queryCell(t, db, "SELECT "+col+" FROM daily_basic WHERE ts_code=? AND trade_date=?", code, date)
}

// TestDegradedRowLandsWithSourceAndNulls 覆盖 K1a/K1b/K1c/K1d/K1f：
// 一批四行——主链路一天、新浪降级一天、东财降级一天、真停牌一天。
func TestDegradedRowLandsWithSourceAndNulls(t *testing.T) {
	const code = "600000.SH"
	db := openDB(t)
	rows := []string{
		// 主链路（baostock）：全部列都有读数。
		"2026-09-18," + code + ",10.0,10.6,9.9,10.5,10.0,120000,1260000,3,1.1,1,5.0,8.1,0.9,2.2,1.1,0,baostock",
		// 新浪降级：tradestatus/涨跌/估值/ST 全是 NA（拿不出），日期用紧凑形（该腿的真实形态）。
		"20260921," + code + ",10.5,11.0,10.4,10.9,10.5,130000,1417000,3,1.2,NA,NA,NA,NA,NA,NA,NA,sina_degraded",
		// 东财降级：涨跌幅是真实读数（-2.0），停牌态/估值/ST 是 NA。
		"2026-09-22," + code + ",10.9,10.9,10.0,10.7,10.9,110000,1177000,3,1.0,NA,-2.0,NA,NA,NA,NA,NA,eastmoney_degraded",
		// 真停牌：主链路读到 tradestatus=0，必须整行跳过（等价 Tushare 缺行语义）。
		"2026-09-23," + code + ",10.7,10.7,10.7,10.7,10.7,0,0,3,0,0,0,8.1,0.9,2.2,1.1,0,baostock",
	}
	mustLoad(t, db, bsSidecar(t, rows).URL, code, "2026-09-01", "2026-09-30")
	// 装载返回值是「四表合计行数」，不能当根数用：daily 根数单独数（停牌行不该在里面）。
	// 拆成两枚**单一成因**断言：<3 只能是降级行被跳掉（K1a），>3 只能是停牌行被写进去（K1c）。
	// 合成一句 `!= 3` 会把两条锁编号写进同一段文案，于是方向相反的两枚破坏（D4 缺测也跳行 /
	// D5 摘掉停牌判据）都撞在同一句上，反证就无从判断「这枚破坏到底被哪把尺子拦住了」。
	if got := countRows(t, db, "daily", ""); got < 3 {
		t.Fatalf("K1a：daily 根数=%d，少于 3 ⇒ 降级腿的行被整行跳掉了（缺测 tradestatus 不该当停牌）", got)
	} else if got > 3 {
		t.Fatalf("K1c：daily 根数=%d，多于 3 ⇒ tradestatus=0 的停牌行被写了进去（停牌等价缺行语义）", got)
	}

	// K1a：三行都在，停牌那行不在。
	for _, d := range []string{dayPrimary, daySina, dayEm} {
		if got := dailyCell(t, db, "close", code, d); got == nil {
			t.Fatalf("%s 行没落库（K1a）", d)
		}
	}
	if rows2, err := db.QueryRows("SELECT trade_date FROM daily WHERE ts_code=? AND trade_date=?", code, dayHalt); err != nil {
		t.Fatalf("停牌行查询: %v", err)
	} else if len(rows2) != 0 {
		t.Fatalf("tradestatus=0 的停牌行被落库了（K1c 破）：%v", rows2)
	}

	// K1a：来源列如实记录（这是降级行在库里唯一的痕迹）。
	wantSrc := map[string]string{
		dayPrimary: store.DailySourceBaostock,
		daySina:    "sina_degraded",
		dayEm:      "eastmoney_degraded",
	}
	for d, want := range wantSrc {
		if got := dailyCell(t, db, "source", code, d); got != want {
			t.Fatalf("%s source=%v，期望 %q（写侧来源值与 sidecar 字面量必须逐字一致）", d, got, want)
		}
	}

	// K1b：降级行的估值/ST 两腿都给不出 ⇒ NULL，不是 0。
	for _, d := range []string{daySina, dayEm} {
		for _, col := range []string{"pe_ttm", "pb", "ps_ttm", "pcf_ttm", "is_st"} {
			if got := basisCell(t, db, col, code, d); got != nil {
				t.Fatalf("%s daily_basic.%s=%v，期望 NULL（0 在 is_st 语义里是确定非 ST）", d, col, got)
			}
		}
	}
	// K1b 的分腿口径：涨跌两列（pct_chg/change）按"这一腿到底有没有这个读数"分别落，
	// 不是一刀切 NULL——新浪腿整列拿不出（NULL），东财腿有真实涨跌幅（照落）。
	// 这一条同时是"别把缺测标记扩散到能拿出的列"的反证支：把 Eastmoney 的 pctChg 也写成 NA
	// 或把 sina 的空串折成 0，本断言都会红。
	if got := dailyCell(t, db, "pct_chg", code, daySina); got != nil {
		t.Fatalf("新浪降级行 pct_chg=%v，期望 NULL（旧形态是空串→0＝平盘，正是这次要修的）", got)
	}
	if got := dailyCell(t, db, "change", code, daySina); got != nil {
		t.Fatalf("新浪降级行 change=%v，期望 NULL（涨跌缺失时不得由 close-preclose 反算补一个："+
			"半 known 行会让读侧误判这行有涨跌数据）", got)
	}
	if got := dailyCell(t, db, "pct_chg", code, dayEm); got == nil {
		t.Fatalf("东财降级行 pct_chg=NULL：涨跌幅是它的真实读数，不得一并抹掉")
	} else if f, _ := got.(float64); f != -2.0 {
		t.Fatalf("东财降级行 pct_chg=%v，期望 -2", got)
	}
	if got := dailyCell(t, db, "change", code, dayEm); got == nil {
		t.Fatalf("东财降级行 change=NULL：pct_chg 有读数时 change 同进同退照落")
	}
	// K1b 反向：有读数的列不得被一起抹成 NULL（否则降级日只剩价格，价格本身是真实读数）。
	if got := dailyCell(t, db, "close", code, daySina); got == nil {
		t.Fatalf("降级行 close=NULL：真实价格不得被缺测标记扩散掉")
	}
	if got := basisCell(t, db, "turnover_rate", code, daySina); got == nil {
		t.Fatalf("降级行 turnover_rate=NULL：换手率两腿都给得出，不得写成缺测")
	}

	// K1d：主链路行的真实读数照常落库。
	if got := dailyCell(t, db, "pct_chg", code, dayPrimary); got == nil {
		t.Fatalf("主链路行 pct_chg=NULL")
	} else if f, _ := got.(float64); f != 5.0 {
		t.Fatalf("主链路行 pct_chg=%v，期望 5", got)
	}
	if got := basisCell(t, db, "is_st", code, dayPrimary); got == nil {
		t.Fatalf("主链路行 is_st=NULL：读到 0 就该落 0，NULL 只留给「没读数」这一种形态")
	}

	// K1f：ST 态未知的降级行照写停板价（±10%），读侧才有涨跌停护栏可用。
	for _, d := range []string{daySina, dayEm} {
		lim, err := db.QueryRows("SELECT up_limit, down_limit FROM stk_limit WHERE ts_code=? AND trade_date=?", code, d)
		if err != nil {
			t.Fatalf("stk_limit 查询 %s: %v", d, err)
		}
		if len(lim) != 1 || lim[0]["up_limit"] == nil {
			t.Fatalf("%s 缺停板价（K1f）：is_st 未知不等于不给护栏，护栏缺失会让回测涨跌停判据整体失效", d)
		}
	}

	// 读侧判据闭环：同一张表上，降级两行被 LIKE 判据圈出、主链路一行不被圈出。
	deg, err := db.QueryRows("SELECT COUNT(*) AS n FROM daily WHERE ts_code=? AND "+store.DailySourceDegradedOnly(""), code)
	if err != nil {
		t.Fatalf("降级计数: %v", err)
	}
	if toCount(deg) != 2 {
		t.Fatalf("降级行数=%v，期望 2（写侧值与读侧 LIKE 模式脱节＝筛不到降级数据）", deg[0]["n"])
	}
	keep, err := db.QueryRows("SELECT COUNT(*) AS n FROM daily WHERE ts_code=? AND "+store.DailySourceNotDegraded(""), code)
	if err != nil {
		t.Fatalf("非降级计数: %v", err)
	}
	if toCount(keep) != 1 {
		t.Fatalf("非降级行数=%v，期望 1", keep[0]["n"])
	}
}

func toCount(rows []map[string]any) int64 {
	if len(rows) == 0 {
		return -1
	}
	switch v := rows[0]["n"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return -1
}

// TestLegacySidecarWithoutSourceColumn 覆盖 K1e：老 sidecar 的响应没有 source 列。
// 装载不得因此报错（写入面校验会拒未知列，但缺列不是未知列），并且必须按主链路盖章——
// 否则老世代的行全是 NULL，读侧判据只能靠 NULL 兜，"这一列没落地"与"这行数据坏"就分不开。
func TestLegacySidecarWithoutSourceColumn(t *testing.T) {
	const code = "600000.SH"
	db := openDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/kline", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "date,code,open,high,low,close,preclose,volume,amount,adjustflag,"+
			"turn,tradestatus,pctChg,peTTM,pbMRQ,psTTM,pcfNcfTTM,isST")
		fmt.Fprintln(w, "2026-09-18,"+code+",10.0,10.6,9.9,10.5,10.0,120000,1260000,3,1.1,1,5.0,8.1,0.9,2.2,1.1,0")
	})
	mux.HandleFunc("/adjust_factor", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "code,dividOperateDate,backAdjustFactor,adjustFactor")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if n := mustLoad(t, db, srv.URL, code, "2026-09-01", "2026-09-30"); n != 3 {
		// 四表合计：一根 bar 写 daily + daily_basic + stk_limit 三行（复权因子为空）。
		t.Fatalf("插入合计=%d，期望 3（daily/daily_basic/stk_limit 各一行）", n)
	}
	if got := dailyCell(t, db, "source", code, dayPrimary); got != store.DailySourceBaostock {
		t.Fatalf("缺 source 列时盖章=%v，期望 %q（缺列世代按主链路处理）", got, store.DailySourceBaostock)
	}
	// 反向：盖章成主链路的行不会被降级判据误伤（把 dailySourceOf 的缺省值改成 ""/降级名都会红）。
	deg, err := db.QueryRows("SELECT COUNT(*) AS n FROM daily WHERE " + store.DailySourceDegradedOnly(""))
	if err != nil {
		t.Fatalf("降级计数: %v", err)
	}
	if toCount(deg) != 0 {
		t.Fatalf("老 sidecar 行被判成降级（%v）⇒ 读侧会凭空丢掉整段历史", deg[0]["n"])
	}
}
