// sweep_exit.go 扫参统一出场旋钮（STRATEGY_OPTIMIZE_PLAN §内置一键应用）。
//
// 本文件实现了扫参统一出场逻辑，用于在策略优化过程中统一所有战法的出场规则。
// 扫参排名用"阶段高点回撤止盈% + 最长持仓天"的统一引擎度量所有战法；
// 要让寻优冠军参数在实盘产生同样的行为，四个手写战法都需要这两个旋钮。
//
// 核心逻辑：
//   - 阶段高点回撤止盈：从持仓期间最高点回撤超过trailPct%时触发出场
//   - 最长持仓天：持仓超过maxHoldDays天时强制出场
//
// 使用方式：
//   - trailPct<=0 或 maxHoldDays<=0 的维度跳过
//   - 两者都未配置返回nil（不触发退出）
//   - 默认0保持既有退出规则完全不变
//
// English: shared trailing+hold exit knob so every hand-written strategy can honor its
// sweep-approved params; zero values keep legacy behaviour untouched.
package strategy

import (
	"time"
)

// ApplyTrailingHoldExit 统一出场函数。
// 实现了扫参优化的统一出场逻辑，用于在实盘中应用扫参寻优得到的参数。
//
// 出场条件：
//  1. 移动止盈离场：从阶段高点回撤 ≥trailPct%（且曾盈利）→ 触发止盈
//  2. 超期离场：持仓 ≥maxHoldDays 天 → 强制离场
//
// 参数说明：
//   - ctx: 退出上下文，包含持仓成本、当前价格、入场时间等
//   - trailPct: 回撤止盈百分比（≤0表示不启用）
//   - maxHoldDays: 最长持仓天数（≤0表示不启用）
//
// 返回值：
//   - nil: 继续持有（未触发退出条件）
//   - *ExitResult: 触发退出，包含退出理由和优先级
//
// 阶段高点取 EntryMeta["highest_price"]（入场写入、逐日由调用方抬高）。
//
// English: uniform exit — trailing drawdown from stage high and timeout hold; disabled dimensions
// are skipped. Returns nil when neither knob is configured or no condition hits.
func ApplyTrailingHoldExit(ctx *ExitContext, trailPct float64, maxHoldDays int) *ExitResult {
	if ctx == nil || ctx.CostPrice <= 0 || ctx.CurPrice <= 0 {
		return nil
	}
	if trailPct <= 0 && maxHoldDays <= 0 {
		return nil
	}
	stageHigh := ctx.CostPrice
	if h, ok := ctx.EntryMeta["highest_price"]; ok && h > stageHigh {
		stageHigh = h
	}
	if trailPct > 0 && stageHigh > ctx.CostPrice {
		dd := (ctx.CurPrice - stageHigh) / stageHigh * 100
		if dd <= -trailPct {
			return &ExitResult{Reason: "扫参止盈(移动回撤)", Priority: P2}
		}
	}
	if maxHoldDays > 0 && ctx.EntryAt != "" {
		if entryDate, err := time.Parse("2006-01-02", ctx.EntryAt); err == nil {
			if days := int(ctx.Now.Sub(entryDate).Hours() / 24); days >= maxHoldDays {
				return &ExitResult{Reason: "扫参超期离场", Priority: P3}
			}
		}
	}
	return nil
}
