package dragon

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断破局龙策略是否触发退出信号。
// 检查顺序：买入回撤（全出/半仓）→ 炸板回落（跌破封板价）→ 买入日收盘不佳 → 次日开盘不及预期 → 超期退出。
// 返回 nil 表示继续持有。
func CheckExit(ctx *strategy.ExitContext, cfg *config.DragonConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	// 成本或现价非法时无法评估，视为不退出
	if cost <= 0 || price <= 0 {
		return nil
	}

	// 持仓盈亏率（正=盈利）
	pnlPct := (price - cost) / cost * 100

	// 买入后回撤：跌超 BuyPullbackSellAllPct 全出；跌超 BuyPullbackSellHalfPct 半仓减
	if pnlPct <= -cfg.BuyPullbackSellAllPct {
		return &strategy.ExitResult{Reason: "买入回撤全出", Priority: strategy.P1}
	}
	if pnlPct <= -cfg.BuyPullbackSellHalfPct {
		return &strategy.ExitResult{Reason: "买入回撤半仓", Priority: strategy.P2}
	}

	// 炸板回落：以入场时的封板价（limit_price）为基准，跌破阈值触发半仓/全出
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

	// 尾盘检查（14:55~15:00）：买入日收盘浮亏超过 BuyDayCloseBelow 则离场
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

	// 次日及以后：按入场日期计算持仓天数
	if ctx.EntryAt != "" {
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			days := int(now.Sub(entryDate).Hours() / 24)
			today := now.Format("2006-01-02")
			// 持仓 ≥1 天：检查开盘价，低于 NextOpenIfBelow 视为次日不及预期
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
			// 持仓 ≥2 天：龙战法为超短策略，超期强制离场
			if days >= 2 {
				return &strategy.ExitResult{Reason: "破局龙超期", Priority: strategy.P3}
			}
		}
	}

	return nil
}

// NeedUpdateHighest 破局龙策略无需更新最高价。
func NeedUpdateHighest() bool { return false }
