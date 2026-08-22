// rule_exit_overrides.go 规则级出场参数覆盖注册表（§P2-d 实盘接线）。
//
// 扫参审批把 exit_trail_pct / exit_max_hold_days 写入 applied_*.json 后，
// 因子/形态战法的持仓退出不再吃全局硬编码的 8%/15 天，而是按规则覆盖执行。
// 注册表以"规则 ID + 显示名"双键维护（持仓记录 pos.Strategy 存的是信号时的
// 规则显示名，如"因子战法#1"；ID 键兜底未来直存 ID 的场景）。
// 刷新时机：Agent.ReloadFactorRules / ReloadPatternRules（审批热重载）与
// registry 启动装配（buildWithLibrary）两处调用 SetRuleExitOverrides。
//
// English: per-rule exit override registry for live trading. Sweep approvals persist
// exit_trail_pct / exit_max_hold_days into applied_*.json; factor/pattern positions then exit by
// their rule's params instead of the global 8%/15d defaults. Keyed by both rule ID and display
// name (positions record the display name); refreshed on hot-reload and at startup assembly.
package combat_agent

import (
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

// SetRuleExitOverrides 用战法库条目重建规则级出场注册表（热重载与启动装配共用）。
// 仅启用且带正数覆盖字段的条目入表；其余键清除，保证停用/删除后立即失效。
// English: rebuilds the override registry from library entries; only enabled entries with
// positive override fields register, so disabling/removing a rule takes effect immediately.
func SetRuleExitOverrides(factors []research.AppliedFactorEntry, patterns []research.AppliedPatternEntry) {
	next := map[string]ruleExitOverride{}
	for i := range factors {
		e := &factors[i]
		if !e.Enabled || (e.ExitTrailPct <= 0 && e.ExitMaxHoldDays <= 0) {
			continue
		}
		ov := ruleExitOverride{trailPct: e.ExitTrailPct, holdDays: e.ExitMaxHoldDays}
		next[e.ID] = ov
		next[e.Name] = ov
	}
	for i := range patterns {
		e := &patterns[i]
		if !e.Enabled || (e.ExitTrailPct <= 0 && e.ExitMaxHoldDays <= 0) {
			continue
		}
		ov := ruleExitOverride{trailPct: e.ExitTrailPct, holdDays: e.ExitMaxHoldDays}
		next[e.ID] = ov
		next[e.Name] = ov
	}
	exitOvMu.Lock()
	defer exitOvMu.Unlock()
	exitOvByKey = next
}

// ruleExitParamsFor 按持仓 Strategy 字符串查出场覆盖：精确匹配规则 ID 或显示名；
// 未命中返回 nil（调用方回退全局默认）。匹配对大小写与首尾空白不敏感。
// English: looks up the exit override for a position's strategy string (rule ID or display name);
// nil means no override — caller falls back to global defaults.
func ruleExitParamsFor(strategy string) *ruleExitOverride {
	key := strings.ToLower(strings.TrimSpace(strategy))
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
