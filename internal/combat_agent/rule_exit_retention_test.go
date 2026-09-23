// 文件：rule_exit_retention_test.go
// 包名：combat_agent
// 所属模块：「对抗式/量化交易决策 agent（买卖信号、风控）」
// 模块职责：§EXIT-RETAIN（2026-09-23）规则级出场覆盖"跟随持仓、不跟随启用开关"的语义锁。
//
// 被测缺陷（真实代码，非假想）：旧 SetRuleExitOverrides 按 `!e.Enabled` 直接跳过，
// 停用一条战法会连带把它名下**存量持仓**的移动止盈/最长持有改回全局 8%/15 天。
// 而 rules.research.stale_adj_basis_action="disable" 这类无人值守路径（以及 lifecycle 衰退降级）
// 现在能自动把规则置为停用——于是"改持仓的离场风险参数"这件事可以在无人决策的情况下发生，
// 且方向未知（8%/15 天可能更紧也可能更松）。本文件钉住修正后的四条语义：
//   - (a) 停用 + 仍持有 → ID 与显示名双键都还命中覆盖；
//   - (b) 停用 + 无持仓 → 覆盖立即失效（旧行为保留）；
//   - (c) 删除 → 立即失效（即使仍持有；条目已不存在，参数无从保留）；
//   - (d) stale_adj_basis_action=disable 这条自动路径端到端不改写持仓的出场参数。
//     注意：该路径本身只在 LoadEnabledFactorRules 侧过滤买入、不改文件里的 enabled 位；
//     真正会把 enabled 写成 false 的是 lifecycle 降级（research.SetAppliedFactorEnabled），
//     故本用例把两者叠在一起跑，覆盖"自动停用已落库"这一最坏形态。
//
// English: exits follow positions, not the enable flag. A disabled rule that still owns open
// positions keeps its override (both id and display-name keys); with no positions it loses it; a
// deleted rule loses it immediately even while held; and the unattended stale-adjustment-basis
// disable path cannot rewrite the exit parameters of positions still held.
package combat_agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/research"
)

// heldOf 便捷构造：按策略键各计 n 笔持仓（键原文写入，注册表侧自行归一化）。
// English: builds a held-keys map with n positions per raw strategy key.
func heldOf(n int, keys ...string) HeldStrategyKeys {
	h := HeldStrategyKeys{}
	for _, k := range keys {
		for i := 0; i < n; i++ {
			h.Add(k)
		}
	}
	return h
}

// assertOverride 断言某规则的两个键命中同一份覆盖值（ok=false 时断言两键都无覆盖）。
// English: asserts both the id and the display-name key resolve to the same override (or to none).
func assertOverride(t *testing.T, wantOK bool, trail float64, hold int, id, name string) {
	t.Helper()
	for _, key := range []string{id, name} {
		gotTrail, gotHold, ok := RuleExitOverrideFor(key)
		if ok != wantOK {
			t.Fatalf("键 %q 命中态不符: ok=%v want=%v (trail=%v hold=%v)", key, ok, wantOK, gotTrail, gotHold)
		}
		if wantOK && (gotTrail != trail || gotHold != hold) {
			t.Fatalf("键 %q 覆盖值不符: got trail=%v hold=%v want trail=%v hold=%v", key, gotTrail, gotHold, trail, hold)
		}
	}
}

// TestExitOverridesRetainedForDisabledRuleWithPositions §EXIT-RETAIN 主用例 (a)(b)(c)。
func TestExitOverridesRetainedForDisabledRuleWithPositions(t *testing.T) {
	t.Cleanup(func() { SetRuleExitOverrides(nil, nil, nil) })

	disabled := []research.AppliedFactorEntry{{
		ID: "fac_21", Name: "因子战法#21", Enabled: false,
		ExitTrailPct: 6, ExitMaxHoldDays: 9,
	}}

	// (b) 停用 + 无开放持仓：覆盖立即失效（旧行为，回退全局 8%/15 天）
	SetRuleExitOverrides(disabled, nil, nil)
	assertOverride(t, false, 0, 0, "fac_21", "因子战法#21")

	// (a) 停用 + 仍有开放持仓（持仓 Strategy 存的是显示名，历史形态）：双键继续命中
	SetRuleExitOverrides(disabled, nil, heldOf(1, "因子战法#21"))
	assertOverride(t, true, 6, 9, "fac_21", "因子战法#21")

	// (a2) 持仓按规则 ID 记录（新形态）同样保留；显示名键也一并存活（双键同值语义不变）
	SetRuleExitOverrides(disabled, nil, heldOf(2, "fac_21"))
	assertOverride(t, true, 6, 9, "fac_21", "因子战法#21")

	// (a3) 形态战法同样适用；持仓数从 >0 变 0 后下一次重建即失效
	disabledPattern := []research.AppliedPatternEntry{{
		ID: "pat_31", Name: "形态战法#31", Enabled: false, ExitMaxHoldDays: 4,
	}}
	SetRuleExitOverrides(nil, disabledPattern, heldOf(1, "形态战法#31"))
	assertOverride(t, true, 0, 4, "pat_31", "形态战法#31")
	SetRuleExitOverrides(nil, disabledPattern, heldOf(0, "形态战法#31"))
	assertOverride(t, false, 0, 0, "pat_31", "形态战法#31")

	// (c) 删除：条目不在列表里 → 即使仍标记为持有也立即失效（删除是显式不可逆动作，参数已随条目消失）
	SetRuleExitOverrides(nil, nil, heldOf(3, "fac_21", "因子战法#21"))
	assertOverride(t, false, 0, 0, "fac_21", "因子战法#21")

	// 持仓键大小写/空白不敏感（与 ruleExitParamsFor 查询口径同源）
	SetRuleExitOverrides(disabled, nil, heldOf(1, "  因子战法#21  "))
	assertOverride(t, true, 6, 9, "FAC_21", "因子战法#21")

	// 无覆盖字段的停用条目即便有持仓也不入表（没有可保留的东西 → 全局默认）
	SetRuleExitOverrides([]research.AppliedFactorEntry{{ID: "fac_22", Name: "因子战法#22", Enabled: false}}, nil,
		heldOf(1, "fac_22"))
	assertOverride(t, false, 0, 0, "fac_22", "因子战法#22")
}

// TestExitOverridesUnaffectedByStaleAdjBasisDisable (d) §ADJ-BASIS-2 的
// stale_adj_basis_action=disable 自动路径 + lifecycle 落库停用，端到端（从库文件载入）
// 不得改写仍持有持仓的出场参数。
func TestExitOverridesUnaffectedByStaleAdjBasisDisable(t *testing.T) {
	t.Cleanup(func() { SetRuleExitOverrides(nil, nil, nil) })
	// 处置策略是 research 包级状态：跑完恢复缺省 shadow，别把别的用例带沟里。
	t.Cleanup(func() {
		research.ConfigureStaleAdjBasisActionFunc(nil)
		research.ConfigureStaleAdjBasisAction(research.StaleAdjBasisShadow)
	})

	dir := t.TempDir()
	// 一条旧口径（adj_basis 缺失 → 载入侧判 stale）且带扫参审批出场参数的已启用战法。
	entries := []map[string]any{{
		"id": "fac_41", "name": "因子战法#41", "enabled": true, "candidate_id": 41,
		"applied_at":    "2026-08-23 00:00:00",
		"factors":       []string{"mom_5"},
		"weights":       map[string]float64{"mom_5": 1},
		"directions":    map[string]int{"mom_5": 1},
		"buy_threshold": 60.0, "horizon": 5,
		"exit_trail_pct": 6.0, "exit_max_hold_days": 9,
	}}
	b, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := research.ConfigureStaleAdjBasisAction(research.StaleAdjBasisDisable); got != research.StaleAdjBasisDisable {
		t.Fatalf("处置策略注入失败: %q", got)
	}

	// 载入侧判定：必须是 stale（否则本用例什么都没测到）
	loaded, err := research.ListAppliedFactorRules(dir)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("库载入异常: n=%d err=%v", len(loaded), err)
	}
	if !loaded[0].StaleAdjBasis {
		t.Fatal("旧口径条目应被载入侧判为 stale，否则用例无效")
	}

	// 装配出场注册表（启动装配同款：ListAppliedFactorRules + SetRuleExitOverrides），持仓按显示名持有
	SetRuleExitOverrides(loaded, nil, heldOf(1, "因子战法#41"))
	assertOverride(t, true, 6, 9, "fac_41", "因子战法#41")

	// 自动路径把买入切断：fail-close 后该条目不进 enabled 集合（新买入信号停）
	enabled, err := research.LoadEnabledFactorRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Fatalf("stale_adj_basis_action=disable 下不应注入买入: %d 条", len(enabled))
	}

	// 无人值守的降级落库（lifecycle_run 用的同一函数）把 enabled 位真写成 false，
	// 再走一遍完整的热重载链（重新载入 + 重建注册表）
	if err := research.SetAppliedFactorEnabled(dir, "fac_41", false); err != nil {
		t.Fatal(err)
	}
	loaded2, err := research.ListAppliedFactorRules(dir)
	if err != nil || len(loaded2) != 1 || loaded2[0].Enabled {
		t.Fatalf("停用未落库: %+v err=%v", loaded2, err)
	}
	SetRuleExitOverrides(loaded2, nil, heldOf(1, "因子战法#41"))
	assertOverride(t, true, 6, 9, "fac_41", "因子战法#41")

	// 持仓平掉后（held 空）覆盖才失效——"停用"本身不抢这个决定权
	SetRuleExitOverrides(loaded2, nil, nil)
	assertOverride(t, false, 0, 0, "fac_41", "因子战法#41")
}

// TestHeldStrategyKeysAddAndCount 锁 Add/CountFor 的归一化与"一笔只计一次"口径：
// open_positions 与保留判定共用这张表，键口径错了就是页面数字错。
func TestHeldStrategyKeysAddAndCount(t *testing.T) {
	h := HeldStrategyKeys{}
	h.Add("  Fac_1 ")
	h.Add("fac_1")
	h.Add("")
	h.Add("因子战法#1")
	if n := h.CountFor("FAC_1"); n != 2 {
		t.Fatalf("按 ID 计数应为 2（大小写/空白归一 + 空串忽略）, got %d", n)
	}
	if n := h.CountFor("因子战法#1"); n != 1 {
		t.Fatalf("按显示名计数应为 1, got %d", n)
	}
	if n := h.CountFor("fac_404"); n != 0 {
		t.Fatalf("未持有应为 0, got %d", n)
	}
	// 手工字面量 map（未归一化键）也能查得到——注册表侧兜底扫一遍
	raw := HeldStrategyKeys{"  PAT_9 ": 3}
	if n := raw.CountFor("pat_9"); n != 3 {
		t.Fatalf("字面量 map 兜底查询失败: got %d", n)
	}
	var zero HeldStrategyKeys
	if n := zero.CountFor("fac_1"); n != 0 {
		t.Fatalf("nil map 查询应为 0, got %d", n)
	}
}
