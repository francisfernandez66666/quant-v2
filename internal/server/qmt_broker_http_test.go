// qmt_broker_http_test.go — §QMT-DUAL 网关 active 通道 HTTP 端点测试。
// 覆盖 GET/POST /api/qmt/broker：adminMiddleware 鉴权（非 admin 403）、
// 未接入实盘（无控制器）时的兜底响应、非法请求体 400。
// 说明：切换/状态读取的实际业务逻辑（brokerStub 网关交互）已由 internal/trading/qmt_broker_test.go
// 在控制器层覆盖，这里只验证 HTTP 面（路由挂载 + 收权 + 错误路径）。
// English: HTTP-layer tests for the dual-path gateway broker endpoints — admin authz (403 for
// non-admin), not-wired fallbacks, and invalid-body 400. The switch/status business logic itself
// is covered at controller level in internal/trading/qmt_broker_test.go, so this file pins only the
// HTTP surface: route registration, privilege enforcement, and error paths.
package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"quant-trading-v2/internal/auth"
)

// TestQMTBrokerNotWired 未接入实盘（无控制器）时：GET 应 200 + ok:false（前端展示"未接入/不可达"），
// POST 切换应 503（调用方走失败分支，绝不 200 假成功）。
// English: without a live controller — GET returns 200 + ok:false (UI shows "not wired/unreachable"),
// POST returns 503 (so the caller hits the failure branch, never a fake 200 success).
func TestQMTBrokerNotWired(t *testing.T) {
	s, admin := newAdminTestServer(t)

	// GET：无控制器 → 200 + {ok:false,...}，JSON 可解析且 ok=false
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/broker", ""))
	if rr.Code != 200 {
		t.Fatalf("GET broker 未接入应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		OK bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET broker 应返回 JSON: %v", err)
	}
	if body.OK {
		t.Fatalf("GET broker 未接入应 ok=false, got %s", rr.Body.String())
	}

	// POST：无控制器 → 503 "real book not available"
	rr2 := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/broker", `{"broker":"queued"}`))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST broker 未接入应 503, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}

// TestQMTBrokerSwitchInvalidBody POST 请求体非法（非 JSON / 缺 broker 字段）→ 400。
// English: POST /api/qmt/broker with a malformed body (non-JSON / missing broker) returns 400.
func TestQMTBrokerSwitchInvalidBody(t *testing.T) {
	s, admin := newAdminTestServer(t)

	for name, body := range map[string]string{
		"非JSON":    `not-json`,
		"缺broker": `{}`,
		"空串":      ``,
	} {
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/broker", body))
		if rr.Code != 400 {
			t.Fatalf("[%s] POST 非法请求体应 400, got %d body=%s", name, rr.Code, rr.Body.String())
		}
	}
}

// TestQMTBrokerAdminOnly 非 admin 账号访问 GET/POST /api/qmt/broker 一律 403（adminMiddleware 收权）。
// English: non-admin accounts get 403 on both GET and POST /api/qmt/broker (adminMiddleware).
func TestQMTBrokerAdminOnly(t *testing.T) {
	s, _ := newAdminTestServer(t)
	normal, err := s.auth.CreateUser("member", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	if rr := adminDo(s, adminReq(s, normal, http.MethodGet, "/api/qmt/broker", "")); rr.Code != 403 {
		t.Fatalf("非 admin GET broker 应 403, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, normal, http.MethodPost, "/api/qmt/broker", `{"broker":"queued"}`)); rr.Code != 403 {
		t.Fatalf("非 admin POST broker 应 403, got %d", rr.Code)
	}
}