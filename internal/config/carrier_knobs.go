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
	// 无战法止盈点的票启动阶梯所需的最低**峰值浮盈**（百分点）。
	// 0＝只要峰值站上成本即可；D1 生效前的 0 会让峰值只涨 1.5% 的票武装（11-25 现场：
	// 000415/000680/000735 峰值 +1.49/+1.47/+2.88 个点，在浮亏 2.6~3.8 个点时被卖半）。
	// 3.0 与最低档 half_retr（10cm 的 3%）对齐：D1 生效后它在三种板宽上都不 binding，
	// 但写 3 能让"回撤 3% 卖一半"这条口径在参数文件里看得见，且将来若 D1 判据被放宽，
	// 它还会是一条独立的锁（写 0 就不会）。
	carrierTPPeakArmPct = 3.0
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

// §SIDE-POLICY-20261124 熊市左右侧比例与牛市镜像剧本的载体出厂旋钮。
// 数值只在此处定义，载体规则层必须从 PARAMS 读；缺键时按方案弃权，不写兜底常量。
const (
	carrierSidePolicyEnabledDefault  = false
	carrierSideFlowStrongDelta       = 0.50
	carrierSideFlowSlopeStep         = 0.15
	carrierSideFlowPMDelta           = 0.20
	carrierSideGapHighPct            = 1.00
	carrierSideGapLowPct             = -1.00
	carrierSidePMIndexDropPct        = -1.00
	carrierSidePMIndexRisePct        = 1.00
	carrierSideBearTargetL1          = 0.85
	carrierSideBearTargetL2          = 0.70
	carrierSideBullTargetL1          = 1.05
	carrierSideBullTargetL2          = 1.10
	carrierSideBullTargetL3          = 1.15
	carrierSideWeightLo              = 0.80
	carrierSideWeightHi              = 1.20
	carrierSideLeftScoreFloor        = 80.0
	carrierSideLeftScoreMargin       = 10.0
	carrierSideLeftStableRounds      = 2
	carrierSideLeftStableGapSec      = 60.0
	carrierSideLeftStableMaxDrop     = 2.0
	carrierSidePMCheckFrom           = "13:00"
	carrierSidePMBlockUntil          = "14:00"
	carrierSideBucketMinutes         = 30
	carrierSidePolicyTTLSec          = 2700.0
	carrierSideFreshnessHeartbeatSec = 1200.0
	// D6-a 2026-11-25：极强右侧回买。margin 是**分数点**（阈值 + 15 分），不是百分比。
	// owner 2026-11-25 裁决「极强选 A，默认开」⇒ On = true（显式写 false 才是退路）。
	// 它只解 tp_rebuy_block 这一条：same_symbol_day 与 bear L1 的 side_policy_no_new
	// 都排在它外面/后面，穿透不出去（§239 有负锁）。
	carrierSideRightRebuyOn     = true
	carrierSideRightRebuyMargin = 15.0
	// D7 2026-11-25：动态目标的分时释放阶梯。owner 2026-11-25 裁决「默认开」⇒ On = true。
	// 数值全是「MS 派生目标的百分比」，单位是倍率（0.70 = 70%），不是百分数。
	// full_at 之后一律 1.0；pm_check_at 之前走 AM 档；两者之间 latch 活跃则保持 AM 档。
	carrierSideRampOn     = true
	carrierSideRampBullAM = 0.70
	carrierSideRampBullPM = 1.00
	// 2026-11-25 owner 修正：开盘默认熊市只放 30% 动态仓位（原 0.35）。
	carrierSideRampBearAM    = 0.30
	carrierSideRampBearPM    = 0.50
	carrierSideRampPMCheckAt = "13:00"
	carrierSideRampFullAt    = "14:00"
)

// CarrierSidePolicyEnabledDefault 返回侧策略总闸的出厂值；账号 runtime 显式值优先。
func CarrierSidePolicyEnabledDefault() bool { return carrierSidePolicyEnabledDefault }

// CarrierSideFlowStrongDelta 返回强净流入/强净流出的归一阈值。
func CarrierSideFlowStrongDelta() float64 { return carrierSideFlowStrongDelta }

// CarrierSideFlowSlopeStep 返回每 30 分钟斜率触发档。
func CarrierSideFlowSlopeStep() float64 { return carrierSideFlowSlopeStep }

// CarrierSideFlowPMDelta 返回 13:00 累计净流入强度变化阈值。
func CarrierSideFlowPMDelta() float64 { return carrierSideFlowPMDelta }

// CarrierSideGapHighPct 返回 09:30 高开阈值（百分点）。
func CarrierSideGapHighPct() float64 { return carrierSideGapHighPct }

// CarrierSideGapLowPct 返回 09:30 低开阈值（百分点）。
func CarrierSideGapLowPct() float64 { return carrierSideGapLowPct }

// CarrierSidePMIndexDropPct 返回 13:00 指数大跌阈值（百分点）。
func CarrierSidePMIndexDropPct() float64 { return carrierSidePMIndexDropPct }

// CarrierSidePMIndexRisePct 返回牛市镜像的指数大涨确认阈值（百分点）；只分档，不新增准入硬闸。
func CarrierSidePMIndexRisePct() float64 { return carrierSidePMIndexRisePct }

// CarrierSideBearTargetL1 返回 bear 收紧第一档目标乘子。
func CarrierSideBearTargetL1() float64 { return carrierSideBearTargetL1 }

// CarrierSideBearTargetL2 返回 bear 收紧第二档目标乘子。
func CarrierSideBearTargetL2() float64 { return carrierSideBearTargetL2 }

// CarrierSideBullTargetL1 返回 bull 放宽第一档目标乘子。
func CarrierSideBullTargetL1() float64 { return carrierSideBullTargetL1 }

// CarrierSideBullTargetL2 返回 bull 放宽第二档目标乘子。
func CarrierSideBullTargetL2() float64 { return carrierSideBullTargetL2 }

// CarrierSideBullTargetL3 返回 bull 放宽第三档目标乘子。
func CarrierSideBullTargetL3() float64 { return carrierSideBullTargetL3 }

// CarrierSideWeightLo 返回最终单笔权重下限。
func CarrierSideWeightLo() float64 { return carrierSideWeightLo }

// CarrierSideWeightHi 返回最终单笔权重上限。
func CarrierSideWeightHi() float64 { return carrierSideWeightHi }

// CarrierSideLeftScoreFloor 返回强左侧信号分数地板。
func CarrierSideLeftScoreFloor() float64 { return carrierSideLeftScoreFloor }

// CarrierSideLeftScoreMargin 返回强左侧超过规则阈值的最小分差。
func CarrierSideLeftScoreMargin() float64 { return carrierSideLeftScoreMargin }

// CarrierSideLeftStableRounds 返回强左侧连续观察轮数。
func CarrierSideLeftStableRounds() int { return carrierSideLeftStableRounds }

// CarrierSideLeftStableGapSec 返回强左侧最小观察间隔（秒）。
func CarrierSideLeftStableGapSec() float64 { return carrierSideLeftStableGapSec }

// CarrierSideLeftStableMaxDrop 返回强左侧连续观察允许的最大分数回落。
func CarrierSideLeftStableMaxDrop() float64 { return carrierSideLeftStableMaxDrop }

// CarrierSidePMCheckFrom 返回午后检查起点。
func CarrierSidePMCheckFrom() string { return carrierSidePMCheckFrom }

// CarrierSidePMBlockUntil 返回 no-new latch 终点。
func CarrierSidePMBlockUntil() string { return carrierSidePMBlockUntil }

// CarrierSideBucketMinutes 返回资金流固定桶长（分钟）。
func CarrierSideBucketMinutes() int { return carrierSideBucketMinutes }

// CarrierSidePolicyTTLSec 返回进攻态新鲜度 TTL（秒）。
func CarrierSidePolicyTTLSec() float64 { return carrierSidePolicyTTLSec }

// CarrierSideFreshnessHeartbeatSec 返回进攻态新鲜度心跳（秒）。
func CarrierSideFreshnessHeartbeatSec() float64 { return carrierSideFreshnessHeartbeatSec }

// CarrierSideRightRebuyEnabled 返回 D6-a 极强右侧回买总闸（缺省开，显式 false 才是退路）。
// English: master switch for the strong-right-entry rebuy escape hatch (D6-a).
func CarrierSideRightRebuyEnabled() bool { return carrierSideRightRebuyOn }

// CarrierSideRightRebuyMargin 返回 D6-a 的"极强"分差（**分数点**，不是百分比）。
// English: score margin above the rule threshold that counts as "extremely strong".
func CarrierSideRightRebuyMargin() float64 { return carrierSideRightRebuyMargin }

// CarrierSideRampEnabled 返回 D7 分时释放总闸（缺省开，显式 false 才是退路）。
// English: master switch for the time-of-day release ramp (D7).
func CarrierSideRampEnabled() bool { return carrierSideRampOn }

// CarrierSideRampBullAM 返回牛市上午档（MS 派生目标的百分比，倍率）。
// English: bull morning ramp factor (fraction of the MS-derived target).
func CarrierSideRampBullAM() float64 { return carrierSideRampBullAM }

// CarrierSideRampBullPM 返回牛市 13:00 无坏信号后的档位。
// English: bull post-13:00 factor when no bad signal is latched.
func CarrierSideRampBullPM() float64 { return carrierSideRampBullPM }

// CarrierSideRampBearAM 返回熊市上午档。
// English: bear morning ramp factor.
func CarrierSideRampBearAM() float64 { return carrierSideRampBearAM }

// CarrierSideRampBearPM 返回熊市 13:00 无坏信号后的档位（上午档 + 15 个点）。
// English: bear post-13:00 factor when no bad signal is latched.
func CarrierSideRampBearPM() float64 { return carrierSideRampBearPM }

// CarrierSideRampPMCheckAt 返回午后检查起点（HH:MM）。
// English: afternoon check start time.
func CarrierSideRampPMCheckAt() string { return carrierSideRampPMCheckAt }

// CarrierSideRampFullAt 返回无条件满额时刻（HH:MM）。
// English: time after which the ramp is 1.0 unconditionally.
func CarrierSideRampFullAt() string { return carrierSideRampFullAt }

// §T-LEG-F3-20261125 上市天数闸的自动切换旋钮（owner 2026-11-25 裁决「先 A 后 B」）。
//
// A 档 = 闸显式关（floor 0），B 档 = 闸开（floor = t_min_listed_days）。
// 切换判据是**数据覆盖**：`list_date` 是静态字段，补一次就永远对，
// 所以"观察期"在数据侧没有意义（方案 §10 F3）。覆盖读不到 ⇒ 留在 A 档并在
// 读数里点名（`off(auto cov=na)`）——看不见不等于放行。
const (
	carrierTListedDaysAuto           = true
	carrierTListedDaysCoverageMinPct = 95.0
)

// CarrierTListedDaysAuto 返回上市天数闸是否按数据覆盖自动切换（缺省开）。
// English: whether the listed-days gate is switched by data coverage.
func CarrierTListedDaysAuto() bool { return carrierTListedDaysAuto }

// CarrierTListedDaysCoverageMinPct 返回切到 B 档所需的最小 list_date 覆盖率（%）。
// English: minimum list_date coverage (percent) required to enable the gate.
func CarrierTListedDaysCoverageMinPct() float64 { return carrierTListedDaysCoverageMinPct }
