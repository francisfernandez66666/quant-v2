// 文件概述（internal/auth/login_order_f6_test.go）
// §F6-20261114 登录分码顺序：bcrypt 必须先于 Enabled/expired 判定——错口令一律
// "wrong password"（上层 401 同形），不能再探出账号是否被停用/过期（P1-1 枚举面）；
// 口令正确时保留 403 分码（续期引导产品需求，§FIX-20261028-F04 不得回退）。
// English: §F6-20261114 — password check must precede account-state checks: a wrong
// password always yields "wrong password"; the disabled/expired codes remain only for
// correct passwords (renewal guidance is a product requirement).
package auth

import "testing"

func newF6Manager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	if err := m.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return m
}

func TestF6WrongPasswordNeverLeaksState(t *testing.T) {
	m := newF6Manager(t)
	code, _ := m.CreateInvite()
	u, err := m.CreateTemp(24*60*60, code) // 临时号禁密码登录；另建正常号做口令腿
	_ = u
	_ = code
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	code0, _ := m.CreateInvite()
	normal, err := m.Register("f6user", "pw-right", code0)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// ① 错口令 + 启用 → wrong password（401 同形基线）
	if _, err := m.Login("f6user", "pw-wrong"); err == nil || err.Error() != "wrong password" {
		t.Fatalf("错口令应 wrong password, got %v", err)
	}
	// ② 错口令 + 停用（核心：此前返回 account disabled ⇒ 探出状态）
	if err := m.SetEnabled(normal.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, err := m.Login("f6user", "pw-wrong"); err == nil || err.Error() != "wrong password" {
		t.Fatalf("错口令+停用应仍 wrong password（不得探出停用态）, got %v", err)
	}
	// ③ 正确口令 + 停用 → account disabled（产品分码保留）
	if _, err := m.Login("f6user", "pw-right"); err == nil || err.Error() != "account disabled" {
		t.Fatalf("正确口令+停用应 account disabled, got %v", err)
	}
}

func TestF6WrongPasswordNeverLeaksExpiry(t *testing.T) {
	m := newF6Manager(t)
	codeE, _ := m.CreateInvite()
	normal, err := m.Register("f6exp", "pw-right", codeE)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// 直接受测层造过期号（API 层 SetExpiry 拒负数天）
	m.mu.Lock()
	for i := range m.db.Users {
		if m.db.Users[i].ID == normal.ID {
			m.db.Users[i].ExpiresAt = 1 // 1970 年 = 已过期
		}
	}
	m.mu.Unlock()
	// ① 错口令 + 过期 → wrong password（不得探出过期态）
	if _, err := m.Login("f6exp", "pw-wrong"); err == nil || err.Error() != "wrong password" {
		t.Fatalf("错口令+过期应仍 wrong password, got %v", err)
	}
	// ② 正确口令 + 过期 → account expired（续期引导保留）
	if _, err := m.Login("f6exp", "pw-right"); err == nil || err.Error() != "account expired" {
		t.Fatalf("正确口令+过期应 account expired, got %v", err)
	}
}
