// baostock sidecar 客户端的测试（httptest 模拟 sidecar，不依赖真实 baostock）。
// （Tests for the baostock sidecar client using an httptest mock sidecar.）
// English: Tests for the baostock sidecar client using an httptest mock sidecar (no real baostock dependency).
package data

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// bsMock 最小 sidecar mock：按路由返回 CSV 或 "error: ..."。
// （bsMock is a minimal sidecar stub serving CSV or error per route.）
// English: bsMock is a minimal sidecar stub serving CSV or error per route.
func bsMock(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/trade_days", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("calendar_date,is_open\n2020-01-01,0\n2020-01-02,1\n"))
	})
	mux.HandleFunc("/all_stock", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,code_name,tradeStatus\nsh.600000,浦发银行,1\nsz.000001,平安银行,1\nbj.830000,某北交所股,1\n"))
	})
	mux.HandleFunc("/kline", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("date,code,open,high,low,close,preclose,volume,amount,turn,tradestatus,pctChg,peTTM,pbMRQ,psTTM,pcfNcfTTM,isST\n" +
			"2020-01-02,sh.600000,10.0,10.5,9.9,10.2,10.0,123400,1250000.5,0.9,1,2.0,8.1,0.9,2.2,1.1,0\n" +
			"2020-01-03,sh.600000,,10.8,10.1,10.8,10.2,0,0,,0,5.9,8.5,0.95,2.4,1.2,0\n"))
	})
	mux.HandleFunc("/index_kline", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("date,code,open,high,low,close,preclose,volume,amount,pctChg\n" +
			"2020-01-02,sh.000300,4000,4010,3990,4005,3998,100000,1.2e11,0.2\n"))
	})
	mux.HandleFunc("/adjust_factor", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,dividOperateDate,backAdjustFactor,adjustFactor\nsh.600000,2020-01-02,1.1,1.0\n"))
	})
	mux.HandleFunc("/profit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,pubDate,statDate,epsTTM,roeAvg,gpMargin,npMargin,netProfit,MBRevenue\n" +
			"sh.600000,2020-04-29,2019-12-31,1.5,12.3,30.1,15.2,58000.0,180000.0\n"))
	})
	mux.HandleFunc("/growth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,pubDate,statDate,YOYNI\nsh.600000,2020-04-29,2019-12-31,5.5\n"))
	})
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,pubDate,statDate,liabilityToAsset\nsh.600000,2020-04-29,2019-12-31,92.0\n"))
	})
	mux.HandleFunc("/dividend", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("code,dividPlanAnnounceDate,dividOperateDate,cashDividendRatio\nsh.600000,2020-04-29,2020-07-01,3.0\n"))
	})
	mux.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("error: 查询失败，请重试"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBaostockTradeDays 验证交易日历接口解析：is_open 字段与日期字符串原样保留。
func TestBaostockTradeDays(t *testing.T) {
	c := NewBaostockClient(bsMock(t).URL)
	rows, err := c.TradeDays("20200101", "20200105")
	if err != nil {
		t.Fatalf("TradeDays: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行，得到 %d", len(rows))
	}
	if rows[0].S("calendar_date") != "2020-01-01" {
		t.Errorf("日历日期未保留字符串: %v", rows[0].S("calendar_date"))
	}
	if rows[1].I("is_open") != 1 {
		t.Errorf("is_open 解析失败: %v", rows[1].F("is_open"))
	}
}

// TestBaostockAllStock 验证全市场股票列表解析及 baostock↔Tushare 代码双向互转。
func TestBaostockAllStock(t *testing.T) {
	c := NewBaostockClient(bsMock(t).URL)
	rows, err := c.AllStock()
	if err != nil {
		t.Fatalf("AllStock: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("期望 3 行，得到 %d", len(rows))
	}
	got := BsCodeToTS(rows[0].S("code"))
	if got != "600000.SH" {
		t.Errorf("BsCodeToTS(sh.600000)=%s", got)
	}
	if BsCodeToTS(rows[2].S("code")) != "830000.BJ" {
		t.Errorf("BsCodeToTS(bj) 错误: %s", BsCodeToTS(rows[2].S("code")))
	}
}

// TestBaostockKlineEmptyCells 验证停牌日空单元格解析为 0，且字段名大小写归一。
func TestBaostockKlineEmptyCells(t *testing.T) {
	c := NewBaostockClient(bsMock(t).URL)
	rows, err := c.StockKline("sh.600000", "20200101", "20200110")
	if err != nil {
		t.Fatalf("StockKline: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行，得到 %d", len(rows))
	}
	// 第二行为停牌模拟：open/volume/turn 空 → nil
	// English: second row simulates a suspension: open/volume/turn empty → nil
	if rows[1].F("open") != 0 {
		t.Errorf("空 open 应为 0: %v", rows[1].F("open"))
	}
	if rows[1].F("tradestatus") != 0 {
		t.Errorf("停牌日 tradestatus 应为 0: %v", rows[1].F("tradestatus"))
	}
	if v := rows[0].F("pettm"); v != 8.1 {
		t.Errorf("pettm 期望 8.1（大小写归一），得到 %v", v)
	}
	if v := rows[0].F("isst"); v != 0 {
		t.Errorf("isst 期望 0，得到 %v", v)
	}
}

// TestBaostockFina 验证财务数据接口解析：字符串日期原样保留、数值正确转换。
func TestBaostockFina(t *testing.T) {
	c := NewBaostockClient(bsMock(t).URL)
	rows, err := c.FinaProfit("sh.600000", 2019, 4)
	if err != nil {
		t.Fatalf("FinaProfit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("期望 1 行，得到 %d", len(rows))
	}
	if rows[0].S("pubdate") != "2020-04-29" {
		t.Errorf("pubDate 保留字符串: %v", rows[0].S("pubdate"))
	}
	if v := rows[0].F("netprofit"); v != 58000.0 {
		t.Errorf("netProfit 期望 58000，得到 %v", v)
	}
}

// TestBaostockErrorPrefix 验证业务错误（"error:" 前缀）被转为 Go error 返回。
func TestBaostockErrorPrefix(t *testing.T) {
	c := NewBaostockClient(bsMock(t).URL)
	_, err := c.call("error", nil, nil)
	if err == nil {
		t.Fatal("期望业务错误，得到 nil")
	}
}

// TestBaostockCodeConversion 验证 baostock 与 Tushare 代码双向互转及裸代码容错补全。
func TestBaostockCodeConversion(t *testing.T) {
	fwd := map[string]string{
		"600000.SH": "sh.600000",
		"000001.SZ": "sz.000001",
		"688001.SH": "sh.688001",
		"300001.SZ": "sz.300001",
		"830000.BJ": "bj.830000",
		"000300.SH": "sh.000300",
		"600000":    "sh.600000",
		"000001":    "sz.000001",
		"830000":    "bj.830000",
	}
	for ts, want := range fwd {
		if got := TsCodeToBS(ts); got != want {
			t.Errorf("TsCodeToBS(%s)=%s, 期望 %s", ts, got, want)
		}
	}
	// 反向：仅对带后缀的代码做往返校验（无后缀输入本就会补全交易所后缀）
	// English: reverse: round-trip check only for codes with a suffix (suffixless input already gets the exchange suffix filled in)
	round := map[string]string{
		"600000.SH": "sh.600000",
		"000001.SZ": "sz.000001",
		"830000.BJ": "bj.830000",
	}
	for ts, bs := range round {
		if got := BsCodeToTS(bs); got != ts {
			t.Errorf("BsCodeToTS(%s)=%s, 期望 %s", bs, got, ts)
		}
	}
	// 裸代码容错（akshare fallback 输出）：按首位猜交易所补全。
	// English: bare-code tolerance (akshare fallback output): infer the exchange from the first digit and fill it in.
	bare := map[string]string{
		"600000": "600000.SH",
		"000001": "000001.SZ",
		"300750": "300750.SZ",
		"830000": "830000.BJ",
		"688001": "688001.SH",
	}
	for code, want := range bare {
		if got := BsCodeToTS(code); got != want {
			t.Errorf("BsCodeToTS(%s)=%s, 期望 %s", code, got, want)
		}
	}
}
