// §MT 多租户核心（auth 层）测试：存量库迁移、租户配额、邀请码租户绑定、
// 平台运营者判定与降级保护、跨租户迁移。
// English: §MT multi-tenancy core tests: legacy migration, quotas, invite-tenant binding,
// platform-admin semantics and cross-tenant moves.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTenantMgr 在临时目录里建好并 Init 一个多租户管理器，
// 让各用例的 users.json 落盘互不干扰。
func newTenantMgr(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	if err := m.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return m
}

// TestTenantLegacyMigration 存量库（无 TenantID、无 tenants 表、SchemaVersion=1）升级后：
// 全员迁入 t_default、租户表补齐、落盘可见。
func TestTenantLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := DB{
		Users: []User{
			{ID: "u_1", Username: "admin", Role: RoleAdmin, Enabled: true},
			{ID: "u_2", Username: "alice", Role: RoleUser, Enabled: true},
		},
		SchemaVersion: 1,
	}
	b, _ := json.MarshalIndent(legacy, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	if err := m.Init(); err != nil {
		t.Fatalf("init legacy: %v", err)
	}
	for _, id := range []string{"u_1", "u_2"} {
		if got := m.TenantOf(id); got != DefaultTenantID {
			t.Errorf("%s 应迁入 %s, got %q", id, DefaultTenantID, got)
		}
	}
	ts := m.ListTenants()
	if len(ts) != 1 || ts[0].ID != DefaultTenantID || !ts[0].Enabled {
		t.Fatalf("应自动补齐 t_default, got %+v", ts)
	}
	if m.SchemaVersion() != 2 {
		t.Errorf("升级后 schema 应为 2, got %d", m.SchemaVersion())
	}
	// 落盘复核（重启幂等不再改动）
	raw, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	var db DB
	if err := json.Unmarshal(raw, &db); err != nil {
		t.Fatal(err)
	}
	if db.Tenants[DefaultTenantID] == nil || db.Users[0].TenantID != DefaultTenantID {
		t.Errorf("迁移未落盘: %+v", db.Tenants)
	}
	m2 := NewManager(dir)
	if err := m2.Init(); err != nil {
		t.Fatal(err)
	}
	if len(m2.ListTenants()) != 1 {
		t.Errorf("二次启动应幂等")
	}
}

// TestTenantQuotaEnforcement MaxUsers 配额：达到上限后开户返回 ErrTenantQuota。
func TestTenantQuotaEnforcement(t *testing.T) {
	m := newTenantMgr(t)
	quota := TenantQuota{MaxUsers: 2}
	tt, err := m.CreateTenant("甲租户", quota)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := m.CreateUserInTenant(tt.ID, "a1", "pw", "", nil, 0); err != nil {
		t.Fatalf("member1: %v", err)
	}
	if _, err := m.CreateUserInTenant(tt.ID, "a2", "pw", "", nil, 0); err != nil {
		t.Fatalf("member2: %v", err)
	}
	_, err = m.CreateUserInTenant(tt.ID, "a3", "pw", "", nil, 0)
	if !errors.Is(err, ErrTenantQuota) {
		t.Fatalf("满额开户应 ErrTenantQuota, got %v", err)
	}
	// temp 号不计配额但需租户存在
	if m.TenantMemberCount(tt.ID) != 2 {
		t.Errorf("成员数应为 2, got %d", m.TenantMemberCount(tt.ID))
	}
	// 配额独立于 t_default：默认租户开户不受甲租户配额影响
	if _, err := m.CreateUser("free", "pw", "", nil, 0); err != nil {
		t.Errorf("默认租户开户应成功: %v", err)
	}
}

// TestInviteTenantBinding 邀请码绑定租户：注册者落入该租户并受其配额二检。
func TestInviteTenantBinding(t *testing.T) {
	m := newTenantMgr(t)
	tt, err := m.CreateTenant("乙租户", TenantQuota{MaxUsers: 1})
	if err != nil {
		t.Fatal(err)
	}
	code, err := m.CreateInviteFor(tt.ID)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	u, err := m.Register("bob", "pw", code)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.TenantID != tt.ID {
		t.Errorf("注册者应落邀请租户, got %q", u.TenantID)
	}
	// 满额后签发新邀请码即被拒
	if _, err := m.CreateInviteFor(tt.ID); !errors.Is(err, ErrTenantQuota) {
		t.Errorf("满额签发邀请应 ErrTenantQuota, got %v", err)
	}
	// 停用租户后：已签发票据注册也被拒（二检）
	off := false
	if err := m.UpdateTenant(tt.ID, nil, &off, nil); err != nil {
		t.Fatal(err)
	}
	inv := &Invite{Code: "QTman1", CreatedAt: 1, TenantID: tt.ID}
	m.db.Invites["QTman1"] = inv // 测试内直接种一张存量票
	if _, err := m.Register("carol", "pw", "QTman1"); !errors.Is(err, ErrTenantDisabled) {
		t.Errorf("停用租户注册应 ErrTenantDisabled, got %v", err)
	}
	// 默认邀请码仍可用且落 t_default
	c2, err := m.CreateInvite()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := m.Register("dave", "pw", c2)
	if err != nil || u2.TenantID != DefaultTenantID {
		t.Fatalf("默认邀请应落 t_default: %v %+v", err, u2)
	}
}

// TestPlatformAdminSemantics 平台运营者判定与"最后一名平台 admin 禁止降级"。
func TestPlatformAdminSemantics(t *testing.T) {
	m := newTenantMgr(t)
	pa, _ := m.CreateUser("root", "pw", RoleAdmin, nil, 0)
	if !m.IsPlatformAdmin(pa.ID) {
		t.Fatal("t_default 的 admin 应为平台运营者")
	}
	tt, _ := m.CreateTenant("丙租户", TenantQuota{})
	ta, _ := m.CreateUserInTenant(tt.ID, "tadmin", "pw", RoleAdmin, nil, 0)
	if m.IsPlatformAdmin(ta.ID) {
		t.Error("租户 admin 不是平台运营者")
	}
	// 降级唯一平台 admin 应被拒
	if err := m.SetRole(pa.ID, RoleUser); err == nil {
		t.Error("最后一名平台 admin 不应可降级")
	}
	// 新增第二名平台 admin 后可降级
	pa2, _ := m.CreateUser("root2", "pw", RoleAdmin, nil, 0)
	if err := m.SetRole(pa.ID, RoleUser); err != nil {
		t.Errorf("存在第二名平台 admin 时应可降级: %v", err)
	}
	_ = pa2
}

// TestMoveUserTenant 跨租户迁移：目标满额/停用拒绝，成功落盘。
func TestMoveUserTenant(t *testing.T) {
	m := newTenantMgr(t)
	a, _ := m.CreateTenant("A", TenantQuota{MaxUsers: 5})
	b, _ := m.CreateTenant("B", TenantQuota{MaxUsers: 1})
	u, _ := m.CreateUserInTenant(a.ID, "mv", "pw", "", nil, 0)
	if err := m.MoveUser(u.ID, b.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	if m.TenantOf(u.ID) != b.ID {
		t.Errorf("迁移后租户应变更, got %q", m.TenantOf(u.ID))
	}
	if err := m.MoveUser(u.ID, "t_nope"); err == nil {
		t.Error("迁往不存在租户应报错")
	}
	// B 已被 u 占满，再迁一人进 B 触发配额
	u2, _ := m.CreateUserInTenant(a.ID, "mv2", "pw", "", nil, 0)
	if err := m.MoveUser(u2.ID, b.ID); !errors.Is(err, ErrTenantQuota) {
		t.Errorf("迁入满额租户应 ErrTenantQuota, got %v", err)
	}
}

// TestDefaultTenantUndeletable 系统租户不可停用。
func TestDefaultTenantUndeletable(t *testing.T) {
	m := newTenantMgr(t)
	off := false
	if err := m.UpdateTenant(DefaultTenantID, nil, &off, nil); err == nil {
		t.Error("t_default 不应可停用")
	}
}
