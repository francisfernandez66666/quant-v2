// 破局龙战法离场判定：止盈/买入回撤/炸板回落/尾盘/次日不及预期/超期等出场逻辑（CheckExit）。
package dragon

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断破局龙策略是否触发退出信号。（CheckExit decides whether the Dragon strategy should exit.）
// 检查顺序：止盈 → 买入回撤（全出/半仓）→ 炸板回落（跌破封板价）→ 买入日收盘不佳 → 次日开盘不及预期 → 超期退出。（Checks in order:
// take-profit → post-buy pullback (all/half) → broken-seal retreat → bad close on entry day → weak next-day open → timeout.）
// 返回 nil 表示继续持有。（Returns nil to keep holding.）
func CheckExit(ctx *strategy.ExitContext, cfg *config.DragonConfig) *strategy.ExitResult {
	// §扫参统一出场（STRATEGY_OPTIMIZE_PLAN）：配置了 trailing_drawback_pct/max_hold_days
	// 时优先执行——与扫参排名同口径；未配置(0)时完全走既有规则，行为零变更。
	if res := strategy.ApplyTrailingHoldExit(ctx, cfg.TrailingDrawbackPct, cfg.MaxHoldDays); res != nil {
		return res
	}
	cost := ctx.CostPrice
	price := ctx.CurPrice
	// 成本或现价非法时无法评估，视为不退出（Cannot evaluate with invalid cost/price; hold）
	if cost <= 0 || price <= 0 {
		return nil
	}

	// 持仓盈亏率（正=盈利）（Holding P&L percentage, positive = profit）
	pnlPct := (price - cost) / cost * 100

	// 止盈（C2）：浮盈达到 TakeProfitPct（默认 10%）落袋。龙战法为超短策略，
	// 封板利润到目标即锁定，避免次日回吐（Take profit: once unrealized gain hits
	// TakeProfitPct (default 10%), lock it in — an ultra-short dragon shouldn't give back gains).
	tp := cfg.TakeProfitPct
	if tp <= 0 {
		tp = 10
	}
	if pnlPct >= tp {
		return &strategy.ExitResult{Reason: "破局龙止盈", Priority: strategy.P2}
	}

	// 买入后回撤（C4 ATR 动态止损优先）：止损距离取 ATR×mult（启用且日K充足时），
	// 否则回退固定百分比；跌超全出线清仓、跌超半仓线减半。
	// English: post-buy pullback — the C4 ATR dynamic stop (ATR×mult when enabled and bars suffice) takes
	// precedence over the fixed percent; below the all-out line → exit all, below the half line → exit half.
	sellAll := ctx.ATRStopPct(cfg.BuyPullbackSellAllPct)
	sellHalf := ctx.ATRStopPct(cfg.BuyPullbackSellHalfPct)
	if pnlPct <= -sellAll {
		return &strategy.ExitResult{Reason: "买入回撤全出", Priority: strategy.P1}
	}
	if pnlPct <= -sellHalf {
		return &strategy.ExitResult{Reason: "买入回撤半仓", Priority: strategy.P2}
	}

	// 炸板回落：以入场时的封板价（limit_price）为基准，跌破阈值触发半仓/全出（Broken seal: measured from the entry limit price, breaching thresholds triggers half/all exit）
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

	// 尾盘检查（14:55~15:00）：买入日收盘浮亏超过 BuyDayCloseBelow 则离场（Late-session check 14:55–15:00: exit if entry-day close loss ≤ BuyDayCloseBelow）
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

	// 次日及以后：按入场日期计算持仓天数（From the next day on: compute holding days from the entry date）
	if ctx.EntryAt != "" {
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			days := int(now.Sub(entryDate).Hours() / 24)
			today := now.Format("2006-01-02")
			// 持仓 ≥1 天：检查开盘价，低于 NextOpenIfBelow 视为次日不及预期（Held ≥1 day: exit if today's open is below NextOpenIfBelow）
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
			// 持仓 ≥2 天：龙战法为超短策略，超期强制离场（Held ≥2 days: ultra-short-term, force exit on timeout）
			if days >= 2 {
				return &strategy.ExitResult{Reason: "破局龙超期", Priority: strategy.P3}
			}
		}
	}

	return nil
}

// NeedUpdateHighest 破局龙策略无需更新最高价。（NeedUpdateHighest reports that Dragon does not track a stage high price.）
func NeedUpdateHighest() bool { return false }
