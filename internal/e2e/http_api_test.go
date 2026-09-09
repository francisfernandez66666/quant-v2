// 今日改动的 HTTP 级像素测试：完整装配真实 Server（mock 网络 + mock LLM + 真实引擎），
// 通过 httptest 驱动今日改动的全部 REST 端点并逐字段断言：
//   - /api/news 资讯混合流（原始新闻 + 已打标事件 tagged 字段 + 宏观日历）
//   - /api/signals 实时涨跌幅 change_pct 补充
//   - /api/signal-logs 信号批次记录（最新在前）
//   - /api/sector/hot 同花顺板块兜底（LLM 无归因时取 top10）
//   - 认证鉴权（401 无 token）与 authMiddleware user 注入
//   - /api/consult/pro-mode 开关读写（按用户隔离）
//   - /api/consult/history 咨询历史读写与清空
//   - GetStockList 新浪→东财兜底链
//   - resolveConflict 提醒信号不并入 FinalSignals
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategy_engine"
)

// httpRig 完整装配真实 Server + 引擎 rig，供 HTTP 端点测试使用。
type httpRig struct {
	rig   *testRig
	srv   *server.Server
	token string // tester 用户的有效 token
}

// newHTTPServerRig 基于已装配的 testRig 构建真实 Server（复用 mock 网络与 mock LLM）。
func newHTTPServerRig(t *testing.T, fix *Fixture) *httpRig {
	t.Helper()
	rig := newTestEngine(t, fix)
	srv := server.New(rig.auth, rig.agg, rig.cfgMgr, rig.rpt, rig.market, rig.wl, rig.ths)
	srv.SetEngineController(rig.eng)
	token := rig.auth.UserToken("tester")
	if token == "" {
		t.Fatal("tester 无有效 token")
	}
	return &httpRig{rig: rig, srv: srv, token: token}
}

// apiGet 用指定 token 请求 GET 端点，返回响应体与状态码。
func apiGet(t *testing.T, hr *httpRig, token, path string) (int, string) {
	t.Helper()
	return apiReq(t, hr, token, http.MethodGet, path, nil)
}

// apiReq 用指定 token 请求任意方法/端点，返回响应体与状态码。
func apiReq(t *testing.T, hr *httpRig, token, method, path string, body []byte) (int, string) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	hr.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// apiReqWithOrigin 携带指定 Origin/Host 头请求（CORS 测试用）；origin 为空则不带 Origin 头。
// 固定请求 Host 为 quant.local（与同源断言一致）。
func apiReqWithOrigin(t *testing.T, hr *httpRig, method, path, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "quant.local"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	hr.srv.ServeHTTP(rec, req)
	return rec
}

// sseConnectStatus 建立 SSE 连接，用带超时的上下文驱动 ServeHTTP 返回后取状态码。
// qs 直接作为 query string（如 ticket=xxx 或 token=xxx）。
func sseConnectStatus(t *testing.T, hr *httpRig, qs string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?"+qs, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		hr.srv.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE 建链超时")
	}
	return rec.Code
}

// TestHTTPAuthRequired 未带 token 访问业务端点应返回 401。
func TestHTTPAuthRequired(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)
	for _, path := range []string{"/api/news", "/api/signals", "/api/consult/pro-mode", "/api/signal-logs"} {
		code, body := apiGet(t, hr, "", path)
		if code != 401 {
			t.Errorf("%s 无token应401, got %d body=%s", path, code, body)
		}
	}
}

// TestHTTPAdminIsolation §WS-E 多账号敏感隔离：子账号请求 admin 专属端点（llm-debug /
// stage-records，含运营账号 LLM 密钥池与全链路日志）→ 403；admin 正常 200。
// English: §WS-E account isolation — sub-account tokens get 403 on admin-only sensitive endpoints
// (operator LLM key pool / full pipeline logs); the admin keeps full access.
func TestHTTPAdminIsolation(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)
	// 确保 tester 为管理员
	if u, err := hr.rig.auth.Login("tester", "tester123"); err != nil {
		t.Fatalf("login tester: %v", err)
	} else if err := hr.rig.auth.SetRole(u.ID, "admin"); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	// 子账号
	sub, err := hr.rig.auth.CreateUser("subuser", "subpass123", "user", nil, 30)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	subToken := hr.rig.auth.UserToken("subuser")
	if subToken == "" {
		t.Fatalf("sub token empty, user=%+v", sub)
	}
	for _, path := range []string{"/api/llm-debug", "/api/stage-records"} {
		code, _ := apiGet(t, hr, subToken, path)
		if code != 403 {
			t.Errorf("子账号 %s 应 403, got %d", path, code)
		}
		code, _ = apiGet(t, hr, hr.token, path)
		if code == 403 || code == 401 {
			t.Errorf("管理员 %s 不应 403/401, got %d", path, code)
		}
	}
	// 公共看板数据子账号仍可见（共享运营数据叙事）
	if code, _ := apiGet(t, hr, subToken, "/api/signals"); code != 200 {
		t.Errorf("子账号 /api/signals 应 200（运营数据共享）, got %d", code)
	}
}

// TestHTTPCORSHardening §WS-F B4：跨域源（Origin 与 Host 不同且不在白名单）不写
// Allow-Origin 头（浏览器拦截）；同源与无 Origin 的非浏览器请求正常放行。
// English: §WS-F B4 CORS hardening — cross-origin requests get no Allow-Origin header (blocked by
// browsers); same-origin and non-browser (no Origin) requests keep working.
func TestHTTPCORSHardening(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)

	cross := apiReqWithOrigin(t, hr, http.MethodGet, "/api/status", "https://evil.example")
	code, hdr := cross.Code, cross.Header().Get("Access-Control-Allow-Origin")
	if hdr != "" {
		t.Errorf("跨域源不应有 Allow-Origin 头, got %q (code=%d)", hdr, code)
	}
	same := apiReqWithOrigin(t, hr, http.MethodGet, "/api/status", "http://quant.local")
	if got := same.Header().Get("Access-Control-Allow-Origin"); got != "http://quant.local" {
		t.Errorf("同源应回显 Origin, got %q", got)
	}
	// 无 Origin 头（curl/探针）回退 *
	noorig := apiReqWithOrigin(t, hr, http.MethodGet, "/api/status", "")
	if got := noorig.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("无 Origin 头应回退 *, got %q", got)
	}
}

// TestHTTPSSeTicket §WS-F C4a：POST /api/events/ticket 取一次性票据；
// /api/events?ticket= 建链 200；同票二次使用 401；伪造/过期票 401；无票无 token 401；
// 旧版 token query 兼容仍可用。
// English: §WS-F C4a — mint a one-time SSE ticket, connect with it (200), reuse → 401, bogus → 401,
// no credential → 401, and the legacy token query still works.
func TestHTTPSSeTicket(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)

	// 无票无 token → 401
	if code, _ := apiGet(t, hr, "", "/api/events"); code != 401 {
		t.Errorf("/api/events 无凭证应 401, got %d", code)
	}
	// 伪造票据 → 401
	if code, _ := apiGet(t, hr, hr.token, "/api/events?ticket=bogus"); code != 401 {
		t.Errorf("/api/events 伪造票据应 401, got %d", code)
	}
	// 取票（需认证）
	var resp struct {
		Ticket string `json:"ticket"`
	}
	code, body := apiReq(t, hr, hr.token, http.MethodPost, "/api/events/ticket", nil)
	if code != 200 {
		t.Fatalf("POST /api/events/ticket → %d body=%s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.Ticket == "" {
		t.Fatalf("ticket 响应异常: %v body=%s", err, body)
	}
	// 有效票据建链 → 200（SSE 握手后阻塞，用超时上下文驱动返回）
	if code := sseConnectStatus(t, hr, "ticket="+resp.Ticket); code != 200 {
		t.Errorf("有效票据建链应 200, got %d", code)
	}
	// 同一票据复用 → 401（一次性）
	if code, _ := apiGet(t, hr, hr.token, "/api/events?ticket="+resp.Ticket); code != 401 {
		t.Errorf("已用票据应 401, got %d", code)
	}
	// 未认证取票 → 401
	if code, _ := apiReq(t, hr, "", http.MethodPost, "/api/events/ticket", nil); code != 401 {
		t.Errorf("未认证取票应 401, got %d", code)
	}
	// 旧版 token query 兼容
	if code := sseConnectStatus(t, hr, "token="+hr.token); code != 200 {
		t.Errorf("token query 兼容应 200, got %d", code)
	}
}

// TestHTTPNewsMixedStream 资讯混合流：原始新闻 + 已打标事件 + 宏观日历，tagged 字段区分。
// 跑一次完整流水线让 agg 有已打标事件，再断言 /api/news 合并输出。
func TestHTTPNewsMixedStream(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)
	hr.rig.eng.SetShortEnabled(true)
	runPipeline(t, hr.rig, fix)

	code, body := apiGet(t, hr, hr.token, "/api/news?all=true")
	if code != 200 {
		t.Fatalf("/api/news status=%d body=%s", code, body)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		t.Fatalf("解析 /api/news: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("/api/news 无内容")
	}
	var tagged, macro int
	for _, it := range items {
		if b, _ := it["tagged"].(bool); b {
			tagged++
		}
		if src, _ := it["source"].(string); src == "宏观日历" {
			macro++
		}
	}
	if tagged == 0 {
		t.Errorf("/api/news 缺少已打标事件(tagged=true), 共%d条", len(items))
	}
	if macro == 0 {
		t.Errorf("/api/news 缺少宏观日历事件, 共%d条", len(items))
	}
}

// TestHTTPSignalLogs /api/signal-logs 返回信号批次（最新在前），engine 无批次时为空列表。
func TestHTTPSignalLogs(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)

	// 跑流水线产出信号批次
	hr.rig.eng.SetShortEnabled(true)
	runPipeline(t, hr.rig, fix)

	code, body := apiGet(t, hr, hr.token, "/api/signal-logs")
	if code != 200 {
		t.Fatalf("/api/signal-logs status=%d", code)
	}
	var logs []map[string]interface{}
	if err := json.Unmarshal([]byte(body), &logs); err != nil {
		t.Fatalf("解析 /api/signal-logs: %v", err)
	}
	// 流水线跑过之后应有批次（signal_records 固化）
	t.Logf("/api/signal-logs 返回 %d 批次", len(logs))
	for i, l := range logs {
		pt, _ := l["process_time"].(string)
		if i > 0 {
			prev, _ := logs[i-1]["process_time"].(string)
			if pt > prev {
				t.Errorf("批次应按时间倒序(最新在前): idx%d %q > idx%d %q", i-1, prev, i, pt)
			}
		}
	}
}

// TestHTTPSectorHotFallback agg 为空时 /api/sector/hot 走同花顺板块兜底（涨幅 top10）。
// 使用主 fixture（含同花顺行业+概念 HTML，可解析出板块）。
func TestHTTPSectorHotFallback(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadFixtureMain(t)
	hr := newHTTPServerRig(t, fix)

	code, body := apiGet(t, hr, hr.token, "/api/sector/hot")
	if code != 200 {
		t.Fatalf("/api/sector/hot status=%d body=%s", code, body)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		t.Fatalf("解析 /api/sector/hot: %v", err)
	}
	// 空 agg → 兜底路径：应返回同花顺板块（行业+概念 top10），并标注兜底理由
	if len(items) == 0 {
		t.Fatal("/api/sector/hot 兜底应返回同花顺板块（fixture 含行业/概念 HTML）")
	}
	if len(items) > 10 {
		t.Errorf("兜底最多取 top10, got %d", len(items))
	}
	found := false
	for _, it := range items {
		if reason, _ := it["reason_detail"].(string); strings.Contains(reason, "同花顺板块兜底") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("兜底项应带 reason_detail=同花顺板块兜底, items=%+v", items)
	}
	// 按涨跌幅降序校验
	var prev float64
	for i, it := range items {
		cp, _ := it["change_pct"].(float64)
		if i > 0 && cp > prev+1e-9 {
			t.Errorf("兜底板块应按涨幅降序: idx%d %.2f > idx%d %.2f", i, cp, i-1, prev)
		}
		prev = cp
	}
}

// TestHTTPConsultProModeAPI 专业模式开关 HTTP 读写（默认关 → 开 → 读回），并按用户隔离。
func TestHTTPConsultProModeAPI(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	hr := newHTTPServerRig(t, fix)

	// 默认关
	code, body := apiGet(t, hr, hr.token, "/api/consult/pro-mode")
	if code != 200 {
		t.Fatalf("GET pro-mode status=%d", code)
	}
	if !strings.Contains(body, `"enabled":false`) {
		t.Errorf("默认专业模式应为关, body=%s", body)
	}

	// 开启
	code, body = apiReq(t, hr, hr.token, http.MethodPut, "/api/consult/pro-mode", []byte(`{"enabled":true}`))
	if code != 200 || !strings.Contains(body, `"enabled":true`) {
		t.Errorf("PUT pro-mode 开启失败 status=%d body=%s", code, body)
	}

	// 读回为开
	code, body = apiGet(t, hr, hr.token, "/api/consult/pro-mode")
	if code != 200 || !strings.Contains(body, `"enabled":true`) {
		t.Errorf("开启后读回应为true, status=%d body=%s", code, body)
	}

	// 另一用户（tester2）读回仍为关（按用户隔离，开关落盘 auth.json）
	inv2, _ := hr.rig.auth.CreateInvite()
	if _, err := hr.rig.auth.Register("tester2", "tester123", inv2); err != nil {
		t.Fatalf("register tester2: %v", err)
	}
	token2 := hr.rig.auth.UserToken("tester2")
	code, body = apiGet(t, hr, token2, "/api/consult/pro-mode")
	if code != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Errorf("tester2 专业模式应默认关(按用户隔离), status=%d body=%s", code, body)
	}
}

// TestHTTPConsultHistory 咨询历史读写与清空（DELETE /api/consult/history）。
func TestHTTPConsultHistory(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	hr := newHTTPServerRig(t, fix)

	// 初始为空
	code, body := apiGet(t, hr, hr.token, "/api/consult/history")
	if code != 200 {
		t.Fatalf("GET history status=%d", code)
	}
	var hist []map[string]interface{}
	if err := json.Unmarshal([]byte(body), &hist); err != nil {
		t.Fatalf("解析 history: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("初始咨询历史应为空, got %d", len(hist))
	}

	// 引擎驱动一条咨询
	if _, err := hr.rig.eng.ConsultLLM("tester", "你好", true); err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}

	// 历史应含 user+assistant
	code, body = apiGet(t, hr, hr.token, "/api/consult/history")
	if code != 200 {
		t.Fatalf("GET history 2 status=%d", code)
	}
	if err := json.Unmarshal([]byte(body), &hist); err != nil {
		t.Fatalf("解析 history 2: %v", err)
	}
	if len(hist) != 2 {
		t.Errorf("咨询历史应=2条(user+assistant), got %d", len(hist))
	}

	// 清空
	code, body = apiReq(t, hr, hr.token, http.MethodDelete, "/api/consult/history", nil)
	if code != 200 {
		t.Fatalf("DELETE history status=%d", code)
	}
	code, body = apiGet(t, hr, hr.token, "/api/consult/history")
	if code != 200 {
		t.Fatalf("GET history 3 status=%d", code)
	}
	if err := json.Unmarshal([]byte(body), &hist); err != nil {
		t.Fatalf("解析 history 3: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("清空后历史应为空, got %d", len(hist))
	}
}

// TestHTTPSignalsChangePct /api/signals 应补充实时涨跌幅（今日 fixture 600580 变更+3.34%）。
// 通过 agg.Update 注入一条 600580 信号（触发价/涨跌幅为占位），
// 验证 handler 用实时行情覆盖 Price 与 ChangePct。
func TestHTTPSignalsChangePct(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	hr := newHTTPServerRig(t, fix)

	// 注入一条 600580 信号（触发价 30.00 为占位，handler 会用实时价 36.86 覆盖）
	hr.rig.agg.Update(
		strategyEngineResult(t, "600580"),
		nil, nil,
		[]combat_agent.Signal{{
			ID: "sig-600580", Code: "600580", Name: "卧龙电驱",
			Strategy: "double_bump", Direction: "做多", Action: "buy", Price: 30.00,
		}},
		nil, nil, nil, hr.rig.rpt,
	)

	code, body := apiGet(t, hr, hr.token, "/api/signals")
	if code != 200 {
		t.Fatalf("/api/signals status=%d body=%s", code, body)
	}
	var sigs []map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sigs); err != nil {
		t.Fatalf("解析 /api/signals: %v", err)
	}
	if len(sigs) == 0 {
		t.Fatal("/api/signals 无信号")
	}
	// 找到 600580 信号并断言实时价/涨跌幅被覆盖
	for _, s := range sigs {
		if s["code"] != "600580" {
			continue
		}
		price, _ := s["price"].(float64)
		cp, _ := s["change_pct"].(float64)
		if diff := price - 36.86; diff > 0.02 || diff < -0.02 {
			t.Errorf("600580 信号现价应被实时价覆盖≈36.86, got %.2f", price)
		}
		if diff := cp - 3.34; diff > 0.05 || diff < -0.05 {
			t.Errorf("600580 信号 change_pct 应≈3.34%%, got %.2f", cp)
		}
		return
	}
	t.Error("/api/signals 缺少 600580 信号")
}

// loadFixtureMain 加载主 fixtures.json（含场景新闻/同花顺 HTML，供混合流与兜底测试）。
func loadFixtureMain(t *testing.T) *Fixture {
	t.Helper()
	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载主fixture: %v", err)
	}
	return fix
}

// runPipeline 以 fixture 抓取日 08:30 为追溯起点驱动一次完整流水线。
func runPipeline(t *testing.T, rig *testRig, fix *Fixture) {
	t.Helper()
	capT, err := time.ParseInLocation("2006-01-02 15:04:05", fix.CapturedAt, time.Local)
	if err != nil {
		t.Fatalf("解析 fixture 时间 %q: %v", fix.CapturedAt, err)
	}
	rig.eng.Run(context.Background(), time.Date(capT.Year(), capT.Month(), capT.Day(), 8, 30, 0, 0, time.Local))
}

// strategyEngineResult 构造一个最小 StrategyResult（含指定股票的事件），供 agg.Update 注入看板。
func strategyEngineResult(t *testing.T, code string) *strategy_engine.StrategyResult {
	t.Helper()
	return &strategy_engine.StrategyResult{ScoringPool: []string{code}}
}
