// Package paper 独立模拟盘（纸面交易）引擎：把策略信号按实时价撮合成虚拟持仓，产出净值曲线并记录滑点/延迟，与真实持仓完全隔离。
package paper

import (
	"log"
	"math"
)

// poolkey.go 战法 → 资金池 key 映射（§OPTIMIZE_POOL_INTEGRATION_PLAN A1/C）。
//
// 寻优排名行的 (strategy 显示名, strategy_kind) 与模拟盘战法池是两套标识：
// 排名行 strategy="双响炮" kind=""（内置）/ strategy="因子战法#1" kind="fac_1"（库规则），
// 而池 key 是 dragon/double_bump/n_shape/dragon_return/factor/pattern。
// 审批下发池纪律、寻优页回显池实测都依赖这层映射；纯字符串逻辑独立成文件便于单测。

// PoolKeyForStrategy 把寻优排名行的 (strategy, strategy_kind) 映射为模拟盘池 key。
// §C 规则细分池：库规则的 kind（fac_1/pat_2）**本身就是池 key**——每条规则独立寻优、
// 独立资金池、独立纪律；内置战法按显示名映射到类型池；无法识别返回 ""（其他/手动池，
// 调用方不应向该池下发纪律）。旧 factor/pattern 聚合池仅承载存量持仓，不再新建。
// §名称规整：接受中英别名（N形超短/双突破/dragon 等），与 combat_agent.NormalizeStrategyName
// 同一口径；新增 momentum 池（§动量入模拟盘）。
// English: maps an optimization row's (strategy name, strategy_kind) to a paper pool key;
// since Phase C the library rule ID IS the pool key (per-rule pools); builtins map by display
// name (aliases accepted); returns "" (other/manual pool) when unrecognizable.
func PoolKeyForStrategy(strategy, kind string) string {
	if len(kind) >= 4 && (kind[:4] == "fac_" || kind[:4] == "pat_") {
		return kind // 规则粒度池：fac_1/fac_2/pat_3 各自独立
	}
	switch strategy {
	case "双响炮", "双突破", "双凸", "double_bump":
		return "double_bump"
	case "龙头", "dragon":
		return "dragon"
	case "N形", "N形超短", "N字型", "n_shape":
		return "n_shape"
	case "龙回头", "dragon_return":
		return "dragon_return"
	case "动量", "momentum":
		return "momentum"
	}
	return ""
}

// IsRulePoolKey 判断池 key 是否为规则细分池（fac_/pat_ 前缀）。
func IsRulePoolKey(key string) bool {
	return len(key) >= 4 && (key[:4] == "fac_" || key[:4] == "pat_")
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

// ── §C 规则细分池：开池 + 动态标签 ──

// ensurePoolMinFrac 新规则池从总现金划拨的比例（等比缩其余池，守恒不破）。
const ensurePoolMinFrac = 0.05

// ensurePoolFloor 新池保底最低金额（元）：总现金太小也至少给这个数，避免 0 元池永远买不起 1 手。
const ensurePoolFloor = 1000.0

// EnsurePool 为新审批的库规则开立独立资金池（幂等；已存在直接返回 false）。
// 资金来源=从所有现有池**等比**划拨（Σ池现金=总现金守恒），新池拿
// max(总现金×5%, ¥1000) 且不超过总现金×25%（防止首个规则池吸走过多）。
// 同时把 key 追加进 poolTypes（展示/重建可见），并持久化。
// English: creates a dedicated cash pool for a newly approved library rule (idempotent).
// Funds are carved proportionally from existing pools (conservation preserved); the new pool
// gets max(5% of total, ¥1000) capped at 25%. The key joins poolTypes and state persists.
func (e *Engine) EnsurePool(key string) bool {
	if key == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.pools[key]; ok {
		return false // 已存在（含存量聚合池 factor/pattern）
	}
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	if total <= 0 {
		return false
	}
	give := math.Max(ensurePoolFloor, total*ensurePoolMinFrac)
	if cap := total * 0.25; give > cap {
		give = cap
	}
	scale := (total - give) / total
	for k, v := range e.pools {
		e.pools[k] = v * scale
	}
	e.pools[key] = give
	if !containsStr(e.poolTypes, key) {
		e.poolTypes = append(e.poolTypes, key)
	}
	if e.extraPoolKeys == nil {
		e.extraPoolKeys = map[string]bool{}
	}
	e.extraPoolKeys[key] = true
	log.Printf("[paper] 规则细分池已开立：%s 划拨 %.0f 元（总现金 %.0f 等比缩放）", key, give, total)
	e.persist()
	return true
}

// containsStr 判断字符串切片中是否包含指定字符串（池 key 查重用）。
// English: reports whether a string slice contains the given string (pool-key dedup).
func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SetPoolLabelResolver 注入规则 ID → 显示名 解析器（server 用战法库名字表装配；
// 未注入或查不到时回退 key 本身）。供 fac_1/pat_2 等动态池展示。
func (e *Engine) SetPoolLabelResolver(fn func(string) string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.labelFn = fn
}

// poolLabelOf 池 key → 展示名：基础类型走静态表；规则池走解析器；兜底 key 本身。
// （strategyPoolLabel 对未识别 key 返回 "其他"，据此区分是否命中基础类型。）
func (e *Engine) poolLabelOf(key string) string {
	if l := strategyPoolLabel(key); l != "其他" || key == "" {
		return l
	}
	if e.labelFn != nil {
		if l := e.labelFn(key); l != "" {
			return l
		}
	}
	return key
}

// ── §Phase3 paper A/B 对照组 ──

// SetPoolABGroup 给资金池打 A/B 组标签（如 A=回测最优实盘验证、B=灰度新战法观察），
// 持久化；空 label 清除该池标记。
// English: tags a cash pool with an A/B group label (e.g. A=live validation of the backtest champion,
// B=grayscale candidate under observation); an empty label clears the tag. Persisted.
func (e *Engine) SetPoolABGroup(poolKey, label string) {
	if poolKey == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if label == "" {
		delete(e.poolGrp, poolKey)
	} else {
		if e.poolGrp == nil {
			e.poolGrp = map[string]string{}
		}
		e.poolGrp[poolKey] = label
	}
	e.persist()
}

// PoolABGroup 返回指定池的 A/B 组标签（无标记返回空串）。
// English: returns a pool's A/B group label (empty when unset).
func (e *Engine) PoolABGroup(poolKey string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.poolGrp[poolKey]
}

// PoolABGroups 返回全部 A/B 组标记（poolKey → label）。
// English: returns all A/B group tags (poolKey → label).
func (e *Engine) PoolABGroups() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.poolGrp))
	for k, v := range e.poolGrp {
		out[k] = v
	}
	return out
}

// ── §Phase4 IR 动态仓位 ──

// SetPoolIR 设置某池参考 IR（信息比率），自动买入金额按其缩放；IR==0 清除（恢复默认单笔预算）。
// 负 IR 允许保留（缩到 0.6 下限），与"清零（恢复默认）"语义区分。
// English: sets a pool's reference IR — the auto-buy amount scales by it; IR==0 clears the override
// (back to the default per-trade budget). Negative IR is allowed (shrinks to the 0.6 floor),
// distinct from clearing. Persisted.
func (e *Engine) SetPoolIR(poolKey string, ir float64) {
	if poolKey == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ir == 0 {
		delete(e.poolIR, poolKey)
	} else {
		if e.poolIR == nil {
			e.poolIR = map[string]float64{}
		}
		e.poolIR[poolKey] = ir
	}
	e.persist()
}

// PoolIR 返回某池参考 IR（无配置返回 0）。
// English: returns a pool's reference IR (0 when unset).
func (e *Engine) PoolIR(poolKey string) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.poolIR[poolKey]
}

// applyPoolIRLocked 计算某池自动买入金额的 IR 缩放系数（须持锁）。无 IR（key 缺失）→ 1.0；
// 负 IR → 缩到 0.6 下限（低质量信号少配）。
// 映射：预算倍数 = clamp(0.6 + IR, 0.6, 2.0) —— 高 IR 战法加大单笔预算，低 IR/负 IR 缩仓
// 至少保留 60% 基线（不超配低质信号，也不过度剥夺新战法试错资金）。
// English: returns this pool's auto-buy amount IR scale (caller holds the lock). Missing key → 1.0;
// negative IR → the 0.6 floor. Mapping: budget multiplier = clamp(0.6 + IR, 0.6, 2.0).
func (e *Engine) applyPoolIRLocked(poolKey string) float64 {
	ir, ok := e.poolIR[poolKey]
	if !ok || ir == 0 {
		return 1.0
	}
	if v := 0.6 + ir; v < 0.6 {
		return 0.6
	} else if v > 2.0 {
		return 2.0
	} else {
		return v
	}
}

// PoolIRScales 返回各池当前生效的 IR 缩放系数（poolKey → scale，1.0=未配置）。
// English: returns the currently effective IR scale per pool (poolKey → scale; 1.0 = unset).
func (e *Engine) PoolIRScales() map[string]float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]float64, len(e.poolIR))
	for k := range e.poolIR {
		out[k] = e.applyPoolIRLocked(k)
	}
	return out
}
