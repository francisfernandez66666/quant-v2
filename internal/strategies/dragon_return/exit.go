package dragon_return

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断龙回头策略是否触发退出信号。（CheckExit decides whether the Dragon Return strategy should exit.）
// 检查顺序：止损（-StopLossPct）→ 止盈 T2（Target2）→ 移动止损（回撤 TrailingDrawback）→ 跌破 MA20×0.98 → 止盈 T1 → 超期。
// 返回 nil 表示继续持有。（Checks in order: stop-loss → take-profit T2 → trailing stop → break below MA20×0.98 → T1 → timeout;
// returns nil to keep holding.）
func CheckExit(ctx *strategy.ExitContext, cfg *config.DragonReturnConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	// 成本或现价非法时无法评估，视为不退出（Cannot evaluate with invalid cost/price; hold）
	if cost <= 0 || price <= 0 {
		return nil
	}

	// 维护阶段最高价（优先取 EntryMeta 记录，其次取现价）（Track the stage high, preferring EntryMeta then live price）
	highest := cost
	if ctx.EntryMeta != nil {
		if h, ok := ctx.EntryMeta["highest_price"]; ok && h > highest {
			highest = h
		}
	}
	if price > highest {
		highest = price
	}

	// 盈亏率（P&L percentage）
	lossPct := (price - cost) / cost * 100

	// 止损：浮亏达到 StopLossPct（默认 -5%）立即止损离场（C4：ATR 动态止损启用时
	// 止损距离取 ATR×mult，否则回退 StopLossPct）。
	// English: stop-loss — exit immediately at −StopLossPct (default −5%); C4: the ATR dynamic stop
	// (ATR×mult) takes precedence when enabled, else fall back to StopLossPct.
	sl := -ctx.ATRStopPct(cfg.StopLossPct)
	if lossPct <= sl {
		return &strategy.ExitResult{Reason: "龙回头止损", Priority: strategy.P1}
	}

	// 止盈 T2：到达目标价2（默认成本×1.25），兑现主升利润（Take-profit T2: at cost×Target2Multiplier, default 1.25×）
	target2 := cost * cfg.Target2Multiplier
	if price >= target2 {
		return &strategy.ExitResult{Reason: "龙回头止盈T2", Priority: strategy.P2}
	}

	// 移动止损：从阶段最高点回撤超过 TrailingDrawback（默认 8%）且已盈利过 → 保护利润（Trailing stop: −TrailingDrawback from stage high after profit, default 8%）
	trailPct := (price - highest) / highest * 100
	trailThreshold := -cfg.TrailingDrawback
	if trailPct <= trailThreshold && highest > cost {
		return &strategy.ExitResult{Reason: "龙回头移动止盈", Priority: strategy.P2}
	}

	// 破位：收盘跌破 MA20×0.98，中期支撑失守离场（Breakdown: close below MA20×0.98 loses the mid-term support）
	if len(ctx.DailyK) >= 20 {
		var ma20Sum float64
		for i := len(ctx.DailyK) - 20; i < len(ctx.DailyK); i++ {
			ma20Sum += ctx.DailyK[i].Close
		}
		ma20 := ma20Sum / 20
		lastClose := ctx.DailyK[len(ctx.DailyK)-1].Close
		if lastClose < ma20*0.98 {
			return &strategy.ExitResult{Reason: "龙回头破位", Priority: strategy.P2}
		}
	}

	// 止盈 T1：到达目标价1（默认成本×1.0）且非深套状态（浮亏 <2%）→ 先保本兑现（Take-profit T1: at cost×1.0 when not deeply underwater (loss <2%) → lock in breakeven）
	target1 := cost * cfg.Target1Multiplier
	if price >= target1 && price < target2 && lossPct > -2 {
		return &strategy.ExitResult{Reason: "龙回头止盈T1", Priority: strategy.P2}
	}

	// 超期：持仓超过 MaxHoldDays（默认 8 天）强制离场（二波逻辑失效）（Timeout: held beyond MaxHoldDays (default 8) → force exit, second-leg logic has lapsed）
	if ctx.EntryAt != "" {
		now := ctx.Now
		if now.IsZero() {
			now = time.Now()
		}
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			days := int(now.Sub(entryDate).Hours() / 24)
			if days >= cfg.MaxHoldDays {
				return &strategy.ExitResult{Reason: "龙回头超期退出", Priority: strategy.P3}
			}
		}
	}

	return nil
}

// NeedUpdateHighest 龙回头策略需要更新最高价。（NeedUpdateHighest reports that Dragon Return tracks the stage high price.）
func NeedUpdateHighest() bool { return true }
