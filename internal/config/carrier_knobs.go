// Package config 的载体侧形态旋钮（§BOX-FIX-20261123）。
//
// 本文件只放"QMT 载体与云端决策机都必须用同一个数"的策略旋钮，并各给一个
// 只读访问器。值只能有一个家：载体（QMT 内置策略 / 本地执行器）读的是烘进
// `local_executor_params.json` 的数，由 `cmd/derive_carrier_params` 从这里的访问器
// 取出、再由 `scripts/derive_carrier_params.py` 写进参数文件。任何一枚数在 Python
// 里再写一遍就是第二本账——改真源那天载体继续用旧值，而不会有任何东西会响
// （本仓反复锤过的双账形态，见 AGENTS「参数单点同源」）。
package config

// §BOX-FIX-20261123 载体形态旋钮的出厂值。
//
// 口径说明：
//   - 卖出观察窗（sell_confirm_rounds / sell_confirm_sec）：一个卖出触发必须连续命中
//     至少 2 轮、且持续至少 60 秒才允许真的发单。今日 000672 从 -3% 减半到 -6% 清仓
//     只隔 58 秒、000009 只因低于 MA5×0.96 一分钱就被清掉——都是"一跳清仓"。
//     观察窗只推迟**时机**，不改变方向与数量（RD 半仓 / SL 全清 / BK 可用量）。
//     云端 `exit_confirm_min=15` 是另一条腿（云端卖出）的口径，刻意不复用、不换算。
//   - 上限折扣（market_scale_ceiling_margin）：盒子自算的 market_scale 不得高于外脑
//     读数，且再让 10%（owner 2026-11-23 裁决用 71.5% 那一档：外脑 0.7946 × 0.9 =
//     0.7152）。外脑读数缺失/过期时盒子退回盲区帽（missing_key_target_cap），
//     所以断更只会更保守，不会放大敞口。
//   - 同票重发闸（buy_retry_improve_pct / buy_retry_max_per_day）：同一只票当日已经发过
//     买单之后，只有"价格真的往下走"才允许再发一次，且一天最多 3 次。今日 000088
//     （盐田港）从 13:23 到 13:54 以同一个 4.56 连发 5 笔、笔笔 600 秒被陈旧撤单、
//     笔笔零成交（当天最低 4.57），把 1.7~2 万资金锁了半小时——这就是这条闸要关掉的形态：
//     撤单释放当日买意向是对的（手动撤单必须能重来），但"价格没反转就照原价再发"不是。
const (
	carrierSellConfirmRounds        = 2
	carrierSellConfirmSec           = 60.0
	carrierMarketScaleCeilingMargin = 0.9
	// 再发一次必须比上一次的限价低这么多（百分比）。0.2% 在 4~10 元区间约等于"至少低一跳"，
	// 既不放过同价重发，也不会把正常回调挡在门外。
	carrierBuyRetryImprovePct = 0.2
	// 同一只票一天的买单次数上限（含第一次）。这是防"价格来回抖"的病态循环的硬底。
	carrierBuyRetryMaxPerDay = 3
)

// CarrierSellConfirmRounds 返回卖出观察窗的确认轮数（连续命中多少轮才放行）。
// English: rounds a sell trigger must hold before firing.
func CarrierSellConfirmRounds() int { return carrierSellConfirmRounds }

// CarrierSellConfirmSec 返回卖出观察窗的确认秒数（持续命中多少秒才放行）。
// English: seconds a sell trigger must hold before firing.
func CarrierSellConfirmSec() float64 { return carrierSellConfirmSec }

// CarrierMarketScaleCeilingMargin 返回盒子自算 market_scale 相对外脑读数的保守折扣。
// English: conservative discount applied to the cloud MarketScale ceiling.
func CarrierMarketScaleCeilingMargin() float64 { return carrierMarketScaleCeilingMargin }

// CarrierBuyRetryImprovePct 返回同票再发买单要求的最小价格改善（百分比）。
// English: minimum price improvement (%) required before re-buying the same symbol.
func CarrierBuyRetryImprovePct() float64 { return carrierBuyRetryImprovePct }

// CarrierBuyRetryMaxPerDay 返回同一只票当日买单次数上限（含第一次）。
// English: hard cap on buy attempts per symbol per trading day.
func CarrierBuyRetryMaxPerDay() int { return carrierBuyRetryMaxPerDay }

// §TP-LADDER-20261124 两段式止盈阶梯的出厂值（owner 2026-11-24 九条裁决）。
//
// 口径：旧载体只有一条 `take_profit_ratio=1.05` 的**全平**腿，命中即清仓、不看战法、
// 不看涨停、无观察窗（今天 002579/000678/000009 都在 +7%~+14% 被一刀切清掉）。
// 现在换成：Stage 0 按各战法自己的止盈点卖**一半**；Stage 1 剩余跟着**峰值**走，
// 回吐到线再清。没有战法止盈点的票（`fac_*` 占绝大多数）走"峰值回撤"两档。
//
// 单位（这里最容易出岔，别混）：
//   - `...TakeProfitPct` 与 `...GivebackPP`、`...PeakArmPct` 是**百分点**（30 表示 +30%）；
//   - `...RetrPct` 是**相对峰值价的百分比**（3 表示从最高价回落 3%，不是 3 个百分点）。
//
// 为什么有 TP 的票 Stage 1 用"百分点回吐"而不是"相对峰值价回撤"：owner 说的
// 「30% 掉到 25% 清」在相对峰值价口径下只有 3.85% 的回撤，永远够不到 5% 的清仓线——
// 两句话本来就是两种量纲，各用各的才对。
const (
	// 阶梯总闸。false 只停 TP/TT 两条腿（-6% 清仓、-3% 减仓、破位照旧），
	// 这是**异常止血**用的开关，不是灰度：本方案按 owner 令实盘直上，没有影子期。
	carrierTPLadderEnabled = true
	// Stage 1 的常量（规则层不许写字面量；0＝Stage 0 用 falsy 判，不需要第二个常量）。
	carrierTPStageHalf = 1
	// Stage 0 卖出的仓位比例（owner 裁决 2："卖一半"）。规则层不许写字面量，所以 0.5
	// 也必须有个家；与 -3% 减仓的 reduce_position_fraction 刻意分开——两个 0.5 是两条腿，
	// 调其中一条不该顺手改掉另一条。
	carrierTPHalfFraction = 0.5
	// 无战法止盈点的票启动阶梯所需的最低**峰值浮盈**（百分点）。0＝只要峰值站上成本即可。
	carrierTPPeakArmPct = 0.0
	// 10cm 主板：峰值回落 3% 卖一半、5% 清剩余（owner 裁决 2）。
	carrierTPTrailHalfRetrPct  = 3.0
	carrierTPTrailClearRetrPct = 5.0
	// 20cm 创业板/科创板：owner 裁决 6 给了清仓 −8%，卖半按 10cm 的 3:5 等比推成 5%
	// （owner 2026-11-24 复核："这个对"）。
	carrierTPTrailHalfRetrPct20  = 5.0
	carrierTPTrailClearRetrPct20 = 8.0
	// 30cm 北交所（4/8/92 段，复用载体既有的 t_bse_board_prefixes）：类推 6%/10%
	// （owner 2026-11-24 复核："这个也对"）。
	carrierTPTrailHalfRetrPct30  = 6.0
	carrierTPTrailClearRetrPct30 = 10.0
	// 有战法止盈点的票：Stage 1 允许从峰值浮盈回吐这么多**百分点**才清（30→25）。
	carrierTPGivebackPP = 5.0
	// 裁决 3：破位腿在有浮盈时只减半，不再整仓清（今天 000797 +2.2% 被 BK 全清是靶心）。
	carrierTPBreakdownTrimOnProfit = true
	// 裁决 7：当日已触发过止盈（TP/TT）的票，当天禁止再加仓/回买。
	carrierTPRebuyBlock = true
	// 裁决 9：龙头止盈点兜底 30%（与纸面的 dragon.TakeProfitPct 出厂 10 刻意不同——
	// 龙头是超短战法，实盘这条腿要的是"封板后别把利润全让回去"）。
	carrierTPDragonTakeProfitPct = 30.0
	// 裁决 9 + owner 2026-11-24 追加：龙头今日封过板、开板这么久仍不回封就清仓。
	// **不要求已过 30%**——「龙头很可能买入的时候已经是龙尾巴了，买入第二天可能就断板」。
	carrierTPDragonOpenNoResealSec = 900.0
)

// CarrierTPLadderEnabled 返回止盈阶梯总闸（异常止血用）。
// English: master switch for the two-stage take-profit ladder.
func CarrierTPLadderEnabled() bool { return carrierTPLadderEnabled }

// CarrierTPStageHalf 返回 Stage 1 的阶段编号。
// English: stage number that means "half already sold".
func CarrierTPStageHalf() int { return carrierTPStageHalf }

// CarrierTPHalfFraction 返回 Stage 0 卖出的仓位比例（0.5＝卖一半）。
// English: fraction of the position sold at the strategy take-profit point.
func CarrierTPHalfFraction() float64 { return carrierTPHalfFraction }

// CarrierTPPeakArmPct 返回无战法止盈点的票启动阶梯所需的最低峰值浮盈（百分点）。
// English: minimum peak P&L (points) before the retrace ladder may arm.
func CarrierTPPeakArmPct() float64 { return carrierTPPeakArmPct }

// CarrierTPTrailHalfRetrPct 返回 10cm 主板"卖一半"的峰值回撤（相对峰值价 %）。
// English: peak retrace (%) that triggers the half sale on main-board names.
func CarrierTPTrailHalfRetrPct() float64 { return carrierTPTrailHalfRetrPct }

// CarrierTPTrailClearRetrPct 返回 10cm 主板"清剩余"的峰值回撤（相对峰值价 %）。
// English: peak retrace (%) that clears the rest on main-board names.
func CarrierTPTrailClearRetrPct() float64 { return carrierTPTrailClearRetrPct }

// CarrierTPTrailHalfRetrPct20 返回 20cm 创业板/科创板"卖一半"的峰值回撤。
// English: half-sale peak retrace (%) on ChiNext/STAR names.
func CarrierTPTrailHalfRetrPct20() float64 { return carrierTPTrailHalfRetrPct20 }

// CarrierTPTrailClearRetrPct20 返回 20cm 创业板/科创板"清剩余"的峰值回撤。
// English: clear peak retrace (%) on ChiNext/STAR names.
func CarrierTPTrailClearRetrPct20() float64 { return carrierTPTrailClearRetrPct20 }

// CarrierTPTrailHalfRetrPct30 返回 30cm 北交所"卖一半"的峰值回撤。
// English: half-sale peak retrace (%) on Beijing Stock Exchange names.
func CarrierTPTrailHalfRetrPct30() float64 { return carrierTPTrailHalfRetrPct30 }

// CarrierTPTrailClearRetrPct30 返回 30cm 北交所"清剩余"的峰值回撤。
// English: clear peak retrace (%) on Beijing Stock Exchange names.
func CarrierTPTrailClearRetrPct30() float64 { return carrierTPTrailClearRetrPct30 }

// CarrierTPGivebackPP 返回有战法止盈点的票 Stage 1 允许的浮盈回吐（百分点）。
// English: peak-P&L give-back (points) allowed before clearing the rest.
func CarrierTPGivebackPP() float64 { return carrierTPGivebackPP }

// CarrierTPBreakdownTrimOnProfit 返回破位腿"有浮盈只减半"是否生效。
// English: should a profitable breakdown trim instead of clearing?
func CarrierTPBreakdownTrimOnProfit() bool { return carrierTPBreakdownTrimOnProfit }

// CarrierTPRebuyBlock 返回"当日止盈过的票禁止再买"是否生效。
// English: block same-day re-buys of a name that took profit today.
func CarrierTPRebuyBlock() bool { return carrierTPRebuyBlock }

// CarrierTPDragonTakeProfitPct 返回龙头战法的载体止盈点（百分点，账号显式值优先）。
// English: carrier-side dragon take-profit point (points); an explicit account value wins.
func CarrierTPDragonTakeProfitPct() float64 { return carrierTPDragonTakeProfitPct }

// CarrierTPDragonOpenNoResealSec 返回龙头开板不回封多久判清仓（秒）。
// English: seconds off the limit-up board before the dragon leg clears.
func CarrierTPDragonOpenNoResealSec() float64 { return carrierTPDragonOpenNoResealSec }

// CarrierTPDragonReturnTakeProfitPct 返回龙回头战法的止盈点（百分点）——转调账号级策略出厂表
// （`DragonReturn.TakeProfitPct` 是**小数** 0.25，这里只做单位换算 ×100，不产生新数）。
// English: dragon-return take-profit point; the strategy config stores 0.25, this is the same number as points.
func CarrierTPDragonReturnTakeProfitPct() float64 {
	return DefaultStrategyConfig().DragonReturn.TakeProfitPct * 100
}
