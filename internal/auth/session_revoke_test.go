// Package auth — §D1/§D7 修复回归测试：
//
//	D1: /api/admin/users 曾经把 Sessions[].Token（SHA-256 哈希）随 PublicUser() 一并序列化，
//	    配合 ValidateToken 路径二 matchPlain 可回放冒用；PublicUser 必须清空 Sessions/TokenExp。
//	D7: 无自助退出——清 localStorage 后服务端会话仍有效到 TTL；新增 RevokeSession。
//
// English: §D1/D7 regression tests for session-credential stripping and self-service logout.
package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPublicUserStripsSessionCredentials 断言 PublicUser 输出不含任何会话哈希/兼容槽/过期时间，
// 且 JSON 序列化后 `sessions` / `token` / `token_exp` 键整体缺席（omitempty 生效）。
func TestPublicUserStripsSessionCredentials(t *testing.T) {
	m := newTestMgr(t)
	code, _ := m.CreateInvite()
	if _, err := m.Register("d1user", "pw", code); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Login("d1user", "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Login("d1user", "pw"); err != nil {
		t.Fatal(err) // 两设备登录，Sessions 至少 2 条
	}
	var raw *User
	for _, u := range m.ListUsers() {
		if u.Username == "d1user" {
			cp := u
			raw = &cp
		}
	}
	if raw == nil {
		t.Fatal("ListUsers 未找到 d1user")
	}
	if len(raw.Sessions) != 0 {
		t.Errorf("PublicUser Sessions 应清空，实得 %d 条", len(raw.Sessions))
	}
	if raw.Token != "" || raw.PasswordHash != "" || raw.TokenExp != 0 {
		t.Errorf("PublicUser 敏感字段应全部清零，Token=%q PasswordHash=%q TokenExp=%d",
			raw.Token, raw.PasswordHash, raw.TokenExp)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, leak := range []string{`"sessions"`, `"token_exp"`} {
		if strings.Contains(s, leak) {
			t.Errorf("PublicUser JSON 不应包含 %s，实际=%s", leak, s)
		}
	}
	// password_hash / token 声明无 omitempty（历史字段），断言值必为空字符串
	if strings.Contains(s, `"password_hash":"`) && !strings.Contains(s, `"password_hash":""`) {
		t.Errorf("password_hash 值应为空字符串，实际=%s", s)
	}
}

// TestEnabledSerializesWhenFalse 断言 Enabled=false 在 JSON 输出中显式存在（D3 修复：
// 前端读 undefined 会误判为 true）；Enabled=true 同样可见（不再 omitempty）。
func TestEnabledSerializesWhenFalse(t *testing.T) {
	m := newTestMgr(t)
	code, _ := m.CreateInvite()
	u, err := m.Register("d3user", "pw", code)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}
	got := m.UserByID(u.ID)
	if got == nil {
		t.Fatal("UserByID 返回 nil")
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"enabled":false`) {
		t.Errorf("禁用用户 JSON 应包含 \"enabled\":false，实得=%s", b)
	}
	if err := m.SetEnabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(m.UserByID(u.ID))
	if !strings.Contains(string(b2), `"enabled":true`) {
		t.Errorf("启用用户 JSON 应包含 \"enabled\":true，实得=%s", b2)
	}
}

// TestRevokeSessionSelfLogout 断言：
//  1. 有效 Bearer 吊销后 ValidateToken 立即失效；
//  2. 同用户其他设备会话仍有效（不误伤）；
//  3. 二次 RevokeSession 同 token 返回 false（幂等）；
//  4. 兼容槽（u.Token）指向被吊销哈希时同步清空。
func TestRevokeSessionSelfLogout(t *testing.T) {
	m := newTestMgr(t)
	code, _ := m.CreateInvite()
	ru, err := m.Register("d7user", "pw", code)
	if err != nil {
		t.Fatal(err)
	}
	lu1, err := m.Login("d7user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	lu2, err := m.Login("d7user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !m.RevokeSession(lu1.Token) {
		t.Fatal("RevokeSession 应命中已存在会话")
	}
	if m.ValidateToken(lu1.Token) != nil {
		t.Error("吊销后 lu1 令牌应立刻失效")
	}
	if m.ValidateToken(lu2.Token) == nil {
		t.Error("吊销 lu1 不应误伤 lu2")
	}
	if m.RevokeSession(lu1.Token) {
		t.Error("重复 RevokeSession 应返回 false")
	}
	// 兼容槽同步：若 u.Token 曾指向 lu1 哈希，应已清空；否则指向 lu2 保留。
	// 简单断言：吊销全部三条会话（Register 首会话 + 两次 Login）后 Sessions 与 Token 全空。
	if !m.RevokeSession(lu2.Token) {
		t.Fatal("RevokeSession lu2 应命中")
	}
	if !m.RevokeSession(ru.Token) {
		t.Fatal("RevokeSession Register 首会话应命中")
	}
	m.mu.RLock()
	var tok string
	var n int
	for _, u := range m.db.Users {
		if u.Username == "d7user" {
			tok = u.Token
			n = len(u.Sessions)
		}
	}
	m.mu.RUnlock()
	if n != 0 {
		t.Errorf("全部会话吊销后 Sessions 应为空，实得 %d", n)
	}
	if tok != "" {
		t.Errorf("兼容槽 Token 应同步清空，实得 %q", tok)
	}
}
