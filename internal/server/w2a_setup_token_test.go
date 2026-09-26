package server

// w2a_setup_token_test.go —— §0926E2E-W2A（2026-09-26 二波）：SETUP_TOKEN 初始化守卫行为用例。
//
// 缺陷背景（全量审计缺陷 6）：守卫代码自 §P1-5 就在（server.go handleSetupSubmit），
// 但部署面（systemd 单元 / 注册脚本 / 部署脚本）从不注入 SETUP_TOKEN ⇒ 现网恒为"未开启"，
// 且**零测试覆盖**——守卫哪天被改坏没人知道。本批把部署面接线的同时补满行为用例：
//   ① 开启时缺令牌/错令牌 → 401，绝不落到建号步；
//   ② 令牌走 X-Setup-Token 头 与 走请求体 setup_token 字段 两条路都要通；
//   ③ 全链成功：空库 + 正确令牌 + 用户名密码 → 200 建管理员；
//   ④ 反证（守卫不得误伤）：未配置 SETUP_TOKEN 时不带令牌也放行——这正是"部署面零命中"
//     的现网形态，测试把该语义钉死，逼部署面接线成为唯一的开启方式。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
)

// newW2aSetupServer 构造最小可用 /setup 服务器：auth 用独立临时目录（空库=未初始化），
// setupToken 模拟环境变量注入结果（空串=守卫关闭）。
func newW2aSetupServer(t *testing.T, setupToken string) *Server {
	t.Helper()
	am := auth.NewManager(t.TempDir())
	if err := am.Init(); err != nil { // 不 Init 则 db 为 nil（NewManager 只建壳），必须真实走一遍空库初始化
		t.Fatalf("auth.Init: %v", err)
	}
	return &Server{
		auth:       am,
		setupToken: setupToken,
	}
}

// w2aPost 直调 handleSetupSubmit：body 为请求体 JSON，hdr 为可选的 X-Setup-Token 头值（空=不带）。
func w2aPost(t *testing.T, s *Server, body, hdr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(body))
	if hdr != "" {
		req.Header.Set("X-Setup-Token", hdr)
	}
	rr := httptest.NewRecorder()
	s.handleSetupSubmit(rr, req)
	return rr
}

// TestW2aGuardRejectsMissingOrWrongToken：守卫开启时，缺令牌与错令牌都必须 401，
// 且绝不能把用户名密码当成建号请求受理（401 先于 400/200）。
func TestW2aGuardRejectsMissingOrWrongToken(t *testing.T) {
	s := newW2aSetupServer(t, "s3cret-token")
	cases := []struct {
		name string
		body string
		hdr  string
	}{
		{"不带任何令牌", `{"username":"admin","password":"pw12345678"}`, ""},
		{"请求体令牌错", `{"username":"admin","password":"pw12345678","setup_token":"wrong"}`, ""},
		{"头令牌错", `{"username":"admin","password":"pw12345678"}`, "wrong"},
		{"头与体都错（体不得被头盖过放行）", `{"username":"admin","password":"pw12345678","setup_token":"bad1"}`, "bad2"},
	}
	for _, c := range cases {
		rr := w2aPost(t, s, c.body, c.hdr)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: 期望 401，实得 %d (%s)", c.name, rr.Code, rr.Body.String())
		}
	}
	// 反证守卫确实拦住了建号：库里必须仍是未初始化（无管理员）。
	if s.auth.IsInitialized() {
		t.Fatal("401 路径却完成了初始化：守卫未先于建号步")
	}
}

// TestW2aTokenViaHeaderAndBody：两条供令牌的路都放行（通过守卫后止步于参数校验 400，
// 证明"放行判定发生在守卫处而非撞库成功"）。
func TestW2aTokenViaHeaderAndBody(t *testing.T) {
	s := newW2aSetupServer(t, "s3cret-token")
	if rr := w2aPost(t, s, `{"username":"","password":""}`, "s3cret-token"); rr.Code != http.StatusBadRequest {
		t.Fatalf("头路放行后应止步 400（缺用户名），实得 %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := w2aPost(t, s, `{"username":"","password":"","setup_token":"s3cret-token"}`, ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("体路放行后应止步 400（缺用户名），实得 %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestW2aFullSetupHappyPath：空库 + 正确令牌 + 合法账号 → 200 并回发 token，系统转入已初始化。
func TestW2aFullSetupHappyPath(t *testing.T) {
	s := newW2aSetupServer(t, "s3cret-token")
	rr := w2aPost(t, s, `{"username":"admin","password":"pw12345678"}`, "s3cret-token")
	if rr.Code != http.StatusOK {
		t.Fatalf("正确令牌建号应 200，实得 %d (%s)", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out["token"] == "" {
		t.Fatalf("200 响应应带新管理员 token，实得 %s", rr.Body.String())
	}
	if !s.auth.IsInitialized() {
		t.Fatal("建号成功后应处于已初始化态")
	}
}

// TestW2aUnsetTokenKeepsOpen（反证 + 现网形态钉死）：SETUP_TOKEN 未配置时守卫不拦——
// 这正是缺陷 6 的成因（部署面零命中 ⇒ 守卫恒开不了）。本用例把"未配置=不拦"记档，
// 部署面接线（§0926E2E-W2A 注册步/quant.env 注入）完成前，现网即处于此形态。
func TestW2aUnsetTokenKeepsOpen(t *testing.T) {
	s := newW2aSetupServer(t, "")
	rr := w2aPost(t, s, `{"username":"admin","password":"pw12345678"}`, "")
	if rr.Code == http.StatusUnauthorized {
		t.Fatal("未配置 SETUP_TOKEN 时不得判 401（守卫语义是环境变量开启才生效）")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("未初始化 + 合法账号应建号成功 200，实得 %d (%s)", rr.Code, rr.Body.String())
	}
}
