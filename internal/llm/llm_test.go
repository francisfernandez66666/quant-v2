// llm_test.go — LLM 客户端批量/流式解析、容错清理与探活（Ping）的单元测试。
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAnalyzeHotTopicBatchIsolatesSubBatch LLM 批量失败（重试耗尽）时子批隔离：
// 返回 nil 占位（不做关键词兜底），不返回错误，主干继续；并报告失败批索引供留队重试。
func TestAnalyzeHotTopicBatchIsolatesSubBatch(t *testing.T) {
	c := New(Config{APIKey: ""}) // 无 key → Chat 必然失败
	titles := []string{"某公司涨停", "海外指数小幅波动", "某公司业绩暴跌"}

	results, failedIdx, err := c.AnalyzeHotTopicBatch(titles)
	if err != nil {
		t.Fatalf("子批隔离后不应返回错误，实际 %v", err)
	}
	if len(results) != len(titles) {
		t.Fatalf("期望返回 %d 条结果，实际 %d", len(titles), len(results))
	}
	for i, ht := range results {
		if ht != nil {
			t.Fatalf("第%d条失败子批应为 nil 占位（不做关键词兜底），实际非 nil", i)
		}
	}
	// 全批失败（无 key）→ 全部索引应标记失败
	if len(failedIdx) != len(titles) {
		t.Fatalf("期望 %d 条失败索引，实际 %d", len(titles), len(failedIdx))
	}
}

// TestAnalyzeHotTopicFailureReturnsNil LLM 单条失败（重试耗尽）应返回 nil,err，
// 不再返回关键词兜底结果，由调用方入重试队列。
func TestAnalyzeHotTopicFailureReturnsNil(t *testing.T) {
	c := New(Config{APIKey: ""}) // 无 key → Chat 必然失败
	ht, err := c.AnalyzeHotTopic("某公司涨停")
	if err == nil {
		t.Fatalf("无 key 应返回错误")
	}
	if ht != nil {
		t.Fatalf("失败时应返回 nil（不做关键词兜底），实际 %v", ht)
	}
}

// TestCleanJSONInvalidEscape 非法转义（如 9B 推理模型输出 \( ）应被剥掉反斜杠，
// 保留原字符，使响应可被 json.Unmarshal 正常解析。
func TestCleanJSONInvalidEscape(t *testing.T) {
	raw := "```json\n[{\"index\":1,\"reason\":\"磷化铟上游(云南锗业/有研新材)受益\\，海外自产确认价值\",\"sectors\":[\"半导体材料\"]}]\n```"
	got := cleanJSON(raw)
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("cleanJSON 后仍无法解析: %v\nraw=%q\ncleaned=%q", err, raw, got)
	}
	reason, _ := arr[0]["reason"].(string)
	if !strings.Contains(reason, "(云南锗业/有研新材)") {
		t.Fatalf("非法转义处理错误, reason=%q", reason)
	}
}

// TestCleanJSONValidEscape 合法转义（\n \uXXXX \" \\）必须原样保留，不得误伤。
func TestCleanJSONValidEscape(t *testing.T) {
	raw := `[{"index":1,"title":"A \"quoted\" \u5f53\u524d","reason":"line1\nline2","path":"a\\b"}]`
	got := cleanJSON(raw)
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("合法转义被误伤: %v\ncleaned=%q", err, got)
	}
	title, _ := arr[0]["title"].(string)
	if title != "A \"quoted\" 当前" {
		t.Fatalf("标题转义解析错误: %q", title)
	}
	reason, _ := arr[0]["reason"].(string)
	if !strings.Contains(reason, "line1\nline2") {
		t.Fatalf("\\n 转义解析错误: %q", reason)
	}
	path, _ := arr[0]["path"].(string)
	if path != `a\b` {
		t.Fatalf("\\\\ 转义解析错误: %q", path)
	}
}

// TestFlexibleFloatStringScore 9B 模型把 score 输出为字符串（"+0.75"）时也能解析。
func TestFlexibleFloatStringScore(t *testing.T) {
	var s struct {
		Score flexibleFloat `json:"score"`
	}
	for _, raw := range []string{`{"score":"+0.75"}`, `{"score":-0.5}`, `{"score":0.25}`, `{"score":" 1 "}`} {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("解析失败 %s: %v", raw, err)
		}
		if s.Score == 0 {
			t.Fatalf("score 不应为 0: %s", raw)
		}
	}
	// 非法值容错为 0，不崩
	var zero struct {
		Score flexibleFloat `json:"score"`
	}
	if err := json.Unmarshal([]byte(`{"score":"abc"}`), &zero); err != nil {
		t.Fatalf("非法 score 应容错不报错: %v", err)
	}
	if zero.Score != 0 {
		t.Fatalf("非法 score 应为 0, 实际 %v", zero.Score)
	}
}

// TestCleanJSONBarePlusNumber 裸 '+' 前缀数值（"score": +0.75）为非法 JSON，应剥掉 '+' 可解析。
func TestCleanJSONBarePlusNumber(t *testing.T) {
	raw := `[{"index":1,"score": +0.75,"sectors":["半导体材料"]},{"index":2,"score":-0.5}]`
	got := cleanJSON(raw)
	var arr []struct {
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("裸 + 号数值清理后仍无法解析: %v\ncleaned=%q", err, got)
	}
	if len(arr) != 2 || arr[0].Score != 0.75 || arr[1].Score != -0.5 {
		t.Fatalf("数值解析错误: %+v", arr)
	}
	// 字符串内的 '+' 不能被误删
	rawStr := `[{"reason":"A + B 传导"}]`
	gotStr := cleanJSON(rawStr)
	if !strings.Contains(gotStr, "A + B") {
		t.Fatalf("字符串内 + 被误删: %q", gotStr)
	}
}

// TestChatMessagesRequiresAPIKey 多轮咨询（股票咨询页）在 LLM 未配置时应直接报错，
// 避免用空 Key 发起无意义请求。
func TestChatMessagesRequiresAPIKey(t *testing.T) {
	c := New(Config{APIKey: ""})
	_, err := c.ChatMessages([]Message{{Role: "user", Content: "分析一下某股票"}})
	if err == nil {
		t.Fatalf("期望无 Key 时报错，实际无错误")
	}
	if !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Fatalf("期望错误提示缺少 API Key，实际 %v", err)
	}
}

// TestParseHotTopicBatchTwoStage 整体数组带畸形对象时，逐对象抢救应捞回完好对象，
// 无法修复的坏对象（如 "score"):0 键冒号间夹杂括号）只丢该条，不连累其余。
func TestParseHotTopicBatchTwoStage(t *testing.T) {
	resp := `[
{"index":1,"level":"板块","direction":"利好","score":0.75,"sectors":["半导体材料"],"related_stocks":["云南锗业","有研新材"]},
{"index":2,"level":"板块","direction":"利空","score":-0.75,"sectors":["光模块"]},
{"index":3,"level":"个股","direction":"中性","score"):0,"sectors":[]}
]`
	raw, err := parseHotTopicBatch(resp)
	if err != nil {
		t.Fatalf("两段式解析应成功: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("坏对象(3)应被丢弃, 期望2条实际 %d: %+v", len(raw), raw)
	}
	if int(raw[0].Index) != 1 || raw[0].Direction != "利好" {
		t.Fatalf("第1条应保留: %+v", raw[0])
	}
	if int(raw[1].Index) != 2 || raw[1].Direction != "利空" {
		t.Fatalf("第2条应保留: %+v", raw[1])
	}
}

// TestParseHotTopicBatchStringIndex 模型把 index 输出为字符串（"1"）时应容错。
func TestParseHotTopicBatchStringIndex(t *testing.T) {
	raw, err := parseHotTopicBatch(`[{"index":"1","level":"板块","direction":"利好"}]`)
	if err != nil || len(raw) != 1 || int(raw[0].Index) != 1 {
		t.Fatalf("字符串 index 应容错: err=%v raw=%+v", err, raw)
	}
}

// TestParseHotTopicBatchAllBroke 完全没有可解析对象时返回错误（触发重试队列）。
func TestParseHotTopicBatchAllBroke(t *testing.T) {
	_, err := parseHotTopicBatch(`garbage without braces`)
	if err == nil {
		t.Fatalf("应判定解析失败")
	}
}

// sseServer 构造一个按 handler 返回响应的 httptest 服务器，客户端指向该服务器。
func sseServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(Config{
		APIKey:            "test-key",
		APIURL:            srv.URL + "/v1/chat/completions",
		Model:             "test-model",
		Timeout:           5 * time.Second,
		Streaming:         true,
		StreamIdleTimeout: 300 * time.Millisecond,
	})
	return c, srv
}

// TestStreamChatSuccess 流式分片累加 content、忽略 reasoning_content、遇 [DONE] 结束。
func TestStreamChatSuccess(t *testing.T) {
	chunk := func(delta string) string {
		return fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q}}]}`+"\n\n", delta)
	}
	var hits int
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, chunk("美股"))
		fmt.Fprint(w, chunk("上涨"))
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"","reasoning_content":"思维链"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	got, err := c.Chat("system", "user")
	if err != nil {
		t.Fatalf("流式调用应成功: %v", err)
	}
	if got != "美股上涨" {
		t.Fatalf("应只累加 content 且忽略 reasoning, 实际 %q", got)
	}
	if hits != 1 {
		t.Fatalf("流式成功不应回落非流式, 请求次数=%d", hits)
	}
}

// TestStreamChatIdleTimeout 相邻分片间隔超过空闲阈值判定卡死返回错误。
func TestStreamChatIdleTimeout(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"第一片"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(600 * time.Millisecond) // 超过 idleTimeout=300ms
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"第二片"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	_, err := c.Chat("system", "user")
	if err == nil || !strings.Contains(err.Error(), "空闲超时") {
		t.Fatalf("分片间隔超阈值应报空闲超时, 实际 %v", err)
	}
}

// TestStreamChatStatusError 非 2xx 状态码应返回带状态码的错误。
func TestStreamChatStatusError(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
	})
	_, err := c.Chat("system", "user")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("应返回 429 错误, 实际 %v", err)
	}
}

// TestStreamChatFallbackNonStream 上游对 stream=true 返回普通 JSON（非 SSE）时，
// 流式解析无有效内容应自动回落到非流式成功取回。
func TestStreamChatFallbackNonStream(t *testing.T) {
	reply := `{"choices":[{"message":{"role":"assistant","content":"非流式答复"}}]}`
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, reply)
	})
	got, err := c.Chat("system", "user")
	if err != nil {
		t.Fatalf("应回落到非流式成功: %v", err)
	}
	if got != "非流式答复" {
		t.Fatalf("应取到非流式内容, 实际 %q", got)
	}
}

// TestNonStreamChat 关闭流式开关后走一次性非流式取回。
func TestNonStreamChat(t *testing.T) {
	reply := `{"choices":[{"message":{"role":"assistant","content":"一次性答复"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			t.Errorf("streaming=false 时不应发送 stream=true")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	got, err := c.Chat("system", "user")
	if err != nil || got != "一次性答复" {
		t.Fatalf("非流式应成功: err=%v got=%q", err, got)
	}
}

// TestPingOK 校验探活请求成功路径（极小 max_tokens、一次往返即返回）。
func TestPingOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			t.Error("探活不应使用流式")
		}
		if req.MaxTokens != 1 {
			t.Errorf("探活 max_tokens 应为 1, 实际 %d", req.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"好"}}]}`)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	if err := c.Ping(); err != nil {
		t.Fatalf("探活应成功: %v", err)
	}
}

// TestPingNoKey 未配置 APIKey 应直接报错。
func TestPingNoKey(t *testing.T) {
	c := New(Config{APIKey: "", APIURL: "http://x", Model: "m"})
	if err := c.Ping(); err == nil {
		t.Fatal("无 APIKey 探活应失败")
	}
}

// TestPingUpstreamError 上游返回非 2xx 时探活应报错并带出状态码。
func TestPingUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false})
	if err := c.Ping(); err == nil {
		t.Fatal("上游 500 探活应失败")
	}
}

// htmlLoginPage 定义在 probe_test.go（探测侧的控制台重定向夹具），两处共用同一份样本，
// 避免"探测认得出、运行时认不出"的判据分叉。

// TestPingRejectsHTMLPage §P0 2026-09-20：启动预检必须看响应体。
//
// 旧实现只看状态码，于是"api_url 填成供应商网页控制台域名"这种地址**永远是"启动预检通过"**
// （控制台 307 → 登录页 HTML + HTTP 200），每轮启动日志都在报平安，把排查方向彻底带偏。
func TestPingRejectsHTMLPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, htmlLoginPage)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	err := c.Ping()
	if err == nil {
		t.Fatal("上游回网页时预检必须失败——但旧实现会报『通过』")
	}
	if !errors.Is(err, ErrUpstreamNotAPI) {
		t.Fatalf("应判定为 ErrUpstreamNotAPI, got %v", err)
	}
}

// TestChatHTMLUpstreamIsDiagnosedAsWrongEndpoint §P0 2026-09-20（广州线上实录）：
// 上游回网页时，错误必须**直指"地址填成了网页控制台"**，而不是旧的
// "no response from LLM (流式: data分片=0, 响应摘录=<!DOCTYPE html>…)"——后者等于没给线索。
//
// 两种上游都要覆盖：诚实声明 text/html 的（postCtx 拦下），以及 Content-Type 谎报
// application/json 的（只能靠响应体形态在流式出口拦下）。
func TestChatHTMLUpstreamIsDiagnosedAsWrongEndpoint(t *testing.T) {
	cases := []struct {
		name  string
		ctype string
	}{
		{"上游声明 text/html", "text/html; charset=utf-8"},
		{"上游谎报 application/json", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				fmt.Fprint(w, htmlLoginPage)
			}))
			defer srv.Close()
			c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: true, Timeout: 5 * time.Second})
			_, err := c.Chat("sys", "user")
			if err == nil {
				t.Fatal("上游回网页不得被当成成功")
			}
			if !errors.Is(err, ErrUpstreamNotAPI) {
				t.Fatalf("应判定为 ErrUpstreamNotAPI, got %v", err)
			}
			if strings.Contains(err.Error(), "no response from LLM") {
				t.Fatalf("不得停留在旧的无线索形态: %v", err)
			}
			if !strings.Contains(err.Error(), "网页控制台") {
				t.Fatalf("文案必须给出可操作指引（地址填成了网页控制台）: %v", err)
			}
		})
	}
}

// TestNonStreamHTMLUpstreamIsDiagnosed 非流式通道同样要识别"上游回网页"。
// 非流式是 D1 评分/新闻归因走的路，若只有流式能识别，那些链路仍会把 HTML 当"响应格式怪"。
func TestNonStreamHTMLUpstreamIsDiagnosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 谎报 JSON：绕过 Content-Type 快判，逼出"看响应体"这条兜底路径。
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, htmlLoginPage)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	_, err := c.nonStreamChat(ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !errors.Is(err, ErrUpstreamNotAPI) {
		t.Fatalf("非流式遇 HTML 应判定为 ErrUpstreamNotAPI, got %v", err)
	}
}

// TestNormalizedURLApiStillWorks 反向护栏：正常的 JSON 响应（含 choices）不得被新判定误杀。
// 没有这条，一次"过度收紧"就会把全部链路打死。
func TestNormalizedURLApiStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"好"}}]}`)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	if err := c.Ping(); err != nil {
		t.Fatalf("正常 JSON 响应不得被误判: %v", err)
	}
	if got, err := c.Chat("s", "u"); err != nil || got != "好" {
		t.Fatalf("正常链路应可用: got=%q err=%v", got, err)
	}
}

// TestStripThinkTags §生产 20260916：思考型模型（minimax-m3）把推理原文写进 content，
// 必须剥离 <think>…</think>，否则漏进咨询回复/干扰 JSON 解析。
func TestStripThinkTags(t *testing.T) {
	in := "<think>用户说涨1%，判断为板块联动。</think>[{\"code\":\"600580\",\"score\":16}]"
	want := "[{\"code\":\"600580\",\"score\":16}]"
	if got := stripThinkTags(in); got != want {
		t.Fatalf("think 剥离失败: got %q", got)
	}
	if got := stripThinkTags("[{\"code\":\"600580\",\"score\":16}]"); got != "[{\"code\":\"600580\",\"score\":16}]" {
		t.Fatalf("无标签应原样返回: got %q", got)
	}
	if got := stripThinkTags("普通回答，无标签"); got != "普通回答，无标签" {
		t.Fatalf("普通文本应原样返回: got %q", got)
	}
}

// TestIsTransientLLMError §生产 20260916：5xx/网络类瞬时错误应重试，4xx（额度/鉴权）不重试。
func TestIsTransientLLMError(t *testing.T) {
	yes := []string{"LLM API 返回 502: error code: 502", "HTTP 503", "Post \"http://x\": connection reset by peer", "context deadline exceeded"}
	no := []string{"LLM API 返回 402: payment required", "model_not_found", "LLM_API_KEY not set", ""}
	for _, s := range yes {
		if !isTransientLLMError(errString(s)) {
			t.Fatalf("%q 应判定为瞬时错误", s)
		}
	}
	for _, s := range no {
		if isTransientLLMError(errString(s)) {
			t.Fatalf("%q 不应判定为瞬时错误", s)
		}
	}
	if isTransientLLMError(nil) {
		t.Fatal("nil 不应判定为瞬时错误")
	}
}

type errString string

// errString 让字符串直接充当 error。
func (e errString) Error() string { return string(e) }

// TestProviderBaseURLIsNormalized §P0 2026-09-18「按供应商文档填了 API URL 却一直用不了」回归。
//
// 客户端把 APIURL **原样**当请求地址（post/streamPost 都是 http.NewRequest(POST, c.apiURL)），
// 但各家文档给的都是 base URL（https://api.siliconflow.cn/v1），设置页的占位符也是 base 形态
// （https://api.openai.com/v1）—— 照抄进去就会 POST 到 /v1 上，供应商回 404/405：
// 配置存对了、页面回显也对，就是一次都调不通（表现为「LLM 挂了 / 改了没生效」）。
func TestProviderBaseURLIsNormalized(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"供应商 base URL（SiliconFlow 文档形态）", "https://api.siliconflow.cn/v1", "https://api.siliconflow.cn/v1/chat/completions"},
		{"base URL 带尾斜杠", "https://api.siliconflow.cn/v1/", "https://api.siliconflow.cn/v1/chat/completions"},
		{"只有主机名", "https://api.siliconflow.cn", "https://api.siliconflow.cn/v1/chat/completions"},
		{"OpenAI 形态", "https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"DeepSeek 形态", "https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
		{"版本段带后缀", "https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/chat/completions"},
		{"已是完整 endpoint：保持不动", "https://api.siliconflow.cn/v1/chat/completions", "https://api.siliconflow.cn/v1/chat/completions"},
		{"自建网关非标准路径：不猜", "https://gw.example.com/llm/complete", "https://gw.example.com/llm/complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(Config{APIKey: "k", APIURL: tc.in, Model: "m"}).apiURL; got != tc.want {
				t.Fatalf("APIURL 规范化不符: in=%s got=%s want=%s", tc.in, got, tc.want)
			}
		})
	}
}

// TestChatHitsChatCompletionsPath 端到端：给 base URL 时，真实请求路径必须是 /v1/chat/completions。
func TestChatHitsChatCompletionsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", APIURL: srv.URL + "/v1", Model: "m", Streaming: false, Timeout: 5 * time.Second})
	if _, err := c.Chat("sys", "hi"); err != nil {
		t.Fatalf("Chat 应成功: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("请求路径应为 /v1/chat/completions，实际 %q —— 打到 base 路径上就是供应商 404/405", gotPath)
	}
}
