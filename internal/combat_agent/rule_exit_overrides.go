// rule_exit_overrides.go 规则级出场参数覆盖注册表（§P2-d 实盘接线）。
//
// 扫参审批把 exit_trail_pct / exit_max_hold_days 写入 applied_*.json 后，
// 因子/形态战法的持仓退出不再吃全局硬编码的 8%/15 天，而是按规则覆盖执行。
// 注册表以"规则 ID + 显示名"双键维护（持仓记录 pos.Strategy 存的是信号时的
// 规则显示名，如"因子战法#1"；ID 键兜底未来直存 ID 的场景）。
// 刷新时机：Agent.ReloadFactorRules / ReloadPatternRules（审批热重载）与
// registry 启动装配（buildWithLibrary）两处调用 SetRuleExitOverrides。
//
// §EXIT-RETAIN（2026-09-23）出场覆盖**跟随持仓，不跟随启用开关**：
// 一条规则只要还有开放持仓是在它名下开的，它的出场覆盖就继续对这些持仓生效，
// 即使该规则已被停用（人工点「停用」，或 research 侧 stale_adj_basis_action=disable /
// lifecycle 衰退降级这类**无人值守**的自动路径落库）。停用只切断新开仓——这正是
// 操作员对"停用"二字的理解。旧实现按 !Enabled 直接清表，等于停用一条规则就顺手
// 改了它名下存量持仓的止盈/超期口径（且方向未知：全局 8%/15 天可能更紧也可能更松），
// 属于越权的静默风险参数变更，故本次修正。
// 删除（delete）仍然立即撤销覆盖：删除是显式、不可逆动作，条目本身已不存在，
// 无参数可保留（见下方"删除语义"注释）。
//
// English: per-rule exit override registry for live trading. Sweep approvals persist
// exit_trail_pct / exit_max_hold_days into applied_*.json; factor/pattern positions then exit by
// their rule's params instead of the global 8%/15d defaults. Keyed by both rule ID and display
// name (positions record the display name); refreshed on hot-reload and at startup assembly.
// §EXIT-RETAIN: overrides follow the POSITION, not the enable flag — a disabled rule that still
// owns open positions keeps governing those positions' exits (disabling only cuts new buys, which
// is what the operator means by it, and matters now that unattended paths can disable a rule).
// Deleting a rule still drops its overrides immediately: the entry itself is gone.
package combat_agent

import (
	"log"
	"strings"
	"sync"

	"quant-trading-v2/internal/research"
)

// ruleExitOverride 单条规则的出场覆盖（0 值字段表示该项不覆盖）。
type ruleExitOverride struct {
	trailPct float64 // 移动止盈回撤阈值（%）
	holdDays int     // 最大持仓天数
}

var (
	exitOvMu    sync.RWMutex
	exitOvByKey = map[string]ruleExitOverride{} // 规则 ID 与显示名双键同值
)

// HeldStrategyKeys 当前开放持仓的策略键计数表：
//   - key = 持仓记录的 Strategy 原文（规则 ID 或显示名），经 Add 归一化（小写 + 去首尾空白）；
//   - value = 该策略键下的开放持仓笔数（>0 即"仍持有"）。
//
// 生产方（engine.Registry.OpenPositionStrategyCounts）用 Add 写入，因此 map 内键恒为归一化形态；
// 消费方（SetRuleExitOverrides / CountFor）查询侧同样归一化，两侧口径一致。
// 手工构造的字面量 map（测试）也允许，但请传原始策略串，注册表内部会再归一化一次。
//
// English: counts of currently open positions keyed by their strategy string (rule ID or display
// name, normalized); a positive count means the rule still owns positions, so its exit override is
// retained even while the rule is disabled.
type HeldStrategyKeys map[string]int

// normalizeExitKey 出场覆盖策略键的归一化：与 ruleExitParamsFor 的查询口径严格一致
// （大小写与首尾空白不敏感），两处必须同源，否则"入表键"和"查询键"会错配。
// English: the single normalization shared by registration and lookup.
func normalizeExitKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NormalizeStrategyKey 策略键归一化的跨包出口（规则 ID / 显示名 / 持仓 Strategy 三处同一口径）。
// server 侧战法库 open_positions 匹配用它，避免两处各写一遍 lower+trim 后慢慢漂开。
// English: exported strategy-key normalization so the library payload matches positions with the
// same caliber the exit-override registry uses.
func NormalizeStrategyKey(s string) string { return normalizeExitKey(s) }

// Add 记一笔开放持仓（key 为持仓 Strategy 原文，空串忽略）。
// English: records one open position under its raw strategy key (empty keys ignored).
func (h HeldStrategyKeys) Add(strategy string) {
	k := normalizeExitKey(strategy)
	if k == "" || h == nil {
		return
	}
	h[k]++
}

// CountFor 查询某策略键（规则 ID 或显示名）的开放持仓数；键未归一化也可查询。
// English: open-position count for a rule id / display name.
func (h HeldStrategyKeys) CountFor(strategy string) int {
	if len(h) == 0 {
		return 0
	}
	k := normalizeExitKey(strategy)
	if k == "" {
		return 0
	}
	if n, ok := h[k]; ok {
		return n
	}
	// 兜底：调用方直接构造的字面量 map 可能带未归一化键（手工 map/测试桩），
	// 线性扫一次——注册表条目数量级为战法条数，成本可忽略。
	for raw, n := range h {
		if normalizeExitKey(raw) == k {
			return n
		}
	}
	return 0
}

// SetRuleExitOverrides 用战法库条目重建规则级出场注册表（热重载与启动装配共用）。
// held 为当前开放持仓的策略键计数（nil/空 = 无持仓，语义退化为旧的"停用即失效"）：
//   - 启用 + 带正数覆盖字段 → 入表（原语义不变）；
//   - **停用** + 带正数覆盖字段 + held 命中该条 ID 或显示名 → 继续入表（出场覆盖跟随持仓，
//     停用只切断新开仓），并打一条 WARN 说明该规则因持仓而被保留；
//   - 停用且无开放持仓 → 不入表，覆盖立即失效；
//   - 其余键清除。
//
// 删除语义（保持旧行为）：删除的规则根本不在 factors/patterns 列表里，因此其覆盖会立即
// 撤销——删除是显式不可逆操作，条目与其参数已一同消失，无从保留。
//
// English: rebuilds the override registry from library entries. `held` counts open positions by
// strategy key: an enabled entry with positive override fields registers (unchanged); a DISABLED
// entry keeps its override while it still owns open positions (exits follow the position, disabling
// only cuts new buys — logged as a WARN); a disabled entry with no positions loses it immediately.
// Deleting a rule still drops its overrides at once, since the entry itself is gone.
func SetRuleExitOverrides(factors []research.AppliedFactorEntry, patterns []research.AppliedPatternEntry, held HeldStrategyKeys) {
	next := map[string]ruleExitOverride{}
	register := func(id, name string, enabled bool, trailPct float64, holdDays int) {
		if trailPct <= 0 && holdDays <= 0 {
			return // 无覆盖字段的条目不入表（与旧实现一致）
		}
		if !enabled {
			// ID 与显示名可能归一化后同值（如规则就叫 "fac_1"）：此时一笔持仓会被计两次，先判等再相加。
			heldN := held.CountFor(id)
			if normalizeExitKey(name) != normalizeExitKey(id) {
				heldN += held.CountFor(name)
			}
			if heldN <= 0 {
				return // 停用且已无持仓 → 覆盖立即失效（回退全局 8%/15 天默认）
			}
			// §EXIT-RETAIN：仅按持仓留痕，不打账号/资金信息
			log.Printf("[combat_agent] WARN 规则 %s(%s) 已停用但仍有 %d 笔开放持仓：其出场覆盖（移动止盈 %.2f%% / 最长持有 %d 天）按持仓继续生效，停用只切断新开仓；要立即撤销该覆盖需删除规则或平掉持仓",
				id, name, heldN, trailPct, holdDays) // 参数只含规则标识与持仓笔数，不含账号信息
		}
		// 保留与启用走同一条登记路径：ID 与显示名两个键都写，持仓的 Strategy 字符串两种都可能是
		ov := ruleExitOverride{trailPct: trailPct, holdDays: holdDays}
		next[normalizeExitKey(id)] = ov
		next[normalizeExitKey(name)] = ov
	}
	for i := range factors {
		e := &factors[i]
		register(e.ID, e.Name, e.Enabled, e.ExitTrailPct, e.ExitMaxHoldDays)
	}
	// 形态战法与因子战法同规则处理（出场覆盖字段一致）
	for i := range patterns {
		e := &patterns[i]
		register(e.ID, e.Name, e.Enabled, e.ExitTrailPct, e.ExitMaxHoldDays)
	}
	// 整表替换：读侧（持仓查覆盖）看到的永远是一份完整快照，不会读到半新半旧
	exitOvMu.Lock()
	defer exitOvMu.Unlock()
	exitOvByKey = next
}

// ruleExitParamsFor 按持仓 Strategy 字符串查出场覆盖：精确匹配规则 ID 或显示名；
// 未命中返回 nil（调用方回退全局默认）。匹配对大小写与首尾空白不敏感。
// English: looks up the exit override for a position's strategy string (rule ID or display name);
// nil means no override — caller falls back to global defaults.
func ruleExitParamsFor(strategy string) *ruleExitOverride {
	key := normalizeExitKey(strategy)
	if key == "" {
		return nil
	}
	exitOvMu.RLock()
	ov, ok := exitOvByKey[key]
	exitOvMu.RUnlock()
	if !ok {
		return nil
	}
	return &ov
}

// RuleExitOverrideFor 导出查询：按持仓 Strategy 字符串取规则级出场覆盖（供引擎层测试/诊断）。
// ok=false 表示无覆盖，调用方回退全局默认。
// English: exported lookup for rule-level exit overrides by strategy string.
func RuleExitOverrideFor(strategyName string) (trailPct float64, holdDays int, ok bool) {
	ov := ruleExitParamsFor(strategyName)
	if ov == nil {
		return 0, 0, false
	}
	return ov.trailPct, ov.holdDays, true
}
