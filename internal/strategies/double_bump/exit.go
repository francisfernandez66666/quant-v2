package double_bump

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断双凸策略是否触发退出信号。
func CheckExit(ctx *strategy.ExitContext, cfg *config.DoubleBumpConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil
	}

	pnlPct := (price - cost) / cost * 100

	highest := cost
	if ctx.EntryMeta != nil {
		if h, ok := ctx.EntryMeta["highest_price"]; ok && h > highest {
			highest = h
		}
	}
	if price > highest {
		highest = price
	}

	if len(ctx.DailyK) >= 5 {
		last := ctx.DailyK[len(ctx.DailyK)-1]
		prev := ctx.DailyK[len(ctx.DailyK)-2]

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

		var ma5Sum float64
		for i := len(ctx.DailyK) - 5; i < len(ctx.DailyK); i++ {
			ma5Sum += ctx.DailyK[i].Close
		}
		ma5 := ma5Sum / 5
		if last.Close < ma5 {
			return &strategy.ExitResult{Reason: "双凸破MA5", Priority: strategy.P2}
		}
	}

	tp := cfg.DoubleBumpTakeProfitPct
	if tp <= 0 {
		tp = 15
	}
	if pnlPct >= tp {
		return &strategy.ExitResult{Reason: "双凸止盈", Priority: strategy.P2}
	}

	trailPct := (price - highest) / highest * 100
	if trailPct <= -8 && highest > cost {
		return &strategy.ExitResult{Reason: "双凸回撤退出", Priority: strategy.P2}
	}

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

// NeedUpdateHighest 双凸策略需要更新最高价。
func NeedUpdateHighest() bool { return true }
