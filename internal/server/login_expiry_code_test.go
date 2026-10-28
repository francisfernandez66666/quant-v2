// 登录分码回归（§FIX-20261028-F04，2026-10-29）。
//
// 缺陷本体：auth.Manager.Login 已经分好三种失败（account expired / account disabled /
// wrong password），server.handleLogin 却把 err 整体丢掉、统一写 401 invalid credentials。
// 结果是"账号到期"与"口令打错"在屏上完全同形、没有任何续期引导——本轮 UAT 就因此把自己
// 锁在门外 15 分钟（22:36 还 200，22:48 起 401/32B，续期后立刻恢复）。
//
// 本文件锁四件事：
//  1. 到期账号 + 正确口令 ⇒ 403 account_expired（并带可操作文案）；
//  2. 停用账号 + 正确口令 ⇒ 403 account_disabled；
//  3. 口令错 ⇒ 401 invalid credentials（与旧行为一致）；
//  4. 用户名不存在 ⇒ 401 invalid credentials，**必须与第 3 条同形**（防枚举不回退）。
//
// English: login failure codes — expired/disabled accounts get an explicit 403 code plus an
// actionable message, while wrong-password and unknown-user stay byte-identical 401s so the
// anti-enumeration contract is not weakened.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"errors"
)

// loginBody 登录响应里本测试关心的字段。
type loginBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Token   string `json:"token"`
}

// postLogin 直接打登录端点（不带任何既有会话）。
func postLogin(t *testing.T, s *Server, user, pass string) (int, loginBody) {
	t.Helper()
	payload := `{"username":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	var out loginBody
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

// TestLoginFailureCodeMapping 分码映射的表驱动断言（到期/停用给码，其余一律空码）。
//
// 为什么不造一个"真过期账号"来打端点：auth.CreateUser 与 auth.SetExpiry 都拒绝负数天数
//（运维面本来就没有"立即置过期"的入口，停用走 SetEnabled），为测试去加一条产品 API 不划算。
// 分码规则本身是纯函数，直接钉住；"Login 真的会返回这三条错误串"由 auth 包的同名契约测试锁。
func TestLoginFailureCodeMapping(t *testing.T) {
	cases := []struct {
		err       error
		wantCode  string
		wantInMsg string
	}{
		{errors.New("account expired"), "account_expired", "续期"},
		{errors.New("account disabled"), "account_disabled", "停用"},
		{errors.New("wrong password"), "", ""},
		{errors.New("user not found"), "", ""},
		{errors.New("cannot login with temp account"), "", ""},
		{nil, "", ""},
	}
	for _, c := range cases {
		code, msg := loginFailureCode(c.err)
		if code != c.wantCode {
			t.Fatalf("err=%v 分码应为 %q，实到 %q", c.err, c.wantCode, code)
		}
		if c.wantInMsg != "" && !strings.Contains(msg, c.wantInMsg) {
			t.Fatalf("err=%v 文案应含 %q，实到 %q", c.err, c.wantInMsg, msg)
		}
	}
}

// TestLoginWrongPasswordAndUnknownUserStayIdentical 口令错与用户名不存在必须保持同形 401。
//
// 这是本条修复的**安全底线**：分码只对"用户名存在且口令正确"的账号生效，
// 绝不因为要提示到期就把"用户名是否存在"漏出去。
func TestLoginWrongPasswordAndUnknownUserStayIdentical(t *testing.T) {
	s, _ := newAdminTestServer(t)
	if _, err := s.auth.CreateUser("normal1", "goodpass", auth.RoleUser, nil, 0); err != nil {
		t.Fatalf("create user: %v", err)
	}
	codeWrong, bodyWrong := postLogin(t, s, "normal1", "badpass")
	codeGhost, bodyGhost := postLogin(t, s, "no-such-user", "whatever")
	if codeWrong != http.StatusUnauthorized || codeGhost != http.StatusUnauthorized {
		t.Fatalf("两者都应 401，实到 wrong=%d ghost=%d", codeWrong, codeGhost)
	}
	if bodyWrong.Error != "invalid credentials" || bodyWrong.Error != bodyGhost.Error {
		t.Fatalf("口令错与不存在必须同形，实到 %q vs %q", bodyWrong.Error, bodyGhost.Error)
	}
	if bodyWrong.Message != bodyGhost.Message {
		t.Fatalf("两条的文案也必须一致，实到 %q vs %q", bodyWrong.Message, bodyGhost.Message)
	}
}

// TestLoginNormalAccountStillWorks 正常账号对照：分码不得误伤成功路径。
func TestLoginNormalAccountStillWorks(t *testing.T) {
	s, _ := newAdminTestServer(t)
	if _, err := s.auth.CreateUser("normal2", "goodpass", auth.RoleUser, nil, 0); err != nil {
		t.Fatalf("create user: %v", err)
	}
	code, body := postLogin(t, s, "normal2", "goodpass")
	if code != http.StatusOK || body.Token == "" {
		t.Fatalf("正常账号应 200 且有 token，实到 %d %+v", code, body)
	}
}
