// Package risk 实现下单前风控闸门（gate.go 的 Gate 为生产唯一入口）与组合级 M8 兜底
// 共享判定（M8CheckWith）。
// §F-7（20260917 缺陷修复批）：删除旧 Engine（CheckSignal/CheckDrawdown/ResolveConflict/
// M8Check/PositionLimitCheck 等）——生产零调用（信号准入已由 signalctl 单点化、下单风控由
// Gate.CheckLiveOrder 承担，M8 实盘执行走 scoring_loop 调用本文件 M8CheckWith）；
// 此前"三份 M8 判定并存"（旧 Engine / Gate.CheckPortfolio / scoring_loop 内联）收敛为一份。
// English: the legacy Engine is removed (§F-7): zero production callers after signalctl admission
// and the live Gate; M8 fallback now has exactly one judge (M8CheckWith) shared by the live loop,
// Gate.CheckPortfolio and tests.
package risk

import (
	"fmt"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckResult 风控检查结果。（CheckResult is the outcome of a risk check.）
type CheckResult struct {
	// 是否通过
	Pass bool `json:"pass"`
	// 建议动作：pass/block/reduce/sell_all
	Action string `json:"action"`
	// 关联优先级（仅阻断时有意义）
	Priority strategy.Priority `json:"priority"`
	// 阻断原因描述
	Reason string `json:"reason"`
	// 是否被彻底阻断（不进入后续流程）
	Blocked bool `json:"blocked"`
}

// M8CheckWith M8 组合回撤兜底判定——全系统唯一实现（Gate.CheckPortfolio 委托、
// 实盘 scoring_loop.checkM8RealDrawdown 直调、测试直接调用）。
// §R7 阈值归一（自 scoring_loop 内联版收敛来，20260917 §F-7 起为本函数职责）：
// 阈值接受正数（"回撤 10% 触发"写 10）或负数（旧口径），一律归一为负值参与比较；
// 0 或未设置视为关闭——避免误填 0/正值导致 M8 静默失效或恒触发。
// English: the single authoritative M8 portfolio-drawdown judge shared by the live loop and the
// gate; positive thresholds are normalized to the negative comparison scale (R7), and 0/unset
// disables the check.
func M8CheckWith(cfg *config.Rules, currentTotal, peakTotal float64) *CheckResult {
	// 无配置 → 不做任何检查，视为通过（fail-open）。
	if cfg == nil {
		return &CheckResult{Pass: true}
	}
	rc := cfg.RiskCtrl
	thr := rc.M8PortfolioDrawdownPct
	// 阈值归一（§R7）：接受正数（"回撤 10% 触发"写 10）或旧口径负数，统一归一为负值再比较；
	// 0 或未设置（归一后仍 ≥0）视为关闭。
	if thr > 0 {
		thr = -thr
	}
	// M8 兜底未启用、阈值未配置（归一后 ≥0）或无有效峰值时不检查
	if !rc.M8Enabled || thr >= 0 || peakTotal <= 0 {
		return &CheckResult{Pass: true}
	}
	// 组合回撤 = (当前市值 - 峰值市值) / 峰值市值 * 100
	drawdown := (currentTotal - peakTotal) / peakTotal * 100
	// 回撤跌破（≤）阈值 → 触发 M8 兜底：P1 优先级、清仓动作、彻底阻断后续加仓流程。
	if drawdown <= thr {
		return &CheckResult{
			Pass:     false,
			Action:   "sell_all",
			Priority: strategy.P1,
			Reason:   fmt.Sprintf("M8兜底触发: 组合回撤%.1f%%", drawdown),
			Blocked:  true,
		}
	}
	// 回撤仍在阈值内 → 通过。
	return &CheckResult{Pass: true}
}
