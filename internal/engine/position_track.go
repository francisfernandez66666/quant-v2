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
//
// English: TP/SL mapping for paper opens (C3 legacy, kept after the unified-book refactor): after a
// paper fillLocked, registry.paperMirror writes the report holding book via the SetMirror callback.
// Only the strategy→TP/SL percent mapping remains here for the mirror (paper is the single source of
// truth; rpt stays consistent via mirroring so CheckPositionsExits keeps working).
package engine

import (
	"quant-trading-v2/internal/config"
)

// paperOpenTpSl 返回模拟盘镜像开仓的统一止盈/止损百分比（单位：百分数）。
// §统一纪律 E：不再按战法各自默认（龙头10/8、龙回头25/5、动量10/8…），统一走
// rules.paper.discipline 总纪律（默认止盈+15/止损−6）；战法自带止盈止损降级为触发通知。
// ATR 动态止损仍由调用方（registry.paperMirror）覆盖，此函数只提供统一固定百分比。
// English: returns the unified take-profit/stop-loss percent for a paper open (percent units). Per-strategy
// defaults (dragon 10/8, dragon_return 25/5, momentum 10/8…) are replaced by the unified discipline from
// rules.paper.discipline (default +15/−6); strategy-native TP/SL degrade to notification-only. The ATR
// dynamic stop still overrides at the caller (registry.paperMirror).
func paperOpenTpSl(disc config.DisciplineConfig) (tp, sl float64) {
	tp = disc.TakeProfitPct
	if tp <= 0 {
		tp = 15
	}
	sl = disc.StopLossPct
	if sl <= 0 {
		sl = 6
	}
	return
}
