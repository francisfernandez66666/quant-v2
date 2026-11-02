// quant_trade_perm_test.go — §MULTI-ACCT-B 批5（2026-11-02）：第六枚实盘权限位 quant_trade 的账本侧锁。
//
// 三件事必须成立，缺一就会出事：
//
//	① AllPerms() 收录 quant_trade——白名单里没有它，SetPerms 会把用户管理页的勾选 400 掉；
//	② SetPerms 接受它、并仍然拒绝未知位——白名单不能因为加了一位就顺手变成"什么都收"；
//	③ **没有任何现存账号自动获得它**——这枚位能撤单、能按紧急停止，是资损级动作面，
//	   只能由 owner 在建号/权限页显式授予；一旦哪天有人把它塞进 owner 五人授权或 schema 迁移，
//	   现网七个账号会在一夜之间全都长出自己的实盘动作面（本批的整条链路就是为此而锁）。
//
// English: the sixth bit must be whitelisted, must still reject unknown bits, and must never be
// auto-granted to any existing or newly created account (no migration, no owner-side fan-out).
package auth

import (
	"path/filepath"
	"testing"
)

// A1：AllPerms 恰有 7 位（research_approve + 五个运营组位 + 本批的 quant_trade），
// 逐项唯一、且必须包含 quant_trade。条数用等值而不是"≥"：多加一位同样要在这里显式承认。
func TestAllPermsIncludesQuantTrade(t *testing.T) {
	all := AllPerms()
	// 期望集合写死在这里，避免"循环自身当判据"的自证（AllPerms 少一项/多一项都会红）。
	want := map[string]bool{
		PermResearchApprove: true,
		PermFundsAdmin:      true,
		PermTradingAdmin:    true,
		PermUserAdmin:       true,
		PermConfigAdmin:     true,
		PermOpsAdmin:        true,
		PermQuantTrade:      true,
	}
	if len(all) != len(want) {
		t.Fatalf("AllPerms 位数应为 %d，实得 %d（%v）", len(want), len(all), all)
	}
	seen := map[string]bool{}
	for _, p := range all {
		if !want[p] {
			t.Fatalf("AllPerms 出现未登记的位 %q", p)
		}
		if seen[p] {
			t.Fatalf("AllPerms 出现重复位 %q", p)
		}
		seen[p] = true
	}
	if !seen[PermQuantTrade] {
		t.Fatal("AllPerms 必须包含 quant_trade（否则 SetPerms 会把勾选 400 掉）")
	}
	if PermQuantTrade != "quant_trade" {
		t.Fatalf("quant_trade 字面量漂移：实得 %q（前端契约按这个字符串对齐）", PermQuantTrade)
	}
}

// A2：SetPerms 白名单——quant_trade 存得下去、读得回来；未知位仍然整批拒绝。
func TestSetPermsAcceptsQuantTradeRejectsUnknown(t *testing.T) {
	m := newTestMgr(t)
	u, err := m.CreateUser("friend", "pw", RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := m.SetPerms(u.ID, []string{PermQuantTrade}); err != nil {
		t.Fatalf("quant_trade 应属白名单: %v", err)
	}
	if !m.HasExplicitPerm(u.ID, PermQuantTrade) {
		t.Fatal("设置后应显式持有 quant_trade")
	}
	// quant_trade 是显式位，不是角色：RoleUser 账号靠 HasPerm 也应当只认 perms（不隐式扩权）。
	if m.HasExplicitPerm(u.ID, PermTradingAdmin) {
		t.Fatal("持有 quant_trade 不得等价于持有 trading_admin（它是子集，不是别名）")
	}
	if err := m.SetPerms(u.ID, []string{"quant_trade_all_accounts"}); err == nil {
		t.Fatal("未知权限位必须被拒绝")
	}
	// 未知位被拒后原值必须还在（SetPerms 是"整体覆盖"，非法批次不能半个字节都不落）。
	if !m.HasExplicitPerm(u.ID, PermQuantTrade) {
		t.Fatal("非法批次不应改动已登记的权限位")
	}
}

// A3：建号路径不自动发位——首个 admin 的五人授权（grantOwnerPermsLocked）只补五个运营组位，
// 第二个 admin、普通成员、/setup 首个 admin 都拿不到 quant_trade。
func TestNoAutoGrantQuantTradeOnCreate(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	first, err := m.SetupInitialAdmin("admin", "pw")
	if err != nil {
		t.Fatalf("SetupInitialAdmin: %v", err)
	}
	second, err := m.CreateUser("second", "pw", RoleAdmin, []string{PermResearchApprove}, 0)
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}
	member, err := m.CreateUser("member", "pw", RoleUser, []string{PermFundsAdmin}, 0)
	if err != nil {
		t.Fatalf("CreateUser(member): %v", err)
	}
	for _, u := range []*User{first, second, member} {
		if u.HasExplicitPerm(PermQuantTrade) {
			t.Errorf("账号 %s（role=%s）不得自动获得 quant_trade，实得 perms=%v", u.ID, u.Role, u.Perms)
		}
	}
	if !first.HasExplicitPerm(PermTradingAdmin) {
		t.Error("前提检查：首个 admin 仍应有 trading_admin（本批没动五人授权）")
	}
}

// A4：迁移路径同样不发位——把库降回 version=2 再 Init，五个运营组位照旧补齐（旧行为不变），
// quant_trade 一个账号都不会被加上；且迁移备份照旧落盘。
func TestMigrationDoesNotGrantQuantTrade(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	owner, err := m.CreateUser("admin", "pw", RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser(owner): %v", err)
	}
	second, err := m.CreateUser("test01", "pw", RoleAdmin, []string{PermResearchApprove}, 0)
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}
	for _, p := range []string{PermFundsAdmin, PermTradingAdmin, PermUserAdmin, PermConfigAdmin, PermOpsAdmin} {
		if err := m.RevokePerm(owner.ID, p); err != nil {
			t.Fatalf("RevokePerm(%s): %v", p, err)
		}
	}
	m.mu.Lock()
	m.db.SchemaVersion = 2
	if err := m.save(); err != nil {
		t.Fatalf("save version=2: %v", err)
	}
	m.mu.Unlock()
	if err := m.Init(); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	// 旧迁移行为一字不变：首个 admin 补齐五个运营组位。
	for _, p := range []string{PermFundsAdmin, PermTradingAdmin, PermUserAdmin, PermConfigAdmin, PermOpsAdmin} {
		if !m.HasExplicitPerm(owner.ID, p) {
			t.Errorf("迁移后首个 admin 应有 %s（本批不许改旧迁移）", p)
		}
	}
	// 本批的新位一个都没发出去。
	for _, uid := range []string{owner.ID, second.ID} {
		if m.HasExplicitPerm(uid, PermQuantTrade) {
			t.Errorf("迁移不得给 %s 追加 quant_trade", uid)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "auth.json.pre-migrate-*"))
	if len(matches) == 0 {
		t.Fatal("迁移前备份 auth.json.pre-migrate-* 未落盘")
	}
}
