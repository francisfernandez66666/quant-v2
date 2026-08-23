// sweep_exit.go 扫参统一出场旋钮（STRATEGY_OPTIMIZE_PLAN §内置一键应用）。
//
// 扫参排名用"阶段高点回撤止盈% + 最长持仓天"的统一引擎度量所有战法；要让寻优冠军参数
// 在实盘产生同样的行为，四个手写战法都需要这两个旋钮。龙回头原生已有（TrailingDrawback/
// MaxHoldDays），其余三个经本助手在各自 CheckExit 最前面执行——配置 >0 才启用，
// 默认 0 保持既有退出规则完全不变。
// English: shared trailing+hold exit knob so every hand-written strategy can honor its
// sweep-approved params; zero values keep legacy behaviour untouched.
package strategy

import (
	"time"
)

// ApplyTrailingHoldExit 统一出场：从阶段高点回撤 ≥trailPct%（且曾盈利）→ 移动止盈离场；
// 持仓 ≥maxHoldDays 天 → 超期离场。trailPct<=0 或 maxHoldDays<=0 的维度跳过；
// 两者都未配置返回 nil。阶段高点取 EntryMeta["highest_price"]（入场写入、逐日由调用方抬高）。
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
