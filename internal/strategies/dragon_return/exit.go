package dragon_return

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断龙回头策略是否触发退出信号。
func CheckExit(ctx *strategy.ExitContext, cfg *config.DragonReturnConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil
	}

	highest := cost
	if ctx.EntryMeta != nil {
		if h, ok := ctx.EntryMeta["highest_price"]; ok && h > highest {
			highest = h
		}
	}
	if price > highest {
		highest = price
	}

	lossPct := (price - cost) / cost * 100

	sl := -cfg.StopLossPct
	if lossPct <= sl {
		return &strategy.ExitResult{Reason: "龙回头止损", Priority: strategy.P1}
	}

	target2 := cost * cfg.Target2Multiplier
	if price >= target2 {
		return &strategy.ExitResult{Reason: "龙回头止盈T2", Priority: strategy.P2}
	}

	trailPct := (price - highest) / highest * 100
	trailThreshold := -cfg.TrailingDrawback
	if trailPct <= trailThreshold && highest > cost {
		return &strategy.ExitResult{Reason: "龙回头移动止盈", Priority: strategy.P2}
	}

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

	target1 := cost * cfg.Target1Multiplier
	if price >= target1 && price < target2 && lossPct > -2 {
		return &strategy.ExitResult{Reason: "龙回头止盈T1", Priority: strategy.P2}
	}

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

// NeedUpdateHighest 龙回头策略需要更新最高价。
func NeedUpdateHighest() bool { return true }
