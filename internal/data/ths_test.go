// ths_test.go — 同花顺实时行情解析单元测试。
// §2026-09-20 重写：旧测试按"data.items 下按证券 id 索引的位置数组"构造夹具，
// 与线上真实结构（顶层 {"items":{"<字段id>":<字符串值>}} 扁平字典）完全不符，
// 于是"夹具绿、线上永远 no data"——这正是同花顺报价源长期失效未被发现的原因。
// 本文件所有夹具一律取自线上真实报文（字段 id 与取值均为实测值）。
// ths_test.go — THS realtime-quote parsing tests. Rewritten 2026-09-20: the old fixture
// assumed positional arrays under data.items, which never matched the real flat
// {"items":{"<field id>":<string>}} payload, so tests were green while production always
// returned "no data". All fixtures below are captured from the live endpoint.
package data

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// thsRecorderTransport 记录实际请求的 URL，并回放同花顺 K 线真实报文（字符串 data + 11 列行）。
// English: records the actual outbound URL and replays a real-shaped THS K-line payload.
type thsRecorderTransport struct {
	dailyBody  string
	minuteBody string
	gotURL     []string
}

// RoundTrip 记录器传输：按 URL 关键字返回预置的日线/分钟线固定报文，同时登记请求 URL 供断言。
// English: recorder transport — returns canned daily/minute payloads by URL keyword and logs URLs.
func (t *thsRecorderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.gotURL = append(t.gotURL, req.URL.String())
	body := t.dailyBody
	if strings.Contains(req.URL.Path, "/"+thsScale5Min+"/") {
		body = t.minuteBody
	}
	if body == "" {
		body = `{"data":""}`
	}
	return &http.Response{
		StatusCode: 200,
		Status:     "OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

// thsReal600580 线上实测报文（2026-09-20 抓取，卧龙电驱）。
// 字段：5=代码 6=昨收 7=今开 8=最高 9=最低 10=现价 13=成交量(股) 19=成交额(元)
// 199112=涨跌幅(%) 1968584=换手率(%) 264648=涨跌额 name=名称。
// 另保留若干本端不读的字段（402/527198/49 等），用于验证未知 id 被安全忽略。
// Live payload captured 2026-09-20 for 600580, plus unread ids to prove unknown fields
// are safely ignored.
const thsReal600580 = `quotebridge_v2_realhead_hs_600580_last({"items":{` +
	`"402":"1562117511.00","407":"1557632511.00","527198":"13128044.000",` +
	`"10":"27.27","24":"27.27","25":"58690.00","30":"27.28","31":"155806.00",` +
	`"8":"27.63","9":"27.00","13":"24712384.00","19":"672864810.00","7":"27.25",` +
	`"15":"12611944.00","14":"11584340.00","69":"29.80","70":"24.38",` +
	`"49":"200.00","51":"","12":"17","17":"176800.00","74":"17100.00","75":"34.00",` +
	`"264648":"0.180","199112":"0.66","2942":"60.577","1968584":"1.587",` +
	`"2034120":"60.577","1378761":"27.228","526792":"2.326",` +
	`"5":"600580","6":"27.09","name":"\u5367\u9f99\u7535\u9a71"}})`

// TestParseTHSQuoteRealShape 真实扁平字典报文应完整解析出全部字段。
// 关键：换手率（1968584）——新浪无此字段，正是必须靠同花顺补的那一列。
// English: the real flat-dict payload must parse fully; turnover (1968584) is the column
// only THS can supply (Sina has no turnover field).
func TestParseTHSQuoteRealShape(t *testing.T) {
	si, err := parseTHSQuote([]byte(thsReal600580), "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	if si.Name != "卧龙电驱" {
		t.Errorf("name 应=卧龙电驱, got %q（\\u 转义未解码?）", si.Name)
	}
	if si.Price != 27.27 {
		t.Errorf("Price 应=27.27, got %.2f", si.Price)
	}
	if si.PrevClose != 27.09 || si.Close != 27.09 {
		t.Errorf("昨收应=27.09, got PrevClose=%.2f Close=%.2f", si.PrevClose, si.Close)
	}
	if si.Open != 27.25 || si.High != 27.63 || si.Low != 27.00 {
		t.Errorf("开/高/低错误: %.2f/%.2f/%.2f", si.Open, si.High, si.Low)
	}
	if si.Volume != 24712384 {
		t.Errorf("成交量应=24712384(股), got %.0f", si.Volume)
	}
	if si.Amount != 672864810 {
		t.Errorf("成交额应=672864810(元), got %.0f", si.Amount)
	}
	if si.ChangePct != 0.66 {
		t.Errorf("涨跌幅应=0.66%%, got %.4f", si.ChangePct)
	}
	if si.Turnover != 1.587 {
		t.Errorf("换手率应=1.587%%, got %.4f", si.Turnover)
	}
	// 字段齐全时以字段为准，不用昨收推算（口径不混）。
	if want := (27.27 - 27.09) / 27.09 * 100; si.ChangePct != 0.66 {
		t.Errorf("字段存在时应直取 0.66 而非推算 %.4f", want)
	}
}

// TestParseTHSQuoteDerivesChangePct 涨跌幅字段缺失时才用昨收推算（旧行为保留，但需有条件）。
// English: derive change pct from prev close only when the field is absent.
func TestParseTHSQuoteDerivesChangePct(t *testing.T) {
	body := []byte(`quotebridge_v2_realhead_hs_600580_last({"items":{"5":"600580","6":"35.67","10":"36.86","name":"test"}})`)
	si, err := parseTHSQuote(body, "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	expect := (36.86 - 35.67) / 35.67 * 100
	if diff := si.ChangePct - expect; diff > 0.01 || diff < -0.01 {
		t.Errorf("ChangePct 应≈%.4f, got %.4f", expect, si.ChangePct)
	}
}

// TestParseTHSQuoteCodeMismatch 响应串号必须报错降级，绝不把别人的价格当成这只票的。
// English: a code mismatch must error out so the caller degrades — never attribute another
// security's price to this one.
func TestParseTHSQuoteCodeMismatch(t *testing.T) {
	body := []byte(`quotebridge_v2_realhead_hs_600580_last({"items":{"5":"000001","10":"27.27"}})`)
	if _, err := parseTHSQuote(body, "600580"); err == nil {
		t.Fatal("代码不一致应返回错误")
	} else if !strings.Contains(err.Error(), "code mismatch") {
		t.Errorf("错误信息应含 code mismatch, got %v", err)
	}
}

// TestParseTHSQuoteNoPrice 无现价（停牌/空报文）应报错，不得返回零价行情。
// English: a missing price must error rather than yield a zero-price quote.
func TestParseTHSQuoteNoPrice(t *testing.T) {
	body := []byte(`quotebridge_v2_realhead_hs_600580_last({"items":{"5":"600580","name":"x"}})`)
	if _, err := parseTHSQuote(body, "600580"); err == nil {
		t.Fatal("缺现价应返回错误")
	}
	// 完全不符形状的报文（旧夹具那种 data.items 位置数组）也必须报错——
	// 这条断言存在的意义就是防止解析器再退回旧形态。
	legacy := []byte(`{"data":{"items":{"1":["","hs_1.600580","卧龙电驱",35.0,39.08,34.67,36.86,1.0,2.0,35.67]}}}`)
	if _, err := parseTHSQuote(legacy, "600580"); err == nil {
		t.Fatal("旧形态（data.items 位置数组）不应再被解析成功")
	}
}

// TestParseTHSQuoteNumericTolerance 值若被上游改回数值型仍可解析（字符串为主形态）。
// English: tolerate the upstream switching values back to numbers (strings are the main form).
func TestParseTHSQuoteNumericTolerance(t *testing.T) {
	body := []byte(`{"items":{"5":"600580","6":27.09,"7":27.25,"8":27.63,"9":27.00,"10":27.27,"13":24712384,"19":672864810,"199112":0.66,"1968584":1.587}}`)
	si, err := parseTHSQuote(body, "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	if si.Price != 27.27 || si.Turnover != 1.587 || si.Volume != 24712384 {
		t.Errorf("数值型容错失败: price=%.2f turnover=%.4f vol=%.0f", si.Price, si.Turnover, si.Volume)
	}
}

// TestTHSURLsUseBareCode URL 必须用 bare hs_{6位代码}，带市场前缀是被上游 404 的旧写法。
// English: URLs must use the bare hs_{code} form; the market-prefixed form 404s upstream.
func TestTHSURLsUseBareCode(t *testing.T) {
	// 带 .SH 后缀也要能剥干净。
	if got, want := thsRealheadURL("600580.SH"), "https://d.10jqka.com.cn/v2/realhead/hs_600580/last.js"; got != want {
		t.Errorf("thsRealheadURL: got %s want %s", got, want)
	}
	if got, want := thsLineURL("300489", thsScaleDaily), "https://d.10jqka.com.cn/v6/line/hs_300489/01/last.js"; got != want {
		t.Errorf("thsLineURL daily: got %s want %s", got, want)
	}
	// 分钟线 scale 必须是 60（1 分钟）；旧的 06 上游 404/502。
	if got, want := thsLineURL("300489", thsScaleOneMin), "https://d.10jqka.com.cn/v6/line/hs_300489/60/last.js"; got != want {
		t.Errorf("thsLineURL minute: got %s want %s", got, want)
	}
	if thsScaleOneMin == "06" {
		t.Error("分钟线 scale 不得退回无效值 06")
	}
	for _, u := range []string{thsRealheadURL("600580"), thsLineURL("600580", thsScaleDaily)} {
		if strings.Contains(u, "hs_0.") || strings.Contains(u, "hs_1.") {
			t.Errorf("URL 不得含市场前缀: %s", u)
		}
	}
}

// TestTHSKLineRequestURLs 锁定"实际发出的请求 URL"——不只是 thsLineURL 辅助函数，
// 而是 GetTHSKLine / GetTHSMinuteKLine 真实走到的地址，防止再次退回带市场前缀 / 无效 scale。
// English: locks the URLs actually requested by GetTHSKLine / GetTHSMinuteKLine.
func TestTHSKLineRequestURLs(t *testing.T) {
	rt := &thsRecorderTransport{
		dailyBody: `quotebridge_v6_line_hs_300489_01_last({"total":"2",` +
			`"data":"20260917,222.00,226.50,219.80,223.60,15000000,3350000000.00,10.900,,0.00,0;` +
			`20260918,227.00,231.80,215.02,224.62,16119800,3594643900.00,11.696,,0.00,0"})`,
		minuteBody: `quotebridge_v6_line_hs_300489_60_last({"total":"1",` +
			`"data":"202609181500,224.50,224.62,224.40,224.62,313100,70300000.00,0.210,,,0"})`,
	}
	tc := NewTHSClient()
	tc.SetTransport(rt)

	kl, err := tc.GetTHSKLine("300489")
	if err != nil || len(kl) != 2 {
		t.Fatalf("GetTHSKLine: err=%v n=%d", err, len(kl))
	}
	if kl[1].Close != 224.62 || kl[1].Amount != 3594643900 {
		t.Errorf("日线末根 close/amount 错误: %.2f/%.0f", kl[1].Close, kl[1].Amount)
	}
	mk, err := tc.GetTHSMinuteKLine("300489", 5)
	if err != nil || len(mk) != 1 {
		t.Fatalf("GetTHSMinuteKLine: err=%v n=%d", err, len(mk))
	}
	if got := mk[0].Date.Format("2006-01-02 15:04"); got != "2026-09-18 15:00" {
		t.Errorf("分钟线时间应2026-09-18 15:00, got %s", got)
	}
	// 不支持的周期必须报错降级，不得拿别的周期顶替（15 分钟同花顺无此档）。
	if _, err := tc.GetTHSMinuteKLine("300489", 15); err == nil {
		t.Error("15 分钟周期应返回错误（同花顺无此档，须降级而非顶替）")
	}

	if len(rt.gotURL) != 2 {
		t.Fatalf("应发出 2 次请求, got %d: %v", len(rt.gotURL), rt.gotURL)
	}
	wantDaily := "https://d.10jqka.com.cn/v6/line/hs_300489/01/last.js"
	// 5 分钟必须映射到 scale 30（1 分钟是 60）——映射错了 MACD 口径就错了。
	wantMinute := "https://d.10jqka.com.cn/v6/line/hs_300489/30/last.js"
	if rt.gotURL[0] != wantDaily {
		t.Errorf("日线 URL 错误\n got  %s\n want %s", rt.gotURL[0], wantDaily)
	}
	if rt.gotURL[1] != wantMinute {
		t.Errorf("5 分钟线 URL 错误（须映射到 scale 30）\n got  %s\n want %s", rt.gotURL[1], wantMinute)
	}
	for _, u := range rt.gotURL {
		if strings.Contains(u, "hs_0.") || strings.Contains(u, "hs_1.") || strings.Contains(u, "/06/") {
			t.Errorf("URL 含旧缺陷形态（市场前缀或无效 scale 06）: %s", u)
		}
	}
}

// TestTHSIntradayScaleMap 周期→scale 映射全表锁定（穷举实测 2026-09-20）。
// English: locks the period→scale map (exhaustively measured 2026-09-20).
func TestTHSIntradayScaleMap(t *testing.T) {
	want := map[int]string{1: "60", 5: "30", 30: "40", 60: "50"}
	for mins, code := range want {
		got, ok := thsIntradayScale(mins)
		if !ok || got != code {
			t.Errorf("%d 分钟应映射到 scale %s, got %q ok=%v", mins, code, got, ok)
		}
	}
	for _, bad := range []int{0, 2, 3, 10, 15, 20, 45, 120} {
		if got, ok := thsIntradayScale(bad); ok {
			t.Errorf("%d 分钟不受支持，不得返回映射（got %q）", bad, got)
		}
	}
}

// TestTHSQuoteRequestURL GetQuote 必须打到 bare hs_{code} 的 realhead 地址。
// English: GetQuote must hit the bare hs_{code} realhead URL.
func TestTHSQuoteRequestURL(t *testing.T) {
	rt := &thsRecorderTransport{dailyBody: thsReal600580}
	tc := NewTHSClient()
	tc.SetTransport(rt)

	si, err := tc.GetQuote("600580.SH")
	if err != nil || si == nil {
		t.Fatalf("GetQuote: %v", err)
	}
	want := "https://d.10jqka.com.cn/v2/realhead/hs_600580/last.js"
	if len(rt.gotURL) != 1 || rt.gotURL[0] != want {
		t.Errorf("realhead URL 错误\n got  %v\n want %s", rt.gotURL, want)
	}
}
