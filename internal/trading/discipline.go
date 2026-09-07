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

	// 若已结算，直接返回已确认动作（窗口结束且无信号 → 离场）。
	if st.Settled {
		if st.Confirmed {
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
		return *st, nil
	}

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
			return *st, nil // 窗口内有信号，延持（下轮若仍无信号继续累计到 settle 后确认）
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
