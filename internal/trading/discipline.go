// discipline.go — 统一止盈止损纪律（探针 + 扳机）裁决引擎。
// 实盘与模拟盘共用同一套口径：探针(5s)实时扫描是否触及判定线；触发后进入固定观察窗
// （不滚动重置），窗内持续有同向信号 → 跟随；窗结算仍无信号 → 按判定线离场。
//
// 判定线（全部后台可配，internal/config.DisciplineConfig）：
//   - 止损线 −StopLossPct：浮亏达此值触发止损判定
//   - 止盈线 +TakeProfitPct：盈利达此值触发止盈判定
//   - 移动止盈：突破止盈线后按 最高价 − MaxPullbackPct 动态上移；利润回落到 +15−(−6)=+21 必触发
//   - 深破兜底：浮亏达 StopLossPct×DeepStopMult（默认 −12）触发深破判定，同样走观察窗
//
// 触发后：窗口内有同向信号 → 延持；无 → 全平（止损/止盈）或半平（减仓类）。
//
// §P0-C（2026-09-22 傍晚批，owner 裁决 2 = B 最小止血）：延持态（Settled 且未 Confirmed）
// 过去在函数顶部被早退吞掉，一次延持即**终态失明**——该持仓之后每轮都不再被任何判定线评估
// （止损/止盈/深破全盲），资金级漏卖。现摘掉该早退：延持持仓回到逐轮判定线评估轨道，
// 结算点与延持语义本身不动（那是 §SELLPOINT 未批准的策略改动）。切闸（qmt.sell_unified_mode=on）
// 与旧五路退役不在本批范围。
// English: §P0-C(B) — an extended-hold position used to be swallowed by the early return at the
// top of probeDiscipline, which made it permanently un-assessed by every discipline line.
package trading

import (
	"math"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// DisciplineLine 本持仓当前命中的判定线类型。
type DisciplineLine int

const (
	// LineNone 未命中任何判定线。
	LineNone DisciplineLine = iota
	// LineStopLoss 命中止损线（浮亏 ≥ StopLossPct）。
	LineStopLoss
	// LineTakeProfit 命中止盈线（盈利 ≥ TakeProfitPct）。
	LineTakeProfit
	// LineTrail 命中移动止盈线（突破止盈线后从最高价回撤 ≥ MaxPullbackPct）。
	LineTrail
	// LineDeepBreach 命中深破兜底（浮亏 ≥ StopLossPct×DeepStopMult）。
	LineDeepBreach
)

// DisciplineAction 裁决输出动作。
type DisciplineAction int

const (
	// ActionHold 无动作（未命中 / 窗口内有信号继续持有）。
	ActionHold DisciplineAction = iota
	// ActionClose 全仓离场（止损/止盈/深破，窗结算无信号）。
	ActionClose
	// ActionTrim 减半仓（首触止损线未深破且无反向确认）。
	ActionTrim
	// ActionConfirm 已命中判定线，进入观察窗（等待窗结算）。
	ActionConfirm
)

// String 纪律动作的稳定字符串形态（落库/日志用）。
func (a DisciplineAction) String() string {
	switch a {
	case ActionClose:
		return "close"
	case ActionTrim:
		return "trim"
	case ActionConfirm:
		return "confirm"
	default:
		return "hold"
	}
}

// PositionState 单只持仓的纪律状态机（跨探针轮次保持）。
type PositionState struct {
	Code        string
	Line        DisciplineLine // 首次命中的判定线（锁定，不随后续价格波动滚动）
	FirstTouch  time.Time      // 首次命中时刻
	SettleStart time.Time      // 观察窗起点（对齐到 15min K 边界）
	WindowMin   int            // 观察窗分钟数（止损/止盈 15，移动止盈 trail_confirm_min）
	Settled     bool           // 是否已结算
	Confirmed   bool           // 窗结算时已确认（无信号 → 离场）
	HighPrice   float64        // 移动止盈用的持仓最高价（跟踪）
}

// Decision 一次探针的单持仓裁决结果。
type Decision struct {
	Code   string
	Action DisciplineAction
	Line   DisciplineLine
	PnlPct float64
	Reason string
	Signal *combat_agent.Signal // 窗结算无信号时，产出离场信号（供 autoSell）
}

// probeforDiscipline 纯裁决函数：对单个持仓，按当前价、信号、时间与配置返回动作。
// st 为持仓的纪律状态（nil 时新建）；返回更新后的状态。hasBull = 该股当前是否有做多信号
// （止盈判定用：有同向信号则延持）；hasBear = 是否有做空/利空信号（止损判定用：有则硬清）。
func probeDiscipline(st *PositionState, code string, entryPrice, highPrice, curPrice float64, hasBull, hasBear bool, now time.Time, cfg config.DisciplineConfig) (PositionState, *Decision) {
	sl := cfg.StopLossPct
	if sl <= 0 {
		sl = 6
	}
	tp := cfg.TakeProfitPct
	if tp <= 0 {
		tp = 15
	}
	pb := cfg.MaxPullbackPct
	if pb <= 0 {
		pb = 6
	}
	dm := cfg.DeepStopMult
	if dm <= 0 {
		dm = 2
	}
	exitMin := cfg.ExitConfirmMin
	if exitMin <= 0 {
		exitMin = 15
	}
	trailMin := cfg.TrailConfirmMin
	if trailMin <= 0 {
		trailMin = 45
	}

	if st == nil {
		st = &PositionState{Code: code, HighPrice: highPrice}
	}
	if curPrice > 0 && curPrice > st.HighPrice {
		st.HighPrice = curPrice
	}
	if entryPrice <= 0 || curPrice <= 0 {
		// 无有效价，无法判定（停牌/行情缺失）——保留状态，不下结论。
		return *st, nil
	}
	pnl := (curPrice - entryPrice) / entryPrice * 100

	// 已确认离场（Settled && Confirmed）：短路重放同一处置单，语义与改动前逐字一致。
	if st.Settled && st.Confirmed {
		reason := "止损离场"
		switch st.Line {
		case LineTakeProfit, LineTrail:
			reason = "止盈离场"
		case LineDeepBreach:
			reason = "深破止损离场"
		}
		d := &Decision{Code: code, Action: ActionClose, Line: st.Line, PnlPct: pnl, Reason: reason}
		if st.Line == LineStopLoss && pnl > -2*sl {
			d.Action = ActionTrim
			d.Reason = "首触止损未深破，减半仓"
		}
		return *st, d
	}
	// 延持态（Settled && !Confirmed）：**不早退**。
	// 为什么不早退 = 终态失明（本批 P0-C / §20260921C M10 同族：静默不再评估）——
	// 窗结算只要命中"有反向信号 → 延持"就落 Settled=true/Confirmed=false，旧实现在这里
	// return *st, nil，于是此后每一轮都在同一处早退，该持仓永久不再被任何判定线评估：
	// 信号消失、继续跌到深破都不出卡（现网默认执行的就是这条旧通道 ⇒ 漏卖是资金级的）。
	// 新通道 internal/signalctl/sell.go 的延持只顺延结算点、不落 Settled，所以无此病；
	// 本批按 B 方案只摘掉早退（下方"窗口期结算"段的结算点与延持判定一律不动），
	// 让延持持仓继续按判定线逐轮评估。
	// 资金外溢守卫：延持态重新出卡必须"本轮仍破线且不轻于原始锁定线"（reevalAllowsSettle），
	// 不是任意一轮都可能卖。
	// English: §P0-C(B) — never early-return on Settled&&!Confirmed; that was a terminal blind
	// spot. Re-evaluate the lines each round, gated by reevalAllowsSettle so an exit card only
	// fires while the position still breaches a line no lighter than the locked original one.
	extendHold := st.Settled && !st.Confirmed

	// 未结算：判断当前命中哪条判定线。
	line := LineNone
	// 深破兜底（最高优先级）：浮亏 ≥ 止损线×倍数。
	if pnl <= -sl*dm {
		line = LineDeepBreach
	} else if pnl <= -sl {
		// 止损线：但有反向(做空/利空)信号或深破 → 硬清；否则进入观察窗。
		if hasBear {
			st.Line, st.FirstTouch, st.SettleStart, st.WindowMin, st.Settled, st.Confirmed = LineStopLoss, now, alignSettle(now, exitMin), exitMin, true, true
			return *st, &Decision{Code: code, Action: ActionClose, Line: LineStopLoss, PnlPct: pnl, Reason: "触止损线且有利空/做空信号，硬止损"}
		}
		line = LineStopLoss
	} else if pnl >= tp {
		// 止盈线：有做多信号 → 延持；无 → 进入观察窗。
		if hasBull {
			return *st, nil // 有同向信号继续持有
		}
		// 但若已突破更多且从最高价回撤超 MaxPullback → 移动止盈优先。
		if st.HighPrice > 0 && (st.HighPrice-curPrice)/st.HighPrice*100 >= pb {
			line = LineTrail
		} else {
			line = LineTakeProfit
		}
	} else if st.HighPrice > entryPrice && (st.HighPrice-curPrice)/st.HighPrice*100 >= pb {
		// 已有利润、从最高价回撤 ≥ MaxPullback → 移动止盈（洗盘过滤窗更长）。
		line = LineTrail
	}

	if line == LineNone {
		return *st, nil
	}

	// §P0-C(B) 延持态重评估守卫：延持之后要再次出卡，本轮命中的判定线必须"不轻于"延持时
	// 锁定的原始线（同族仍破线，或止盈延持后跌进损失族）。反向情形（原锁止损线、价格已反弹
	// 回止盈区）一律不收卡——否则等于把失明换成"按反弹后的止盈价挂止损标签卖出"。
	if extendHold && !reevalAllowsSettle(st.Line, line) {
		return *st, nil
	}

	// 首次命中：锁定判定线 + 首触时刻 + 固定观察窗（不滚动）。
	if st.Line == LineNone {
		st.Line = line
		st.FirstTouch = now
		w := exitMin
		if line == LineTrail {
			w = trailMin // 移动止盈用更长窗过滤洗盘
		}
		st.WindowMin = w
		st.SettleStart = alignSettle(now, w)
		return *st, &Decision{Code: code, Action: ActionConfirm, Line: line, PnlPct: pnl,
			Reason: "命中判定线，进入观察窗"}
	}

	// 窗口期结算：已过结算点 → 检查窗口内是否有同向信号。
	if !now.Before(st.SettleStart) {
		st.Settled = true
		// 止盈类窗口：有做多信号 → 延持；无 → 离场。
		// 止损类窗口：有做空/利空信号 → 硬清；无 → 离场。
		hold := hasBull
		if st.Line == LineStopLoss || st.Line == LineDeepBreach {
			hold = hasBear
		}
		st.Confirmed = !hold
		if hold {
			// 窗口内有信号 → 延持：置 Settled/不置 Confirmed 后**不再早退**（§P0-C B 方案），
			// 下一轮继续按判定线评估；本函数其余结算逻辑（结算点、延持判定）保持原样。
			return *st, nil
		}
		d := &Decision{Code: code, Action: ActionClose, Line: st.Line, PnlPct: pnl}
		switch st.Line {
		case LineTakeProfit, LineTrail:
			d.Reason = "止盈窗结算无信号，止盈离场"
		case LineDeepBreach:
			d.Reason = "深破窗结算无信号，止损离场"
		default:
			d.Reason = "止损窗结算无信号，止损离场"
			d.Action = ActionTrim
		}
		return *st, d
	}

	return *st, &Decision{Code: code, Action: ActionConfirm, Line: st.Line, PnlPct: pnl,
		Reason: "观察窗内等待信号"}
}

// isLossLine 判定线是否属"损失族"（止损线 / 深破兜底）；反之为止盈类（止盈 / 移动止盈）。
// English: whether a discipline line belongs to the loss family (stop-loss / deep breach).
func isLossLine(l DisciplineLine) bool {
	return l == LineStopLoss || l == LineDeepBreach
}

// reevalAllowsSettle 延持态（Settled && !Confirmed）重新出钱的准入判据：本轮命中的判定线
// 必须"不轻于"延持时锁定的原始线，即
//   - 同族仍命中（原始止损线仍破 / 原始止盈线仍达标，含止损 → 深破的加深）→ 放行；
//   - 盈利族延持后跌进损失族（风险升级）→ 放行；
//   - 损失族延持后价格反弹回止盈区（原始破线条件已消失）→ 拒绝。
//
// 依据：B 方案只修"终态失明"，不放宽成"任意一轮都可能卖"——多出来的那一次卖出必须落在
// 确实仍在破线的价格区间上。
// English: §P0-C(B) guard — an extended-hold position may only re-enter settlement while the
// current round still breaches a line no lighter than the locked original line.
func reevalAllowsSettle(orig, cur DisciplineLine) bool {
	if cur == LineNone {
		return false
	}
	if orig == LineNone {
		return true // 异常存量态（已结算却无线）：按正常重评估放行，不额外收卡
	}
	if isLossLine(orig) == isLossLine(cur) {
		return true
	}
	return !isLossLine(orig) && isLossLine(cur)
}

// alignSettle 把触发时刻对齐到固定窗口边界（如 15min K）：从交易日整点起按窗口分钟数对齐。
// 首次触及时刻对齐到该窗口的结束边界，锁死一次结算，不滚动。
func alignSettle(t time.Time, winMin int) time.Time {
	if winMin <= 0 {
		winMin = 15
	}
	// 以交易时段整点(09:30 起)为锚：分钟数相对 09:30 的偏移，取整到 winMin 的倍数。
	base := time.Date(t.Year(), t.Month(), t.Day(), 9, 30, 0, 0, t.Location())
	off := int(t.Sub(base).Minutes())
	if off < 0 {
		off = 0
	}
	next := (off/winMin + 1) * winMin
	return base.Add(time.Duration(next) * time.Minute)
}

// round2pct 保留两位小数的百分比。
func round2pct(v float64) float64 {
	return math.Round(v*100) / 100
}
