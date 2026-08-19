// 用户/账号管理与管理员代配他人账号配置端点（仅 admin 角色可调用）。
// English: user/account management plus admin-side per-account configuration endpoints (admin role only).
// Package server: admin-only user management and per-account config endpoints.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// handleAuthMe 处理 GET /api/auth/me：返回当前登录用户的公开信息（角色/权限位），
// 前端据此渲染管理员菜单与功能按钮。
// English: handles GET /api/auth/me — returns the current user's public info (role/perms), which the
// frontend uses to render the admin menu and action buttons.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	if user == nil {
		writeError(w, 401, "unauthorized")
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"id":         user.ID,
		"username":   user.Username,
		"role":       user.Role,
		"perms":      user.Perms,
		"enabled":    user.Enabled,
		"expires_at": user.ExpiresAt,
	})
}

// handleListUsers 处理 GET /api/admin/users：列出全部用户（公开视图，不含密码/令牌）。
// English: handles GET /api/admin/users — lists all users (public view, no passwords/tokens).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users := s.auth.ListUsers()
	writeJSON(w, 200, map[string]interface{}{
		"users": users,
		"perms": auth.AllPerms(),
	})
}

// createUserReq 管理员开户请求体。
// English: createUserReq is the admin account-creation request body.
type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"` // 可选，缺省 user
	// English: optional, defaults to user.
	Perms []string `json:"perms"` // 可选，权限位列表
	// English: optional, list of permission bits.
	ExpiresDays int `json:"expires_days"` // 可选，账号有效期天数（0=永久）
	// English: optional, account validity in days (0 = permanent).
}

// handleCreateUser 处理 POST /api/admin/users：管理员创建正式用户并返回其公开视图。
// English: handles POST /api/admin/users — the admin creates a real user and returns its public view.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}
	if req.ExpiresDays < 0 {
		writeError(w, 400, "expires_days must be >= 0")
		return
	}
	user, err := s.auth.CreateUser(req.Username, req.Password, req.Role, req.Perms, req.ExpiresDays)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	log.Printf("[admin] 创建用户 %s (role=%s perms=%v expires_days=%d)", req.Username, user.Role, req.Perms, req.ExpiresDays)
	writeJSON(w, 201, map[string]interface{}{"user": user.PublicUser()})
}

// setUserRoleReq 设置角色请求体。
// English: setUserRoleReq is the set-role request body.
type setUserRoleReq struct {
	Role string `json:"role"`
}

// handleSetUserRole 处理 POST /api/admin/users/{id}/role：设置用户角色。
// English: handles POST /api/admin/users/{id}/role — sets a user's role.
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setUserRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.SetRole(id, req.Role); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 用户 %s 角色 → %s", id, req.Role)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// setUserPermsReq 设置权限位请求体。
// English: setUserPermsReq is the set-perms request body.
type setUserPermsReq struct {
	Perms []string `json:"perms"`
}

// handleSetUserPerms 处理 POST /api/admin/users/{id}/perms：整体覆盖用户权限位列表。
// English: handles POST /api/admin/users/{id}/perms — overwrites a user's whole permission-bit list.
func (s *Server) handleSetUserPerms(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setUserPermsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.SetPerms(id, req.Perms); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 用户 %s 权限 → %v", id, req.Perms)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// setUserPasswordReq 重置密码请求体。
// English: setUserPasswordReq is the password-reset request body.
type setUserPasswordReq struct {
	Password string `json:"password"`
}

// handleSetUserPassword 处理 POST /api/admin/users/{id}/password：重置用户密码并重签令牌。
// English: handles POST /api/admin/users/{id}/password — resets a user's password and re-issues the token.
func (s *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setUserPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.ChangePassword(id, req.Password); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 用户 %s 已重置密码", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// setUserEnabledReq 启/禁用请求体。
// English: setUserEnabledReq is the enable/disable request body.
type setUserEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// setUserExpiryReq 设置有效期请求体。
// English: setUserExpiryReq is the set-expiry request body.
type setUserExpiryReq struct {
	ExpiresDays int `json:"expires_days"` // 有效期天数（0=永久）
	// English: validity days (0 = permanent).
}

// handleSetUserExpiry 处理 POST /api/admin/users/{id}/expiry：设置账号有效期天数（0=永久）。
// English: handles POST /api/admin/users/{id}/expiry — sets an account's validity in days (0 = permanent).
func (s *Server) handleSetUserExpiry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setUserExpiryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.SetExpiry(id, req.ExpiresDays); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 用户 %s 有效期天数 → %d", id, req.ExpiresDays)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleSetUserEnabled 处理 POST /api/admin/users/{id}/enabled：启用/禁用账号。
// English: handles POST /api/admin/users/{id}/enabled — enables/disables an account.
func (s *Server) handleSetUserEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setUserEnabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if err := s.auth.SetEnabled(id, req.Enabled); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 用户 %s enabled=%v", id, req.Enabled)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeleteUser 处理 DELETE /api/admin/users/{id}：删除用户（管理员不可删）。
// English: handles DELETE /api/admin/users/{id} — deletes a user (admins cannot be deleted).
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.auth.DeleteUser(id); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[admin] 已删除用户 %s", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── 管理员代配他人账号配置 ──
// English: --- admin-side provisioning of other accounts' configuration ---

// handleAdminGetStrategyConfig 处理 GET /api/admin/users/{id}/config/strategy：
// 读取指定账号的战法参数（账号级覆盖优先，否则全局）。
// English: handles GET /api/admin/users/{id}/config/strategy — reads the account's strategy params
// (account-level override wins, otherwise the global config).
func (s *Server) handleAdminGetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	writeJSON(w, 200, s.cfg.GetStrategyConfigFor(id))
}

// handleAdminSetStrategyConfig 处理 POST /api/admin/users/{id}/config/strategy：
// 保存指定账号的战法参数覆盖。
// English: handles POST /api/admin/users/{id}/config/strategy — saves the account's strategy param overrides.
func (s *Server) handleAdminSetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	var cfg config.StrategyConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetStrategyConfigFor(id, &cfg)
	log.Printf("[admin] 用户 %s 战法参数已保存", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleAdminGetD1Config 处理 GET /api/admin/users/{id}/config/d1。
// English: handles GET /api/admin/users/{id}/config/d1.
func (s *Server) handleAdminGetD1Config(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	writeJSON(w, 200, s.cfg.GetD1ConfigFor(id))
}

// handleAdminSetD1Config 处理 POST /api/admin/users/{id}/config/d1。
// English: handles POST /api/admin/users/{id}/config/d1.
func (s *Server) handleAdminSetD1Config(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	var cfg config.D1Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetD1ConfigFor(id, &cfg)
	log.Printf("[admin] 用户 %s D1 规则已保存", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleAdminGetLongShortConfig 处理 GET /api/admin/users/{id}/config/longshort。
// English: handles GET /api/admin/users/{id}/config/longshort.
func (s *Server) handleAdminGetLongShortConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	writeJSON(w, 200, s.cfg.GetLongShortConfigFor(id))
}

// handleAdminSetLongShortConfig 处理 POST /api/admin/users/{id}/config/longshort。
// English: handles POST /api/admin/users/{id}/config/longshort.
func (s *Server) handleAdminSetLongShortConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	var cfg config.LongShortConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetLongShortConfigFor(id, cfg)
	log.Printf("[admin] 用户 %s 做多/做空开关已保存", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleAdminGetLLMConfig 处理 GET /api/admin/users/{id}/config/llm：
// 读取指定账号的 LLM 配置（含多 key）。
// English: handles GET /api/admin/users/{id}/config/llm — reads the account's LLM config (incl. multiple keys).
func (s *Server) handleAdminGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	cfg := s.cfg.GetLLMConfigFor(id)
	var apiKeys []string
	if v, ok := s.auth.GetConfig(id, "llm_api_keys"); ok && v != "" {
		apiKeys = splitLLMKeys(v)
	}
	if len(apiKeys) == 0 {
		if v, ok := s.auth.GetConfig(id, "llm_api_key"); ok && v != "" {
			apiKeys = []string{v}
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"api_url":           cfg.APIURL,
		"api_keys":          apiKeys,
		"model":             cfg.Model,
		"stream":            cfg.StreamingEnabled(),
		"timeout_sec":       cfg.TimeoutSec,
		"batch_concurrency": cfg.BatchConcurrency,
		"classifier_model":  cfg.ClassifierModel,
	})
}

// handleAdminSetLLMConfig 处理 POST /api/admin/users/{id}/config/llm：
// 保存指定账号的 LLM 配置（仅落盘，不触发全局 llmRecreate，避免干扰其它账号）。
// English: handles POST /api/admin/users/{id}/config/llm — saves the account's LLM config (disk only; it
// does not trigger a global llmRecreate so other accounts are not disturbed).
func (s *Server) handleAdminSetLLMConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	var req setLLMConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetLLMConfigFor(id, &config.LLMConfig{
		APIURL:           req.APIURL,
		Model:            req.Model,
		TimeoutSec:       req.TimeoutSec,
		Stream:           req.Stream,
		BatchConcurrency: req.BatchConcurrency,
		ClassifierModel:  req.ClassifierModel,
	})
	if req.APIKey != "" {
		s.auth.SetConfig(id, "llm_api_key", req.APIKey)
	}
	if len(req.APIKeys) > 0 {
		s.auth.SetConfig(id, "llm_api_keys", strings.Join(req.APIKeys, ","))
	}
	log.Printf("[admin] 用户 %s LLM 配置已保存", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
