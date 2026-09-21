// h3_perms_test.go — §H3（2026-09-22 修复批）成员越权写全局收口回归锁：
// POST /api/action（ignore/buy/sell 手动指令通道，ignore 墓碑写的是运营账号引擎信号簿）、
// POST /api/news/showall（全局资讯开关）、POST /api/news/reanalyze（全量 LLM 补推）
// 对非 admin 一律 403（adminMiddleware）；admin 通道保持 200 可用。
// English: §H3 regression — global-state write endpoints (manual action incl. ignore, news
// showall toggle, reanalyze) must 403 for members and stay 200 for admin.
package server

import (
	"net/http"
	"testing"

	"quant-trading-v2/internal/auth"
)

// memberOf 在既有服务上补建一个普通成员账号。
func memberOf(t *testing.T, s *Server) *auth.User {
	t.Helper()
	m, err := s.auth.CreateUser("h3member", "pw", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return m
}

func TestH3ActionAdminOnly(t *testing.T) {
	s, admin := newAdminTestServer(t)
	m := memberOf(t, s)
	// 成员 ignore（旧 authMiddleware 下 200 且写穿运营账号信号簿）→ 必须 403
	if rr := adminDo(s, adminReq(s, m, http.MethodPost, "/api/action", `{"code":"600000.SH","action":"ignore"}`)); rr.Code != 403 {
		t.Fatalf("成员 POST /api/action ignore 应 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 成员 buy 同样 403（不再有机会走到内部 admin 判定前的信号簿/日志路径）
	if rr := adminDo(s, adminReq(s, m, http.MethodPost, "/api/action", `{"code":"600000.SH","action":"buy","price":10,"qty":100}`)); rr.Code != 403 {
		t.Fatalf("成员 POST /api/action buy 应 403, got %d", rr.Code)
	}
	// admin 通道回归可用（空引擎环境 ignore → 200 ignored，与 §F-1 语义一致）
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/action", `{"code":"600000.SH","action":"ignore"}`))
	if rr.Code != 200 {
		t.Fatalf("admin POST /api/action ignore 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestH3NewsShowAllAndReanalyzeAdminOnly(t *testing.T) {
	s, _ := newAdminTestServer(t)
	m := memberOf(t, s)
	if rr := adminDo(s, adminReq(s, m, http.MethodPost, "/api/news/showall", `{"enabled":true}`)); rr.Code != 403 {
		t.Fatalf("成员 POST /api/news/showall 应 403, got %d", rr.Code)
	}
	if rr := adminDo(s, adminReq(s, m, http.MethodPost, "/api/news/reanalyze", ``)); rr.Code != 403 {
		t.Fatalf("成员 POST /api/news/reanalyze 应 403, got %d", rr.Code)
	}
	// GET showall 只读口保持成员可达（列表渲染依赖，回归护栏）
	if rr := adminDo(s, adminReq(s, m, http.MethodGet, "/api/news/showall", ``)); rr.Code != 200 {
		t.Fatalf("成员 GET /api/news/showall 只读应 200, got %d", rr.Code)
	}
}
