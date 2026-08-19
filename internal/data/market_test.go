package data

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// testResp 构造一个 mock HTTP 响应（本地 helper，避免跨包依赖）。
// English: testResp builds a mock HTTP response (local helper to avoid cross-package dependencies).
func testResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// listMockTransport 按 host 区分响应：新浪列表 host 返回可切换结果，东财返回股票列表 JSON。
// 另按 clist 的 pz 参数区分 GetStockList（pz=10000→diff map）与 GetStockPE（pz=1→diff 数组），
// 并统计 PE 请求次数以验证 TTL 缓存命中。
// English: listMockTransport routes responses by host: the Sina list host returns switchable results, Eastmoney returns stock-list JSON.
// English: It also distinguishes GetStockList (pz=10000 → diff map) from GetStockPE (pz=1 → diff array) by the clist pz parameter, and counts PE requests to verify TTL cache hits.
type listMockTransport struct {
	sinaFail bool
	peCalls  int
}

func (rt *listMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch {
	case strings.Contains(host, "vip.stock.finance.sina.com.cn"):
		if rt.sinaFail {
			return testResp(500, "upstream error"), nil
		}
		return testResp(200,
			`[{"symbol":"sh600580","code":"600580","name":"卧龙电驱"},{"symbol":"sz300750","code":"300750","name":"宁德时代"}]`), nil
	case strings.Contains(host, "push2.eastmoney.com") && req.URL.Path == "/api/qt/clist/get":
		if req.URL.Query().Get("pz") == "1" {
			// GetStockPE 单查：diff 为数组（f9 市盈率）
			// English: GetStockPE single query: diff is an array (f9 = P/E ratio)
			rt.peCalls++
			price := "18.5"
			if strings.Contains(req.URL.Query().Get("fs"), "300750") {
				price = "22.1"
			}
			return testResp(200,
				`{"data":{"total":1,"diff":[{"f12":"A","f14":"X","f9":`+price+`}]}}`), nil
		}
		// GetStockList：diff 为 map
		// English: GetStockList: diff is a map
		return testResp(200,
			`{"data":{"total":2,"diff":{"0":{"f12":"600580","f14":"卧龙电驱","f9":18.5,"f20":1.2e11},"1":{"f12":"300750","f14":"宁德时代","f9":22.1,"f20":8e11}}}}`), nil
	}
	return testResp(404, ""), nil
}

// TestGetStockPEAndTTLCache PE 预取：东财 clist 单查 f9 市盈率；TTL 缓存命中时二次调用不再请求。
// English: TestGetStockPEAndTTLCache PE prefetch: Eastmoney clist single query of f9 P/E ratio; when the TTL cache hits, a second call makes no further request.
func TestGetStockPEAndTTLCache(t *testing.T) {
	rt := &listMockTransport{}
	m := NewMarketAPI()
	m.SetTransport(rt)

	pe := m.GetStockPE("600580")
	if pe <= 0 {
		t.Fatalf("GetStockPE 应>0, got %v", pe)
	}
	if diff := pe - 18.5; diff > 0.01 || diff < -0.01 {
		t.Errorf("600580 PE 应=18.5, got %.2f", pe)
	}

	// 第二次调用命中 TTL 缓存，不再发起网络请求
	// English: the second call hits the TTL cache and makes no network request
	if got := m.GetStockPE("600580"); got != pe {
		t.Errorf("二次调用应命中缓存返回 %.2f, got %.2f", pe, got)
	}
	if rt.peCalls != 1 {
		t.Errorf("PE 请求应仅1次(缓存命中), got %d", rt.peCalls)
	}
}

// TestGetStockListSinaPrimary 新浪主源成功时优先返回新浪列表。
// English: TestGetStockListSinaPrimary returns the Sina list first when the Sina primary source succeeds.
func TestGetStockListSinaPrimary(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(&listMockTransport{sinaFail: false})
	list, err := m.GetStockList()
	if err != nil {
		t.Fatalf("GetStockList: %v", err)
	}
	if list["卧龙电驱"] != "600580" || list["宁德时代"] != "300750" {
		t.Errorf("新浪主源应返回2只, got %v", list)
	}
}

// TestGetStockListFallbackEastMoney 新浪失败时兜底东财列表（含 PE/市值字段解析）。
// English: TestGetStockListFallbackEastMoney falls back to the Eastmoney list when Sina fails (including PE/market-cap field parsing).
func TestGetStockListFallbackEastMoney(t *testing.T) {
	m := NewMarketAPI()
	m.SetTransport(&listMockTransport{sinaFail: true})
	list, err := m.GetStockList()
	if err != nil {
		t.Fatalf("兜底东财: %v", err)
	}
	if list["卧龙电驱"] != "600580" || list["宁德时代"] != "300750" {
		t.Errorf("东财兜底应返回2只, got %v", list)
	}
}
