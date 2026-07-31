package dragon

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断破局龙策略是否触发退出信号。
func CheckExit(ctx *strategy.ExitContext, cfg *config.DragonConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil
	}

	pnlPct := (price - cost) / cost * 100

	if pnlPct <= -cfg.BuyPullbackSellAllPct {
		return &strategy.ExitResult{Reason: "买入回撤全出", Priority: strategy.P1}
	}
	if pnlPct <= -cfg.BuyPullbackSellHalfPct {
		return &strategy.ExitResult{Reason: "买入回撤半仓", Priority: strategy.P2}
	}

	if ctx.EntryMeta != nil {
		if limitPrice, ok := ctx.EntryMeta["limit_price"]; ok && limitPrice > 0 {
			breakPct := (price - limitPrice) / limitPrice * 100
			if breakPct <= -cfg.BreakerSellHalfPct {
				return &strategy.ExitResult{Reason: "炸板半仓", Priority: strategy.P2}
			}
			if breakPct <= -cfg.BreakerSellAllPct {
				return &strategy.ExitResult{Reason: "炸板全出", Priority: strategy.P1}
			}
		}
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now()
	}
	todayClose := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
	if now.After(todayClose.Add(-5*time.Minute)) && now.Before(todayClose) {
		if pnlPct <= cfg.BuyDayCloseBelow {
			return &strategy.ExitResult{Reason: "买入日收盘不佳", Priority: strategy.P2}
		}
	}

	if ctx.EntryAt != "" {
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			days := int(now.Sub(entryDate).Hours() / 24)
			today := now.Format("2006-01-02")
			if entryDate.Format("2006-01-02") != today && days >= 1 {
				openPrice := price
				if len(ctx.DailyK) > 0 {
					openPrice = ctx.DailyK[len(ctx.DailyK)-1].Open
				}
				openPct := (openPrice - cost) / cost * 100
				if openPct <= cfg.NextOpenIfBelow {
					return &strategy.ExitResult{Reason: "次日开盘不及预期", Priority: strategy.P2}
				}
			}
			if days >= 2 {
				return &strategy.ExitResult{Reason: "破局龙超期", Priority: strategy.P3}
			}
		}
	}

	return nil
}

// NeedUpdateHighest 破局龙策略无需更新最高价。
func NeedUpdateHighest() bool { return false }
