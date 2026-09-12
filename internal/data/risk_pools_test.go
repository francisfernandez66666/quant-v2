package data

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestHithink 构造一个指向 httptest 服务器的 HithinkClient（不限速、注入测试 Key）。
// handler 决定池接口返回体；返回可改写的 HithinkBaseURL 复位闭包已用 defer 处理。
func newTestHithink(t *testing.T, handler http.HandlerFunc) *HithinkClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	t.Cleanup(func() { HithinkBaseURL = old })
	t.Setenv(HithinkAPIKeyEnv, "test-key")
	c, err := NewHithinkClient()
	if err != nil {
		t.Fatal(err)
	}
	c.limiter = newHithinkLimiter(1000)
	return c
}

// TestRiskPoolConverterMapping 校验 hithink 三池条目→LimitUpStock 字段映射（引擎各消费方依赖）。
func TestRiskPoolConverterMapping(t *testing.T) {
	items := []HithinkLimitUpItem{
		{ThsCode: "600519.SH", Ticker: "600519", Name: "贵州茅台", LastPrice: 1272.8, PriceChangeRatioPct: 10.01,
			ContinueDayCnt: 3, LimitUpTime: "09:35", SealMoney: 8.8e7, TurnoverRatioPct: 1.2, Turnover: 5.5e8, OpenTimes: 2},
		{ThsCode: "000001.SZ", Name: "平安银行", LastPrice: 11.2}, // Ticker 缺省→从 thscode 剥点号取裸码
	}
	got := hithinkItemsToLimitUp(items)
	if len(got) != 2 {
		t.Fatalf("want 2 got %d", len(got))
	}
	g := got[0]
	if g.Code != "600519" || g.LianBan != 3 || g.FirstSeal != "09:35" || g.SealAmt != 8.8e7 ||
		g.BreakCount != 2 || g.ChangePct != 10.01 || g.Price != 1272.8 || g.Turnover != 1.2 {
		t.Fatalf("字段映射异常: %+v", g)
	}
	if got[1].Code != "000001" {
		t.Fatalf("裸码应从 thscode 剥离: %+v", got[1])
	}
}

// TestPoolLimitUpHithinkPrimary 验证协调器涨停池优先命中 hithink，转换后 Source=hithink。
func TestPoolLimitUpHithinkPrimary(t *testing.T) {
	hk := newTestHithink(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"success","data":{"timestamp":1,"pagination":{"total":1,"pages":1,"size":200,"page":1},"item":[{"thscode":"600519.SH","ticker":"600519","name":"贵州茅台","last_price":1272.8,"price_change_ratio_pct":10.01,"continue_day_cnt":3,"limit_up_time":"09:35","seal_money":88000000}]}}`))
	})
	dc := NewDataCoordinator(nil, nil) // eastMoney=nil：hithink 命中即不触发兜底
	dc.SetHithink(hk)
	res, err := dc.PoolLimitUp("")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "hithink" {
		t.Fatalf("应命中 hithink 主源, got=%s", res.Source)
	}
	if len(res.Stocks) != 1 || res.Stocks[0].Code != "600519" || res.Stocks[0].LianBan != 3 {
		t.Fatalf("涨停池转换异常: %+v", res.Stocks)
	}
}

// TestPoolLimitDownHithinkOnly 验证跌停池仅 hithink 提供：无客户端时返回错误（上层按 0 弃权）。
func TestPoolLimitDownHithinkOnly(t *testing.T) {
	dc := NewDataCoordinator(nil, nil) // 未 SetHithink → hithink 缺席
	if _, err := dc.PoolLimitDown(""); err == nil {
		t.Fatal("hithink 缺席时跌停池应返回错误（绝不编造）")
	}
}

// TestPoolHelpers 覆盖裸码剥离与日期毫秒换算（空/非法→0=当日）。
func TestPoolHelpers(t *testing.T) {
	if sixFromThsCode("600519.SH") != "600519" || sixFromThsCode("600519") != "600519" {
		t.Fatal("sixFromThsCode 异常")
	}
	if poolDateMillis("") != 0 || poolDateMillis("bad-date") != 0 {
		t.Fatal("poolDateMillis 空/非法应返回 0（hithink 默认取当日）")
	}
	if poolDateMillis("2026-09-12") <= 0 {
		t.Fatal("合法日期应返回正毫秒时间戳")
	}
}
