// discipline_tracker.go — 实盘持仓的统一止盈止损纪律状态机接入层（探针+扳机）。
// DisciplineTracker 持有每持仓的 PositionState（跨 5s 探针轮次保持窗口状态），把
// probeDiscipline 的裁决输出映射为 PositionAdvice 并入 trading.Advise，使实盘与模拟盘
// 同口径执行：止损−6/止盈+15/移动止盈=最高价−6/深破−12，触发后固定观察窗（不滚动），
// 窗内无同向信号才离场（战法自带止盈止损降级为触发通知）。
//
// English: the unified stop-loss/take-profit discipline state-machine adapter for the live book
// (probe+trigger). DisciplineTracker keeps per-holding PositionState across 5s probe rounds and maps
// probeDiscipline's decisions onto PositionAdvice inside trading.Advise, so live and paper enforce the
// same lines: SL −6 / TP +15 / trail = high −6 / deep −12, each with a fixed non-rolling confirm window
// (no same-direction signal by settlement → exit). Strategy-native TP/SL degrade to notifications.
package trading

import (
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// DisciplineTracker 实盘持仓纪律状态机容器（跨探针轮次保持每持仓窗口状态）。
// 仅服务单个实盘主账号（pushRealAdvice 每轮喂入该账号持仓），故以纯数字代码为键。
// English: the live-holdings discipline state container (window state survives probe rounds). It serves
// a single primary live account (pushRealAdvice feeds that account's holdings), keyed by pure code.
type DisciplineTracker struct {
	mu     sync.Mutex
	states map[string]*PositionState // code → 纪律状态机（未命中线/已离场的持仓会清理）
}

// NewDisciplineTracker 创建实盘纪律状态机。
// English: creates a live discipline tracker.
func NewDisciplineTracker() *DisciplineTracker {
	return &DisciplineTracker{states: map[string]*PositionState{}}
}

// ProbeAll 对全部实盘持仓执行一轮探针裁决，返回纪律动作建议（止损/止盈/减仓/持有观察）。
//   - 无纪律命中 → 不产出（让 Advise 的加仓/格局正常判定）；
//   - 命中判定线进入观察窗 → 产出低强度"持有"建议（窗口期阻止加仓/格局，前端可见"观察中"）；
//   - 窗结算无同向信号 → 产出 止损/止盈（全平）或 减仓（半平），Source=discipline 供实盘自动执行；
//   - 行情缺失的持仓跳过（不伪造现价）；已平仓代码的状态清理，重新入场从零开始。
//
// English: runs one probe round over all live holdings and returns discipline advices (止损/止盈/减仓/
// 持有观察). No line hit → nothing (add/hold judge normally); inside a confirm window → a low "持有"
// advice (blocks add/hold, surfaces as "observing"); settled without a same-direction signal → 止损/止盈
// (full) or 减仓 (half), Source=discipline for live auto-exec. Missing-quote holdings are skipped;
// closed codes are pruned so re-entry starts fresh.
func (t *DisciplineTracker) ProbeAll(in AdviceInput, disc config.DisciplineConfig) []PositionAdvice {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	held := make(map[string]bool, len(in.Positions))
	var out []PositionAdvice
	for i := range in.Positions {
		p := in.Positions[i]
		code := pureCode(p.TsCode)
		held[code] = true
		quote := in.Quotes[code]
		if quote == nil || quote.Price <= 0 {
			continue // 无有效现价无法裁决（停牌/行情缺失），保留状态不下结论
		}
		// 信号方向：做多信号（止盈延持用）+ 利空归因（止损硬清用），与 CheckPositionAlerts 同口径。
		hasBull := false
		if sc, ok := in.Scores[code]; ok {
			hasBull = sc.SignalActive
		}
		hasBear := in.BearReasons[code] != ""
		// 持仓纪律状态：首次探针以持仓最高价（缺省成本价）为移动止盈基准。
		st := t.states[code]
		if st == nil {
			high := p.HighestPrice
			if high <= 0 {
				high = p.CostPrice
			}
			st = &PositionState{Code: code, HighPrice: high}
		}
		next, dec := probeDiscipline(st, code, p.CostPrice, st.HighPrice, quote.Price, hasBull, hasBear, now, disc)
		t.states[code] = &next
		if dec == nil {
			continue // 未命中/窗口内延持 → 交给加仓/格局判定
		}
		if pa := disciplineAdvice(dec, p, quote.Price, now); pa != nil {
			out = append(out, *pa)
		}
	}
	// 清理已平仓代码的状态（重新入场从零开始，避免旧窗口残留误判）。
	for code := range t.states {
		if !held[code] {
			delete(t.states, code)
		}
	}
	return out
}

// Reset 清空全部持仓纪律状态（引擎重启/账号切换时调用；nil 安全）。
// English: clears all holding discipline states (call on engine restart / account switch; nil-safe).
func (t *DisciplineTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states = map[string]*PositionState{}
}

// disciplineAdvice 把纪律裁决 Decision 映射为 PositionAdvice（Source=discipline）。
// English: maps a discipline Decision onto PositionAdvice (Source=discipline).
func disciplineAdvice(d *Decision, p store.RealPosition, price float64, now time.Time) *PositionAdvice {
	if d == nil {
		return nil
	}
	pa := baseAdvice(p, price, now)
	pa.RefPrice = price // 现价有效，作为自动卖出的挂单价（autoExecuteRealSells 守卫使用）
	pa.Reason = d.Reason
	pa.Source = "discipline"
	switch d.Action {
	case ActionClose:
		pa.Level = "高"
		switch d.Line {
		case LineTakeProfit, LineTrail:
			pa.Action = "止盈" // 止盈/移动止盈窗结算无信号 → 全平
		default: // LineStopLoss, LineDeepBreach
			pa.Action = "止损"
		}
	case ActionTrim:
		pa.Action, pa.Level = "减仓", "高" // 首触止损未深破 → 半平
	case ActionConfirm:
		pa.Action, pa.Level = "持有", "低" // 已命中判定线，观察窗内等待信号（阻止加仓/格局）
	default:
		return nil
	}
	return pa
}
