// IPO 启动板块事件测试：未上市公司（宇树科技等）经 LLM 产业链分析，
// 产出 Level=板块 且带 Sectors/上下游个股的事件，修复"新股上市归因不出板块"断链。
package newsagent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// mockIPOTransport 提供东财新股日历 mock（宇树科技 688836 明日上市）。
// 上市日期用相对日期（明天）：parseEastMoneyIPO 只保留今日及以后的记录，
// 写死的历史日期会随时间腐烂（20260819 实录：测试跑过 fixture 日期后集体失效）。
type mockIPOTransport struct{}

// RoundTrip 实现 http.RoundTripper：拦截东财 IPO 日历请求并返回 mock 新股数据（宇树科技明日上市）。
func (m *mockIPOTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 仅拦截东财 IPO 日历，其余返回空
	if strings.Contains(req.URL.Host, "eastmoney.com") && strings.Contains(req.URL.RawQuery, "IPOAPPLY") {
		tomorrow := time.Now().AddDate(0, 0, 1).Format("20060102")
		apply := time.Now().Format("20060102")
		body := fmt.Sprintf(`{"success":true,"result":{"data":[
			{"SECURITY_CODE":"688836","SECURITY_NAME":"宇树科技","APPLY_DATE":%q,"ISSUE_PRICE":150.80,"LISTING_DATE":%q,"SECURITY_MARKET_ABBR":"科创板"}
		]}}`, apply, tomorrow)
		return &http.Response{
			StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{"data":[]}}`))}, nil
}

// 模拟 LLM chat 端点：根据"宇树科技"返回机器人板块 + 上下游传导。
func newMockLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"level":"板块","sentiment":"正面","score":0.8,"direction":"利好","impact_level":"高","event_type":"行业","urgency":"关注","sectors":["机器人"],"upstream_sectors":["机器人"],"downstream_sectors":["人形机器人"],"related_stocks":["卧龙电驱","三花智控","绿的谐波","双环传动"],"upstream_stocks":["卧龙电驱","三花智控"],"downstream_stocks":["绿的谐波","双环传动"],"strategy":"龙头","reason":"宇树科技上市将带动人形机器人产业链价值传导，上游核心供应商直接受益"}`},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	return srv
}

// TestBuildIPOBootEvents 验证：宇树科技(688836) 明日上市 → LLM 分析出机器人板块事件，
// Level=板块、含机器人 Sectors、上下游个股（卧龙电驱/三花智控）传导。
func TestBuildIPOBootEvents(t *testing.T) {
	// LLM mock
	llmSrv := newMockLLMServer(t)
	defer llmSrv.Close()
	llmClient := llm.New(llm.Config{APIURL: llmSrv.URL, APIKey: "test-key", Model: "mock", Streaming: false})

	// MarketAPI mock
	api := data.NewMarketAPI()
	api.SetTransport(&mockIPOTransport{})

	a := New(api, llmClient, nil, t.TempDir())
	evs := a.BuildIPOBootEvents()
	if len(evs) == 0 {
		t.Fatal("应产出至少 1 个 IPO 启动板块事件")
	}

	// 同向传导应合并为 1 个事件
	ev := evs[0]
	if ev.Level != "板块" {
		t.Fatalf("IPO启动事件应为板块级, got %q", ev.Level)
	}
	foundRobot := false
	for _, s := range ev.Sectors {
		if s == "机器人" {
			foundRobot = true
		}
	}
	if !foundRobot {
		t.Fatalf("事件应归因机器人板块, got %v", ev.Sectors)
	}
	if !containsStr(ev.RelatedStocks, "卧龙电驱") || !containsStr(ev.RelatedStocks, "三花智控") {
		t.Fatalf("事件应含上下游影响个股(卧龙电驱/三花智控), got %v", ev.RelatedStocks)
	}
	if ev.Score <= 0 || ev.Direction != "利好" {
		t.Fatalf("IPO启动事件应利好正分, got score=%v dir=%s", ev.Score, ev.Direction)
	}

	// 缓存生效：同日第二次调用不再调 LLM（不产出重复）
	before := len(evs)
	again := a.BuildIPOBootEvents()
	if len(again) != 0 {
		t.Fatalf("同日重复调用应被缓存拦截(防每轮全跑), got %d", len(again))
	}
	_ = before
}

// TestBuildIPOBootEventsCacheReset 跨交易日缓存重置：新的交易日重新分析。
func TestBuildIPOBootEventsCacheReset(t *testing.T) {
	llmSrv := newMockLLMServer(t)
	defer llmSrv.Close()
	llmClient := llm.New(llm.Config{APIURL: llmSrv.URL, APIKey: "test-key", Model: "mock", Streaming: false})
	api := data.NewMarketAPI()
	api.SetTransport(&mockIPOTransport{})
	a := New(api, llmClient, nil, t.TempDir())

	if evs := a.BuildIPOBootEvents(); len(evs) == 0 {
		t.Fatal("首次调用应产出事件")
	}
	// 模拟跨交易日：强制清缓存（等价新交易日首轮）
	a.bootCacheDay = ""
	a.bootCache = nil
	if evs := a.BuildIPOBootEvents(); len(evs) == 0 {
		t.Fatal("跨交易日应重新分析产出事件")
	}
}

// TestBuildIPOBootEventsNoLLM LLM 未配置时不产出（不 panic）。
func TestBuildIPOBootEventsNoLLM(t *testing.T) {
	api := data.NewMarketAPI()
	api.SetTransport(&mockIPOTransport{})
	a := New(api, nil, nil, t.TempDir())
	if evs := a.BuildIPOBootEvents(); len(evs) != 0 {
		t.Fatalf("LLM未配置不应产出, got %d", len(evs))
	}
}

// TestBuildIPOBootEventsSkipsListed 已上市新股不产出启动事件。
func TestBuildIPOBootEventsSkipsListed(t *testing.T) {
	llmSrv := newMockLLMServer(t)
	defer llmSrv.Close()
	llmClient := llm.New(llm.Config{APIURL: llmSrv.URL, APIKey: "test-key", Model: "mock", Streaming: false})
	api := data.NewMarketAPI()
	api.SetTransport(&listedIPOTransport{})
	a := New(api, llmClient, nil, t.TempDir())
	if evs := a.BuildIPOBootEvents(); len(evs) != 0 {
		t.Fatalf("已上市新股不应产出启动事件, got %d", len(evs))
	}
}

// listedIPOTransport 返回已上市新股（ListStatus=L）。
type listedIPOTransport struct{}

// RoundTrip 实现 http.RoundTripper：返回已上市新股（ListStatus=L）的 mock 数据。
func (m *listedIPOTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "eastmoney.com") && strings.Contains(req.URL.RawQuery, "IPOAPPLY") {
		body := `{"success":true,"result":{"data":[
			{"SECURITY_CODE":"688836","SECURITY_NAME":"宇树科技","APPLY_DATE":"20260810","ISSUE_PRICE":150.80,"LISTING_DATE":"20260812","LIST_STATUS":"L"}
		]}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{"data":[]}}`))}, nil
}
