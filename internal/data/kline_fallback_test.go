package data

import (
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

// TestParseTHSLine 同花顺 JSONP K线（data 为 CSV 字符串数组，字段同东财顺序）。
// English: TestParseTHSLine Tonghuashun JSONP K-line (data is a CSV string array, field order same as Eastmoney).
func TestParseTHSLine(t *testing.T) {
	body := []byte(`quotebridge_v6_line_hs_1.600206_01_last({
		"data":[
			"2026-08-04,33.67,36.36,32.93,35.00,967576,355000000",
			"2026-08-05,35.00,39.81,35.00,39.00,881698,350000000",
			"2026-08-06,39.00,43.79,39.00,43.79,1030100,420000000"
		]
	})`)
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
}
