// handlers_qmt_admin_test.go — §0925EVE-W3-G（FIX_PLAN ⑫ C3）：第三态「待核对」人工收敛
// 两个 admin 端点的 HTTP 面测试：
//
//	GET  /api/qmt/pending-review —— 成功透传（含 qty null 容错、truncated 显式化）、
//	  网关读失败必须 502（绝不 200 空数组冒充）、未接入 503、非 admin 403；
//	POST /api/qmt/order-confirm —— wire_ref→signal_id 映射与 Bearer 透传、成功回传结论、
//	  网关 409 如实映射、参数非法 400、非 admin 403。
//
// 网关侧用 httptest 桩（顺带锁住「客户端确实带 token 调 /admin/*」这条鉴权线）；
// English: HTTP-surface tests for the pending-review list and manual order-confirm endpoints,
// with a stub gateway that also pins the Bearer-token wiring.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newQMTAdminStubGateway 启动一个「网关 admin 面」httptest 桩：
// 校验 Bearer token（错 token → 401，对齐 gateway.py _auth_ok），按脚本返回
// /admin/status 与 /admin/order-confirm 响应，并记录最后一次收到的请求体。
// English: stub gateway validating the Bearer token and scriptable admin responses.
type qmtAdminStub struct {
	token       string
	statusBody  string // GET /admin/status 回包（默认含两条待核对）
	confirmFn   func(body map[string]any) (int, map[string]any)
	lastAuth    string
	lastConfirm map[string]any
}

func newQMTAdminStubGateway(t *testing.T, token string) (*httptest.Server, *qmtAdminStub) {
	t.Helper()
	st := &qmtAdminStub{
		token: token,
		statusBody: `{"ok":true,"ts":"2026-09-25 22:00:00","active":"queued","failover_enable":true,` +
			`"unresolved_orders":[` +
			`{"signal_id":"sell:600000.SH:龙抬头:2026-09-25","code":"600000.SH","side":"卖出","qty":300,` +
			`"created_at":"2026-09-25 14:55:01","dispatch_in_flight":false},` +
			`{"signal_id":"buy:000001.SZ:x:2026-09-25","code":"000001.SZ","side":"买入","qty":null,` +
			`"created_at":"2026-09-25 14:56:02","dispatch_in_flight":true}],` +
			`"unresolved_count":25,"dispatch_signal_guard":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.lastAuth = r.Header.Get("Authorization")
		if st.lastAuth != "Bearer "+token {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "unauthorized"})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/status":
			if strings.HasPrefix(st.statusBody, "HTTPERR:") { // 触发上游读失败
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`boom`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(st.statusBody))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/order-confirm":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			st.lastConfirm = body
			code, resp := 200, map[string]any{"ok": true, "err": ""}
			// 默认桩对齐网关真实回包形态：released 分支带 released:true，
			// settled 分支带 status（缺省「已撤」由网关决定）。
			if d, _ := body["decision"].(string); d == "released" {
				resp["released"] = true
			} else {
				resp["status"] = "已撤"
			}
			if st.confirmFn != nil {
				code, resp = st.confirmFn(body)
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"ok":false,"err":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

// newQMTAdminTestServer 装配带实盘控制器（指向桩网关）的完整路由 Server。
// English: full-route Server whose QMT controller points at the stub gateway.
func newQMTAdminTestServer(t *testing.T) (*Server, *auth.User, *qmtAdminStub) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	const token = "stub-gw-token"
	srv, st := newQMTAdminStubGateway(t, token)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = token
	ctrl := trading.NewController(nil, db, admin.ID, cfg, nil)
	s.SetEngineController(fakeCtrl{qmt: ctrl})
	return s, admin, st
}

// TestQMTPendingReviewSuccess GET 待核对清单成功路径：200 + 清单/计数/truncated 透传，
// qty=null 行不得拖垮整包解码（按 0 如实透传）。
// English: success path — list/count/truncated relayed; a null qty row degrades to 0, not a decode failure.
func TestQMTPendingReviewSuccess(t *testing.T) {
	s, admin, st := newQMTAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/pending-review", ""))
	if rr.Code != 200 {
		t.Fatalf("应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if st.lastAuth != "Bearer stub-gw-token" {
		t.Fatalf("客户端未带网关 Bearer token 调 /admin/status: %q", st.lastAuth)
	}
	var body struct {
		OK            bool                         `json:"ok"`
		Orders        []trading.PendingReviewOrder `json:"orders"`
		UnresolvedCnt int                          `json:"unresolved_count"`
		Truncated     bool                         `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
	}
	if !body.OK || len(body.Orders) != 2 {
		t.Fatalf("清单透传错误: %s", rr.Body.String())
	}
	if body.Orders[1].Qty != 0 || body.Orders[0].Qty != 300 {
		t.Fatalf("qty 解析错误（null→0 容错失效）: %+v", body.Orders)
	}
	if !body.Orders[1].DispatchInFlight {
		t.Fatalf("dispatch_in_flight 取证位丢失: %+v", body.Orders[1])
	}
	if body.UnresolvedCnt != 25 || !body.Truncated {
		t.Fatalf("全量计数/截断位错误: count=%d truncated=%v", body.UnresolvedCnt, body.Truncated)
	}
}

// TestQMTPendingReviewFailureIsVisible 网关读失败必须显式报错（502），
// 绝不 200+空数组冒充「没有待核对单」——这是本面板的元验收（D3 同族教训）。
// English: a gateway read failure surfaces as 502 — never a 200 empty list masquerading as "all clear".
func TestQMTPendingReviewFailureIsVisible(t *testing.T) {
	s, admin, st := newQMTAdminTestServer(t)
	st.statusBody = "HTTPERR:boom" // 桩直接 500
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/pending-review", ""))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("网关读失败应 502（不得空清单冒充），got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "查询失败") {
		t.Fatalf("失败原因未带回: %s", rr.Body.String())
	}
}

// TestQMTPendingReviewNotWired 未接入实盘（无控制器）→ 503，同样不得空清单冒充。
// English: not wired → 503 (an empty list would silently mean "no risk").
func TestQMTPendingReviewNotWired(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/pending-review", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接入应 503, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestQMTPendingReviewAdminOnly 非 admin 访问两新端点一律 403（adminMiddleware）。
// English: non-admin gets 403 on both endpoints.
func TestQMTPendingReviewAdminOnly(t *testing.T) {
	s, _, _ := newQMTAdminTestServer(t)
	member, err := s.auth.CreateUser("member-qmt", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/qmt/pending-review", "")); rr.Code != 403 {
		t.Fatalf("非 admin GET pending-review 应 403, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodPost, "/api/qmt/order-confirm",
		`{"wire_ref":"sell:x","decision":"released"}`)); rr.Code != 403 {
		t.Fatalf("非 admin POST order-confirm 应 403, got %d", rr.Code)
	}
}

// TestQMTOrderConfirmReleased released 分支：wire_ref 映射为网关 signal_id、
// decision 原样转发、200 回传 released 结论。
// English: released branch — wire_ref maps to signal_id, verdict relayed with 200.
func TestQMTOrderConfirmReleased(t *testing.T) {
	s, admin, st := newQMTAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/order-confirm",
		`{"wire_ref":"sell:600000.SH:龙抬头:2026-09-25","decision":"released"}`))
	if rr.Code != 200 {
		t.Fatalf("released 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if st.lastConfirm == nil || st.lastConfirm["signal_id"] != "sell:600000.SH:龙抬头:2026-09-25" ||
		st.lastConfirm["decision"] != "released" {
		t.Fatalf("转发网关请求体错误: %+v", st.lastConfirm)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["ok"] != true || body["released"] != true {
		t.Fatalf("回传结论错误: %s", rr.Body.String())
	}
}

// TestQMTOrderConfirmGatewayReject 网关业务拒绝（409 非待核对态）如实映射 409+原因，
// 不洗成 200。
// English: a gateway 409 rejection is mapped honestly (code + reason), never washed into a 200.
func TestQMTOrderConfirmGatewayReject(t *testing.T) {
	s, admin, st := newQMTAdminTestServer(t)
	st.confirmFn = func(body map[string]any) (int, map[string]any) {
		return 409, map[string]any{"ok": false, "err": "order is not in 待核对 state (status=已成) — nothing to confirm"}
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/order-confirm",
		`{"wire_ref":"sell:ghost","decision":"settled","order_id":"123456"}`))
	if rr.Code != 409 {
		t.Fatalf("网关 409 应映射 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "nothing to confirm") {
		t.Fatalf("网关拒绝原因未回传: %s", rr.Body.String())
	}
}

// TestQMTOrderConfirmBadRequests 参数校验：非法 JSON/缺 wire_ref/非法 decision 均 400
// （400 面把错误请求挡在网关之前，避免污染审计行）。
// English: 400 on malformed body / missing anchor / unknown decision.
func TestQMTOrderConfirmBadRequests(t *testing.T) {
	s, admin, _ := newQMTAdminTestServer(t)
	for name, body := range map[string]string{
		"非法JSON":     `not-json`,
		"缺锚点":        `{"decision":"released"}`,
		"decision非法": `{"wire_ref":"x","decision":"reissue"}`,
		"空体":         ``,
	} {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/order-confirm", body))
		if rr.Code != 400 {
			t.Fatalf("[%s] 非法请求应 400, got %d body=%s", name, rr.Code, rr.Body.String())
		}
	}
}

// TestQMTOrderConfirmTokenFailureVisible 网关鉴权失败（首尔侧 token 与网关不一致）必须
// 可见且不冒充成功：网关 401 映射为 502（401 留给本 API 自己的会话语义，绝不把网关
// 鉴权失败外溢成 401，否则前端会误判「管理员自己的登录过期」）。
// English: gateway-side auth mismatch maps to 502 (a leaked 401 would look like the admin's
// own session expired).
func TestQMTOrderConfirmTokenFailureVisible(t *testing.T) {
	srv, _ := newQMTAdminStubGateway(t, "other-token") // 桩只认 other-token
	db, err := store.Open(filepath.Join(t.TempDir(), "live2.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, admin := newAdminTestServer(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "stale-token" // 配置漂移：首尔侧持旧 token
	ctrl := trading.NewController(nil, db, admin.ID, cfg, nil)
	s.SetEngineController(fakeCtrl{qmt: ctrl})
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/order-confirm",
		`{"wire_ref":"sell:x","decision":"released"}`))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("网关鉴权失败应 502（不得漏 401/洗 200），got %d body=%s", rr.Code, rr.Body.String())
	}
	// GET 面同理：网关 401 不得冒充空清单
	rr2 := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/pending-review", ""))
	if rr2.Code != http.StatusBadGateway {
		t.Fatalf("清单读取遇网关 401 应 502, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}
