package double_bump

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断双凸策略是否触发退出信号。（CheckExit decides whether the Double Bump strategy should exit.）
// 检查顺序：放量派发（P1）→ 跌破 MA5（P2）→ 达到止盈线（P2）→ 最高价回撤 8%（P2）→ 调整超期（P3）。（Checks in order:
// volume distribution (P1) → break below MA5 (P2) → take-profit (P2) → 8% trail stop (P2) → timeout (P3).）
// 返回 nil 表示继续持有。（Returns nil to keep holding.）
func CheckExit(ctx *strategy.ExitContext, cfg *config.DoubleBumpConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	// 成本或现价非法时无法评估，视为不退出（Cannot evaluate with invalid cost/price; hold）
	if cost <= 0 || price <= 0 {
		return nil
	}

	// 当前持仓盈亏率（%，正=盈利）（Current P&L percentage, positive = profit）
	pnlPct := (price - cost) / cost * 100

	// C4 ATR 硬止损：启用且日K充足时，跌破 ATR×mult 立即离场（避免双凸回撤过大）。
	// English: C4 ATR hard stop — when active and bars suffice, exit once price breaks below ATR×mult.
	if ctx.ATRStopMult > 0 && ctx.ATR > 0 && pnlPct <= -ctx.ATRStopPct(8) {
		return &strategy.ExitResult{Reason: "双凸ATR止损", Priority: strategy.P1}
	}

	// 维护阶段最高价（优先取 EntryMeta 中记录的，其次取现价）（Track the stage high, preferring EntryMeta then live price）
	highest := cost
	if ctx.EntryMeta != nil {
		if h, ok := ctx.EntryMeta["highest_price"]; ok && h > highest {
			highest = h
		}
	}
	if price > highest {
		highest = price
	}

	// 有足够日K线（≥5根）才进行量价形态类检查（派发/破MA5）（Require ≥5 daily bars for volume/price formation checks）
	if len(ctx.DailyK) >= 5 {
		last := ctx.DailyK[len(ctx.DailyK)-1]
		prev := ctx.DailyK[len(ctx.DailyK)-2]

		// 派发信号：放量（>近4日均量×1.5）且收阴 → 主力出货，立即离场（Distribution: >1.5× 4-day avg volume with a down close → exit now）
		if last.Volume > 0 {
			avgVol := 0.0
			for i := len(ctx.DailyK) - 5; i < len(ctx.DailyK)-1; i++ {
				avgVol += ctx.DailyK[i].Volume
			}
			avgVol /= 4
			if avgVol > 0 && last.Volume > avgVol*1.5 && last.Close < prev.Close {
				return &strategy.ExitResult{Reason: "双凸派发信号", Priority: strategy.P1}
			}
		}

		// 跌破 MA5：短期趋势转弱，减仓离场（Break below MA5: short-term trend weakening, exit）
		var ma5Sum float64
		for i := len(ctx.DailyK) - 5; i < len(ctx.DailyK); i++ {
			ma5Sum += ctx.DailyK[i].Close
		}
		ma5 := ma5Sum / 5
		if last.Close < ma5 {
			return &strategy.ExitResult{Reason: "双凸破MA5", Priority: strategy.P2}
		}
	}

	// 止盈：浮盈达到 DoubleBumpTakeProfitPct（默认 15%）落袋（Take profit: unrealized gain ≥ DoubleBumpTakeProfitPct, default 15%）
	tp := cfg.DoubleBumpTakeProfitPct
	if tp <= 0 {
		tp = 15
	}
	if pnlPct >= tp {
		return &strategy.ExitResult{Reason: "双凸止盈", Priority: strategy.P2}
	}

	// 移动止损：从阶段最高点回撤 8% 且已盈利过 → 保护利润退出（Trailing stop: −8% from stage high after profitability → protect gains）
	trailPct := (price - highest) / highest * 100
	if trailPct <= -8 && highest > cost {
		return &strategy.ExitResult{Reason: "双凸回撤退出", Priority: strategy.P2}
	}

	// 调整超期：持仓天数超过 AdjustDaysOverflow（默认 10 天）强制离场（Timeout: held beyond AdjustDaysOverflow days, default 10 → force exit）
	if ctx.EntryAt != "" {
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			days := int(time.Since(entryDate).Hours() / 24)
			overflow := cfg.AdjustDaysOverflow
			if overflow <= 0 {
				overflow = 10
			}
			if days >= overflow {
				return &strategy.ExitResult{Reason: "双凸调整超期", Priority: strategy.P3}
			}
		}
	}

	return nil
}

// NeedUpdateHighest 双凸策略需要更新最高价。（NeedUpdateHighest reports that Double Bump tracks the stage high price.）
func NeedUpdateHighest() bool { return true }
