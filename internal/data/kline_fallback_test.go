// kline_fallback_test.go — 腾讯/同花顺 K线解析单元测试：验证字段序、分钟K时间格式、脏数据剔除与降级链防护。
package data

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestParseTencentDayKLine 腾讯日K字段序 [date, open, close, high, low, volume]。
// English: TestParseTencentDayKLine Tencent daily-K field order [date, open, close, high, low, volume].
func TestParseTencentDayKLine(t *testing.T) {
	body := `{"code":0,"msg":"","data":{"sh600206":{"qfqday":[
		["2026-07-31","38.640","35.390","38.750","35.330","1087126.000"],
		["2026-08-03","34.500","33.150","35.770","33.000","837131.000"],
		["2026-08-06","40.800","43.790","43.790","40.500","1030100.000"]
	]}}}`

	// 解析入口吃的是"行数组"而非原始报文（上面的 body 仅示意抓取到的格式），
	// 这里刻意用 open>close 且 high/low 与首列不同量级的数据，确保字段序错位就会被下面断言抓到。
	klines, err := parseTencentKLine([][]string{
		{"2026-07-31", "38.640", "35.390", "38.750", "35.330", "1087126.000"},
		{"2026-08-03", "34.500", "33.150", "35.770", "33.000", "837131.000"},
		{"2026-08-06", "40.800", "43.790", "43.790", "40.500", "1030100.000"},
	}, false)
	if err != nil {
		t.Fatalf("parseTencentKLine: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("应3根, got %d", len(klines))
	}
	// 字段序：open=idx1, close=idx2, high=idx3, low=idx4
	// English: field order: open=idx1, close=idx2, high=idx3, low=idx4
	if klines[0].Open != 38.640 || klines[0].Close != 35.390 {
		t.Errorf("首根 open/close 错误: %.3f/%.3f", klines[0].Open, klines[0].Close)
	}
	if klines[0].High != 38.750 || klines[0].Low != 35.330 {
		t.Errorf("首根 high/low 错误: %.3f/%.3f", klines[0].High, klines[0].Low)
	}
	if klines[2].Close != 43.790 {
		t.Errorf("末根 close 应43.79, got %.3f", klines[2].Close)
	}
	_ = body // body 仅示意，实际解析走行数组
	// English: body is only illustrative; parsing actually goes through the row array
}

// TestParseTencentMinuteKLine 腾讯分钟K时间格式 yyyyMMddHHMM 与升序排序。
// English: TestParseTencentMinuteKLine Tencent minute-K time format yyyyMMddHHMM and ascending sorting.
func TestParseTencentMinuteKLine(t *testing.T) {
	rows := [][]string{
		{"202608071445", "48.17", "48.17", "48.17", "48.17", "1703.00", "{}", "2.01"},
		{"202608071430", "48.00", "48.10", "48.15", "47.90", "1500.00", "{}", "1.80"},
		{"202608071435", "48.17", "48.17", "48.17", "48.17", "1212.00", "{}", "1.43"},
	}
	klines, err := parseTencentKLine(rows, true)
	if err != nil {
		t.Fatalf("parseTencentKLine minute: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("应3根, got %d", len(klines))
	}
	if !klines[0].Date.Before(klines[1].Date) || !klines[1].Date.Before(klines[2].Date) {
		t.Errorf("分钟K应按时间升序: %v %v %v", klines[0].Date, klines[1].Date, klines[2].Date)
	}
	if klines[2].Date.Format("1504") != "1445" {
		t.Errorf("末根时间应14:45, got %s", klines[2].Date.Format("1504"))
	}
}

// TestParseTencentKLineInvalid 脏行/非法K线（high<low 等）应被剔除，全部无效返回错误。
// English: TestParseTencentKLineInvalid dirty rows/invalid K-lines (e.g. high<low) should be dropped; returns an error if all are invalid.
func TestParseTencentKLineInvalid(t *testing.T) {
	rows := [][]string{
		{"2026-08-06", "40.800", "43.790", "30.000", "40.500", "1030100.000"}, // high<low
		// English: high<low
		{"bad-date", "40.800", "43.790", "43.790", "40.500", "1030100.000"}, // 日期非法
		// English: invalid date
		{"2026-08-07", "0", "0", "0", "0", "0"}, // 数值为0
		// English: value is 0
	}
	klines, err := parseTencentKLine(rows, false)
	if err == nil || len(klines) != 0 {
		t.Fatalf("全无效行应返回错误且空, got err=%v klines=%d", err, len(klines))
	}
	if err != nil && !strings.Contains(err.Error(), "no valid rows") {
		t.Errorf("错误信息应含 no valid rows, got %v", err)
	}
}

// TestGetTencentKLineRefusesUnadjustedFallback §H3（2026-09-22 PM 批）：响应只有不复权
// "day" 数组（无 qfqday）时 GetTencentKLine 必须**报错拒收**——旧实现静默回退 stk.Day，
// 调用方按"前复权"名义拿到不复权数据，除权日 MA/涨跌幅失真且无痕。
// English: §H3 — with qfqday absent the client must refuse instead of passing unadjusted bars
// under the qfq label.
func TestGetTencentKLineRefusesUnadjustedFallback(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"code":0,"msg":"","data":{"sh600206":{"day":[["2026-08-06","45.80","48.79","48.79","45.50","1030100.000"]]}}}`
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(body)),
		}, nil
	}))
	if _, err := m.GetTencentKLine("600206", 5); err == nil || !strings.Contains(err.Error(), "qfqday missing") {
		t.Fatalf("无 qfqday 应拒收并报错，got err=%v", err)
	}
}

// TestParseTHSLine 同花顺 JSONP K线：data 为 **";" 分隔的单条字符串**（实测 2026-09-20）。
// 行内字段序与东财一致：[日期,开,高,低,收,成交量(股),成交额(元),换手率(%),…]。
// English: TestParseTHSLine THS JSONP K-line — data is a single ";"-separated string
// (measured 2026-09-20); row field order matches Eastmoney.
func TestParseTHSLine(t *testing.T) {
	// 报文按线上真实形状抓取（含 JSONP 外壳 + 字符串 data + 11 列行），非人工构造。
	body := []byte(`quotebridge_v6_line_hs_600206_01_last({"num":3,"name":"\u6709\u7814\u65b0\u6750","total":"3",` +
		`"data":"20260804,33.67,36.36,32.93,35.00,967576,355000000.00,1.200,,0.00,0;` +
		`20260805,35.00,39.81,35.00,39.00,881698,350000000.00,1.150,,0.00,0;` +
		`20260806,39.00,43.79,39.00,43.79,1030100,420000000.00,1.480,,0.00,0"})`)
	klines, err := parseTHSLine(body, false)
	if err != nil {
		t.Fatalf("parseTHSLine: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("应3根, got %d", len(klines))
	}
	if klines[2].Close != 43.79 {
		t.Errorf("末根 close 应43.79, got %.2f", klines[2].Close)
	}
	if klines[2].Amount != 420000000 {
		t.Errorf("末根 amount 应420000000, got %.0f", klines[2].Amount)
	}
	if klines[0].Date.Format("2006-01-02") != "2026-08-04" {
		t.Errorf("首根日期应2026-08-04, got %s", klines[0].Date.Format("2006-01-02"))
	}
	if klines[0].Volume != 967576 {
		t.Errorf("首根 volume 应967576(股), got %.0f", klines[0].Volume)
	}
}

// TestParseTHSLineDataStringShape 字符串 data（主形态）必须能解析——这是 2026-09-20 修复的缺陷本体：
// 旧实现按 []string 反序列化，线上恒定 "no data"，同花顺 K 线降级源从未生效。
// English: locks the bug fixed on 2026-09-20 — the old []string unmarshal always failed on the
// real string-shaped data, so the THS K-line fallback never worked.
func TestParseTHSLineDataStringShape(t *testing.T) {
	body := []byte(`quotebridge_v6_line_hs_300489_01_last({"total":"2",` +
		`"data":"20260917,222.00,226.50,219.80,223.60,15000000,3350000000.00,10.900,,0.00,0;` +
		`20260918,227.00,231.80,215.02,224.62,16119800,3594643900.00,11.696,,0.00,0"})`)
	klines, err := parseTHSLine(body, false)
	if err != nil {
		t.Fatalf("字符串 data 应可解析, got err=%v", err)
	}
	if len(klines) != 2 {
		t.Fatalf("应2根, got %d", len(klines))
	}
	if klines[1].Close != 224.62 || klines[1].Low != 215.02 {
		t.Errorf("末根 close/low 错误: %.2f/%.2f", klines[1].Close, klines[1].Low)
	}
	// 数组形态保留兼容（上游若改型不至于整源失能）。
	arr := []byte(`quotebridge_v6_line_hs_300489_01_last({"data":["20260918,227.00,231.80,215.02,224.62,16119800,3594643900.00,11.696"]})`)
	if kl, err := parseTHSLine(arr, false); err != nil || len(kl) != 1 {
		t.Errorf("数组 data 兼容形态应可解析, got err=%v n=%d", err, len(kl))
	}
}

// TestParseTHSLineMinuteTime 分钟线时间列为 12 位 yyyyMMddHHmm（实测 scale 60）。
// English: minute K-line time column is the 12-digit yyyyMMddHHmm form (measured at scale 60).
func TestParseTHSLineMinuteTime(t *testing.T) {
	body := []byte(`quotebridge_v6_line_hs_300489_60_last({"total":"3",` +
		`"data":"202609181500,224.50,224.62,224.40,224.62,313100,70300000.00,0.210,,,0;` +
		`202609181459,224.30,224.55,224.28,224.50,120000,26900000.00,0.081,,,0;` +
		`202609181458,224.10,224.35,224.05,224.30,98000,21900000.00,0.066,,,0"})`)
	klines, err := parseTHSLine(body, true)
	if err != nil {
		t.Fatalf("parseTHSLine minute: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("应3根, got %d", len(klines))
	}
	if got := klines[0].Date.Format("2006-01-02 15:04"); got != "2026-09-18 15:00" {
		t.Errorf("首根时间应2026-09-18 15:00, got %s (12位 yyyyMMddHHmm 未识别?)", got)
	}
	if klines[0].Close != 224.62 {
		t.Errorf("首根 close 应224.62, got %.2f", klines[0].Close)
	}
}

// TestParseTHSLineInvalid 非法内容/空 data 应返回错误（保证降级链不会喂入脏数据）。
// English: TestParseTHSLineInvalid invalid content/empty data should return an error (so the fallback chain never feeds dirty data).
func TestParseTHSLineInvalid(t *testing.T) {
	if _, err := parseTHSLine([]byte(`quotebridge_xxx({})`), false); err == nil {
		t.Fatal("空 data 应返回错误")
	}
	if _, err := parseTHSLine([]byte(`not json`), true); err == nil {
		t.Fatal("非法 JSONP 应返回错误")
	}
	// 全脏行（high<low / 数值为 0）也必须报错而非静默返回半截数据。
	dirty := []byte(`quotebridge_v6_line_hs_300489_01_last({"data":"20260918,227.00,215.02,231.80,224.62,1,1.00,1.000,,0.00,0;bad-date,1,2,0.5,1.5,1,1.00,1.000,,0.00,0"})`)
	if _, err := parseTHSLine(dirty, false); err == nil {
		t.Fatal("全脏行应返回错误")
	}
}
