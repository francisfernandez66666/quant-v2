// app_version_test.go — §APPVER 2026-09-22 C批：公开版本端点 GET /api/app/version 测试。
//
// 验证三件事：
//  1. 免鉴权可达：不带任何 Authorization 头经完整 mux 请求 → 200（注册处刻意不套 authMiddleware，
//     这是"登录前更新检查"的前提，若哪天被误加鉴权本测试即红）；
//  2. 响应契约：{ok, min_version_code, latest_version_code, apk_url, note} 五字段齐全，
//     Content-Type 为 application/json；
//  3. 未配置=未发布：AppRelease 零值时 min_version_code 返回 0（客户端不拦），
//     配置后原样透出。
//
// 骨架抄自同包 r7_endpoints_test.go（&Server{auth,cfg,mux} + registerRoutes + httptest）。
// English: public endpoint test — no-auth 200 through the real mux, fixed JSON shape, and
// zero-value config reported as "not published" (min=0).
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// newAppVersionServer 构建仅含 auth+cfg+mux 全路由的最小 Server（本端点不碰其他依赖）。
// English: minimal Server with auth+cfg and the full route table — enough for this read-only endpoint.
func newAppVersionServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	cfgMgr := config.NewManager(dir + "/config.json")
	cfgMgr.SetStore(mgr)
	s := &Server{auth: mgr, cfg: cfgMgr, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

// getAppVersion 无 Authorization 直请求公开端点（刻意模拟登录前的原生壳）。
// English: issues the GET without any Authorization header — exactly the pre-login shell path.
func getAppVersion(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/app/version", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	return rr
}

// TestAppVersionPublicEndpoint §APPVER：免鉴权 200 + 字段齐 + 未配置 min=0 + 配置后透出。
// English: unauthenticated 200, full field set, min=0 when unpublished, manifest echoed when set.
func TestAppVersionPublicEndpoint(t *testing.T) {
	s := newAppVersionServer(t)

	// 1) 未配置（DefaultRules 不含 app_release）：200 + min=0（未发布=不拦）。
	rr := getAppVersion(t, s)
	if rr.Code != http.StatusOK {
		t.Fatalf("免鉴权请求应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type 应为 application/json, got %q", ct)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("非法 JSON: %v body=%s", err, rr.Body.String())
	}
	for _, key := range []string{"ok", "min_version_code", "latest_version_code", "apk_url", "note"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("响应缺少字段 %q: %v", key, body)
		}
	}
	if body["ok"] != true {
		t.Fatalf("ok 应为 true, got %v", body["ok"])
	}
	if v, _ := body["min_version_code"].(float64); v != 0 {
		t.Fatalf("未配置时 min_version_code 应为 0（未发布不拦）, got %v", body["min_version_code"])
	}

	// 2) 配置发布单后：原样透出（内存快照即可——端点读的就是当前配置）。
	s.cfg.Get().AppRelease = config.AppReleaseConfig{
		MinVersionCode: 2, LatestVersionCode: 3,
		ApkURL: "https://example.invalid/dl/quant-latest.apk",
		Note:   "安全更新：请升级",
	}
	rr2 := getAppVersion(t, s)
	if rr2.Code != http.StatusOK {
		t.Fatalf("配置后仍应 200, got %d", rr2.Code)
	}
	var got struct {
		OK                bool   `json:"ok"`
		MinVersionCode    int    `json:"min_version_code"`
		LatestVersionCode int    `json:"latest_version_code"`
		ApkURL            string `json:"apk_url"`
		Note              string `json:"note"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if !got.OK || got.MinVersionCode != 2 || got.LatestVersionCode != 3 ||
		got.ApkURL != "https://example.invalid/dl/quant-latest.apk" || got.Note != "安全更新：请升级" {
		t.Fatalf("发布单字段未原样透出: %+v", got)
	}
}
