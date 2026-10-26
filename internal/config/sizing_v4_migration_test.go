// sizing_v4_migration_test.go — §DYN-SIZE v4 一次性迁移（policy_version 1→2）的行为锁。
//
// 判据一律打在**读侧**（GetSizingConfig/GetSizingConfigFor），不打在内存结构体上——
// 迁移的交付定义是"引擎下一次读到的那一份是新口径"，不是"某个字段被赋值了"。
//
// 七条锁，围绕"什么该动、什么绝不能动、没签字就不动"：
//  1. L1 逐字等于旧物化默认（mode=product 且 cap=0.80）→ 两键一起抬，其余因子原样；
//  2. L2 dry-run（apply=false）一个字节都不写，但报告必须逐账号给出前后值——签字单看的就是这张表；
//  3. L3 幂等：落盘后再跑一次不得重复迁移（policy_version=2 标记生效）；
//  4. L4 **键缺失不许物化**：mode/cap 都没写过的载荷本就吃新默认，迁移若写一次就把今天的默认
//     钉成这个账号的显式配置，日后改默认再也传不下去（§1.7 的物化陷阱，本次必须有反证锁）；
//  5. L5 owner 手调过的值（cap=0.60 / mode=power_mean）一律不动——迁移只冲旧默认，不冲选择；
//  6. L6 只中一个键时只改那一个（cap 已被显式改过、mode 还是旧默认）；
//  7. L7 账号三层：专用键 / 账号旧 rules.sizing（物化进专用键）/ 无文档（不建键）。
//
// English: behavior locks for the v4 one-time migration, asserted against the read side. The two
// most load-bearing locks are L2 (dry-run writes nothing) and L4 (an absent key is never
// materialized — writing today's default into the account would pin it forever).
package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// v4OldRules 造一份"v4 之前"的全局规则：sizing 的两个旧物化默认逐字落盘。
// Floor 取一个非出厂值，用来证明迁移只动点名键、不顺手重置别的因子。
func v4OldRules() *Rules {
	return &Rules{Sizing: SizingConfig{
		Enabled:             boolPtr(true),
		BudgetMode:          "dynamic",
		Mode:                legacyMaterializedMode,
		MaxTotalPositionPct: legacyMaterializedCapPct,
		Floor:               0.22,
		Cap:                 1.40,
		PolicyVersion:       1, // 已经过 v1 归一，正是本次要抬到 2 的那批存量
	}}
}

// TestV4MigrationDryRunWritesNothing L2：未签字（apply=false）只出报告，落盘与读侧都必须还是旧值。
func TestV4MigrationDryRunWritesNothing(t *testing.T) {
	m, path := diskManager(t, v4OldRules())
	b0, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读初始 config.json: %v", err)
	}
	initRaw := string(b0)
	rep := m.MigrateSizingToV4(nil, false)
	if rep.Changed != 1 || rep.Written != 0 || rep.Skipped != 1 {
		t.Fatalf("dry-run 应「需迁 1、写 0、跳过 1」，got %s", rep.String())
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("dry-run 必须逐载荷给一行前后值，got %+v", rep.Rows)
	}
	row := rep.Rows[0]
	if row.ModeBefore != legacyMaterializedMode || row.ModeAfter != migratedMode ||
		row.CapBefore != legacyMaterializedCapPct || row.CapAfter != migratedTotalPositionPct {
		t.Fatalf("报告的前后值不符，got %+v", row)
	}
	if row.Written {
		t.Fatalf("dry-run 不得标记已写盘，got %+v", row)
	}
	// 读侧必须仍是旧口径：dry-run 之后行为等于没跑过迁移。
	if got := m.GetSizingConfig(); got.ModeOf() != "product" || got.MaxTotalPositionPct != legacyMaterializedCapPct {
		t.Fatalf("dry-run 后读侧被改动了（应逐字不变）：mode=%q cap=%v", got.ModeOf(), got.MaxTotalPositionPct)
	}
	// 磁盘上的字节也不许变（读侧没变还不够——内存改了但没落盘同样是半态，反之亦然）。
	// 拿"迁移前的整份字节"与"跑完之后的整份字节"比对，而不是比对某个键在不在文件里：
	// SizingConfig 的 max_total_position_pct 无 omitempty，序列化时本来就会出现，
	// 用"文件里有没有这个键"当反证会假红。
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 config.json: %v", err)
	}
	if string(after) != initRaw {
		t.Fatalf("dry-run 不应改动 config.json 的一个字节\n前：%s\n后：%s", initRaw, string(after))
	}
	// 未归一的落盘读数也必须仍带着旧值（读侧判据走 WithDefaults，看不出"有没有被写成新"）。
	disk, _, _ := m.sizingBaseRaw("")
	if disk.Mode != legacyMaterializedMode || disk.MaxTotalPositionPct != legacyMaterializedCapPct || disk.PolicyVersion != 1 {
		t.Fatalf("dry-run 后落盘原值被改动，got mode=%q cap=%v pv=%d", disk.Mode, disk.MaxTotalPositionPct, disk.PolicyVersion)
	}
}

// TestV4MigrationAppliesBothKeys L1：签字后两键一起抬、其余因子原样、重开复现、二次跑幂等。
//
// 幂等那半条是本次唯一防"每次重启都重刷一遍资金参数"的锁：标记写成 policy_version=2 之后，
// 第三、第四次跑都必须判 unchanged——否则 owner 把 mode 显式改回 product 也会被启动链再抬走
// （§DYN-GLOBAL v1 迁移用同一个纪律挡过这件事）。
func TestV4MigrationAppliesBothKeys(t *testing.T) {
	m, path := diskManager(t, v4OldRules())
	rep := m.MigrateSizingToV4(nil, true)
	if rep.Changed != 1 || rep.Written != 1 || rep.Failed != 0 {
		t.Fatalf("应写 1 处且无失败，got %s rows=%+v", rep.String(), rep.Rows)
	}
	got := m.GetSizingConfig()
	if got.ModeOf() != migratedMode {
		t.Fatalf("读侧 mode 应为 power_mean, got %q", got.ModeOf())
	}
	if got.MaxTotalPositionPct != migratedTotalPositionPct {
		t.Fatalf("读侧总仓位上限应为 1.00, got %v", got.MaxTotalPositionPct)
	}
	if got.Floor != 0.22 || got.Cap != 1.40 {
		t.Fatalf("迁移不得顺手改其他因子（floor/cap 应原样）, got floor=%v cap=%v", got.Floor, got.Cap)
	}
	if got.PolicyVersion != sizingPolicyVersionV4 {
		t.Fatalf("应盖 v4 迁移标记 policy_version=2, got %d", got.PolicyVersion)
	}
	// 落盘可重开复现：换新管理器读同一份 config.json，必须还是新口径。
	reopened := NewManager(path)
	if r2 := reopened.GetSizingConfig(); r2.ModeOf() != migratedMode || r2.MaxTotalPositionPct != migratedTotalPositionPct {
		t.Fatalf("重开进程后读侧回退了：mode=%q cap=%v", r2.ModeOf(), r2.MaxTotalPositionPct)
	}
	// 幂等：第二次跑不得再迁，也不得重写。
	second := m.MigrateSizingToV4(nil, true)
	if second.Changed != 0 || second.Written != 0 || second.Unchanged != 1 {
		t.Fatalf("第二次必须判 unchanged（幂等），got %s", second.String())
	}
}

// TestV4MigrationNeverMaterializesAbsentKeys L3：键缺失＝没配过，一个键都不许写进去。
//
// 这是本次最关键的反证锁：GET 回显走 WithDefaults/WithTEffective，把出厂默认填成了生效值，
// 迁移若照那份写盘，就等于把今天的默认钉成这个账号的显式配置——日后调默认对它永远不生效
// （`mode`/`max_total_position_pct` 被物化坑过，正是本次迁移的成因）。
func TestV4MigrationNeverMaterializesAbsentKeys(t *testing.T) {
	m, _ := diskManager(t, &Rules{Sizing: SizingConfig{
		Enabled: boolPtr(true),
		Floor:   0.30, // 有显式配置，但 mode/cap 两键都没写过
	}})
	rep := m.MigrateSizingToV4(nil, true)
	if rep.Changed != 0 || rep.Written != 0 {
		t.Fatalf("mode/cap 键缺失时不得迁移，got %s rows=%+v", rep.String(), rep.Rows)
	}
	// 读侧本来就已经按新默认跑（WithDefaults 补齐），"没写"与"按新口径"必须同时成立。
	got := m.GetSizingConfig()
	if got.ModeOf() != migratedMode || got.MaxTotalPositionPct != migratedTotalPositionPct {
		t.Fatalf("键缺失时读侧应已按新默认，got mode=%q cap=%v", got.ModeOf(), got.MaxTotalPositionPct)
	}
	// 未归一的落盘读数才判得出"没配过"：这里必须仍是空 mode、零新标记。
	raw, _, _ := m.sizingBaseRaw("")
	if strings.TrimSpace(raw.Mode) != "" {
		t.Fatalf("迁移不得把没配过的 mode 物化成显式配置, got %q", raw.Mode)
	}
	if raw.PolicyVersion != 0 {
		t.Fatalf("一处都没写就不该盖 policy_version, got %d", raw.PolicyVersion)
	}
}

// TestV4MigrationLeavesOwnerChoicesAlone L4：**逐键**判据——只有"逐字等于旧物化默认"的那一键才迁。
//
// 三条子例都是容易被两种偷懒实现做错的形状：
//
//	· mode=product + cap=0.60：mode 是旧物化默认（要迁），cap 是 owner 手调（不迁）；
//	· mode=power_mean + cap=0.80：mode 已显式选过（不迁），cap 是旧物化默认（要迁）；
//	· mode=power_mean + cap=0.70：两键都是显式选择（一处都不迁）。
//
// 写成"任一键不是旧默认就整块跳过"，第一条会把 product 永远留在盘上；
// 写成"一迁就把两键都抬成出厂值"，第二条会顺手把 owner 的 0.60 天花板抬成 1.00。
// 两个方向都是资金参数的静默改写，所以逐键钉死，并且判据打在**落盘原值**上
// （sizingBaseRaw 不走 WithDefaults——归一化会把 0 补成默认，用它断言"没被改动"会假绿）。
func TestV4MigrationLeavesOwnerChoicesAlone(t *testing.T) {
	cases := []struct {
		name        string
		sc          SizingConfig
		wantChanged int
		wantWritten int
		wantMode    string
		wantCap     float64
	}{
		{"mode 是旧默认、cap 是手调 → 只迁 mode",
			SizingConfig{Mode: legacyMaterializedMode, MaxTotalPositionPct: 0.60}, 1, 1, migratedMode, 0.60},
		{"cap 是旧默认、mode 已选过 → 只迁 cap",
			SizingConfig{Mode: "power_mean", MaxTotalPositionPct: legacyMaterializedCapPct}, 1, 1, "power_mean", migratedTotalPositionPct},
		{"两键都是显式选择 → 一处都不迁",
			SizingConfig{Mode: "power_mean", MaxTotalPositionPct: 0.70}, 0, 0, "power_mean", 0.70},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := diskManager(t, &Rules{Sizing: c.sc})
			rep := m.MigrateSizingToV4(nil, true)
			if rep.Changed != c.wantChanged || rep.Written != c.wantWritten {
				t.Fatalf("需迁/已写应为 %d/%d, got %s rows=%+v", c.wantChanged, c.wantWritten, rep.String(), rep.Rows)
			}
			got, _, _ := m.sizingBaseRaw("")
			if got.Mode != c.wantMode {
				t.Fatalf("落盘 mode 应为 %q, got %q", c.wantMode, got.Mode)
			}
			if got.MaxTotalPositionPct != c.wantCap {
				t.Fatalf("落盘总仓位上限应为 %v, got %v", c.wantCap, got.MaxTotalPositionPct)
			}
			// 只有真写过才盖标记：没写的载荷不许被标成"已迁"，否则下次改默认再也传不下去且无人知晓。
			if c.wantWritten > 0 && got.PolicyVersion != sizingPolicyVersionV4 {
				t.Fatalf("已写盘应带 policy_version=%d, got %d", sizingPolicyVersionV4, got.PolicyVersion)
			}
			if c.wantWritten == 0 && got.PolicyVersion != 0 {
				t.Fatalf("一处都没写就不该盖标记, got %d", got.PolicyVersion)
			}
		})
	}
}

// TestV4MigrationOnlyOneKeyMatches L6：只中一个旧默认时只改那一个。
func TestV4MigrationOnlyOneKeyMatches(t *testing.T) {
	m, _ := diskManager(t, &Rules{Sizing: SizingConfig{
		Mode:                legacyMaterializedMode,
		MaxTotalPositionPct: 0.65, // owner 手调过：不许被抬成 1.00
	}})
	rep := m.MigrateSizingToV4(nil, true)
	if rep.Changed != 1 || rep.Written != 1 {
		t.Fatalf("应只迁一处，got %s", rep.String())
	}
	got := m.GetSizingConfig()
	if got.ModeOf() != migratedMode {
		t.Fatalf("mode 应被迁成 power_mean, got %q", got.ModeOf())
	}
	if got.MaxTotalPositionPct != 0.65 {
		t.Fatalf("手调过的 0.65 必须原样保留, got %v", got.MaxTotalPositionPct)
	}
}

// TestV4MigrationAccountLayers L7：账号三层各按语义走，无文档的账号不许凭空建键。
func TestV4MigrationAccountLayers(t *testing.T) {
	m, _ := diskManager(t, v4OldRules())
	kv := newMemKV()
	m.SetStore(kv)
	// A：专用键是旧物化默认 → 迁它。
	// B：没有专用键，但账号自己的 rules.sizing 是旧物化默认 → 物化进专用键后迁。
	// C：什么账号级文档都没有 → 跟随全局，不建键。
	aRaw, _ := json.Marshal(SizingConfig{Mode: legacyMaterializedMode, MaxTotalPositionPct: legacyMaterializedCapPct, Floor: 0.30})
	kv.data["A"] = map[string]string{perUserSizingKey: string(aRaw)}
	bRules, _ := json.Marshal(&Rules{Sizing: SizingConfig{Mode: legacyMaterializedMode, MaxTotalPositionPct: legacyMaterializedCapPct}})
	kv.data["B"] = map[string]string{perUserKey: string(bRules)}

	rep := m.MigrateSizingToV4([]string{"A", "B", "C"}, true)
	// 全局 1 + A 1 + B 1 = 3 处写入；C 只被扫描到。
	if rep.Written != 3 {
		t.Fatalf("应写 3 处（全局+A+B），got %s rows=%+v", rep.String(), rep.Rows)
	}
	if rep.Scanned != 4 {
		t.Fatalf("应扫描 4 个载荷（全局+A+B+C），got %d", rep.Scanned)
	}
	for _, uid := range []string{"A", "B"} {
		got, dedicated := m.GetSizingConfigFor(uid)
		if !dedicated {
			t.Fatalf("%s 迁移后应有专用键（读侧优先层）", uid)
		}
		if got.ModeOf() != migratedMode || got.MaxTotalPositionPct != migratedTotalPositionPct {
			t.Fatalf("%s 迁移后读侧未进新口径, got mode=%q cap=%v", uid, got.ModeOf(), got.MaxTotalPositionPct)
		}
	}
	// A 自己的 floor 必须原样保留——迁移只动点名键。
	if got, _ := m.GetSizingConfigFor("A"); got.Floor != 0.30 {
		t.Fatalf("A 的 floor 应保留 0.30, got %v", got.Floor)
	}
	// C 不许被建键（凭空写键会把"跟随全局"变成"冻结在当前值"）。
	if _, ok := m.AccountKVRaw("C", perUserSizingKey); ok {
		t.Fatalf("无账号文档的 C 不得被建专用键")
	}
}

// TestV4MigrationNeedsV4Ceiling 目标上限位移必须进报告（签字单要看的就是"天花板抬了多少"）。
func TestV4MigrationNeedsV4Ceiling(t *testing.T) {
	m, _ := diskManager(t, v4OldRules())
	rep := m.MigrateSizingToV4(nil, false)
	row := rep.Rows[0]
	if row.TargetBefore != 0.80 || row.TargetAfter != 1.00 {
		t.Fatalf("报告必须给出目标仓位天花板的位移 0.80→1.00, got %+v", row)
	}
}
