// lifecycle.go 策略生命周期闭环（§WS-H 维7）：灰度观察 → 自动晋升/回拒 → live 衰退自动降级。
// 提供两个纯函数评估器 + 一个候选生成入口：
//   - EvaluateGrayscale：按灰度库规则 + 该规则在 paper 盘的分池逐笔收益，判定 promote/reject/pending；
//   - EvaluateDemote：按 applied 规则的逐日滚动指标序列（IR/胜率），判定连续 N 日衰退 → disabled；
//   - PromotionCandidates：把 promote 判定落成候选行（审批流复用现有 proposed→applied 通道）。
//
// English: strategy lifecycle (WS-H 维7). Grayscale observation → auto-promote/reject, and live
// decline → auto-disable. Two pure evaluators plus a candidate-generation entry point.
package research

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/store"
)

// PromotionOpts 灰度晋升评估参数（零值回落内置默认）。
// English: promotion evaluation thresholds (zero values fall back to defaults).
type PromotionOpts struct {
	ObservationDays int     // 观察期（交易日），默认 20
	MinTrades       int     // 最小样本数，默认 10
	MinIR           float64 // paper 归因 IR 下限，默认 0.3
	MinWinRate      float64 // 胜率下限（%），默认 40
	MinProfitFactor float64 // 盈亏比下限，默认 1.2
	MaxDrawdownPct  float64 // 最大回撤上限（%），默认 15
}

// fill 用内置默认值补齐 PromotionOpts 的零值字段（调用方可不配置，评估仍确定性）。
// English: fills zero PromotionOpts fields with built-in defaults.
func (o *PromotionOpts) fill() {
	if o.ObservationDays <= 0 {
		o.ObservationDays = 20
	}
	if o.MinTrades <= 0 {
		o.MinTrades = 10
	}
	if o.MinIR <= 0 {
		o.MinIR = 0.3
	}
	if o.MinWinRate <= 0 {
		o.MinWinRate = 40
	}
	if o.MinProfitFactor <= 0 {
		o.MinProfitFactor = 1.2
	}
	if o.MaxDrawdownPct <= 0 {
		o.MaxDrawdownPct = 15
	}
}

// GrayscaleVerdict 一条灰度规则的生命周期判定。
// English: lifecycle verdict for one grayscale rule.
type GrayscaleVerdict struct {
	RuleID          string  `json:"rule_id"`
	CandID          int64   `json:"candidate_id"`
	Kind            string  `json:"kind"`
	EnteredAt       string  `json:"entered_at"`
	ObservationDays int     `json:"observation_days"`
	Verdict         string  `json:"verdict"` // promote | reject | pending
	Reason          string  `json:"reason"`
	Trades          int     `json:"trades"`
	WinRate         float64 `json:"win_rate"`
	IR              float64 `json:"ir"`
	ProfitFactor    float64 `json:"profit_factor"`
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`
}

// EvaluateGrayscale 评估灰度库晋升/回拒：池收益（paper 分池逐笔净收益）不足观察期 →
// pending；样本达标且全部指标过线 → promote；样本达标但任一指标不达标 → reject。
// 未进入灰度库的池（poolTrades 缺键）按 pending（无观测数据）处理。
// English: evaluates grayscale promotion/rejection from per-pool paper net returns. Not yet past the
// observation window → pending; enough trades and all thresholds met → promote; enough trades but a
// threshold missed → reject; missing pool data → pending.
func EvaluateGrayscale(gs *grayscaleFile, poolTrades map[string][]float64, opts PromotionOpts) []GrayscaleVerdict {
	opts.fill()
	out := make([]GrayscaleVerdict, 0, len(gs.Factors)+len(gs.Patterns))
	add := func(ruleID string, candID int64, kind, enteredAt string, observationDays int, trades []float64) {
		v := GrayscaleVerdict{
			RuleID: ruleID, CandID: candID, Kind: kind,
			EnteredAt: enteredAt, ObservationDays: observationDays,
		}
		// 观察期检查：进入时间距今是否 ≥ ObservationDays 个交易日（近似按自然日/观察参数，
		// 精确交易日由调用方用日历裁剪；此处保守用「天数不足=继续观察」）。
		ageDays := 0
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, enteredAt, time.Local); err == nil {
				ageDays = int(time.Since(t).Hours() / 24)
				break
			}
		}
		if ageDays < observationDays {
			v.Verdict = "pending"
			v.Reason = fmt.Sprintf("观察期未满（已%d天<需%d天）", ageDays, observationDays)
			out = append(out, v)
			return
		}
		if trades == nil || len(trades) == 0 {
			v.Verdict = "pending"
			v.Reason = "无 paper 观测收益（池无成交）"
			out = append(out, v)
			return
		}
		st := summarizeTrades(trades)
		v.Trades = st.count
		v.WinRate = st.winRate
		v.IR = st.ir
		v.ProfitFactor = st.profitFactor
		v.MaxDrawdownPct = st.maxDrawdownPct
		reasons := []string{}
		if st.count < opts.MinTrades {
			reasons = append(reasons, fmt.Sprintf("样本不足(%d<%d)", st.count, opts.MinTrades))
		}
		if st.ir < opts.MinIR {
			reasons = append(reasons, fmt.Sprintf("IR不足(%.3f<%.3f)", st.ir, opts.MinIR))
		}
		if st.winRate < opts.MinWinRate {
			reasons = append(reasons, fmt.Sprintf("胜率不足(%.1f%%<%.1f%%)", st.winRate, opts.MinWinRate))
		}
		if st.profitFactor < opts.MinProfitFactor {
			reasons = append(reasons, fmt.Sprintf("盈亏比不足(%.2f<%.2f)", st.profitFactor, opts.MinProfitFactor))
		}
		if st.maxDrawdownPct > opts.MaxDrawdownPct {
			reasons = append(reasons, fmt.Sprintf("回撤过大(%.1f%%>%.1f%%)", st.maxDrawdownPct, opts.MaxDrawdownPct))
		}
		if len(reasons) == 0 {
			v.Verdict = "promote"
			v.Reason = "全部指标达标（IR/胜率/盈亏比/回撤/样本）"
		} else {
			v.Verdict = "reject"
			v.Reason = "未过晋升护栏：" + strings.Join(reasons, "；")
		}
		out = append(out, v)
	}
	for i := range gs.Factors {
		r := &gs.Factors[i]
		key := "fac_" + fmt.Sprintf("%d", r.CandID)
		add(r.ID, r.CandID, "factor", r.EnteredAt, r.ObservationDays, poolTrades[key])
	}
	for i := range gs.Patterns {
		r := &gs.Patterns[i]
		key := "pat_" + fmt.Sprintf("%d", r.CandID)
		add(r.ID, r.CandID, "pattern", r.EnteredAt, r.ObservationDays, poolTrades[key])
	}
	return out
}

// DemoteOpts 衰退自动降级参数。
// English: demotion thresholds.
type DemoteOpts struct {
	ConsecDays     int     // 连续低于阈值的交易日数（默认 3）
	MinIR          float64 // 滚动 IR 下限（默认 0）
	MinWinRate     float64 // 滚动胜率下限（%），默认 35
	MinDailyTrades int     // 单日样本下限（默认 3）
}

func (o *DemoteOpts) fill() {
	if o.ConsecDays <= 0 {
		o.ConsecDays = 3
	}
	if o.MinWinRate <= 0 {
		o.MinWinRate = 35
	}
	if o.MinDailyTrades <= 0 {
		o.MinDailyTrades = 3
	}
}

// DailyStat 某战法单日的滚动归因指标（IR/胜率/当日样本数）。
// English: one day's rolling attribution stat for a strategy (IR/win-rate/sample count).
type DailyStat struct {
	IR      float64
	WinRate float64
	Trades  int
}

// DemoteVerdict 一条 applied 战法的降级判定。
// English: demotion verdict for one applied strategy.
type DemoteVerdict struct {
	RuleID    string `json:"rule_id"`
	Verdict   string `json:"verdict"` // disable | keep
	Reason    string `json:"reason"`
	ConsecLow int    `json:"consec_low_days"`
}

// EvaluateDemote 按逐日滚动指标判定连续 N 日低于衰退阈值 → disable。
// days 按时间升序传入；样本数不足当日计入"低位"（无统计意义视为不达标）。
// English: flags disable when a strategy shows ConsecDays consecutive days below the decline
// thresholds; days with too few trades also count as below-threshold (no significance → fail).
func EvaluateDemote(days []DailyStat, opts DemoteOpts) DemoteVerdict {
	opts.fill()
	v := DemoteVerdict{Verdict: "keep"}
	lowRun := 0
	for _, d := range days {
		below := d.Trades < opts.MinDailyTrades || d.IR < opts.MinIR || d.WinRate < opts.MinWinRate
		if below {
			lowRun++
		} else {
			lowRun = 0
		}
		if lowRun > v.ConsecLow {
			v.ConsecLow = lowRun
		}
		if lowRun >= opts.ConsecDays {
			v.Verdict = "disable"
			v.Reason = fmt.Sprintf("连续%d个交易日低于衰退阈值（IR/胜率/样本）", lowRun)
			return v
		}
	}
	v.Reason = "滚动指标在阈值之上"
	return v
}

// tradeStats 逐笔净收益的汇总统计：样本/胜率/盈亏比/IR（年化，按日频×√252）/最大回撤（%）。
// English: aggregate per-trade net-return stats: count, win rate, profit factor, annualized IR
// (daily × √252), and max drawdown percent.
type tradeStats struct {
	count          int
	winRate        float64
	profitFactor   float64
	ir             float64
	maxDrawdownPct float64
}

func summarizeTrades(returns []float64) tradeStats {
	var s tradeStats
	if len(returns) == 0 {
		return s
	}
	s.count = len(returns)
	win, loss := 0.0, 0.0
	wins := 0
	eq, peak, maxDD := 1.0, 1.0, 0.0
	for _, r := range returns {
		if r > 0 {
			wins++
			win += r
		} else {
			loss += r
		}
		eq *= 1 + r/100
		if eq > peak {
			peak = eq
		}
		if dd := (peak - eq) / peak * 100; dd > maxDD {
			maxDD = dd
		}
	}
	s.winRate = float64(wins) / float64(len(returns)) * 100
	if loss != 0 {
		s.profitFactor = win / -loss
	} else if wins > 0 {
		s.profitFactor = 99 // 无亏损组合按满分处理（与扫参 objectiveValue 同口径，防 Inf 序列化）
	}
	s.ir = irAnnualized(returns)
	s.maxDrawdownPct = maxDD
	return s
}

// irAnnualized 按日频收益序列计算年化 IR：mean/std×√252。
// English: annualized IR from a daily return series (mean/std × √252).
func irAnnualized(returns []float64) float64 {
	n := len(returns)
	if n < 2 {
		return 0
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(n)
	var ss float64
	for _, r := range returns {
		ss += (r - mean) * (r - mean)
	}
	variance := ss / float64(n-1)
	if variance <= 0 {
		return 0
	}
	sd := math.Sqrt(variance)
	return mean / sd * math.Sqrt(252)
}

// PromotionCandidates 把 promote 判定落成候选行（复用现有审批流：Status=proposed，
// Reason 前缀 [晋升候选]，Guard=promotion）。返回生成的候选 ID。auto=false 时仅入库待人工确认。
// English: turns promote verdicts into candidate rows reusing the existing approval flow
// (Status=proposed, Reason prefixed "[晋升候选]", Guard=promotion). Returns generated IDs.
func PromotionCandidates(db *store.DB, gs *grayscaleFile, verds []GrayscaleVerdict) ([]int64, error) {
	if db == nil {
		return nil, nil
	}
	var ids []int64
	for _, v := range verds {
		if v.Verdict != "promote" {
			continue
		}
		// 防重复：同候选已存在 [晋升候选] 或已 applied/approved 则跳过。
		dup, err := db.CandidateExistsPromotion(v.CandID)
		if err == nil && dup {
			continue
		}
		// 从灰度库取出原规则参数，构造候选载荷。
		var factorsJSON, weightsJSON string
		found := false
		for i := range gs.Factors {
			r := &gs.Factors[i]
			if r.CandID != v.CandID {
				continue
			}
			found = true
			fb, _ := json.Marshal(r.Factors)
			wb, _ := json.Marshal(map[string]any{
				"weights": r.Weights, "directions": r.Directions, "buy_threshold": r.BuyThreshold,
			})
			factorsJSON, weightsJSON = string(fb), string(wb)
		}
		for i := range gs.Patterns {
			r := &gs.Patterns[i]
			if r.CandID != v.CandID {
				continue
			}
			found = true
			cb, _ := json.Marshal(r.Conds)
			factorsJSON, weightsJSON = string(cb), "{}"
		}
		if !found {
			continue
		}
		reason := fmt.Sprintf("[晋升候选] 灰度达标：IR=%.3f 胜率=%.1f%% 盈亏比=%.2f 回撤=%.1f%% 样本=%d（%s）",
			v.IR, v.WinRate, v.ProfitFactor, v.MaxDrawdownPct, v.Trades, v.Reason)
		params, _ := json.Marshal(map[string]any{"from_candidate": v.CandID, "lifecycle": "promotion"})
		id, err := db.SaveCandidate(&store.Candidate{
			Kind: v.Kind, Status: store.CandProposed, Guard: "promotion",
			Factors: factorsJSON, Weights: weightsJSON, Params: string(params),
			Metric: v.IR, IR: v.IR, Reason: reason,
		})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// sortedVerdicts 按 RuleID 排序（确定性输出，测试友好）。
func sortedVerdicts(verds []GrayscaleVerdict) {
	sort.Slice(verds, func(i, j int) bool { return verds[i].RuleID < verds[j].RuleID })
}
