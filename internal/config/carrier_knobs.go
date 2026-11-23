// Package config 的载体侧形态旋钮（§BOX-FIX-20261123）。
//
// 本文件只放"QMT 载体与云端决策机都必须用同一个数"的三枚策略旋钮，并各给一个
// 只读访问器。值只能有一个家：载体（QMT 内置策略 / 本地执行器）读的是烘进
// `local_executor_params.json` 的数，由 `cmd/derive_carrier_params` 从这里的访问器
// 取出、再由 `scripts/derive_carrier_params.py` 写进参数文件。任何一枚数在 Python
// 里再写一遍就是第二本账——改真源那天载体继续用旧值，而不会有任何东西会响
// （本仓反复锤过的双账形态，见 AGENTS「参数单点同源」）。
package config

// §BOX-FIX-20261123 载体形态三枚旋钮的出厂值。
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
const (
	carrierSellConfirmRounds        = 2
	carrierSellConfirmSec           = 60.0
	carrierMarketScaleCeilingMargin = 0.9
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
