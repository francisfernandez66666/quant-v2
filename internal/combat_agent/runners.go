// runners.go 四大战法 runner 的统一工厂（C7）：消除 engine/backtest/latency/main_test 四处重复构造，
// 战法按配置驱动组装（各战法自身持有 config.Manager 引用，账号级参数经 SetUserID 绑定）。
//
// 工厂函数职责：
//   - 创建四大内置战法运行器（龙头/双响炮/N形/龙回头）
//   - 创建因子战法和形态战法运行器（默认空规则，由 Engine 注入有效规则后生效）
//   - 返回统一的运行器列表供 Agent 使用
//
// 战法配置绑定：
//   - 各战法持有 config.Manager 引用
//   - 账号级参数通过 SetUserID 绑定
//   - 支持配置热更新
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
//
// 参数：
//   - cfgMgr: 配置管理器引用，各战法通过它读取账号级参数
//   - matcher: D1 事件匹配器，供 N 形战法使用（可为 nil）
//
// 返回值：
//   - 策略运行器列表，包含六个战法运行器
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
