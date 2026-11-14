// 文件概述（internal/server/login_shape_f6_test.go）
// §187 U1/U2/U5/U6：登录失败的 HTTP 层**响应体字节全等**——错口令（正常/停用）与
// 不存在用户必须同形（只比状态码不够，多一个字段就能继续探账号状态）；
// 正确口令 + 停用仍保留 403 account_disabled 的续期引导（产品需求不得被"统一 401"改没）。
// U3/U4（过期号）在 internal/auth/login_order_f6_test.go 的受测层覆盖（API 造不出过期号）。
// English: §F6 HTTP-level byte-equality for login failure shapes.
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func f6Login(t *testing.T, s *Server, username, password string) (int, string) {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
	return rr.Code, rr.Body.String()
}

func f6Sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestF6LoginFailureBodiesByteIdentical(t *testing.T) {
	s, _ := newAdminTestServer(t)
	// 新建两个正常用户（口令不同，便于对照）
	u1, err := s.auth.CreateUser("f6user", "pw-right", "", nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, err = s.auth.CreateUser("f6disabled", "pw-right2", "", nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// U1：错口令 + 启用
	code1, body1 := f6Login(t, s, "f6user", "pw-wrong")
	if code1 != http.StatusUnauthorized {
		t.Fatalf("U1 应 401, got %d body=%s", code1, body1)
	}
	// 停用 f6disabled
	du := s.auth.UserByID(u1.ID) // 仅用于确认 API 可用；停用下面按用户名找回 ID
	_ = du
	var disabledID string
	for _, u := range s.auth.ListUsers() {
		if u.Username == "f6disabled" {
			disabledID = u.ID
		}
	}
	if disabledID == "" {
		t.Fatal("找不到 f6disabled")
	}
	if err := s.auth.SetEnabled(disabledID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	// U2：错口令 + 停用 → 与 U1 字节全等
	code2, body2 := f6Login(t, s, "f6disabled", "pw-wrong2")
	if code2 != code1 || f6Sha(body2) != f6Sha(body1) {
		t.Fatalf("U2 与 U1 必须字节全等: %d/%s vs %d/%s", code2, body2, code1, body1)
	}
	// U6：不存在用户 → 与 U1 字节全等
	code6, body6 := f6Login(t, s, "no_such_user_zz", "x")
	if code6 != code1 || f6Sha(body6) != f6Sha(body1) {
		t.Fatalf("U6 与 U1 必须字节全等: %d/%s vs %d/%s", code6, body6, code1, body1)
	}
	// U5：正确口令 + 停用 → 403 分码保留（产品需求）
	code5, body5 := f6Login(t, s, "f6disabled", "pw-right2")
	if code5 != http.StatusForbidden || !strings.Contains(body5, "account_disabled") {
		t.Fatalf("U5 应 403 account_disabled, got %d %s", code5, body5)
	}
}
