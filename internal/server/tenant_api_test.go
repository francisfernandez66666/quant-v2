// §MT 多租户 HTTP 层测试：租户作用域列表/开户、跨租户拒绝、租户管理端点权限、
// 跨租户迁移、租户频控（authMiddleware 内嵌）。
// English: §MT HTTP-layer tests: tenant-scoped listing/creation, cross-tenant denials,
// tenant-admin endpoint gating, cross-tenant moves and the tenant rate limiter.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/auth"
)

// newTenantTestServer 平台运营者 + 两个租户（B 配额 2 人、API 限流 2/min）+ 各自成员。
func newTenantTestServer(t *testing.T) (s *Server, platform, tenantBAdmin, tenantBUser, tenantAUser *auth.User) {
	t.Helper()
	s, platform = newAdminTestServer(t)
	tb, err := s.auth.CreateTenant("B租户", auth.TenantQuota{MaxUsers: 3})
	if err != nil {
		t.Fatalf("create tenant B: %v", err)
	}
	if _, err := s.auth.CreateTenant("A租户", auth.TenantQuota{}); err != nil {
		t.Fatal(err)
	}
	tenantBAdmin, err = s.auth.CreateUserInTenant(tb.ID, "tb", "pw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tenantBUser, err = s.auth.CreateUserInTenant(tb.ID, "ub", "pw", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tenantAUser, err = s.auth.CreateUser("ua", "pw", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// httpReqWithBearer 构造仅带 Bearer 头的真实请求（不注入上下文，走完整 authMiddleware）。
func httpReqWithBearer(method, path, token string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// TestTenantScopedList 租户 admin 列用户只见本租户；平台运营者见全部。
func TestTenantScopedList(t *testing.T) {
	s, platform, tbAdmin, tbUser, _ := newTenantTestServer(t)

	rr := adminDo(s, adminReq(s, tbAdmin, http.MethodGet, "/api/admin/users", ""))
	if rr.Code != 200 {
		t.Fatalf("租户 admin 列表应 200: %d", rr.Code)
	}
	var body struct {
		Users    []map[string]any `json:"users"`
		Platform bool             `json:"platform"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Platform {
		t.Error("租户 admin 的 platform 应为 false")
	}
	if len(body.Users) != 2 {
		t.Fatalf("租户 admin 应只见本租户 2 人, got %d: %+v", len(body.Users), body.Users)
	}
	for _, u := range body.Users {
		if u["tenant_id"] != "t_default" && u["tenant_id"] == nil {
			// 用户 public 视图应带 tenant_id 字段
		}
	}
	if _, ok := body.Users[0]["tenant_id"]; !ok {
		t.Error("用户视图应包含 tenant_id 字段")
	}
	_ = tbUser

	rr = adminDo(s, adminReq(s, platform, http.MethodGet, "/api/admin/users", ""))
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if !body.Platform || len(body.Users) < 4 {
		t.Errorf("平台运营者应见全部: platform=%v n=%d", body.Platform, len(body.Users))
	}
}

// TestTenantScopedCreate 租户 admin 建号强制落本租户；指定他租户 403；超配额 409。
func TestTenantScopedCreate(t *testing.T) {
	s, platform, tbAdmin, _, _ := newTenantTestServer(t)

	rr := adminDo(s, adminReq(s, tbAdmin, http.MethodPost, "/api/admin/users", `{"username":"ub2","password":"pw"}`))
	if rr.Code != 201 {
		t.Fatalf("本租户建号应 201: %d %s", rr.Code, rr.Body.String())
	}
	// B 租户配额 3（tb+ub+ub2），再加一人应满额 409
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodPost, "/api/admin/users", `{"username":"ub3","password":"pw"}`))
	if rr.Code != 409 {
		t.Fatalf("超配额应 409: %d %s", rr.Code, rr.Body.String())
	}
	// 指定他租户（未超平台配额）→ 403
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodPost, "/api/admin/users", `{"username":"x","password":"pw","tenant_id":"t_default"}`))
	if rr.Code != 403 {
		t.Fatalf("租户 admin 指定他租户应 403: %d", rr.Code)
	}
	// 平台运营者可显式落 t_default
	rr = adminDo(s, adminReq(s, platform, http.MethodPost, "/api/admin/users", `{"username":"p1","password":"pw","tenant_id":"t_default"}`))
	if rr.Code != 201 {
		t.Fatalf("平台运营者显式租户应 201: %d %s", rr.Code, rr.Body.String())
	}
}

// TestTenantCrossTenantDenials 跨租户改角色/删人 403；租户管理端点 403；平台不限。
func TestTenantCrossTenantDenials(t *testing.T) {
	s, platform, tbAdmin, _, taUser := newTenantTestServer(t)

	rr := adminDo(s, adminReq(s, tbAdmin, http.MethodPost, "/api/admin/users/"+taUser.ID+"/enabled", `{"enabled":false}`))
	if rr.Code != 403 {
		t.Fatalf("跨租户禁用应 403: %d", rr.Code)
	}
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodGet, "/api/admin/users/"+taUser.ID+"/config/qmt", ""))
	if rr.Code != 403 {
		t.Fatalf("跨租户读 QMT 配置应 403: %d", rr.Code)
	}
	rr = adminDo(s, adminReq(s, platform, http.MethodPost, "/api/admin/users/"+taUser.ID+"/enabled", `{"enabled":false}`))
	if rr.Code != 200 {
		t.Fatalf("平台运营者跨租户操作应 200: %d", rr.Code)
	}
	// 租户管理端点仅平台可用
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodGet, "/api/tenants", ""))
	if rr.Code != 403 {
		t.Fatalf("租户 admin 访问 /api/tenants 应 403: %d", rr.Code)
	}
	rr = adminDo(s, adminReq(s, platform, http.MethodGet, "/api/tenants", ""))
	if rr.Code != 200 {
		t.Fatalf("平台访问 /api/tenants 应 200: %d", rr.Code)
	}
	var tb struct {
		Tenants []map[string]any `json:"tenants"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &tb)
	if len(tb.Tenants) != 3 {
		t.Errorf("应有 3 个租户, got %d", len(tb.Tenants))
	}
	// 本租户用量
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodGet, "/api/tenant/usage", ""))
	if rr.Code != 200 {
		t.Fatalf("本租户用量应 200: %d", rr.Code)
	}
}

// TestTenantCRUD 租户创建/更新/停用保护 + 随建管理员。
func TestTenantCRUD(t *testing.T) {
	s, platform, _, _, _ := newTenantTestServer(t)

	rr := adminDo(s, adminReq(s, platform, http.MethodPost, "/api/tenants",
		`{"name":"C租户","quota":{"max_users":3,"api_rate_per_min":30},"admin":{"username":"tc","password":"pw"}}`))
	if rr.Code != 201 {
		t.Fatalf("创建租户应 201: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Tenant map[string]any `json:"tenant"`
		Admin  map[string]any `json:"admin"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Admin["username"] != "tc" {
		t.Errorf("应随建租户管理员: %+v", resp)
	}
	tid, _ := resp.Tenant["id"].(string)
	// 重名 409
	rr = adminDo(s, adminReq(s, platform, http.MethodPost, "/api/tenants", `{"name":"C租户"}`))
	if rr.Code != 409 {
		t.Errorf("重名应 409: %d", rr.Code)
	}
	// 配额更新
	rr = adminDo(s, adminReq(s, platform, http.MethodPut, "/api/tenants/"+tid, `{"quota":{"max_users":9,"api_rate_per_min":0}}`))
	if rr.Code != 200 {
		t.Fatalf("配额更新应 200: %d", rr.Code)
	}
	// 停用 t_default 拒绝
	rr = adminDo(s, adminReq(s, platform, http.MethodPut, "/api/tenants/t_default", `{"enabled":false}`))
	if rr.Code != 400 {
		t.Errorf("停用系统租户应 400: %d", rr.Code)
	}
	// 未知租户 404
	rr = adminDo(s, adminReq(s, platform, http.MethodPut, "/api/tenants/t_nope", `{"name":"x"}`))
	if rr.Code != 404 {
		t.Errorf("未知租户应 404: %d", rr.Code)
	}
}

// TestMoveUserTenantEndpoint 平台运营者迁移用户租户；租户 admin 403；目标满额 409。
func TestMoveUserTenantEndpoint(t *testing.T) {
	s, platform, tbAdmin, _, taUser := newTenantTestServer(t)

	rr := adminDo(s, adminReq(s, platform, http.MethodPut, "/api/admin/users/"+taUser.ID+"/tenant", `{"tenant_id":"t_default"}`))
	if rr.Code != 200 { // 已在 t_default，幂等成功
		t.Fatalf("幂等迁移应 200: %d %s", rr.Code, rr.Body.String())
	}
	rr = adminDo(s, adminReq(s, tbAdmin, http.MethodPut, "/api/admin/users/"+taUser.ID+"/tenant", `{"tenant_id":"t_default"}`))
	if rr.Code != 403 {
		t.Fatalf("租户 admin 迁移他人应 403: %d", rr.Code)
	}
	// 填满 B 租户（配额 3）后迁入应 409
	for _, n := range []string{"ub2"} {
		if _, err := s.auth.CreateUserInTenant(tbAdmin.TenantID, n, "pw", "", nil, 0); err != nil {
			t.Fatalf("fill B: %v", err)
		}
	}
	u2, _ := s.auth.CreateUser("mvme", "pw", "", nil, 0)
	// 找出 B 租户 ID
	var tbID string
	for _, tn := range s.auth.ListTenants() {
		if tn.Name == "B租户" {
			tbID = tn.ID
		}
	}
	rr = adminDo(s, adminReq(s, platform, http.MethodPut, "/api/admin/users/"+u2.ID+"/tenant", `{"tenant_id":"`+tbID+`"}`))
	if rr.Code != 409 {
		t.Fatalf("迁入满额租户应 409: %d %s", rr.Code, rr.Body.String())
	}
}

// TestTenantRateLimit 走真实 authMiddleware：B 租户限流 2/min，第 3 个请求 429。
func TestTenantRateLimit(t *testing.T) {
	s, platform, _, _, _ := newTenantTestServer(t)
	d, err := s.auth.CreateTenant("D限流", auth.TenantQuota{APIRatePerMin: 2})
	if err != nil {
		t.Fatal(err)
	}
	du, err := s.auth.CreateUserInTenant(d.ID, "ud", "pw", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = platform
	do := func() int {
		req := httpReqWithBearer(http.MethodGet, "/api/health", du.Token)
		return adminDo(s, req).Code
	}
	if c := do(); c != 200 {
		t.Fatalf("第 1 个请求应 200, got %d", c)
	}
	if c := do(); c != 200 {
		t.Fatalf("第 2 个请求应 200, got %d", c)
	}
	if c := do(); c != http.StatusTooManyRequests {
		t.Fatalf("第 3 个请求应 429, got %d", c)
	}
}
