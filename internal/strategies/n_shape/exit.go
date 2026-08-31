// N 形超短战法离场判定：硬止损/形态失败/尾盘强平/量能衰竭等出场逻辑（CheckExit）。
// 本文件实现了N形超短战法的退出逻辑，包含多种退出条件：
//   - 硬止损：跌破成本价超过阈值
//   - 形态失败：入场时已处于失败阶段
//   - 尾盘强平：超短策略日内了结
//   - 量能衰竭：入场时量比不足
package n_shape

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// CheckExit 判断 N 形策略是否触发退出信号。
// 按优先级依次检查多种退出条件，返回nil表示继续持有，返回ExitResult表示应该退出。
//
// 检查顺序（优先级从高到低）：
//  1. 扫参统一出场（TrailingDrawbackPct/MaxHoldDays）
//  2. 硬止损：跌破成本价超过阈值（支持ATR动态止损）
//  3. 形态失败：入场时已处于失败阶段（NPhaseFailed=5）
//  4. 尾盘强平：14:57后超短策略必须日内了结
//  5. 量能衰竭：入场时量比不足（vol_ratio<0.5）
//
// 返回值：
//   - nil: 继续持有
//   - *ExitResult: 触发退出，包含退出理由和优先级
//
// （CheckExit decides whether the N-shape strategy should exit.）
func CheckExit(ctx *strategy.ExitContext, cfg *config.NShapeConfig) *strategy.ExitResult {
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

	// 硬止损：现价跌破 成本×(1−hardStop) 立即退出（hardStop 为止损比例，默认 0.045 即 -4.5%）
	// C4：ATR 动态止损启用时止损距离为 ATR×mult（百分比口径），否则回退 hardStop。
	// English: hard stop — exit immediately when price ≤ cost×(1−hardStop), where hardStop is the loss
	// ratio (config default 0.08 = -8%; legacy 0.955 multiplier semantic is normalized to ~4.5% loss).
	// C4: when ATR stopping is active the stop distance is ATR×mult (percent), else fall back to hardStop.
	hardStop := cfg.HardStopLoss
	if hardStop <= 0 {
		hardStop = 0.045
	} else if hardStop > 1 {
		// 兼容旧语义：0.955 这类"价格乘数"按 1−x 折算为亏损比例
		// English: backward-compat with the legacy "price multiplier" semantic (e.g. 0.955) — treat as 1−x loss ratio.
		hardStop = 1 - hardStop
	}
	// ATRStopPct 返回的是"亏损百分比"口径（与 pnlPct 同量纲）
	// English: ATRStopPct returns loss-percent units (same scale as pnlPct).
	pnlPct := (price - cost) / cost * 100
	if pnlPct <= -ctx.ATRStopPct(hardStop*100) {
		return &strategy.ExitResult{Reason: "N形硬止损", Priority: strategy.P1}
	}

	// 入场时已处于"形态失败"阶段的持仓（NPhaseFailed=5）直接退出（Entry phase already failed (NPhaseFailed=5) → exit directly）
	if ctx.EntryMeta != nil {
		if phase, ok := ctx.EntryMeta["entry_nphase"]; ok {
			if phase == 5 {
				return &strategy.ExitResult{Reason: "N形形态失败", Priority: strategy.P1}
			}
		}
	}

	// 尾盘门控：14:57 后为尾盘集合竞价，超短策略必须日内了结（Late-session gate: after 14:57 the closing auction runs — ultra-short positions must close intraday）
	now := ctx.Now
	if !now.IsZero() {
		marketClose := time.Date(now.Year(), now.Month(), now.Day(), 14, 57, 0, 0, now.Location())
		if now.After(marketClose) {
			// 入场时形态已"完成"（NPhaseCompleted=4）则视为完整止盈离场（Entry phase completed (NPhaseCompleted=4) → full take-profit exit）
			if ctx.EntryMeta != nil {
				if phase, ok := ctx.EntryMeta["entry_nphase"]; ok && phase == 4 {
					return &strategy.ExitResult{Reason: "N形完成止盈", Priority: strategy.P2}
				}
			}
			// 否则尾盘无条件强平（超短不留隔夜）（Otherwise force-close at the close — no overnight for ultra-short）
			return &strategy.ExitResult{Reason: "N形收盘强平", Priority: strategy.P2}
		}
	}

	// 量能衰竭：入场时记录的 vol_ratio < 0.5 说明承接不足，逢高离场（Volume drain: entry vol_ratio < 0.5 means weak support → exit on strength）
	if ctx.EntryMeta != nil {
		if volRatio, ok := ctx.EntryMeta["vol_ratio"]; ok && volRatio > 0 && volRatio < 0.5 {
			return &strategy.ExitResult{Reason: "N形量能衰竭", Priority: strategy.P3}
		}
	}

	return nil
}

// NeedUpdateHighest N 形策略无需更新最高价。
// N形是超短策略，不追踪阶段最高价，因此返回false。
//
// （NeedUpdateHighest reports that N-shape does not track a stage high price.）
func NeedUpdateHighest() bool { return false }
