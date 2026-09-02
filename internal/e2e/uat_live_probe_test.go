// Package e2e 全场景全流程分支像素级 UAT：真实 TCP 监听 + 完整装配 Server + fixture mock 网络/LLM。
// 逐端点探测前端所调用的全部 REST 契约，并覆盖信号链路与交易（模拟盘/实盘路由/回报）读写流程。
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/store"
)

// uatRig 真实监听服务器 + 已跑流水线的测试 rig。
type uatRig struct {
	rig  *testRig
	srv  *server.Server
	pe   *paper.Engine
	base string
}

// newUATRig 装配真实 Server 并绑定 127.0.0.1 随机端口（真实 TCP，非 httptest），tester 提升为 admin。
func newUATRig(t *testing.T, fix *Fixture) *uatRig {
	t.Helper()
	rig := newTestEngine(t, fix)
	srv := server.New(rig.auth, rig.agg, rig.cfgMgr, rig.rpt, rig.market, rig.wl, rig.ths)
	srv.SetEngineController(rig.eng)
	srv.SetCoordinator(data.NewDataCoordinator(rig.market, rig.ths))

	// 模拟盘引擎（启用），与生产装配一致。预置一条昨日建仓的 600276 持仓（绕过 T+1，验证卖出链路）。
	pc := paper.DefaultConfig()
	pc.Enabled = true
	pc.InitialCapital = 3000000 // 分池后每池仍可买 300750（现价≈410/手）
	pc.FixedAmount = 500000
	yday := "2026-09-01T10:05:00+08:00"
	preSeed := fmt.Sprintf(`{"cash":2500000,"initial_capital":3000000,"max_positions":0,"has_filled":true,
		"positions":{"600276":{"code":"600276","name":"恒瑞医药","strategy":"龙头","strategy_type":"dragon",
		"qty":1000,"cost_price":45,"cost":45000,"signal_price":45,"mark":44.5,
		"signal_at":%q,"filled_at":%q}},
		"trades":[],"orders":[],"equity":[],"realized":0,
		"pools":{},"pool_types":[],"pool_max_pos":{},"pool_perf":{},"pool_buy_rules":{},"pool_grp":{},"pool_ir":{}}`, yday, yday)
	paperPath := filepath.Join(rig.tmp, "paper.json")
	if err := os.WriteFile(paperPath, []byte(preSeed), 0o644); err != nil {
		t.Fatalf("write paper seed: %v", err)
	}
	pe := paper.New(pc, paperPath)
	pe.SetStrategyPools([]string{"dragon", "double_bump", "n_shape", "dragon_return", "momentum"})
	srv.SetPaper(pe)

	// 研究库 + 实盘账本库（opslog 目录随 researchDir 一并生效）。
	rdb, err := store.Open(filepath.Join(rig.tmp, "research.db"))
	if err != nil {
		t.Fatalf("open research db: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	srv.SetResearch(rdb, rig.tmp)
	if err := os.MkdirAll(filepath.Join(rig.tmp, "opslog"), 0o755); err != nil {
		t.Fatalf("mkdir opslog: %v", err)
	}

	u, err := rig.auth.Login("tester", "tester123")
	if err != nil {
		t.Fatalf("login tester: %v", err)
	}
	if err := rig.auth.SetRole(u.ID, "admin"); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	rig.auth.MarkInitialized()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.ServeListener(ln)

	ur := &uatRig{rig: rig, srv: srv, pe: pe, base: "http://" + ln.Addr().String()}
	ur.rig.eng.SetShortEnabled(true)
	runPipeline(t, rig, fix)
	return ur
}

// doReq 真实 HTTP 请求。
func (ur *uatRig) doReq(t *testing.T, method, path, token string, body string) (int, string, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ur.base+path, rd)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// get 便捷 GET，期望非 5xx，并解析 JSON 有效性。
func (ur *uatRig) get(t *testing.T, path, token string, want ...int) (string, int) {
	t.Helper()
	code, body, _ := ur.doReq(t, http.MethodGet, path, token, "")
	if len(want) > 0 {
		if code != want[0] {
			t.Errorf("GET %s → %d, want %d (body=%s)", path, code, want[0], body)
		}
	} else if code >= 500 {
		t.Errorf("GET %s → %d (server error, body=%s)", path, code, body)
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") || strings.HasPrefix(strings.TrimSpace(body), "[") {
		var v interface{}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Errorf("GET %s 返回非 JSON: %v (body=%.200s)", path, err, body)
		}
	}
	return body, code
}

// TestUATLiveProbeAuth 认证层全分支：无 token 401、登录、403 注册关闭、setup、auth/me、CORS。
func TestUATLiveProbeAuth(t *testing.T) {
	dataDisableAll(t)
	fix := loadFixtureMain(t)
	ur := newUATRig(t, fix)

	for _, p := range []string{"/api/signals", "/api/news", "/api/holdings", "/api/paper/state", "/api/dashboard", "/api/signal-logs"} {
		code, body, _ := ur.doReq(t, http.MethodGet, p, "", "")
		if code != 401 {
			t.Errorf("无 token GET %s → %d, want 401 (body=%s)", p, code, body)
		}
	}

	// 登录
	code, body, _ := ur.doReq(t, http.MethodPost, "/api/auth/login", "", `{"username":"tester","password":"tester123"}`)
	if code != 200 {
		t.Fatalf("login → %d (body=%s)", code, body)
	}
	var lg struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &lg); err != nil || lg.Token == "" {
		t.Fatalf("login 响应无 token: %s", body)
	}

	// 错误密码
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/auth/login", "", `{"username":"tester","password":"wrong"}`)
	if code != 401 {
		t.Errorf("错误密码登录 → %d, want 401", code)
	}

	// 注册/临时号已关闭
	code, body, _ = ur.doReq(t, http.MethodPost, "/auth/register", "", `{"username":"x","password":"y"}`)
	if code != 403 {
		t.Errorf("POST /auth/register → %d, want 403 (body=%s)", code, body)
	}
	code, body, _ = ur.doReq(t, http.MethodPost, "/auth/temp", "", `{}`)
	if code != 403 {
		t.Errorf("POST /auth/temp → %d, want 403 (body=%s)", code, body)
	}

	// setup
	code, body, _ = ur.doReq(t, http.MethodGet, "/setup", "", "")
	if code != 200 {
		t.Errorf("GET /setup → %d (body=%s)", code, body)
	}

	// auth/me
	code, body, _ = ur.doReq(t, http.MethodGet, "/api/auth/me", lg.Token, "")
	if code != 200 || !strings.Contains(body, "tester") {
		t.Errorf("GET /api/auth/me → %d (body=%s)", code, body)
	}

	// CORS 预检
	code, _, hdr := ur.doReq(t, http.MethodOptions, "/api/signals", "", "")
	if code != 204 || hdr.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("CORS OPTIONS → %d acao=%q, want 204 *", code, hdr.Get("Access-Control-Allow-Origin"))
	}
}

// TestUATLiveProbeSignal 信号功能专项：全链路端点 + 信号日志 + 评分 + 板块 + 消息 + SSE。
func TestUATLiveProbeSignal(t *testing.T) {
	dataDisableAll(t)
	fix := loadFixtureMain(t)
	ur := newUATRig(t, fix)
	token := ur.rig.auth.UserToken("tester")

	// 信号页
	b, code := ur.get(t, "/api/signals", token)
	if code != 200 {
		t.Fatalf("/api/signals → %d", code)
	}
	var sigs []map[string]interface{}
	if err := json.Unmarshal([]byte(b), &sigs); err != nil {
		t.Fatalf("/api/signals 解析失败: %v", err)
	}
	if len(sigs) == 0 {
		t.Error("/api/signals 为空（流水线应产出信号）")
	}
	for _, s := range sigs {
		if s["code"] == nil || s["name"] == nil {
			t.Errorf("信号字段缺失: %v", s)
		}
		if _, ok := s["change_pct"]; !ok {
			t.Errorf("信号缺 change_pct 字段(前端展示实时涨跌幅): %v", s["code"])
		}
	}

	// 信号日志（当日批次，最新在前）
	b, code = ur.get(t, "/api/signal-logs", token)
	if code != 200 {
		t.Fatalf("/api/signal-logs → %d", code)
	}
	var logs []map[string]interface{}
	json.Unmarshal([]byte(b), &logs)
	t.Logf("/api/signal-logs 批次数=%d", len(logs))

	// 评分
	b, code = ur.get(t, "/api/evaluations", token)
	if code != 200 {
		t.Errorf("/api/evaluations → %d (body=%s)", code, b)
	}

	// 看板 / 状态 / 引擎健康
	for _, p := range []string{"/api/dashboard", "/api/status", "/api/engine_health", "/api/engine/init-status",
		"/api/sector/hot", "/api/sector/hot/records", "/api/data_source_health", "/api/news_source_health",
		"/api/stage-records", "/api/llm-debug", "/api/snapshot", "/api/snapshot/hot", "/api/ipo/calendar",
		"/api/news?all=true", "/api/news/showall", "/api/alerts", "/api/scheduler/status", "/api/metrics"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("信号/看板端点 %s → %d", p, code)
		}
	}

	// 行情端点
	for _, p := range []string{"/api/kline?code=300750&period=101&count=90", "/api/minute?code=300750&scale=1&count=241",
		"/api/depth/300750", "/api/stock/lookup?code=300750"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("行情端点 %s → %d", p, code)
		}
	}

	// SSE：HTTP 长连接注册（auth token）→ 等订阅建立 → 通过服务端 broker 广播 → 确认前端能收到。
	// 注：rig 引擎持有独立 broker，HTTP /api/events 走 srv 自己的 broker（生产由 main.go 统一注入），
	// 此处直接驱动 srv broker 验证 HTTP SSE 传输链路；引擎→broker 的广播链路由主 e2e 覆盖。
	bodyReader := sseConnect(t, ur, token)
	deadlineSub := time.Now().Add(3 * time.Second)
	for ur.srv.GetSSE().Len() == 0 && time.Now().Before(deadlineSub) {
		time.Sleep(20 * time.Millisecond)
	}
	if ur.srv.GetSSE().Len() == 0 {
		t.Error("SSE 订阅未建立（broker.Len()==0）")
	}
	ur.srv.GetSSE().Broadcast(map[string]string{"type": "scan", "status": "done", "bull": "1", "bear": "0", "alert": "1", "time": "10:30:00"})
	deadline := time.After(5 * time.Second)
	got := false
	for !got {
		select {
		case <-deadline:
			t.Error("SSE 5s 内未收到广播")
			got = true
		default:
			line, err := bodyReader.ReadString('\n')
			if err != nil {
				t.Errorf("SSE 读流错误: %v", err)
				got = true
				continue
			}
			if strings.HasPrefix(line, "data:") {
				got = true
				t.Logf("SSE 收到: %s", strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}
}

// TestUATLiveProbeTrading 交易专项：模拟盘买卖/池配置/清盘 + 实盘路由配置/状态 + 持仓 CRUD + 下单执行。
func TestUATLiveProbeTrading(t *testing.T) {
	dataDisableAll(t)
	fix := loadFixtureMain(t)
	ur := newUATRig(t, fix)
	token := ur.rig.auth.UserToken("tester")

	// 模拟盘状态（空盘）
	for _, p := range []string{"/api/paper/state", "/api/paper/positions", "/api/paper/trades", "/api/paper/orders", "/api/paper/equity", "/api/paper/selfcheck"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("模拟盘 GET %s → %d", p, code)
		}
	}

	// 模拟买入 → 持仓/成交/净值应非空
	code, body, _ := ur.doReq(t, http.MethodPost, "/api/paper/buy", token,
		`{"code":"300750","name":"宁德时代","strategy":"dragon","strategy_type":"dragon","strategy_id":"sig-uat","signal_price":180,"price":0,"qty":0}`)
	if code != 200 {
		t.Fatalf("paper/buy → %d (body=%s)", code, body)
	}
	b, code := ur.get(t, "/api/paper/positions", token)
	if code != 200 || !strings.Contains(b, "300750") {
		t.Errorf("买入后 positions 应含 300750 → %d (body=%.300s)", code, b)
	}

	// 当日买入当日卖 → T+1 应被拦截（A 股规则，正确行为）
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/paper/sell", token, `{"code":"300750","price":0,"qty":0}`)
	if code != 400 || !strings.Contains(body, "T+1") {
		t.Errorf("当日买当日卖应被 T+1 拦截 → %d (body=%s)", code, body)
	}

	// 昨日建仓的持仓可正常卖出（验证卖出 HTTP 链路 + 清仓记账，600276 由 paper.json 预置）
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/paper/sell", token, `{"code":"600276","price":44.8,"qty":0}`)
	if code != 200 {
		t.Errorf("paper/sell（昨日仓） → %d (body=%s)", code, body)
	}
	b, code = ur.get(t, "/api/paper/trades", token)
	if code != 200 || !strings.Contains(b, "600276") {
		t.Errorf("卖出后 trades 应含 600276 记录 → %d (body=%.200s)", code, b)
	}

	// 池配置
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/paper/pool/config", token,
		`{"max_positions":10,"pool_caps":{"dragon":3},"pool_allocs":{},"pool_rules":{}}`)
	if code != 200 {
		t.Errorf("paper/pool/config → %d (body=%s)", code, body)
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/paper/pool/reset", token, `{"pool":"dragon"}`)
	if code != 200 {
		t.Errorf("paper/pool/reset → %d", code)
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/paper/reset", token, `{"initial_capital":1000000,"max_positions":20}`)
	if code != 200 {
		t.Errorf("paper/reset → %d", code)
	}

	// 持仓 CRUD：create → add → cost → sell → close
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/positions", token,
		`{"code":"600519","name":"贵州茅台","direction":"long","strategy":"dragon","entry_price":1700,"take_profit_pct":15,"stop_loss_pct":7}`)
	if code != 200 && code != 201 {
		t.Fatalf("POST /api/positions → %d (body=%s)", code, body)
	}
	b, code = ur.get(t, "/api/positions", token)
	if code != 200 || !strings.Contains(b, "600519") {
		t.Errorf("GET /api/positions 应含 600519 → %d", code)
	}
	var pos struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(body), &pos)
	if pos.ID == "" {
		// 部分实现返回列表，尝试从返回中取 id
		var arr []map[string]interface{}
		if json.Unmarshal([]byte(body), &arr) == nil && len(arr) > 0 {
			pos.ID, _ = arr[0]["id"].(string)
		}
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/positions/"+pos.ID+"/exit", token, `{"exit_price":1750}`)
	if code != 200 {
		t.Errorf("POST /api/positions/{id}/exit → %d", code)
	}

	// holdings 手工台账
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/holdings", token, `{"holdings":[{"code":"300750","name":"宁德时代","quantity":200,"cost_price":180}],"available_cash":99999}`)
	if code != 200 {
		t.Errorf("POST /api/holdings → %d", code)
	}
	b, code = ur.get(t, "/api/holdings", token)
	if code != 200 || !strings.Contains(b, "300750") {
		t.Errorf("GET /api/holdings 应含 300750 → %d (body=%.200s)", code, b)
	}
	for _, ep := range []string{"/api/holdings/300750/add", "/api/holdings/300750/cost", "/api/holdings/300750/sell"} {
		code, _, _ = ur.doReq(t, http.MethodPost, ep, token, `{"price":185,"quantity":100}`)
		if code != 200 {
			t.Errorf("POST %s → %d", ep, code)
		}
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/holdings/300750/close", token, `{"price":200}`)
	if code != 200 {
		t.Errorf("POST /api/holdings/300750/close → %d", code)
	}

	// 信号动作 buy/ignore
	for _, a := range []string{"buy", "ignore"} {
		code, _, _ = ur.doReq(t, http.MethodPost, "/api/action", token, `{"code":"300750","action":"`+a+`"}`)
		if code != 200 {
			t.Errorf("POST /api/action (%s) → %d", a, code)
		}
	}

	// 实盘路由：配置读写（qmt 未启用，应返回配置且写进入队）
	b, code = ur.get(t, "/api/config/qmt", token)
	if code != 200 {
		t.Errorf("GET /api/config/qmt → %d (body=%s)", code, b)
	}
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/config/qmt", token, `{"enabled":false,"mode":"manual"}`)
	if code != 200 {
		t.Errorf("POST /api/config/qmt → %d (body=%s)", code, body)
	}
	for _, p := range []string{"/api/qmt/state", "/api/qmt/trades", "/api/positions/real", "/api/positions/advice"} {
		_, code := ur.get(t, p, token)
		if code != 200 && code != 503 {
			t.Errorf("实盘 GET %s → %d (无 DB/控制器时允许 200 或 503)", p, code)
		}
	}
	// 未启用 qmt 时 execute 应被拒（400）
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/positions/execute", token,
		`{"code":"300750","side":"买入","action":"加仓","qty":100,"price":185,"strategy":"dragon"}`)
	if code == 200 {
		t.Error("POST /api/positions/execute 在 qmt 未启用时应被拒绝（不应下真实单）")
	}

	// 做空开关 toggle + 状态
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/short/toggle", token, `{"enabled":false}`)
	if code != 200 {
		t.Errorf("POST /api/short/toggle → %d (body=%s)", code, body)
	}
	_, code = ur.get(t, "/api/short/status", token)
	if code != 200 {
		t.Errorf("GET /api/short/status → %d", code)
	}
	_, code = ur.get(t, "/api/long/status", token)
	if code != 200 {
		t.Errorf("GET /api/long/status → %d", code)
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/long/toggle", token, `{"enabled":true}`)
	if code != 200 {
		t.Errorf("POST /api/long/toggle → %d", code)
	}

	// 自选股
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/watchlist", token, `{"code":"600519"}`)
	if code != 200 {
		t.Errorf("POST /api/watchlist → %d", code)
	}
	b, code = ur.get(t, "/api/watchlist", token)
	if code != 200 || !strings.Contains(b, "600519") {
		t.Errorf("GET /api/watchlist 应含 600519 → %d", code)
	}
	code, _, _ = ur.doReq(t, http.MethodDelete, "/api/watchlist", token, `{"code":"600519"}`)
	if code != 200 {
		t.Errorf("DELETE /api/watchlist → %d", code)
	}

	// 咨询 pro-mode + history
	code, _, _ = ur.doReq(t, http.MethodPut, "/api/consult/pro-mode", token, `{"enabled":true}`)
	if code != 200 {
		t.Errorf("PUT /api/consult/pro-mode → %d", code)
	}
	_, code = ur.get(t, "/api/consult/pro-mode", token)
	if code != 200 {
		t.Errorf("GET /api/consult/pro-mode → %d", code)
	}
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/consult", token,
		`{"message":"宁德时代怎么看？"}`)
	if code != 200 {
		t.Errorf("POST /api/consult → %d (body=%.300s)", code, body)
	}
	b, code = ur.get(t, "/api/consult/history", token)
	if code != 200 {
		t.Errorf("GET /api/consult/history → %d", code)
	}
	if len(strings.TrimSpace(b)) == 0 || b == "[]" {
		t.Error("consult 后 history 应非空")
	}
}

// TestUATLiveProbeAdmin 管理域：用户管理 + 配置 + 研究 + opslog。
func TestUATLiveProbeAdmin(t *testing.T) {
	dataDisableAll(t)
	fix := loadFixtureMain(t)
	ur := newUATRig(t, fix)
	token := ur.rig.auth.UserToken("tester")

	_, code := ur.get(t, "/api/admin/users", token)
	if code != 200 {
		t.Errorf("GET /api/admin/users → %d", code)
	}
	code, body, _ := ur.doReq(t, http.MethodPost, "/api/admin/users", token,
		`{"username":"uat2","password":"uatpass123","role":"user","perms":[],"expires_days":7}`)
	if code != 200 && code != 201 && code != 400 {
		t.Errorf("POST /api/admin/users → %d (body=%s)", code, body)
	}

	for _, p := range []string{"/api/config/strategy", "/api/config/d1", "/api/config/llm"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("GET %s → %d", p, code)
		}
	}
	code, _, _ = ur.doReq(t, http.MethodPost, "/api/config/strategy", token,
		`{"dragon":{"f1_seal_weight":0.3,"f2_resonance_weight":0.25,"f3_premium_weight":0.25,"f4_rs_weight":0.2,"pullback_max_pct":5}}`)
	if code != 200 {
		t.Errorf("POST /api/config/strategy → %d", code)
	}

	for _, p := range []string{"/api/opslog", "/api/opslog/dates"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("GET %s → %d", p, code)
		}
	}

	for _, p := range []string{"/api/research/progress", "/api/research/factors", "/api/research/candidates",
		"/api/research/library", "/api/research/sweep-pools", "/api/research/optimizations",
		"/api/research/backtest/list", "/api/research/backtest/running"} {
		_, code := ur.get(t, p, token)
		if code != 200 {
			t.Errorf("研究 GET %s → %d", p, code)
		}
	}
	code, body, _ = ur.doReq(t, http.MethodPost, "/api/research/backtest-toggle", token, `{"enabled":true}`)
	if code != 200 {
		t.Errorf("POST /api/research/backtest-toggle → %d (body=%s)", code, body)
	}
}

// sseConnect 建立 SSE 连接返回流读取器。
func sseConnect(t *testing.T, ur *uatRig, token string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ur.base+"/api/events?token="+token, nil)
	if err != nil {
		t.Fatalf("sse req: %v", err)
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sse connect: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("sse → %d", resp.StatusCode)
	}
	return bufio.NewReader(resp.Body)
}

// dataDisableAll 关闭真实网络，保证离线可复现。
func dataDisableAll(t *testing.T) {
	t.Helper()
	data.DisableAll = true
	t.Cleanup(func() { data.DisableAll = false })
}
