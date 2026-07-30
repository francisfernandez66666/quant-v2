// Package risk 实现风控引擎，提供信号级风控检查（黑名单/合规）、
// 回撤检测、多信号冲突解决、M8组合兜底以及仓位限制校验。
package risk

import (
	"fmt"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
	"sort"
)

// Engine 风控引擎，依赖配置管理器进行各种风控检查。
type Engine struct {
	cfg *config.Manager // 配置管理器（热加载）
}

// New 创建风控引擎。
// 参数 cfg: 配置管理器（从 rules.json 读取风控参数）。
func New(cfg *config.Manager) *Engine {
	return &Engine{cfg: cfg}
}

// CheckResult 风控检查结果。
type CheckResult struct {
	Pass     bool              `json:"pass"`     // 是否通过
	Action   string            `json:"action"`   // 建议动作：pass/block/reduce/sell_all
	Priority strategy.Priority `json:"priority"` // 关联优先级（仅阻断时有意义）
	Reason   string            `json:"reason"`   // 阻断原因描述
	Blocked  bool              `json:"blocked"`  // 是否被彻底阻断（不进入后续流程）
}

// CheckSignal 对单个交易信号执行风控检查。
// 当前检查项：黑名单过滤 + 合规模式检查。
// 参数 sig: 策略产生的交易信号。
// 返回检查结果，若 Blocked 为 true 则信号不应被执行。
func (e *Engine) CheckSignal(sig *strategy.Signal) *CheckResult {
	cfg := e.cfg.Get()
	rc := cfg.RiskCtrl

	blacklisted := e.checkBlacklist(sig.Code, cfg)
	if blacklisted {
		return &CheckResult{Pass: false, Action: "block", Priority: strategy.P1, Reason: "黑名单股票", Blocked: true}
	}

	compliance := e.checkCompliance(rc.Compliance)
	if !compliance.Pass {
		return compliance
	}

	return &CheckResult{Pass: true, Action: "pass", Priority: sig.Priority, Reason: "风控通过", Blocked: false}
}

// CheckDrawdown 检查单笔持仓的回撤是否触发了指定的止损规则。
// 参数 entryPrice: 入场价；currentPrice: 当前价；cfg: 回撤规则配置。
// 若回撤幅度超过规则设定则返回不通过并附带建议动作。
func (e *Engine) CheckDrawdown(entryPrice, currentPrice float64, cfg config.DrawdownRule) *CheckResult {
	drawdown := (currentPrice - entryPrice) / entryPrice * 100

	if drawdown <= cfg.Pct {
		return &CheckResult{
			Pass:     false,
			Action:   cfg.Action,
			Priority: strategy.P3,
			Reason:   "买入回撤触发",
		}
	}
	return &CheckResult{Pass: true, Action: "hold"}
}

// ResolveConflict 在同一标的多信号冲突时按优先级+动作排序解决。
// 排序规则：优先级高（数值小）优先；同级时卖出>买入>持有。
// 参数 signals: 同一标的的多策略信号列表。
// 返回优先级最高的信号。
func (e *Engine) ResolveConflict(signals []strategy.Signal) *strategy.Signal {
	if len(signals) == 0 {
		return nil
	}

	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Priority != signals[j].Priority {
			return signals[i].Priority < signals[j].Priority
		}
		actionOrder := map[strategy.TradeAction]int{
			strategy.ActionSell: 0,
			strategy.ActionBuy:  1,
			strategy.ActionHold: 2,
		}
		return actionOrder[signals[i].Action] < actionOrder[signals[j].Action]
	})

	return &signals[0]
}

// checkBlacklist 检查股票代码是否在黑名单中。
// 黑名单中的股票直接被阻断。
func (e *Engine) checkBlacklist(code string, cfg *config.Rules) bool {
	for _, item := range cfg.Theme.BlackList {
		if code == item {
			return true
		}
	}
	return false
}

// checkCompliance 检查合规模式是否开启。
// 合规模式下直接放行（实际合规限制由外部系统执行）。
func (e *Engine) checkCompliance(cc config.ComplianceConfig) *CheckResult {
	if cc.ComplianceMode {
		return &CheckResult{Pass: true, Action: "pass", Priority: strategy.P4, Reason: "合规模式"}
	}
	return &CheckResult{Pass: true}
}

// M8Check 检查组合总市值从峰值回撤是否达到 M8 兜底阈值。
// 当 currentTotal 从 peakTotal 的回撤超过配置值时触发全仓卖出。
// 参数 currentTotal: 当前持仓总市值；peakTotal: 历史峰值市值。
// 返回值中的 Blocked 为 true 时表示需要执行清仓操作。
func (e *Engine) M8Check(currentTotal, peakTotal float64) *CheckResult {
	cfg := e.cfg.Get()
	rc := cfg.RiskCtrl
	if !rc.M8Enabled || peakTotal <= 0 {
		return &CheckResult{Pass: true}
	}
	drawdown := (currentTotal - peakTotal) / peakTotal * 100
	if drawdown <= rc.M8PortfolioDrawdownPct {
		return &CheckResult{
			Pass:     false,
			Action:   "sell_all",
			Priority: strategy.P1,
			Reason:   fmt.Sprintf("M8兜底触发: 组合回撤%.1f%%", drawdown),
			Blocked:  true,
		}
	}
	return &CheckResult{Pass: true}
}

// PositionLimitCheck 检查仓位是否超出限制。
// 特殊规则：
//   - N 形策略不受 30%/80% 限制，仅 90% 截断
//   - 其他策略检查单票 PerStockMax 和总仓位 MaxTotalPositionPct
//
// 参数 currentPct: 当前单票仓位百分比；singlePct: 建议仓位百分比；
// totalPct: 建议后总仓位百分比；strategyType: 策略类型。
func (e *Engine) PositionLimitCheck(currentPct, singlePct, totalPct float64, strategyType strategy.SignalType) *CheckResult {
	cfg := e.cfg.Get()
	rc := cfg.RiskCtrl

	if strategyType == strategy.SignalNShape {
		if singlePct > 90 {
			return &CheckResult{Pass: false, Action: "block", Priority: strategy.P1, Reason: "N形单票超90%截断"}
		}
		return &CheckResult{Pass: true}
	}

	if singlePct > rc.PerStockMax {
		return &CheckResult{Pass: false, Action: "block", Priority: strategy.P3, Reason: "单票仓位超限"}
	}
	if totalPct > cfg.Position.MaxTotalPositionPct {
		return &CheckResult{Pass: false, Action: "reduce", Priority: strategy.P3, Reason: "总仓位超限"}
	}

	return &CheckResult{Pass: true}
}
