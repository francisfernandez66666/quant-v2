package n_shape

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断 N 形策略是否触发退出信号。
func CheckExit(ctx *strategy.ExitContext, cfg *config.NShapeConfig) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil
	}

	hardStop := cfg.HardStopLoss
	if hardStop <= 0 {
		hardStop = 0.955
	}
	if price <= cost*hardStop {
		return &strategy.ExitResult{Reason: "N形硬止损", Priority: strategy.P1}
	}

	if ctx.EntryMeta != nil {
		if phase, ok := ctx.EntryMeta["entry_nphase"]; ok {
			if phase == 5 {
				return &strategy.ExitResult{Reason: "N形形态失败", Priority: strategy.P1}
			}
		}
	}

	now := ctx.Now
	if !now.IsZero() {
		marketClose := time.Date(now.Year(), now.Month(), now.Day(), 14, 57, 0, 0, now.Location())
		if now.After(marketClose) {
			if ctx.EntryMeta != nil {
				if phase, ok := ctx.EntryMeta["entry_nphase"]; ok && phase == 4 {
					return &strategy.ExitResult{Reason: "N形完成止盈", Priority: strategy.P2}
				}
			}
			return &strategy.ExitResult{Reason: "N形收盘强平", Priority: strategy.P2}
		}
	}

	if ctx.EntryMeta != nil {
		if volRatio, ok := ctx.EntryMeta["vol_ratio"]; ok && volRatio > 0 && volRatio < 0.5 {
			return &strategy.ExitResult{Reason: "N形量能衰竭", Priority: strategy.P3}
		}
	}

	return nil
}

// NeedUpdateHighest N 形策略无需更新最高价。
func NeedUpdateHighest() bool { return false }
