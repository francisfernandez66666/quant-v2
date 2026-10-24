// sizing_global_migration_test.go — §DYN-GLOBAL（owner 裁决一「dynamic 全局，legacy 开关账户级别」）
// 一次性迁移的行为锁。
//
// 判据一律打在**读侧**（GetSizingConfig/GetSizingConfigFor），不打在内存结构体上——迁移的交付定义是
// "引擎下一次读到的那一份进了下发态"，不是"某个字段被赋值了"（§RestartBeforeVerify 同族）。
//
// 七条锁：
//  1. L1 全局旧形状 → 迁移后读侧三键全进下发态、其余因子逐字保留、落盘可重开复现；
//  2. L2 幂等：第二次调用不得再迁移（policy_version 标记生效）；
//  3. L3 迁后显式改回 legacy 永不再被抬走（这是"标记只在启动写一次"的全部意义）；
//  4. L4 只迁"从未被碰过"的形状：显式 shadow=false / 显式 enabled=true / 已写 dynamic 一律挂起；
//  5. L5 账号层：专用键旧形状被迁、账号自有 rules.sizing 旧形状物化进专用键、无账号文档自动跟随；
//  6. L6 写失败如实进 Failures（旧值仍在用＝行为等于没迁，但必须出声）；
//  7. L7 半开态判据 SizingBudgetWithoutScale / SizingDelivery 真值表（模拟盘每日报警的依据）。
//
// English: behavior locks for the one-time §DYN-GLOBAL migration. Every assertion runs against the
// read side (what the engine will actually consume), not the in-memory struct: seven locks covering
// global rewrite, idempotency, post-ruling opt-outs, untouched-shape-only matching, the three account
// layers, honest failure reporting, and the half-open-state predicate.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSafeRules 造一份"裁决一之前"的全局规则：sizing 三键逐字等于当时的安全默认。
// floor 取一个非出厂值，用来证明迁移只动三键、不顺手重置别的因子。
func oldSafeRules() *Rules {
	return &Rules{Sizing: SizingConfig{
		Enabled:    boolPtr(false),
		BudgetMode: "legacy",
		Shadow:     boolPtr(true),
		Floor:      0.22,
		Cap:        1.40,
	}}
}

// diskManager 用临时 config.json 建管理器（迁移会 Save()，内存管理器没有落盘路径）。
func diskManager(t *testing.T, rules *Rules) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(map[string]any{"rules": rules})
	if err != nil {
		t.Fatalf("序列化测试配置: %v", err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("写测试配置: %v", err)
	}
	m := NewManager(path)
	return m, path
}

// TestDynGlobalMigratesGlobalLegacyShape L1：全局旧形状迁完，读侧必须真进下发态且因子未丢。
func TestDynGlobalMigratesGlobalLegacyShape(t *testing.T) {
	m, path := diskManager(t, oldSafeRules())
	rep := m.MigrateSizingToGlobalDefault(nil)
	if rep.Global != "migrated" {
		t.Fatalf("全局应判 migrated, got %q failures=%+v", rep.Global, rep.Failures)
	}
	got := m.GetSizingConfig()
	if !got.EnabledOn() || !got.DynamicBudgetOn() || got.ShadowOn() {
		t.Fatalf("迁移后读侧应 enabled+dynamic+非影子, got %+v", got.SizingDelivery())
	}
	if got.Floor != 0.22 || got.Cap != 1.40 {
		t.Fatalf("迁移不得顺手改其他因子（floor/cap 应原样）, got floor=%v cap=%v", got.Floor, got.Cap)
	}
	if got.PolicyVersion != sizingPolicyVersion {
		t.Fatalf("应打上迁移标记 policy_version=%d, got %d", sizingPolicyVersion, got.PolicyVersion)
	}
	// 落盘自证：重开一份管理器读同一个文件，读数必须一致（否则只是内存里好看）。
	reopened := NewManager(path)
	if r := reopened.GetSizingConfig(); !r.EnabledOn() || !r.DynamicBudgetOn() || r.ShadowOn() || r.PolicyVersion != 1 {
		t.Fatalf("重开配置后读侧未保持下发态, got %+v", r.SizingDelivery())
	}
}

// TestDynGlobalMigrationIdempotent L2+L3：第二次调用不许再迁；迁后主动改回 legacy 也不许被抬走。
func TestDynGlobalMigrationIdempotent(t *testing.T) {
	m, _ := diskManager(t, oldSafeRules())
	if rep := m.MigrateSizingToGlobalDefault(nil); rep.Global != "migrated" || rep.Migrated != 1 {
		t.Fatalf("首迁应 migrated/1, got %+v", rep)
	}
	second := m.MigrateSizingToGlobalDefault(nil)
	if second.Global != "held" {
		t.Fatalf("二次应判 held（带标记），got %q", second.Global)
	}
	if second.Migrated != 0 {
		t.Fatalf("二次不得重复迁移, got migrated=%d", second.Migrated)
	}
	if len(second.Holds) != 1 || !strings.Contains(second.Holds[0].Reason, "policy_version") {
		t.Fatalf("挂起原因应点名迁移标记, got %+v", second.Holds)
	}
	// 裁决之后 owner 自己把这个账号/全局改回 legacy：形状判据必须认得出"这是显式表态"。
	if _, err := m.MergeSizingConfig(map[string]json.RawMessage{
		"budget_mode": json.RawMessage(`"legacy"`),
		"enabled":     json.RawMessage(`false`),
	}); err != nil {
		t.Fatalf("改回 legacy: %v", err)
	}
	third := m.MigrateSizingToGlobalDefault(nil)
	if third.Global != "held" || third.Migrated != 0 {
		t.Fatalf("显式退回 legacy 后不应再被抬走, got global=%q migrated=%d", third.Global, third.Migrated)
	}
	if got := m.GetSizingConfig(); got.DynamicBudgetOn() || got.EnabledOn() {
		t.Fatalf("owner 显式 legacy 必须保持, got %+v", got.SizingDelivery())
	}
}

// TestDynGlobalOnlyMigratesUntouchedShape L4：三键任一被显式碰过都不许迁，只登记挂起。
func TestDynGlobalOnlyMigratesUntouchedShape(t *testing.T) {
	cases := []struct {
		name string
		sc   SizingConfig
	}{
		{"显式关影子（灰度态）", SizingConfig{Enabled: boolPtr(false), BudgetMode: "legacy", Shadow: boolPtr(false)}},
		{"已显式开总闸", SizingConfig{Enabled: boolPtr(true), BudgetMode: "legacy", Shadow: boolPtr(true)}},
		{"已显式写 dynamic", SizingConfig{Enabled: boolPtr(false), BudgetMode: "dynamic", Shadow: boolPtr(true)}},
		{"enabled 键缺失（本就跟随新默认）", SizingConfig{BudgetMode: "legacy", Shadow: boolPtr(true)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rules := &Rules{Sizing: c.sc}
			m, _ := diskManager(t, rules)
			rep := m.MigrateSizingToGlobalDefault(nil)
			wantDeliver := c.sc.EnabledOn() && !c.sc.ShadowOn()
			got := m.GetSizingConfig()
			if rep.Migrated != 0 {
				t.Fatalf("该形状不应迁移（%s），got migrated=%d holds=%+v", c.name, rep.Migrated, rep.Holds)
			}
			// 关键：读侧"是否真下发"必须和迁移前一致——迁移没有偷偷改语义。
			if (got.EnabledOn() && !got.ShadowOn()) != wantDeliver {
				t.Fatalf("迁移前后下发态被改变: 期望 delivers=%v, got %+v", wantDeliver, got.SizingDelivery())
			}
			// enabled 键缺失那一例是"没表态"，其余三例都该留一条挂起记录。
			wantHolds := 1
			if c.sc.Enabled == nil {
				wantHolds = 0
			}
			if len(rep.Holds) != wantHolds {
				t.Fatalf("挂起记录数应为 %d, got %+v", wantHolds, rep.Holds)
			}
		})
	}
}

// TestDynGlobalMigrationAccountLayers L5：账号三层（专用键 / 账号旧 rules.sizing / 无文档）各按语义走。
func TestDynGlobalMigrationAccountLayers(t *testing.T) {
	// 全局层要真的落盘，所以用带路径的管理器；账号层用内存 KV。
	m, _ := diskManager(t, oldSafeRules())
	kv := newMemKV()
	m.SetStore(kv)
	// A：专用键是旧形状。
	// B：没有专用键，但账号自己的 rules.sizing 是旧形状 → 迁移应物化专用键。
	// C：什么账号级文档都没有 → 自动跟随全局，不许为它凭空建键。
	aRaw, _ := json.Marshal(SizingConfig{Enabled: boolPtr(false), BudgetMode: "legacy", Shadow: boolPtr(true), Floor: 0.30})
	kv.data["A"] = map[string]string{perUserSizingKey: string(aRaw)}
	bRules, _ := json.Marshal(&Rules{Sizing: SizingConfig{Enabled: boolPtr(false), BudgetMode: "legacy", Shadow: boolPtr(true)}})
	kv.data["B"] = map[string]string{perUserKey: string(bRules)}

	rep := m.MigrateSizingToGlobalDefault([]string{"A", "B", "C"})
	if rep.Scanned != 3 {
		t.Fatalf("应扫描 3 个账号, got %d", rep.Scanned)
	}
	// 全局 1 + A 1 + B 1 = 3
	if rep.Migrated != 3 {
		t.Fatalf("应迁移 3 处（全局+A+B）, got %d failures=%+v", rep.Migrated, rep.Failures)
	}
	if len(rep.Followers) != 1 || rep.Followers[0] != "C" {
		t.Fatalf("C 应落在自动跟随, got %+v", rep.Followers)
	}
	for _, uid := range []string{"A", "B"} {
		got, dedicated := m.GetSizingConfigFor(uid)
		if !dedicated {
			t.Fatalf("%s 迁移后应有专用键（读侧优先层），got dedicated=false", uid)
		}
		if !got.EnabledOn() || !got.DynamicBudgetOn() || got.ShadowOn() {
			t.Fatalf("%s 迁移后读侧未进下发态, got %+v", uid, got.SizingDelivery())
		}
	}
	// A 自己的因子（floor=0.30）必须原样保留——迁移只动三键。
	if got, _ := m.GetSizingConfigFor("A"); got.Floor != 0.30 {
		t.Fatalf("A 的 floor 应保留 0.30, got %v", got.Floor)
	}
	// C 不许被建键（凭空写键会把"跟随全局"变成"冻结在当前值"）。
	if _, ok := m.AccountKVRaw("C", perUserSizingKey); ok {
		t.Fatal("C 没有账号级文档，迁移不应为它创建专用键")
	}
	if got, _ := m.GetSizingConfigFor("C"); !got.EnabledOn() || !got.DynamicBudgetOn() {
		t.Fatalf("C 应通过继承吃到全局新默认, got %+v", got.SizingDelivery())
	}
}

// failingKV 只在指定键上写失败，用来单独锤"迁移写失败必须出声"。
type failingKV struct {
	*memKV
	failDedicated bool
}

func (f *failingKV) SetConfig(userID, key, value string) error {
	if f.failDedicated && key == perUserSizingKey {
		return errors.New("disk full")
	}
	return f.memKV.SetConfig(userID, key, value)
}

// TestDynGlobalMigrationReportsFailures L6：写失败进 Failures，且旧值确实还在用（不误报成功）。
func TestDynGlobalMigrationReportsFailures(t *testing.T) {
	m, kv := newSizingTestManager(t, &Rules{Sizing: DefaultSizingConfig()})
	fk := &failingKV{memKV: kv, failDedicated: true}
	m.SetStore(fk)
	aRaw, _ := json.Marshal(SizingConfig{Enabled: boolPtr(false), BudgetMode: "legacy", Shadow: boolPtr(true)})
	fk.data["A"] = map[string]string{perUserSizingKey: string(aRaw)}

	rep := m.MigrateSizingToGlobalDefault([]string{"A"})
	if len(rep.Failures) != 1 || rep.Failures[0].UserID != "A" {
		t.Fatalf("专用键写失败应进 Failures, got %+v", rep.Failures)
	}
	if rep.Migrated != 0 {
		t.Fatalf("写失败不许计入 migrated, got %d", rep.Migrated)
	}
	if got, _ := m.GetSizingConfigFor("A"); got.EnabledOn() {
		t.Fatalf("写失败后应仍是旧值（enabled=false），不能假装迁好了, got %+v", got.SizingDelivery())
	}
}

// TestDynGlobalHalfOpenPredicate L7：半开态判据真值表——这是模拟盘每日报警与面板读数的唯一依据。
func TestDynGlobalHalfOpenPredicate(t *testing.T) {
	cases := []struct {
		name       string
		enabled    *bool
		budgetMode string
		shadow     *bool
		halfOpen   bool
	}{
		{"出厂默认（dynamic + 下发）", nil, "", nil, false},
		{"legacy + 关总闸＝旧口径，不算半开", boolPtr(false), "legacy", boolPtr(true), false},
		{"dynamic + 关总闸＝没有预算决策者", boolPtr(false), "dynamic", boolPtr(true), true},
		{"dynamic + 影子＝没有预算决策者", boolPtr(true), "dynamic", boolPtr(true), true},
		{"dynamic + 下发到位", boolPtr(true), "dynamic", boolPtr(false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := SizingConfig{Enabled: c.enabled, BudgetMode: c.budgetMode, Shadow: c.shadow}.WithDefaults()
			if got := sc.SizingBudgetWithoutScale(); got != c.halfOpen {
				t.Fatalf("半开态判据不符: got=%v want=%v %+v", got, c.halfOpen, sc.SizingDelivery())
			}
			if !c.halfOpen && sc.DynamicBudgetOn() && sc.SizingDelivery().Delivers != (sc.EnabledOn() && !sc.ShadowOn()) {
				t.Fatal("SizingDelivery.Delivers 与交付谓词不一致")
			}
		})
	}
}
