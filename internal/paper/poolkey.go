package paper

// poolkey.go 战法 → 资金池 key 映射（§OPTIMIZE_POOL_INTEGRATION_PLAN A1）。
//
// 寻优排名行的 (strategy 显示名, strategy_kind) 与模拟盘战法池是两套标识：
// 排名行 strategy="双响炮" kind=""（内置）/ strategy="因子战法#1" kind="fac_1"（库规则），
// 而池 key 是 dragon/double_bump/n_shape/dragon_return/factor/pattern。
// 审批下发池纪律、寻优页回显池实测都依赖这层映射；纯字符串逻辑独立成文件便于单测。

// PoolKeyForStrategy 把寻优排名行的 (strategy, strategy_kind) 映射为模拟盘池 key。
// 优先按 kind 前缀判定（库规则权威）；kind 为空时按内置战法显示名映射；
// 无法识别返回 ""（其他/手动池——调用方不应向该池下发纪律）。
// English: maps an optimization row's (strategy name, strategy_kind) to a paper pool key;
// kind prefix wins (library rules are authoritative), falls back to builtin display names;
// returns "" (other/manual pool) when unrecognizable — callers must not push discipline to it.
func PoolKeyForStrategy(strategy, kind string) string {
	switch {
	case len(kind) >= 4 && kind[:4] == "fac_":
		return "factor"
	case len(kind) >= 4 && kind[:4] == "pat_":
		return "pattern"
	}
	switch strategy {
	case "双响炮":
		return "double_bump"
	case "龙头":
		return "dragon"
	case "N形":
		return "n_shape"
	case "龙回头":
		return "dragon_return"
	}
	return ""
}

// ApplyPoolMinScore 把寻优门槛合并进指定池的买入纪律（只改 MinScore 字段，其余保留；
// 池无规则则新建）。minScore<=0 视为清除该字段（置 0 = 不过滤）。持久化。
// English: merges an optimization threshold into a pool's buy discipline (MinScore only,
// other fields preserved; creates the rule when absent). minScore<=0 clears the field
// (0 = no filtering). Persisted.
func (e *Engine) ApplyPoolMinScore(poolKey string, minScore float64) {
	if poolKey == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rule := e.poolBuyRules[poolKey]
	if rule == nil {
		if minScore <= 0 {
			return // 无规则且要清零：无需创建空规则
		}
		rule = &PoolBuyRule{}
	}
	rule.MinScore = minScore
	if rule.MinScore <= 0 && rule.MaxDailyBuys <= 0 && rule.CooldownMinutes <= 0 && rule.BudgetPctPerDay <= 0 {
		delete(e.poolBuyRules, poolKey) // 全零=等价无规则，回收避免脏数据
	} else {
		e.poolBuyRules[poolKey] = rule
	}
	e.persist()
}
