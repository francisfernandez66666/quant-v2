// runners.go 四大战法 runner 的统一工厂（C7）：消除 engine/backtest/latency/main_test 四处重复构造，
// 战法按配置驱动组装（各战法自身持有 config.Manager 引用，账号级参数经 SetUserID 绑定）。
// English: unified factory for the four strategy runners (C7) — removes the four duplicated construction
// sites across engine/backtest/latency/main_test; each strategy keeps a config.Manager reference and
// per-account params bind via SetUserID.
package combat_agent

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	"quant-trading-v2/internal/strategies/n_shape"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy"
)

// NewRunners 构建四大战法 runner（龙头/双响炮/N形/龙回头）+ 因子战法（E6）+ 形态战法（F3）的统一工厂。
// matcher 供 N 形战法 D1 事件匹配使用（可为 nil）。调用方可对每个 runner 设置账号 ID（SetUserID）。
// 因子/形态战法 runner 默认以空规则创建（禁用），由 Engine 从 applied_*.json 注入有效规则后生效。
// English: builds the four strategy runners (Dragon / Double-Bump / N-shape / Dragon-Return) plus the
// factor-strategy (E6) and pattern-strategy (F3) runners through a single factory. matcher feeds the
// N-shape D1 event match (may be nil). Callers may SetUserID per runner. The factor/pattern runners are
// created with empty rules (disabled) and enabled by the Engine injecting valid rules from applied_*.json.
func NewRunners(cfgMgr *config.Manager, matcher *data.EventMatcher) []StrategyRunner {
	return []StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, matcher)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
		{Type: strategy.SignalFactor, Strategy: factorstrat.New()},
		{Type: strategy.SignalPattern, Strategy: patternstrat.New()},
	}
}
