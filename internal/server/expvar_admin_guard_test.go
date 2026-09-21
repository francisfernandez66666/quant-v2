// expvar_admin_guard_test.go — §EXPVAR（2026-09-22 LOW 族）路由守卫：expvar 指标面收 admin。
//
// 现状锤实（与 docs/FIX_PLAN_20260922.md §LOW 族「expvar 收 admin（与 :565 对齐）」对应）：
//   - 本服务的 expvar 暴露面不是 /debug/vars：net/http.DefaultServeMux 从未被挂载监听
//     （quant 只听自有 mux，见 server.go ListenAndServe/Serve/cmd/quant main.go），
//     expvar 包 init 注册的 GET /debug/vars 不可达——本测试以 404 锁死该现状，
//     防止未来有人把 DefaultServeMux 或 /debug/vars 裸挂进鉴权链路之外；
//   - 真实暴露面是 GET /api/metrics（server.go §R4-9）：经 expvar.Handler() 导出
//     整套默认注册表（cmdline/memstats/quant.metrics 业务计数器），修复前仅 authMiddleware，
//     任意登录成员可枚举运维数据。现升 adminMiddleware，与同文件 opslog(:565)/prometheus 口径一致。
//
// English: /debug/vars itself is never mounted (locked at 404); the real expvar surface is
// GET /api/metrics which must be admin-only after the LOW-family fix.
package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestExpvarMetricsAdminOnly §EXPVAR 反例锁：member GET /api/metrics → 403，admin → 200，
// 且响应确为 expvar 全量索引（含 memstats——证明这不是空壳断言）。
// 修复前该路由挂 authMiddleware，member 会拿到 200 —— 本锁即缺陷复用例。
// English: member 403 / admin 200 on the expvar-backed metrics endpoint.
func TestExpvarMetricsAdminOnly(t *testing.T) {
	s, _, _ := newR7TestServer(t)
	admin, err := s.auth.CreateUser("expvaradmin", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := s.auth.CreateUser("expvarmember", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	// member：403（缺陷形态为 200 泄漏 memstats）
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/metrics", "")); rr.Code != 403 {
		t.Errorf("member GET /api/metrics 应 403（§EXPVAR 收 admin）, got %d", rr.Code)
	}

	// admin：200 且为 expvar 索引 JSON
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/metrics", ""))
	if rr.Code != 200 {
		t.Fatalf("admin GET /api/metrics → %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "memstats") {
		t.Errorf("admin 应读到 expvar 全量索引（含 memstats）, got %q", rr.Body.String())
	}
}

// TestDebugVarsNotMounted §EXPVAR 现状锁：/debug/vars 在本服务 mux 上根本未挂载
// （expvar 包只注册进从未被监听的 http.DefaultServeMux），admin/member 访问一律 404。
// 若未来有人显式挂载该路径，必须同 /api/metrics 一样过 adminMiddleware——此锁使其裸挂即红。
// English: /debug/vars is not served by this server at all; keep it that way (or guard it as admin).
func TestDebugVarsNotMounted(t *testing.T) {
	s, _, _ := newR7TestServer(t)
	admin, err := s.auth.CreateUser("dbgvarsadmin", "pw", "admin", nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := s.auth.CreateUser("dbgvarsmember", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/debug/vars", "")); rr.Code != 404 {
		t.Errorf("/debug/vars 不应被挂载（现状 404）, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, member, http.MethodGet, "/debug/vars", "")); rr.Code != 404 {
		t.Errorf("/debug/vars 不应被挂载（现状 404）, got %d", rr.Code)
	}
}
