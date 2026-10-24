// stop_offset.go — §DYN-SIZE P5 止损/止盈**两态偏移**（纯函数，方案 docs/DYN_SIZE_FINAL_PLAN_20261016.md §2.6）。
//
// 需求（owner）："大盘严重不佳（整体 -1% 左右甚至以上）时，止损点同步下调（昨天大盘 -2 个点，
// 止损点下浮 3 个点），防止被突然股灾砸丢筹码；止盈点参考这个逻辑反向上浮。"
//
// 为什么必须做**两态**判别（OSS 调研 §6.7 的方向性冲突）：业界共识是"熊市收紧止损"，与 owner
// 要放宽的诉求方向相反——两者前提不同，不能一刀切：
//
//	单日冲击（大盘单日 ≤ -1% **且** 趋势未破）→ **放宽**止损 / 上浮止盈（防被一根大阴线洗出去）
//	趋势下跌（创 20 日新低 / 跌破下行 MA20 / 连阴 ≥3 日）→ **收紧**止损（优先保命）
//
// 两条豁免（owner 裁决 + 战法纪律）：
//   - **做空持仓**不适用：融券账本口径独立，且"放宽"对空头方向相反；
//   - **N 形超短**不适用：超短的语义就是快进快出，放宽止损直接违背战法纪律。
//
// 另有一条**自保**门：个股当日跌幅与大盘跌幅相差 ≥3pt 时不动——那是个股自身崩坏（利空/暴雷），
// 放宽止损只会把个股风险放大，而不是防"被大盘带下去"。
//
// English: §DYN-SIZE P5 — the two-state stop-loss/take-profit offset. A single-day market shock
// loosens the stop (and raises the target); a broken trend tightens it. Shorts and the N-shape
// ultra-short strategy are exempt, and a stock moving ≥3pt away from the index is left alone
// (that is idiosyncratic damage, not a market-wide shake-out).
package combat_agent

import (
	"fmt"
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// stockIndexDivergencePct 个股与大盆跌幅的"独立崩坏"判定阈值（pt）：超出即不适用偏移。
const stockIndexDivergencePct = 3.0

// StopOffsetInput 两态偏移的输入（市场态一次读 + 个股态逐持仓）。
// IndexChangePct 为 NaN 表示大盘读数不可得 → 不适用偏移（弃权，绝不当作 0）。
type StopOffsetInput struct {
	IndexChangePct   float64 // 大盘最近一个交易日涨跌幅 %（NaN=不可得）
	IndexTrendBroken bool    // 趋势是否破位（创 20 日新低 / 跌破下行 MA20 / 连阴 ≥3 日）
	StockChangePct   float64 // 个股当日涨跌幅 %（NaN=不可得 → 不做独立崩坏判定）
	IsShort          bool    // 做空持仓 → 豁免
	Strategy         string  // 战法显示名（N 形超短豁免）
}

// StopOffsets 有效止损/止盈偏移（百分点）。
//
//	SL > 0 = 放宽（止损线下移 SL 个点）；SL < 0 = 收紧（上移）。
//	TP > 0 = 上浮（止盈目标抬高 TP 个点）。
//
// Applied=false 表示本次不适用（关闭 / 豁免 / 读数不可得），SL/TP 恒 0。
type StopOffsets struct {
	SL      float64
	TP      float64
	Reason  string
	Applied bool
}

// ComputeStopOffsets 两态判别主入口（纯函数，100% 可单测）。
//
// 总开关关（`EnabledOn()==false`，账号级显式关闭）时恒返回零值 → 调用方的有效阈值与改造前
// **逐字一致**。§DYN-GLOBAL 起出厂默认是"开"，本特性因此随动态仓位一起生效；不要它请按账号
// 关掉总开关，或把 `sl_offset_*` 参数归零（每把子闸都有独立退路，不额外加影子判断——影子只约束
// 买入金额，市场态止损偏移按其原设计跟随总开关）。
// English: the pure entry point. With the master switch off it returns the zero value so effective
// thresholds stay byte-identical to pre-change behavior. Since §DYN-GLOBAL the default is on, so
// this feature activates together with dynamic sizing; opt out per account via the master switch or
// by zeroing the sl_offset_* parameters (shadow only gates buy amounts, by original design).
func ComputeStopOffsets(in StopOffsetInput, cfg config.SizingConfig) StopOffsets {
	if !cfg.EnabledOn() {
		return StopOffsets{} // 未启用：零行为变更
	}
	if in.IsShort {
		return StopOffsets{} // 做空持仓豁免（融券口径独立，"放宽"方向相反）
	}
	if NormalizeStrategyName(in.Strategy) == NormalizeStrategyName(string(strategy.SignalNShape)) {
		return StopOffsets{} // N 形超短豁免（快进快出，放宽违背战法纪律）
	}

	// 一态：趋势下跌 → 收紧（优先保命，覆盖"单日冲击"判定）。
	if in.IndexTrendBroken {
		if cfg.BearSLTightenPt <= 0 {
			return StopOffsets{}
		}
		return StopOffsets{SL: -cfg.BearSLTightenPt, Applied: true,
			Reason: fmt.Sprintf("趋势破位,止损收紧%.1fpt", cfg.BearSLTightenPt)}
	}

	// 二态：单日冲击 → 放宽 / 上浮。门槛：大盘跌幅达 SLOffsetMinIndexDrop（默认 1%）。
	if math.IsNaN(in.IndexChangePct) || in.IndexChangePct > -cfg.SLOffsetMinIndexDrop {
		return StopOffsets{} // 跌幅未达门槛 / 读数不可得
	}
	// 个股独立崩坏：跌幅显著偏离大盘 → 不适用（放宽只会放大个股自身风险）。
	if !math.IsNaN(in.StockChangePct) && math.Abs(in.StockChangePct-in.IndexChangePct) >= stockIndexDivergencePct {
		return StopOffsets{}
	}

	excess := math.Abs(in.IndexChangePct) - cfg.SLOffsetMinIndexDrop // 超出门槛的部分（如 -2% → 1.0）
	if excess < 0 {
		excess = 0
	}
	sl := math.Min(excess*cfg.SLOffsetPerPct, cfg.SLOffsetCap)
	tp := math.Min(excess*cfg.TPOffsetPerPct, cfg.TPOffsetCap)
	if sl <= 0 && tp <= 0 {
		return StopOffsets{}
	}
	// 止盈上浮**不破坏**"不得低于成本+费用"的约束：上浮只会把目标推得离成本更远，
	// 故无需额外下限钳制（owner 决策 5 的约束在此按构造满足）。
	return StopOffsets{SL: sl, TP: tp, Applied: true,
		Reason: fmt.Sprintf("大盘%.1f%%单日冲击,止损放宽%.1fpt/止盈上浮%.1fpt", in.IndexChangePct, sl, tp)}
}

// RiskBudgetScale 风险预算守恒系数（方案 §2.6 配套，owner 已裁决强制）。
//
// 止损 6% → 有效 9% 时，同金额下单笔最大亏损被悄悄放大 50%；故按 `原SL / 有效SL` 同比例缩仓
// （6/9 = 0.67）。收紧时有效止损变小 → 系数 >1，但**不放大仓位**（收紧本身已降低风险，不应借此加杠杆），
// 故只向下修正：返回值恒 ≤ 1。
// English: risk-budget conservation — loosening 6%→9% would silently inflate a single trade's max
// loss by 50%, so the position shrinks by baseSL/effectiveSL. Only downward: tightening never
// becomes a licence to size up.
func RiskBudgetScale(baseSLPct, offsetPct float64) float64 {
	if baseSLPct <= 0 {
		return 1.0
	}
	eff := baseSLPct + offsetPct
	if eff <= 0 {
		return 1.0 // 收紧到非正 = 配置异常，不参与
	}
	sc := baseSLPct / eff
	if sc > 1.0 {
		return 1.0 // 收紧不放杠杆
	}
	return sc
}
