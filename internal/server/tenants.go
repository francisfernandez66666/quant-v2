// §MT 多租户 HTTP 层（2026-09-17）：租户管理端点 + 租户作用域判定助手。
// 隔离模型：admin 默认只能管理本租户成员；t_default（系统租户）的 admin 为平台运营者，
// 可跨租户管理、签发/配置租户。全部管理动作 opslog 审计留痕。
// English: §MT multi-tenancy HTTP layer — tenant CRUD (platform-operator only) plus
// tenant-scope helpers used by the admin handlers. Every mutation is audit-logged.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/opslog"
)

// isPlatformAdmin 报告当前请求用户是否平台运营者（admin 且归属 t_default）。
func (s *Server) isPlatformAdmin(r *http.Request) bool {
	u := userFromContext(r)
	if u == nil {
		return false
	}
	return s.auth.IsPlatformAdmin(u.ID)
}

// requirePlatform adminMiddleware 之后的二次闸：非平台运营者一律 403（租户管理专用）。
// 返回 false 时响应已写出。
func (s *Server) requirePlatform(w http.ResponseWriter, r *http.Request) bool {
	if !s.isPlatformAdmin(r) {
		opslog.Logf("quant", "跨租户越权拒绝 uid=%s %s %s（仅平台运营者）", userIDFor(r), r.Method, r.URL.Path)
		writeError(w, 403, "无权限：仅系统租户管理员可管理租户")
		return false
	}
	return true
}

// actorTenant 当前请求用户的生效租户 ID（空用户返回空串）。
func (s *Server) actorTenant(r *http.Request) string {
	u := userFromContext(r)
	if u == nil {
		return ""
	}
	return s.auth.TenantOf(u.ID)
}

// canTouchUser 租户作用域判定：平台运营者可触碰任何人；租户 admin 仅可触碰同租户成员。
// 目标用户不存在按不可触碰处理（调用方自行给 404/403 文案）。
// English: tenant-scope gate — platform admins touch anyone; tenant admins only their own tenant.
func (s *Server) canTouchUser(r *http.Request, targetID string) bool {
	if s.isPlatformAdmin(r) {
		return true
	}
	target := s.auth.TenantOf(targetID)
	if target == "" {
		return false
	}
	return target == s.actorTenant(r)
}

// scopedUsers 当前管理员可见的用户列表（平台运营者=全部；租户 admin=本租户）。
func (s *Server) scopedUsers(r *http.Request) []auth.User {
	if s.isPlatformAdmin(r) {
		return s.auth.ListUsers()
	}
	return s.auth.UsersInTenant(s.actorTenant(r))
}

// tenantView 租户公开视图（含成员用量）。
type tenantView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	MaxUsers      int    `json:"max_users"`
	APIRatePerMin int    `json:"api_rate_per_min"`
	UsedUsers     int    `json:"used_users"`
	IsDefault     bool   `json:"is_default"`
}

// tenantToView 租户 → 公开视图：配额字段取生效值（0→默认），附当前成员用量与系统租户标记。
func (s *Server) tenantToView(t *auth.Tenant) tenantView {
	return tenantView{
		ID: t.ID, Name: t.Name, Enabled: t.Enabled,
		MaxUsers: t.Quota.MaxUsersOr(), APIRatePerMin: t.Quota.APIRateOr(),
		UsedUsers: s.auth.TenantMemberCount(t.ID),
		IsDefault: t.ID == auth.DefaultTenantID,
	}
}

// handleListTenants 处理 GET /api/tenants：平台运营者列出全部租户（含配额用量）。
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r) {
		return
	}
	list := s.auth.ListTenants()
	out := make([]tenantView, 0, len(list))
	for i := range list {
		out = append(out, s.tenantToView(&list[i]))
	}
	writeJSON(w, 200, map[string]interface{}{"tenants": out})
}

// createTenantReq POST /api/tenants 请求体。admin 可选：随租户一并创建该租户的管理员账号。
type createTenantReq struct {
	Name  string `json:"name"`
	Quota struct {
		MaxUsers      int `json:"max_users"`
		APIRatePerMin int `json:"api_rate_per_min"`
	} `json:"quota"`
	Admin *struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"admin"`
}

// handleCreateTenant 处理 POST /api/tenants：创建租户（可选随建租户管理员）。
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r) {
		return
	}
	var req createTenantReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	q := auth.TenantQuota{MaxUsers: req.Quota.MaxUsers, APIRatePerMin: req.Quota.APIRatePerMin}
	if q.MaxUsers < 0 || q.APIRatePerMin < 0 {
		writeError(w, 400, "quota values must be >= 0")
		return
	}
	t, err := s.auth.CreateTenant(strings.TrimSpace(req.Name), q)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	resp := map[string]interface{}{"tenant": s.tenantToView(t)}
	if req.Admin != nil && req.Admin.Username != "" {
		if req.Admin.Password == "" {
			writeError(w, 400, "admin.password required when admin provided")
			return
		}
		au, err := s.auth.CreateUserInTenant(t.ID, req.Admin.Username, req.Admin.Password, auth.RoleAdmin, nil, 0)
		if err != nil {
			// 租户已建、管理员失败：保留租户（幂等重试可再建管理员），显式报错不吞。
			writeError(w, 409, "租户已创建但管理员创建失败: "+err.Error())
			return
		}
		resp["admin"] = au.PublicUser()
	}
	opslog.Audit("tenant_create", userIDFor(r), t.ID, "name="+t.Name)
	log.Printf("[tenant] 租户已创建 id=%s name=%s actor=%s", t.ID, t.Name, userIDFor(r))
	writeJSON(w, 201, resp)
}

// updateTenantReq PUT /api/tenants/{id} 请求体（指针字段=本次要改的，nil=保持原值）。
type updateTenantReq struct {
	Name    *string `json:"name"`
	Enabled *bool   `json:"enabled"`
	Quota   *struct {
		MaxUsers      int `json:"max_users"`
		APIRatePerMin int `json:"api_rate_per_min"`
	} `json:"quota"`
}

// handleUpdateTenant 处理 PUT /api/tenants/{id}：改名/启停/配额调整。
func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r) {
		return
	}
	id := r.PathValue("id")
	var req updateTenantReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	var q *auth.TenantQuota
	if req.Quota != nil {
		if req.Quota.MaxUsers < 0 || req.Quota.APIRatePerMin < 0 {
			writeError(w, 400, "quota values must be >= 0")
			return
		}
		q = &auth.TenantQuota{MaxUsers: req.Quota.MaxUsers, APIRatePerMin: req.Quota.APIRatePerMin}
	}
	if err := s.auth.UpdateTenant(id, req.Name, req.Enabled, q); err != nil {
		if strings.Contains(err.Error(), "不存在") {
			writeError(w, 404, err.Error())
		} else {
			writeError(w, 400, err.Error())
		}
		return
	}
	opslog.Audit("tenant_update", userIDFor(r), id, "ok")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleTenantUsage 处理 GET /api/tenant/usage：任意管理员查看本租户配额与用量。
func (s *Server) handleTenantUsage(w http.ResponseWriter, r *http.Request) {
	tid := s.actorTenant(r)
	t := s.auth.TenantByID(tid)
	if t == nil {
		writeError(w, 404, "tenant not found")
		return
	}
	writeJSON(w, 200, s.tenantToView(t))
}

// moveUserReq PUT /api/admin/users/{id}/tenant 请求体。
type moveUserReq struct {
	TenantID string `json:"tenant_id"`
}

// handleMoveUserTenant 处理 PUT /api/admin/users/{id}/tenant：平台运营者把用户迁入目标租户。
func (s *Server) handleMoveUserTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.auth.UserByID(id) == nil {
		writeError(w, 404, "user not found")
		return
	}
	var req moveUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.MoveUser(id, req.TenantID); err != nil {
		if errors.Is(err, auth.ErrTenantQuota) || errors.Is(err, auth.ErrTenantDisabled) {
			writeError(w, 409, err.Error())
		} else {
			writeError(w, 400, err.Error())
		}
		return
	}
	opslog.Audit("user_move_tenant", userIDFor(r), id, "to="+req.TenantID)
	log.Printf("[tenant] 用户 %s 迁入租户 %s", id, req.TenantID)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// denyOutOfScope 管理端点通用租户护栏：目标不在作用域内→403 并返回 false（响应已写）。
func (s *Server) denyOutOfScope(w http.ResponseWriter, r *http.Request, targetID string) bool {
	if s.canTouchUser(r, targetID) {
		return true
	}
	opslog.Logf("quant", "跨租户管理拒绝 uid=%s target=%s %s %s", userIDFor(r), targetID, r.Method, r.URL.Path)
	writeError(w, 403, "无权限：该账号属于其他租户")
	return false
}
