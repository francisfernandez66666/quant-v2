// position_track.go 买入信号自动纸面开仓（C3）：把 buy 信号写入持仓记录（报告持久化），
// 使 CheckPositionsExits 离场路径（止盈/止损/炸板/超期提醒）真正生效，避免离场逻辑沦为死代码。
// 仅做纸面记录（不真实下单）；是否启用由 PositionConfig.AutoTrackSignals 控制（默认开）。
// English: auto-paper-opens positions from buy signals (C3). Writing the buy into the persisted
// holding log activates the CheckPositionsExits exit path (take-profit / stop-loss / broken-seal /
// timeout alerts), so it is no longer dead code. Paper-only (never really orders); gated by
// PositionConfig.AutoTrackSignals (default on).
package engine

import (
	"log"
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/combat_agent"
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

// paperOpenQty C6 仓位管理：按置信度 + 止损距离计算纸面开仓数量（股数）。
// 以"8% 固定止损"为基准单位风险：qty = 10 × 置信度 × (8 / 止损%)。
// 置信度越高、止损越窄（单位风险越大）→ 数量越多，使各持仓单位风险一致。
// English: C6 position sizing — opening quantity from confidence and stop distance: qty = 10 ×
// confidence × (8 / stop%), normalizing per-unit risk so tighter stops carry more shares.
func paperOpenQty(confidence, stopPct float64) float64 {
	if confidence <= 0 {
		confidence = 0.5
	}
	if stopPct <= 0 {
		stopPct = 8
	}
	return math.Round(10*confidence*(8/stopPct)*100) / 100
}

// paperOpenBuy 对一个买入选中的信号做纸面开仓（幂等：已持仓的代码跳过）。
// 返回是否真正开仓（用于日志）。meta 里为 dragon 补 limit_price（炸板回落基准=买入触发价）；
// 数量按 C6 置信度+ATR 止损距离计算（ATR 有效时优先，否则回退固定止损）。
// English: paper-opens a position for one buy-selected signal (idempotent: codes already held are
// skipped). Returns whether a position was actually opened. limit_price (the broken-seal baseline) is
// filled for dragon from the trigger price; the quantity is sized by C6 confidence + ATR stop distance
// (ATR wins when valid, else the fixed stop).
func (e *Engine) paperOpenBuy(sig combat_agent.Signal) bool {
	if e.rpt == nil || sig.Code == "" || sig.Price <= 0 {
		return false
	}
	if e.rpt.HasHolding(sig.Code) {
		return false
	}
	tp, sl := paperOpenTpSl(sig.Strategy, e.strategyConfig())
	if sig.ATR > 0 {
		if mult := e.atrStopMult(); mult > 0 {
			if atrSl := sig.ATR * mult / sig.Price * 100; atrSl > 0 {
				sl = atrSl
			}
		}
	}
	qty := paperOpenQty(sig.Confidence, sl)
	meta := map[string]float64{}
	if sig.Strategy == "dragon" {
		meta["limit_price"] = sig.Price
	}
	id := sig.ID
	if id == "" {
		id = sig.Code + "@auto"
	}
	e.rpt.LogSignalWithMetaQty(id, sig.Code, sig.Name, "做多", sig.Strategy, sig.Price, tp, sl, qty, meta)
	log.Printf("[engine] 纸面开仓 %s(%s) 战法:%s 数量%.0f 止盈%.0f%% 止损%.0f%%", sig.Name, sig.Code, sig.Strategy, qty, tp, sl)
	return true
}

// atrStopMult 读取当前账号 ATR 动态止损倍数（C6 仓位管理的止损距离来源；未启用时为 0）。
// English: reads the account's ATR stop multiplier (source of the C6 sizing stop distance; 0 when off).
func (e *Engine) atrStopMult() float64 {
	e.mu.RLock()
	cfgMgr, userID := e.cfgMgr, e.userID
	e.mu.RUnlock()
	if cfgMgr == nil {
		return 0
	}
	pos := cfgMgr.GetRulesFor(userID).Position
	if !pos.ATREnabled {
		return 0
	}
	return pos.ATRStopMult
}

// strategyConfig 读取当前账号的策略配置（nil 防护，供纸面开仓止盈/止损计算）。
// English: reads the current account's strategy config (nil-safe), used for paper-open TP/SL.
func (e *Engine) strategyConfig() *config.StrategyConfig {
	e.mu.RLock()
	cfgMgr, userID := e.cfgMgr, e.userID
	e.mu.RUnlock()
	if cfgMgr == nil {
		return nil
	}
	return cfgMgr.GetStrategyConfigFor(userID)
}

// autoTrackEnabled 读取持仓自动纸面开仓开关（缺省开）。
// English: reads the auto-paper-open switch (defaults on).
func (e *Engine) autoTrackEnabled() bool {
	e.mu.RLock()
	cfgMgr, userID := e.cfgMgr, e.userID
	e.mu.RUnlock()
	if cfgMgr == nil {
		return true
	}
	cfg := cfgMgr.GetRulesFor(userID)
	if cfg == nil || !cfg.Position.AutoTrackSignals {
		return false
	}
	return true
}