// position_exits.go 战法退出引擎的实时接线：把四个战法已实现的 CheckExit
// （移动止盈 / 分批止盈 / 尾盘强平 / 破位 / 超期 等）接到持仓实时评估上，
// 输出 清仓 / 减仓 / 提示 三级告警信号（仅提醒，不自动执行）。
// English: wires the four strategies' already-implemented CheckExit engines (trailing stop, staged
// take-profit, intraday close, breakdown, timeout…) onto live position evaluation, emitting
// 清仓/减仓/提示 (close/trim/notice) alert signals (reminder-only, never auto-executed).
package combat_agent

import (
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/indicator"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
)

// normalizePctForExit 把配置中"比例"阈值的字段换算为战法退出引擎使用的"百分数"口径。
// 配置按比例存储（0.05=5%），而退出引擎按百分数比较（-5）；值 >= 1 视为已是百分数原样保留。
// English: converts a config ratio threshold (0.05=5%) to the percent scale the exit engines compare
// against (e.g. -5); values >= 1 are assumed to already be percent and kept as-is.
func normalizePctForExit(v float64) float64 {
	if v > 0 && v < 1 {
		return v * 100
	}
	return v
}

// exitConfigs 由 Agent 持有的原始策略配置派生一份"百分数口径"副本供退出引擎使用，不改动原始配置。
// 涉及按百分比比较的阈值：龙头 回撤/炸板/收盘/开盘 阈值、龙回头 止损/移动回撤、双凸 止盈比例。
// N 形硬止损为比例语义（0.08=-8%），直接透传。调用方持读锁由本方法内部加锁。
// English: derives percent-scaled copies of the strategy configs for the exit engines without mutating
// the originals — dragon drawdown/breaker/close/open thresholds, dragon-return stop-loss/trailing and
// double-bump take-profit are percent-compared; n-shape hard stop stays ratio-based and passes through.
func (a *Agent) exitConfigs() (dragonCfg config.DragonConfig, dbCfg config.DoubleBumpConfig, nsCfg config.NShapeConfig, drCfg config.DragonReturnConfig) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cfg := a.strategyCfg
	if cfg == nil {
		return // 无配置时返回零值，调用方按不触发处理
	}
	// 拷贝后把各战法「百分比语义」阈值统一规范化为小数（除 n_shape 保持比例原值）
	dragonCfg = cfg.Dragon
	dragonCfg.BuyPullbackSellAllPct = normalizePctForExit(dragonCfg.BuyPullbackSellAllPct)
	dragonCfg.BuyPullbackSellHalfPct = normalizePctForExit(dragonCfg.BuyPullbackSellHalfPct)
	dragonCfg.BreakerSellHalfPct = normalizePctForExit(dragonCfg.BreakerSellHalfPct)
	dragonCfg.BreakerSellAllPct = normalizePctForExit(dragonCfg.BreakerSellAllPct)
	dragonCfg.BuyDayCloseBelow = normalizePctForExit(dragonCfg.BuyDayCloseBelow)
	dragonCfg.NextOpenIfBelow = normalizePctForExit(dragonCfg.NextOpenIfBelow)

	dbCfg = cfg.DoubleBump
	dbCfg.DoubleBumpTakeProfitPct = normalizePctForExit(dbCfg.DoubleBumpTakeProfitPct)

	nsCfg = cfg.NShape

	drCfg = cfg.DragonReturn
	drCfg.StopLossPct = normalizePctForExit(drCfg.StopLossPct)
	drCfg.TrailingDrawback = normalizePctForExit(drCfg.TrailingDrawback)
	return
}

// exitStrategy 持仓可接入的战法退出引擎枚举。
// English: the strategy exit engines a position may map onto.
type exitStrategy int

// 持仓可接入的战法退出引擎枚举。
const (
	exitStrategyGeneric      exitStrategy = iota // 手动/未知战法：走通用移动止盈+超期回退
	exitStrategyDragon                           // 龙头
	exitStrategyDoubleBump                       // 双响炮
	exitStrategyNShape                           // N 形超短
	exitStrategyDragonReturn                     // 龙回头(中线)
)

// classifyExitStrategy 把持仓记录的 Strategy 字符串归一化为退出引擎枚举。
// 兼容信号类型常量（dragon/double_bump/n_shape/dragon_return）与中文名（龙头/双响炮/N形/龙回头）。
// English: normalizes a position's Strategy string to an exit-engine enum, accepting both signal-type
// constants and Chinese labels.
func classifyExitStrategy(s string) exitStrategy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dragon", "dragon(龙头)", "龙头":
		return exitStrategyDragon
	case "double_bump", "doublebump", "双凸", "双响炮":
		return exitStrategyDoubleBump
	case "n_shape", "nshape", "n形", "n", "N形":
		return exitStrategyNShape
	case "dragon_return", "dragonreturn", "龙回头", "回":
		return exitStrategyDragonReturn
	}
	return exitStrategyGeneric
}

// toStrategyKLine 把 data.KLine 简化为退出引擎使用的 strategy.KLine。
// English: trims a data.KLine into the strategy.KLine shape the exit engines consume.
func toStrategyKLine(in []data.KLine) []strategy.KLine {
	if len(in) == 0 {
		return nil
	}
	out := make([]strategy.KLine, len(in))
	for i, k := range in {
		out[i] = strategy.KLine{Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume}
	}
	return out
}

// atr14Last 计算日K的 ATR14 末值；K线不足 14 根时返回 0（供信号/退出上下文注入 ATR）。
// English: computes the trailing ATR14 value from daily bars, or 0 when fewer than 14 bars exist
// (feeds ATR into signals and exit contexts).
func atr14Last(klines []data.KLine) float64 {
	if len(klines) < 14 {
		return 0
	}
	hs := make([]float64, len(klines))
	ls := make([]float64, len(klines))
	cs := make([]float64, len(klines))
	for i, k := range klines {
		hs[i], ls[i], cs[i] = k.High, k.Low, k.Close
	}
	atrs := indicator.ATR(hs, ls, cs, 14)
	if v := atrs[len(atrs)-1]; v > 0 {
		return v
	}
	return 0
}

// buildExitContext 由持仓与实时行情构造退出评估上下文，并把持久化的阶段最高价合入 EntryMeta，
// 同时注入 C4 ATR 动态止损参数（ATR14 + 倍数）。日K不足（<14根）时 ATR=0 → 回退固定百分比。
// English: builds the exit-evaluation context from a position and live quote, folding the persisted
// stage high into EntryMeta and injecting the C4 ATR dynamic-stop params (ATR14 + multiplier). With too
// few daily bars (<14) ATR stays 0 so fixed-percentage stops are used.
func buildExitContext(pos report.ExecLog, price float64, dayK []data.KLine, now time.Time) *strategy.ExitContext {
	stageHigh := pos.HighestPrice
	if stageHigh <= 0 {
		stageHigh = pos.EntryPrice // 无阶段高点时回退开仓价
	}
	if price > stageHigh {
		stageHigh = price // 现价创新高则同步抬升
	}
	// 拷贝持仓 EntryMeta，并把阶段高点注入供止损/移动止盈计算
	meta := make(map[string]float64, len(pos.EntryMeta)+1)
	for k, v := range pos.EntryMeta {
		meta[k] = v
	}
	meta["highest_price"] = stageHigh
	return &strategy.ExitContext{
		Code:      pos.Code,
		Name:      pos.Name,
		CostPrice: pos.EntryPrice,
		CurPrice:  price,
		EntryAt:   pos.EntryAt.Format("2006-01-02"),
		EntryMeta: meta,
		DailyK:    toStrategyKLine(dayK),
		Now:       now,
	}
}

// buildExitContextWithATR 在 buildExitContext 基础上注入 ATR 动态止损（C4）。
// 有 ≥14 根日K时用 ATR14 末值；否则 ATR=0（回退固定百分比）。
// English: extends buildExitContext with the C4 ATR dynamic stop — uses the trailing ATR14 value when
// ≥14 daily bars exist, else ATR=0 (fixed-percentage fallback).
func buildExitContextWithATR(pos report.ExecLog, price float64, dayK []data.KLine, now time.Time, enabled bool, mult float64) *strategy.ExitContext {
	ctx := buildExitContext(pos, price, dayK, now)
	if !enabled || mult <= 0 || len(dayK) < 14 {
		return ctx
	}
	if v := atr14Last(dayK); v > 0 {
		ctx.ATR = v
		ctx.ATRStopMult = mult
	}
	return ctx
}

// SellAction 卖出信号强度归一（阶段1.1 打通卖出链路的统一词汇表）：
// 把各告警源（战法退出引擎 / 止盈止损提醒 / 情绪退潮 / 利空归因 / 卖点评估）的
// Action+AlertType 归一为模拟盘可执行的卖出动作：
//   - "close"：全仓卖出 —— 退出引擎 P1 清仓、硬止盈（Action=止盈）、硬止损（Action=止损）
//   - "trim" ：半仓减仓 —— 退出引擎 P2 减仓、情绪退潮减仓、利空归因抛售、卖点评估减仓级
//   - ""     ：不动作 —— 提示/关注/跌幅提醒等仅提醒级，以及做空方向词（非卖出语义）
//
// 判定以 AlertType 优先（退出引擎/情绪/利空归因均用中文 AlertType），Action 兜底
// （止盈/止损提醒的硬级别直接体现在 Action）。软降级（提示/关注）不会误判：
// 它们的 AlertType 与 Action 都不在命中集。
// English: normalizes sell-signal strength across alert sources into a paper-executable action —
// "close" for full exits (P1 清仓, hard take-profit, hard stop-loss), "trim" for half-position trims
// (P2 减仓, emotion-retreat trim, bearish-attribution sell, sell-point 减仓 level), "" for reminder-only
// levels and the short-direction badge. AlertType is checked first with Action as fallback; soft
// downgrades (提示/关注) never match.
func SellAction(s Signal) string {
	if s.Direction == "做空" {
		return "" // 做空方向词是开仓方向语义，不是卖出提醒
	}
	switch s.AlertType {
	case "清仓", "利空抛售":
		// FIX#13：利空归因命中持仓 → 直接清仓（用户决策：收到关联持仓的利空消息自动清仓，
		// 而非此前的半仓 trim——利空事件风险不对称，先避险优先）。
		// English: FIX#13 — bearish-attribution hits now close fully (user decision), not half-trim;
		// bearish events carry one-sided downside risk, so exiting first is the priority.
		return "close"
	case "减仓":
		return "trim"
	}
	switch s.Action {
	case "止盈", "止损":
		return "close"
	case "sell", "卖出":
		return "close"
	}
	return ""
}

// exitSignalFromResult 把战法退出结果映射为告警信号：P1=清仓（立即） P2=减仓 P3=提示。
// English: maps a strategy exit result to an alert signal — P1=清仓 (immediate), P2=减仓, P3=提示.
func exitSignalFromResult(pos report.ExecLog, price float64, res *strategy.ExitResult, now time.Time) Signal {
	alertType, action := "提示", "关注"
	switch res.Priority {
	case strategy.P1:
		alertType, action = "清仓", "卖出"
	case strategy.P2:
		alertType, action = "减仓", "卖出"
	case strategy.P3, strategy.P3_5, strategy.P4:
		alertType, action = "提示", "关注"
	}
	return Signal{
		ID:          seqID(),
		Code:        pos.Code,
		Name:        pos.Name,
		Strategy:    pos.Strategy,
		Direction:   "提醒",
		Action:      action,
		AlertType:   alertType,
		Price:       price,
		Confidence:  1.0,
		Reason:      res.Reason,
		GeneratedAt: now,
	}
}

// genericTrailingExit 手动/未知战法持仓的通用退出回退：移动止盈（从阶段高点回撤）＋ 超期强制离场。
// 止盈/止损按预设百分比仍由 CheckPositionAlerts 负责，这里补上"利润保护"能力。
// English: generic exit fallback for manual/unknown positions — a trailing stop from the stage high plus
// a timeout force-exit; threshold-based TP/SL stays with CheckPositionAlerts, this adds profit protection.
func genericTrailingExit(ctx *strategy.ExitContext, now time.Time) *strategy.ExitResult {
	return genericTrailingExitWith(ctx, now, 8, 15)
}

// genericTrailingExitWith 通用退出的参数化版本：trailPct=移动止盈回撤阈值(%)，
// maxHoldDays=超期天数。§P2-d 实盘接线：因子/形态持仓按规则级覆盖执行（见 ruleExitParamsFor）。
// English: parameterized generic exit — trailing threshold and max hold days injectable so
// factor/pattern positions honor their sweep-approved rule-level overrides.
func genericTrailingExitWith(ctx *strategy.ExitContext, now time.Time, trailPct float64, maxHoldDays int) *strategy.ExitResult {
	cost := ctx.CostPrice
	price := ctx.CurPrice
	if cost <= 0 || price <= 0 {
		return nil
	}
	stageHigh := cost
	if h, ok := ctx.EntryMeta["highest_price"]; ok && h > stageHigh {
		stageHigh = h
	}
	// 移动止盈：阶段高点回撤达阈值（且曾盈利）→ 减仓保护利润
	trail := (price - stageHigh) / stageHigh * 100
	if trail <= -trailPct && stageHigh > cost {
		return &strategy.ExitResult{Reason: "回撤止损(移动止盈)", Priority: strategy.P2}
	}
	// 超期：持仓超过上限未完成形态，强制离场提醒
	// §修复 FIX#10（2026-09-04）：持有天数改按交易日计——旧实现用自然日（now.Sub(entry).Hours()/24），
	// 周末/节假日全被算作持仓日：周五买入下周一即 3 天、跨长假隔周即判 8 天超期提前离场提醒。
	// 现以 data.AddTradingDays 从入场日推 maxHoldDays 个交易日作为截止日，仅当今天≥截止日才触发。
	// English: §FIX#10 — the hold-timeout now counts trading days, not calendar days. The old natural-day
	// math made weekends/holidays count as holding days (buy Friday → "3 days" by Monday, or a week-long
	// break → premature 8-day timeout alerts). The deadline is now entry + maxHoldDays trading days
	// (data.AddTradingDays), and the exit fires only once today ≥ that deadline.
	if ctx.EntryAt != "" {
		entryDate, err := time.Parse("2006-01-02", ctx.EntryAt)
		if err == nil {
			deadline := data.AddTradingDays(entryDate.Format("20060102"), maxHoldDays)
			today := data.TradingDayDate(now)
			if today >= deadline {
				return &strategy.ExitResult{Reason: "持仓超期离场", Priority: strategy.P3}
			}
		}
	}
	return nil
}

// CheckPositionsExits 对全部持仓逐只运行对应战法的退出引擎，返回退出告警信号。
// 与 CheckPositionAlerts（按预设百分比止盈/止损）互补：覆盖移动止盈、分批止盈、尾盘强平、
// 破位、量能派发、形态失败、超期等战法级卖点。仅提醒、不自动执行。
// 入参 quotes 为实时报价（code → 现价），dayKLines 为日K线（code → 历史日K，缺失则跳过需K线的检查），
// now 用于尾盘/超期判定。返回信号列表（空表示无退出建议）。
// English: runs each held position through its strategy's exit engine and returns exit alerts. It
// complements CheckPositionAlerts (percentage TP/SL) with trailing stops, staged take-profits, intraday
// close-outs, breakdowns, volume distribution, formation failures and timeouts. Reminder-only. quotes
// supply live prices, dayKLines supply daily bars (checks needing bars are skipped when absent), and now
// drives the intraday/timeout gates.
func (a *Agent) CheckPositionsExits(rpt *report.Report, quotes map[string]*data.StockInfo, dayKLines map[string][]data.KLine, now time.Time) []Signal {
	dragonCfg, dbCfg, nsCfg, drCfg := a.exitConfigs()

	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}

	var alerts []Signal
	for _, pos := range positions {
		// 实时报价缺失/现价无效（停牌）则跳过，避免以无效价误判退出
		// English: skip when the quote is missing or the price is invalid (suspended).
		q, ok := quotes[pos.Code]
		if !ok || q == nil || q.Price <= 0 {
			continue
		}
		price := q.Price

		// 阶段高点实时更新：仅当价格创新高时抬高并持久化（写盘节流，创新高频率天然低频）
		// English: raise the persisted stage high only on a new high (writes are naturally infrequent).
		if price > pos.HighestPrice {
			rpt.RaiseHighest(pos.SignalID, price)
		}

		atrOn, atrMult := a.atrStopParams()
		ctx := buildExitContextWithATR(pos, price, dayKLines[pos.Code], now, atrOn, atrMult)
		if ctx == nil {
			continue
		}

		var res *strategy.ExitResult
		switch classifyExitStrategy(pos.Strategy) {
		case exitStrategyDragon:
			res = dragon.CheckExit(ctx, &dragonCfg)
		case exitStrategyDoubleBump:
			res = double_bump.CheckExit(ctx, &dbCfg)
		case exitStrategyNShape:
			res = n_shape.CheckExit(ctx, &nsCfg)
		case exitStrategyDragonReturn:
			res = dragon_return.CheckExit(ctx, &drCfg)
		default:
			// §P2-d 实盘接线：因子/形态战法持仓优先用规则级出场覆盖（扫参审批后热生效）
			if ov := ruleExitParamsFor(pos.Strategy); ov != nil {
				trail := ov.trailPct
				if trail <= 0 {
					trail = 8
				}
				hold := ov.holdDays
				if hold <= 0 {
					hold = 15
				}
				res = genericTrailingExitWith(ctx, now, trail, hold)
			} else {
				res = genericTrailingExit(ctx, now)
			}
		}

		if res == nil {
			continue
		}
		alerts = append(alerts, exitSignalFromResult(pos, price, res, now))
	}

	if len(alerts) > 0 {
		log.Printf("[combat_agent] CheckPositionsExits: %d 持仓 → %d 退出提醒", len(positions), len(alerts))
	}
	return alerts
}

// emotionRetreatAlerts 情绪进入退潮/背离阶段时，对全部做多持仓给出减仓建议（仅提醒）。
// English: when the emotion cycle enters 退潮/背离 (retreat/divergence), advises trimming every long
// position (reminder-only).
func (a *Agent) EmotionRetreatAlerts(rpt *report.Report, quotes map[string]*data.StockInfo, phase string, now time.Time) []Signal {
	if phase != "退潮" && phase != "背离" {
		return nil
	}
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}
	var alerts []Signal
	for _, pos := range positions {
		if pos.Direction == "做空" {
			continue
		}
		price := pos.EntryPrice
		if q := quotes[pos.Code]; q != nil && q.Price > 0 {
			price = q.Price
		}
		alerts = append(alerts, Signal{
			ID:          seqID(),
			Code:        pos.Code,
			Name:        pos.Name,
			Strategy:    pos.Strategy,
			Direction:   "提醒",
			Action:      "卖出",
			AlertType:   "减仓",
			Price:       price,
			Confidence:  1.0,
			Reason:      fmt.Sprintf("情绪周期进入[%s],市场赚钱效应收缩,建议对持仓 %s(%s) 整体减仓控制风险", phase, pos.Name, pos.Code),
			GeneratedAt: now,
		})
	}
	return alerts
}

// BearishAttributionAlerts 利空归因持仓抛售提醒（E4）：对做多持仓逐只检查是否命中
// 本轮利空板块/利空个股（bearReasons: code → 归因说明），命中即独立于价格止损产出一条
// "利空归因 → 尽快抛掉"卖出提醒。与 CheckPositionAlerts 的价格止损解耦：只要该持仓被 8b
// 利空识别归因（如板块利空/利空事件/D1 负面传导），即便尚未跌破止损线也提醒避险。
// 已做空持仓忽略（利空对做空是顺向）。
// English: E4 bearish-attribution sell alerts — for each long holding, if it is hit by this round's
// bearish sector/stock signals (bearReasons: code → attribution), emit an independent "利空归因 →
// 尽快抛掉" sell reminder, decoupled from the price-based stop-loss in CheckPositionAlerts: a holding
// hit by 8b bearish attribution (sector bearishness / bearish event / D1-negative propagation) triggers
// even before its stop-loss line is breached. Short positions are ignored (bearish is their direction).
func (a *Agent) BearishAttributionAlerts(rpt *report.Report, quotes map[string]*data.StockInfo, bearReasons map[string]string, now time.Time) []Signal {
	if len(bearReasons) == 0 {
		return nil
	}
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}
	var alerts []Signal
	// 逐持仓检查利空归因命中：仅多头（跳过做空），用实时价覆盖入场价生成卖出提醒。
	for _, pos := range positions {
		if pos.Direction == "做空" {
			continue
		}
		reason, hit := bearReasons[pos.Code]
		if !hit {
			continue
		}
		price := pos.EntryPrice
		if q := quotes[pos.Code]; q != nil && q.Price > 0 {
			price = q.Price
		}
		alerts = append(alerts, Signal{
			ID:          seqID(),
			Code:        pos.Code,
			Name:        pos.Name,
			Strategy:    pos.Strategy,
			Direction:   "提醒",
			Action:      "卖出",
			AlertType:   "利空抛售",
			Price:       price,
			Confidence:  1.0,
			Reason:      "利空归因: " + reason + " → 建议尽快抛售该持仓以规避风险",
			GeneratedAt: now,
		})
	}
	if len(alerts) > 0 {
		log.Printf("[combat_agent] BearishAttributionAlerts: %d 持仓命中利空归因 → %d 抛售提醒", len(positions), len(alerts))
	}
	return alerts
}
