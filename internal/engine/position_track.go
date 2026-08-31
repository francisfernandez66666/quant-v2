// position_track.go 纸面开仓的止盈/止损映射（C3 遗产，阶段1.2 两本账合一后保留）：
// 模拟盘 fillLocked 成交后由 registry.paperMirror 经 SetMirror 回调写 report 持仓账，
// 本文件仅保留战法→止盈/止损百分比的映射函数供镜像使用（paper 为唯一真实账本，
// rpt 由镜像保持一致 → CheckPositionsExits 离场路径照常生效）。
//
// 各战法默认止盈止损：
//   - dragon（龙头）：止盈 10%，止损 8%（买入后回撤清仓阈值）
//   - double_bump（双响炮）：止盈 15%，止损 8%
//   - n_shape（N形）：止盈 10%，止损 8%（硬止损比例）
//   - dragon_return（龙回头）：止盈 25%，止损 5%
// English: TP/SL mapping for paper opens (C3 legacy, kept after the unified-book refactor): after a
// paper fillLocked, registry.paperMirror writes the report holding book via the SetMirror callback.
// Only the strategy→TP/SL percent mapping remains here for the mirror (paper is the single source of
// truth; rpt stays consistent via mirroring so CheckPositionsExits keeps working).
package engine

import (
	"quant-trading-v2/internal/config"
)

// paperOpenTpSl 返回某战法纸面开仓时的止盈/止损百分比（供持仓记录与百分比止盈止损提醒使用）。
// 单位统一为"百分数"（10=10%）；源配置为比例语义（<1，如 0.08=8%）时自动 ×100。
// 各战法缺省值与 internal/combat_agent/position_exits.go 的退出引擎默认保持一致。
// English: returns the take-profit / stop-loss percent for a strategy's paper open, in percent units
// (10 = 10%); ratio-style source values (<1, e.g. 0.08 = 8%) are scaled by 100. Defaults mirror the
// exit engines in internal/combat_agent/position_exits.go.
func paperOpenTpSl(strategyName string, sc *config.StrategyConfig) (tp, sl float64) {
	if sc == nil {
		sc = &config.StrategyConfig{}
	}
	toPct := func(v float64) float64 {
		if v > 0 && v < 1 {
			return v * 100
		}
		return v
	}
	switch strategyName {
	case "dragon":
		tp = toPct(sc.Dragon.TakeProfitPct)
		if tp <= 0 {
			tp = 10
		}
		sl = toPct(sc.Dragon.BuyPullbackSellAllPct)
		if sl <= 0 {
			sl = 8
		}
	case "double_bump":
		tp = toPct(sc.DoubleBump.DoubleBumpTakeProfitPct)
		if tp <= 0 {
			tp = 15
		}
		sl = 8
	case "n_shape":
		tp = 10
		sl = toPct(sc.NShape.HardStopLoss)
		if sl <= 0 {
			sl = 8
		}
	case "dragon_return":
		tp = toPct(sc.DragonReturn.TakeProfitPct)
		if tp <= 0 {
			tp = 25
		}
		sl = toPct(sc.DragonReturn.StopLossPct)
		if sl <= 0 {
			sl = 5
		}
	default:
		// 手动/未知战法：通用止盈止损
		tp, sl = 10, 8
	}
	return
}
