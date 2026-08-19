// Tushare 客户端解析测试（httptest 模拟官方响应，无需真实 token）。
// English: Tushare client parsing tests (httptest mocks the official response, no real token needed).
package data

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockTushare 起一个模拟 Tushare 服务端，返回给定的响应体；并校验请求载荷。
// English: mockTushare starts a mock Tushare server returning the given response body and verifies the request payload.
func mockTushare(t *testing.T, code int, msg string, fields []string, items [][]any, checkPayload func(map[string]any)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if checkPayload != nil {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			checkPayload(p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code": code, "msg": msg,
			"data": map[string]any{"fields": fields, "items": items},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestCallParse 验证字段映射、lowercase 归一、null 处理与数值类型。
// English: TestCallParse verifies field mapping, lowercase normalization, null handling, and numeric types.
func TestCallParse(t *testing.T) {
	old := tushareAPI
	t.Cleanup(func() { tushareAPI = old })
	tushareAPI = mockTushare(t, 0, "ok",
		[]string{"ts_code", "trade_date", "close", "pct_chg", "is_open"},
		[][]any{{"600000.SH", "20250102", 11.8, 3.2, 1}, {"000001.SZ", "20250103", nil, -1.5, nil}},
		nil)

	c := NewTushareClient("test-token")
	rows, err := c.Call("daily", map[string]string{"trade_date": "20250102"}, "ts_code,trade_date,close,pct_chg,is_open")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	// 字段名 lowercased 后可读
	// English: field names are readable once lowercased
	if rows[0].S("ts_code") != "600000.SH" || rows[0].S("TS_CODE") != "600000.SH" {
		t.Fatalf("ts_code 解析异常: %q", rows[0].S("ts_code"))
	}
	if rows[0].F("close") != 11.8 || rows[0].F("pct_chg") != 3.2 {
		t.Fatalf("close/pct_chg 解析异常")
	}
	// null → nil → S/F 回落默认值
	// English: null → nil → S/F fall back to default values
	if rows[1].S("close") != "" || rows[1].F("close") != 0 || rows[1].I("is_open") != 0 {
		t.Fatalf("null 处理异常: %+v", rows[1])
	}
}

// TestCallBusinessError 验证业务失败（积分不足等）返回错误。
// English: TestCallBusinessError verifies that business failures (e.g. insufficient points) return an error.
func TestCallBusinessError(t *testing.T) {
	old := tushareAPI
	t.Cleanup(func() { tushareAPI = old })
	tushareAPI = mockTushare(t, 2001, "积分不足", nil, nil, nil)

	c := NewTushareClient("bad-token")
	if _, err := c.Call("daily", nil, "ts_code"); err == nil {
		t.Fatal("期望业务错误，未得到")
	}
}

// TestCallPayload 验证请求载荷（api_name/token/params/fields）。
// English: TestCallPayload verifies the request payload (api_name/token/params/fields).
func TestCallPayload(t *testing.T) {
	old := tushareAPI
	t.Cleanup(func() { tushareAPI = old })
	tushareAPI = mockTushare(t, 0, "ok", []string{"a"}, [][]any{{1}}, func(p map[string]any) {
		if p["api_name"] != "stock_basic" || p["token"] != "tok-1" {
			t.Fatalf("载荷异常: %+v", p)
		}
		if p["params"].(map[string]any)["trade_date"] != "20250102" {
			t.Fatalf("params 异常: %+v", p["params"])
		}
		if p["fields"] == "" {
			t.Fatal("fields 为空")
		}
	})

	c := NewTushareClient("tok-1")
	if _, err := c.Call("stock_basic", map[string]string{"trade_date": "20250102"}, "a"); err != nil {
		t.Fatalf("Call: %v", err)
	}
}
