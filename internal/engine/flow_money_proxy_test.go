// flow_money_proxy_test.go — §FLOW-MONEY 资金路选 A 的装配锁（2026-11-19 owner 裁决）。
//
// 锁的是"优先/兜底"这条顺序本身，不是解析（解析已由 data 侧锁住）：
//
//	① 装配了中继就必须走中继——直连腿此时不该被打（广州打东财是被 RST 的那条）；
//	② 中继失败必须落回直连腿——广州的 RST 是间歇性的（同日 ulist.np 成功过一次），
//	   一次失败就整天弃权等于把抖动放大成结构性缺数；
//	③ 中继失败且没有直连腿 ⇒ 弃权（不写库、不留 0），fail-closed。
package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// 一条真实形状的 fflow body（两行：昨日 + 今日）。
const proxyTestFFlow = `{"rc":0,"data":{"code":"000001","market":1,"klines":[` +
	`"2026-11-18,-3411456000.0,6336782336.0,-2925322240.0,-3832389632.0,420933632.0",` +
	`"%TODAY%,-11811803136.0,14767779840.0,-2955976704.0,-6693154816.0,-5118648320.0"]}}`

// newProxyTestEngine 造一个只挂 d1Store 的引擎（与既有 flow_money_readings_test 同形）。
func newProxyTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := newFlowMoneyTestEngine(t)
	// 成交额腿：今日 4000 亿（资金流归一的分母）。
	e.indexAmountSeriesFn = func(n int, today string) ([]data.IndexAmountDay, string, error) {
		return []data.IndexAmountDay{
			{TradeDate: "2026-11-18", Amount: 3800e8},
			{TradeDate: today, Amount: 4000e8},
		}, "em:000001.SH", nil
	}
	return e
}

// mockTransport 是 data.MarketAPI 的网络桩：记下被打的 URL，按需回包或报错。
type mockTransport struct {
	calls int64
	body  string
	fail  bool
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&m.calls, 1)
	if m.fail {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestFillFlowMoneyPrefersProxyOverDirect(t *testing.T) {
	e := newProxyTestEngine(t)
	nb := time.Now().In(cntime.Loc)
	today := nb.Format("2006-01-02")
	now := time.Date(nb.Year(), nb.Month(), nb.Day(), 10, 30, 0, 0, cntime.Loc)

	// 中继桩：fflow 返回带今日行的序列；clist 返回一个板块（两条腿都走中继，
	// 这样"直连腿一次都没被打"才是一条真断言，而不是被板块腿的兜底污染）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flow/money" {
			t.Errorf("应只打 /flow/money, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("中继必须带鉴权头, got %q", r.Header.Get("Authorization"))
		}
		var upstream string
		if r.URL.Query().Get("kind") == "clist" {
			upstream = `{"rc":0,"data":{"total":1,"diff":{"0":{"f12":"BK0420","f14":"航空机场","f6":1.0e9,"f62":3.0e6}}}}`
		} else {
			upstream = strings.ReplaceAll(proxyTestFFlow, "%TODAY%", today)
		}
		payload := `{"ok":true,"upstream_status":200,"src":"eastmoney_fflow","cache":"miss","upstream_body":` +
			mustJSONQuote(upstream) + `}`
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// 直连腿桩：一旦被打就记数——这条用例的断言之一就是它**不该**被打。
	direct := &mockTransport{body: strings.ReplaceAll(proxyTestFFlow, "%TODAY%", today)}
	api := data.NewMarketAPI()
	api.SetTransport(direct)

	e.marketAPI = api
	e.SetFlowProxy(data.NewFlowProxy(srv.URL, "tok"))

	e.fillFlowMoney(now)

	if got := atomic.LoadInt64(&direct.calls); got != 0 {
		t.Fatalf("中继可用时直连腿不该被打, calls=%d", got)
	}
	row, err := e.d1Store.GetFlowFactorDaily(today)
	if err != nil || row == nil || row.FlowMarketDelta == nil {
		t.Fatalf("中继取数应写库, row=%+v err=%v", row, err)
	}
	// -118.1 亿 / 4000 亿 ≈ -2.95% → delta ≈ -0.98（夹取前）。
	if *row.FlowMarketDelta > -0.9 || *row.FlowMarketDelta < -1.0 {
		t.Fatalf("大盘 delta 应≈-0.98, got %v", *row.FlowMarketDelta)
	}
}

func TestFillFlowMoneyFallsBackToDirectWhenProxyFails(t *testing.T) {
	e := newProxyTestEngine(t)
	nb := time.Now().In(cntime.Loc)
	today := nb.Format("2006-01-02")
	now := time.Date(nb.Year(), nb.Month(), nb.Day(), 10, 30, 0, 0, cntime.Loc)

	// 中继桩：上游 502（盒子取不到）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"ok":false,"err":"upstream unavailable"}`))
	}))
	defer srv.Close()

	direct := &mockTransport{body: strings.ReplaceAll(proxyTestFFlow, "%TODAY%", today)}
	api := data.NewMarketAPI()
	api.SetTransport(direct)

	e.marketAPI = api
	e.SetFlowProxy(data.NewFlowProxy(srv.URL, "tok"))

	e.fillFlowMoney(now)

	if got := atomic.LoadInt64(&direct.calls); got == 0 {
		t.Fatal("中继失败必须落回直连腿（广州 RST 是间歇性的，一次失败不该整天弃权）")
	}
	row, err := e.d1Store.GetFlowFactorDaily(today)
	if err != nil || row == nil || row.FlowMarketDelta == nil {
		t.Fatalf("兜底腿取到数应写库, row=%+v err=%v", row, err)
	}
}

func TestFillFlowMoneyProxyFailWithoutDirectAbstains(t *testing.T) {
	e := newProxyTestEngine(t)
	nb := time.Now().In(cntime.Loc)
	today := nb.Format("2006-01-02")
	now := time.Date(nb.Year(), nb.Month(), nb.Day(), 10, 30, 0, 0, cntime.Loc)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()

	// 没有直连腿（marketAPI 为 nil）⇒ 只能弃权。
	e.SetFlowProxy(data.NewFlowProxy(srv.URL, "tok"))
	e.fillFlowMoney(now)

	row, err := e.d1Store.GetFlowFactorDaily(today)
	if err != nil {
		t.Fatalf("读库失败: %v", err)
	}
	if row != nil && row.FlowMarketDelta != nil {
		t.Fatalf("无可用取数腿时必须弃权（不许拿 0 或旧值冒充）, got %v", *row.FlowMarketDelta)
	}
}

// mustJSONQuote 把字符串编码成 JSON 字符串字面量（用于把 body 塞进信封）。
func mustJSONQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
