// auth 包测试：注册/登录/令牌校验、临时账户、用户级配置、系统初始化标记与磁盘持久化。
package auth

import (
	"os"
	"path/filepath"
	"strings"
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

	// 令牌校验（§GAP6.2 登录轮换：以登录返回的原始令牌为准）
	if got := m.ValidateToken(lu.Token); got == nil || got.Username != "tester" {
		t.Error("ValidateToken 应返回对应用户(登录轮换后的令牌)")
	}
	if got := m.ValidateToken(u.Token); got != nil {
		t.Error("注册令牌在登录轮换后应失效")
	}
	if got := m.ValidateToken("bogus"); got != nil {
		t.Error("ValidateToken(bogus) 应返回 nil")
	}
	// UserToken 查询（内存缓存 = 最近一次签发的原始令牌）
	if m.UserToken("tester") != lu.Token {
		t.Error("UserToken 应返回最近签发令牌")
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
		if m.db.Users[i].Username == u.Username {
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

// TestCorruptDB §A6 损坏的 auth.json：坏文件改名保留 + Init 报错拒绝启动
// （fail-closed——静默清空会让 /setup 重新敞开被抢占接管）。
func TestCorruptDB(t *testing.T) {
	dir := t.TempDir()
	target := &Manager{dataDir: dir, dbPath: dir + "/auth.json"}
	_ = target.Init()
	write := []byte("{not-valid-json")
	_ = os.WriteFile(target.dbPath, write, 0644)

	m := NewManager(dir)
	err := m.Init()
	if err == nil {
		t.Fatal("损坏库 Init 应报错（fail-closed）")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("错误信息应说明备份位置, got %v", err)
	}
	// 坏文件已改名保留，可人工恢复
	if _, statErr := os.Stat(dir + "/auth.json"); statErr == nil {
		t.Fatal("损坏文件应已改名移走")
	}
	matches, _ := filepath.Glob(dir + "/auth.json.corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("应保留一份 corrupt 备份, got %v", matches)
	}
}

// TestAdminCreateUserAndPerms 管理员开户：角色/权限位/启用状态 + 权限判定。
func TestAdminCreateUserAndPerms(t *testing.T) {
	m := newTestMgr(t)

	u, err := m.CreateUser("alice", "pw1", RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	if !m.IsAdmin(u.ID) {
		t.Error("创建的管理员 IsAdmin 应为 true")
	}
	if !m.HasPerm(u.ID, PermResearchApprove) {
		t.Error("管理员应隐式拥有全部权限位")
	}

	bob, err := m.CreateUser("bob", "pw2", "", []string{PermResearchApprove}, 0)
	if err != nil {
		t.Fatalf("CreateUser(bob): %v", err)
	}
	if m.IsAdmin(bob.ID) {
		t.Error("普通用户 IsAdmin 应为 false")
	}
	if !m.HasPerm(bob.ID, PermResearchApprove) {
		t.Error("bob 应拥有 research_approve 权限")
	}
	if m.HasPerm(bob.ID, "some_other_perm") {
		t.Error("bob 不应拥有未授予的权限位")
	}

	// 撤销权限
	if err := m.RevokePerm(bob.ID, PermResearchApprove); err != nil {
		t.Fatalf("RevokePerm: %v", err)
	}
	if m.HasPerm(bob.ID, PermResearchApprove) {
		t.Error("撤销后 bob 不应再拥有 research_approve")
	}

	// 列表不泄露密码/令牌
	users := m.ListUsers()
	foundBob := false
	for _, uu := range users {
		if uu.Username == "bob" {
			foundBob = true
			if uu.PasswordHash != "" || uu.Token != "" {
				t.Error("ListUsers 不应泄露密码哈希或令牌")
			}
		}
	}
	if !foundBob {
		t.Error("ListUsers 应包含 bob")
	}
}

// TestSetRoleChangePasswordEnabled 角色变更/改密/禁用流转。
func TestSetRoleChangePasswordEnabled(t *testing.T) {
	m := newTestMgr(t)
	u, _ := m.CreateUser("carol", "pw", "", nil, 0)

	// 角色变更
	if err := m.SetRole(u.ID, RoleAdmin); err != nil {
		t.Fatalf("SetRole(admin): %v", err)
	}
	if !m.IsAdmin(u.ID) {
		t.Error("SetRole 后应为管理员")
	}
	if err := m.SetRole(u.ID, "bogus"); err == nil {
		t.Error("非法角色应报错")
	}

	// 改密：旧密码登录失败，新密码成功，且旧 token 失效
	if err := m.ChangePassword(u.ID, "newpw"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := m.Login("carol", "pw"); err == nil {
		t.Error("改密后旧密码不应能登录")
	}
	nu, err := m.Login("carol", "newpw")
	if err != nil {
		t.Fatalf("改密后新密码应能登录: %v", err)
	}
	if m.ValidateToken(u.Token) != nil {
		t.Error("改密后旧 token 应失效")
	}
	if m.ValidateToken(nu.Token) == nil {
		t.Error("改密后新 token 应有效")
	}

	// 禁用：登录/令牌失效，管理员不可禁用
	if err := m.SetRole(u.ID, RoleUser); err != nil {
		t.Fatalf("SetRole(user): %v", err)
	}
	nu, err = m.Login("carol", "newpw")
	if err != nil {
		t.Fatalf("角色改回 user 后新密码应能登录: %v", err)
	}
	if err := m.SetEnabled(u.ID, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if _, err := m.Login("carol", "newpw"); err == nil {
		t.Error("禁用账号不应能登录")
	}
	if m.ValidateToken(nu.Token) != nil {
		t.Error("禁用账号令牌应失效")
	}
	admin, _ := m.CreateUser("root2", "pw", RoleAdmin, nil, 0)
	if err := m.SetEnabled(admin.ID, false); err == nil {
		t.Error("不应允许禁用管理员账号")
	}
}

// TestInitMigrationPromotesAdmin 老库迁移：未标记 admin 的库应自动提升首个用户。
func TestInitMigrationPromotesAdmin(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	_ = m.Init()
	// 手工构造老库 JSON（无 role/enabled 字段）
	raw := []byte(`{"users":[{"id":"u_1","username":"bob","password_hash":"x","token":"t1","created_at":1}],"configs":[]}`)
	_ = os.WriteFile(m.dbPath, raw, 0644)

	m2 := NewManager(dir)
	if err := m2.Init(); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	bob := m2.UserByID("u_1")
	if bob == nil {
		t.Fatal("迁移后应能找到 bob")
	}
	if bob.Role != RoleAdmin {
		t.Errorf("迁移后首个非临时用户应提升为 admin, got %q", bob.Role)
	}
	if !bob.Enabled {
		t.Error("迁移后 bob 应默认启用")
	}
}

// TestUserExpiry 验证账号有效期：有限天数到期后登录/令牌校验失效，永久账号不过期，
// 重新设置天数可续期，设 0 恢复永久。
func TestUserExpiry(t *testing.T) {
	m := newTestMgr(t)
	u, err := m.CreateUser("alice_exp", "pw1", RoleUser, nil, 30)
	if err != nil {
		t.Fatalf("CreateUser with 30d expiry: %v", err)
	}
	if u.ExpiresAt <= 0 {
		t.Fatal("有限有效期账号 ExpiresAt 应 >0")
	}
	// 未到期：登录与令牌校验均应通过
	lu, err := m.Login("alice_exp", "pw1")
	if err != nil || lu == nil {
		t.Fatalf("未到期应可登录: %v", err)
	}
	if v := m.ValidateToken(lu.Token); v == nil {
		t.Fatal("未到期令牌应有效")
	}

	// 模拟过期：直接把 ExpiresAt 改到过去
	if err := m.updateUser(u.ID, func(x *User) error {
		x.ExpiresAt = time.Now().Add(-time.Hour).Unix()
		return nil
	}); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := m.Login("alice_exp", "pw1"); err == nil {
		t.Fatal("过期账号不应允许登录")
	}
	if v := m.ValidateToken(lu.Token); v != nil {
		t.Fatal("过期账号令牌应失效")
	}

	// 续期 30 天 → 恢复
	if err := m.SetExpiry(u.ID, 30); err != nil {
		t.Fatalf("SetExpiry(30): %v", err)
	}
	if v := m.ValidateToken(lu.Token); v == nil {
		t.Fatal("续期后令牌应恢复有效")
	}
	// 设 0 → 永久
	if err := m.SetExpiry(u.ID, 0); err != nil {
		t.Fatalf("SetExpiry(0): %v", err)
	}
	if got := m.UserByID(u.ID); got == nil || got.ExpiresAt != 0 {
		t.Fatal("设 0 后应永久有效(ExpiresAt=0)")
	}

	// 管理员不可设有限有效期
	adm, err := m.CreateUser("root_exp", "pw", RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if err := m.SetExpiry(adm.ID, 30); err == nil {
		t.Fatal("管理员不应允许设置有限有效期")
	}
}

// TestDisabledAccountStaysDisabled §A1：管理员禁用的账号重启后必须保持禁用
// （旧迁移每次启动强制 Enabled=true，封禁形同虚设）。
func TestDisabledAccountStaysDisabled(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	u, err := m.CreateUser("bob", "pw", RoleUser, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}

	m2 := NewManager(dir)
	if err := m2.Init(); err != nil {
		t.Fatal(err)
	}
	if got := m2.ValidateToken(u.Token); got != nil {
		t.Fatal("重启后禁用账号的令牌应仍失效")
	}
	for _, x := range m2.db.Users {
		if x.ID == u.ID && x.Enabled {
			t.Fatal("重启后禁用账号不得被迁移复活")
		}
	}
	// 禁用用户登录同样拒绝
	if _, err := m2.Login("bob", "pw"); err == nil {
		t.Fatal("禁用账号登录应失败")
	}
}

// TestTokenExpiryAndSlidingRenewal §A3：新令牌默认 30 天；登录滑动续期。
func TestTokenExpiryAndSlidingRenewal(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	_ = m.Init()
	u, err := m.Register("carol", "pw")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(tokenTTL).Unix()
	if u.TokenExp == 0 || u.TokenExp < want-60 || u.TokenExp > want+60 {
		t.Fatalf("注册令牌应约 30 天过期, got %d (want≈%d)", u.TokenExp, want)
	}
	// 手动把剩余有效期压到 1 小时 → §GAP6.2 登录即轮换并签发全新 30 天令牌
	m.mu.Lock()
	u.TokenExp = time.Now().Add(time.Hour).Unix()
	m.mu.Unlock()
	lu, err := m.Login("carol", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if lu.Token == "" || lu.Token == u.Token {
		t.Fatal("登录应轮换出全新原始令牌")
	}
	fresh := m.ValidateToken(lu.Token)
	if fresh == nil {
		t.Fatal("登录轮换后的令牌应有效")
	}
	got := fresh.TokenExp
	if got < want-60 || got > want+60 {
		t.Fatalf("登录后令牌应续满 30 天, got %d", got)
	}
}
