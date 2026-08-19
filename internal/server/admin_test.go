// 用户/账号管理端点测试：admin 中间件鉴权、开户、改角色/权限/密码/启禁用、代配配置。
// English: User/account management endpoint tests: admin middleware auth, create account, change role/perms/password/enable-disable, configure on behalf of others.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// newAdminTestServer 构建带认证 + 配置管理器的 Server，用于 admin 端点测试。
// English: newAdminTestServer builds a Server with auth + config manager for admin endpoint tests.
func newAdminTestServer(t *testing.T) (*Server, *auth.User) {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	admin, err := mgr.CreateUser("admin", "adminpw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	cfgMgr := config.NewManager(dir + "/config.json")
	cfgMgr.SetStore(mgr)
	s := &Server{auth: mgr, cfg: cfgMgr, mux: http.NewServeMux()}
	s.registerRoutes()
	return s, admin
}

// adminReq 构造带管理员 token 的请求并注入认证上下文。
// 注意：不走真实中间件，而是直接向请求上下文注入 user（等价于 authMiddleware 的产物）。
// English: adminReq builds a request with an admin token and injects the auth context.
// English: Note: it does not go through the real middleware; instead it injects user directly into the request context (equivalent to the output of authMiddleware).
func adminReq(s *Server, u *auth.User, method, path string, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+u.Token)
	user := s.auth.UserByID(u.ID)
	ctx := context.WithValue(req.Context(), ctxUserKey{}, user)
	return req.WithContext(ctx)
}

// adminDo 走 Server.mux 完整路由执行请求，返回 recorder。
// English: adminDo executes the request through the full Server.mux routes and returns the recorder.
func adminDo(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	return rr
}

// TestAdminMiddlewareBlocksNonAdmin 非管理员访问 admin 端点应 403。
// English: TestAdminMiddlewareBlocksNonAdmin non-admin access to admin endpoints should return 403.
func TestAdminMiddlewareBlocksNonAdmin(t *testing.T) {
	s, admin := newAdminTestServer(t)
	normal, _ := s.auth.CreateUser("user1", "pw", "", nil, 0)

	req := adminReq(s, normal, http.MethodGet, "/api/admin/users", "")
	rr := adminDo(s, req)
	if rr.Code != 403 {
		t.Fatalf("普通用户访问 admin 端点应 403, got %d", rr.Code)
	}

	// 管理员可访问
	req = adminReq(s, admin, http.MethodGet, "/api/admin/users", "")
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("管理员访问应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if users, ok := body["users"].([]any); !ok || len(users) < 2 {
		t.Fatalf("应返回全部用户列表, got %v", body["users"])
	}
	if perms, ok := body["perms"].([]any); !ok || len(perms) == 0 {
		t.Fatalf("应返回权限位清单, got %v", body["perms"])
	}
}

// TestAdminCreateUserFlow admin 开户 + 配权限 + 改密 + 禁用完整流程。
func TestAdminCreateUserFlow(t *testing.T) {
	s, admin := newAdminTestServer(t)

	// 开户
	req := adminReq(s, admin, http.MethodPost, "/api/admin/users",
		`{"username":"newbie","password":"pw1","role":"user","perms":["research_approve"]}`)
	rr := adminDo(s, req)
	if rr.Code != 201 {
		t.Fatalf("开户应 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	u := body["user"].(map[string]any)
	id := u["id"].(string)
	if u["role"] != "user" {
		t.Fatalf("角色应为 user, got %v", u["role"])
	}
	if _, ok := u["token"]; ok {
		t.Error("创建用户响应不应泄露 token")
	}
	if !s.auth.HasPerm(id, auth.PermResearchApprove) {
		t.Error("开户时应授予 research_approve")
	}

	// 提升为管理员
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+id+"/role", `{"role":"admin"}`)
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("设置角色应 200, got %d", rr.Code)
	}
	if !s.auth.IsAdmin(id) {
		t.Error("SetRole(admin) 后应成为管理员")
	}

	// 改密：旧密码失效
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+id+"/password", `{"password":"newpw2"}`)
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("改密应 200, got %d", rr.Code)
	}
	if _, err := s.auth.Login("newbie", "pw1"); err == nil {
		t.Error("改密后旧密码应失效")
	}
	if _, err := s.auth.Login("newbie", "newpw2"); err != nil {
		t.Error("改密后新密码应能登录")
	}

	// 禁用（先降回 user，因为 admin 不可禁用）
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+id+"/role", `{"role":"user"}`)
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("降级应 200, got %d", rr.Code)
	}
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+id+"/enabled", `{"enabled":false}`)
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("禁用应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := s.auth.Login("newbie", "newpw2"); err == nil {
		t.Error("禁用账号不应能登录")
	}

	// 不能禁用 admin 自己
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+admin.ID+"/enabled", `{"enabled":false}`)
	rr = adminDo(s, req)
	if rr.Code != 400 {
		t.Fatalf("禁用 admin 应 400, got %d", rr.Code)
	}
}

// TestAdminSetPerms 管理员整体覆盖用户权限位。
func TestAdminSetPerms(t *testing.T) {
	s, admin := newAdminTestServer(t)
	normal, _ := s.auth.CreateUser("u2", "pw", "", nil, 0)

	req := adminReq(s, admin, http.MethodPost, "/api/admin/users/"+normal.ID+"/perms",
		`{"perms":["research_approve"]}`)
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("设权限应 200, got %d", rr.Code)
	}
	if !s.auth.HasPerm(normal.ID, auth.PermResearchApprove) {
		t.Error("授予后应拥有权限位")
	}

	// 整体覆盖为空 → 撤销
	req = adminReq(s, admin, http.MethodPost, "/api/admin/users/"+normal.ID+"/perms", `{"perms":[]}`)
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("清空权限应 200, got %d", rr.Code)
	}
	if s.auth.HasPerm(normal.ID, auth.PermResearchApprove) {
		t.Error("清空后不应再拥有权限位")
	}
}

// TestAdminPerUserConfig admin 代配他人账号战法参数 + 读回。
func TestAdminPerUserConfig(t *testing.T) {
	s, admin := newAdminTestServer(t)
	normal, _ := s.auth.CreateUser("u3", "pw", "", nil, 0)

	// 写他人战法参数
	req := adminReq(s, admin, http.MethodPost, "/api/admin/users/"+normal.ID+"/config/strategy",
		`{"dragon":{"f1_seal_weight":0.5},"double_bump":{},"n_shape":{},"dragon_return":{},"momentum":{}}`)
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("代配策略应 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 读回该账号配置
	req = adminReq(s, admin, http.MethodGet, "/api/admin/users/"+normal.ID+"/config/strategy", "")
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("读取策略应 200, got %d", rr.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	dragon, ok := got["dragon"].(map[string]any)
	if !ok {
		t.Fatalf("响应应含 dragon 分组, got %v", got)
	}
	if dragon["f1_seal_weight"] != 0.5 {
		t.Fatalf("f1_seal_weight 应=0.5, got %v", dragon["f1_seal_weight"])
	}
}

// TestAuthMe 当前用户信息接口返回角色/权限。
func TestAuthMe(t *testing.T) {
	s, admin := newAdminTestServer(t)
	req := adminReq(s, admin, http.MethodGet, "/api/auth/me", "")
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("/api/auth/me 应 200, got %d", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["role"] != "admin" {
		t.Fatalf("role 应为 admin, got %v", body["role"])
	}
}

// TestAdminDeleteUser admin 删除普通用户成功、删除 admin 被拒。
func TestAdminDeleteUser(t *testing.T) {
	s, admin := newAdminTestServer(t)
	normal, _ := s.auth.CreateUser("del1", "pw", "", nil, 0)

	// 删除普通用户
	req := adminReq(s, admin, http.MethodDelete, "/api/admin/users/"+normal.ID, "")
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("删除普通用户应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if s.auth.UserByID(normal.ID) != nil {
		t.Error("删除后用户应不存在")
	}

	// 删除 admin 被拒
	req = adminReq(s, admin, http.MethodDelete, "/api/admin/users/"+admin.ID, "")
	rr = adminDo(s, req)
	if rr.Code != 400 {
		t.Fatalf("删除 admin 应 400, got %d", rr.Code)
	}
}
