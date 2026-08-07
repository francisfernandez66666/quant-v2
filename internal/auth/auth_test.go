// auth 包测试：注册/登录/令牌校验、临时账户、用户级配置、系统初始化标记与磁盘持久化。
package auth

import (
	"os"
	"testing"
	"time"
)

func newTestMgr(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	if err := m.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return m
}

// TestRegisterLoginValidate ... 注册→登录→令牌校验闭环 + 密码非明文。
func TestRegisterLoginValidate(t *testing.T) {
	m := newTestMgr(t)
	u, err := m.Register("tester", "secret123")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.PasswordHash == "" || u.PasswordHash == "secret123" {
		t.Error("密码应以 bcrypt 存储，不应为明文")
	}
	if len(u.Token) != 64 {
		t.Errorf("令牌应为 32 字节 hex(64字符), got len=%d", len(u.Token))
	}

	// 正确密码登录
	lu, err := m.Login("tester", "secret123")
	if err != nil || lu == nil {
		t.Fatalf("Login(正确) 应成功: %v", err)
	}
	// 错误密码登录拒绝
	if _, err := m.Login("tester", "wrong"); err == nil {
		t.Error("Login(错误密码) 应失败")
	}

	// 令牌校验
	if got := m.ValidateToken(u.Token); got == nil || got.Username != "tester" {
		t.Error("ValidateToken 应返回对应用户")
	}
	if got := m.ValidateToken("bogus"); got != nil {
		t.Error("ValidateToken(bogus) 应返回 nil")
	}
	// UserToken 查询
	if m.UserToken("tester") != u.Token {
		t.Error("UserToken 应返回注册令牌")
	}
}

// TestRegisterDuplicate 重名注册应报错。
func TestRegisterDuplicate(t *testing.T) {
	m := newTestMgr(t)
	if _, err := m.Register("dup", "p"); err != nil {
		t.Fatalf("首次注册: %v", err)
	}
	if _, err := m.Register("dup", "p2"); err == nil {
		t.Error("重复用户名注册应失败")
	}
}

// TestCreateTemp 临时账户：无密码不可登录、令牌可用、过期后失效。
func TestCreateTemp(t *testing.T) {
	m := newTestMgr(t)
	u, err := m.CreateTemp(2 * time.Second)
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if m.ValidateToken(u.Token) == nil {
		t.Error("临时令牌应有效")
	}
	// 临时账户不可密码登录（应被拒绝）
	if _, err := m.Login(u.Username, "x"); err == nil {
		t.Error("临时账户不应允许密码登录")
	}
	// 过期后令牌失效：手动把 TokenExp 改到过去
	m.mu.Lock()
	for i := range m.db.Users {
		if m.db.Users[i].Token == u.Token {
			m.db.Users[i].TokenExp = time.Now().Add(-time.Second).Unix()
		}
	}
	m.mu.Unlock()
	if m.ValidateToken(u.Token) != nil {
		t.Error("过期临时令牌应失效")
	}
}

// TestConfigKV 用户级键值配置：写入/覆盖/读取/缺失。
func TestConfigKV(t *testing.T) {
	m := newTestMgr(t)
	if _, ok := m.GetConfig("u1", "theme"); ok {
		t.Error("初始不应存在 theme")
	}
	if err := m.SetConfig("u1", "theme", "dark"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if v, ok := m.GetConfig("u1", "theme"); !ok || v != "dark" {
		t.Errorf("读取 theme 应=dark, got %q ok=%v", v, ok)
	}
	// 覆盖
	if err := m.SetConfig("u1", "theme", "light"); err != nil {
		t.Fatalf("覆盖: %v", err)
	}
	if v, _ := m.GetConfig("u1", "theme"); v != "light" {
		t.Errorf("覆盖后 theme 应=light, got %q", v)
	}
	// 不同用户隔离
	if _, ok := m.GetConfig("u2", "theme"); ok {
		t.Error("不同用户应隔离")
	}
}

// TestInitializedFlow 系统初始化标记 + 落盘持久化。
func TestInitializedFlow(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	_ = m.Init()

	if m.IsInitialized() {
		t.Error("空库未初始化时 IsInitialized 应=false")
	}
	if k, _ := m.GetConfig("system", "initialized"); k != "" {
		t.Error("未 MarkInitialized 前不应有 inicializado 标记")
	}
	if err := m.MarkInitialized(); err != nil {
		t.Fatalf("MarkInitialized: %v", err)
	}
	if !m.IsInitialized() {
		t.Error("MarkInitialized 后 IsInitialized 应=true")
	}

	// 重新加载同一目录，验证已持久化 + 用户留存
	m2 := NewManager(dir)
	if err := m2.Init(); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if !m2.IsInitialized() {
		t.Error("重载后 IsInitialized 应保持 true")
	}
}

// TestCorruptDB 破坏的 auth.json 应回退为空库而非崩溃。
func TestCorruptDB(t *testing.T) {
	dir := t.TempDir()
	target := &Manager{dataDir: dir, dbPath: dir + "/auth.json"}
	_ = target.Init()
	write := []byte("{not-valid-json")
	_ = os.WriteFile(target.dbPath, write, 0644)

	m := NewManager(dir)
	if err := m.Init(); err != nil {
		t.Fatalf("损坏库 Init 不应报错: %v", err)
	}
	if m.db == nil {
		t.Fatal("损坏库应回退为空库")
	}
}