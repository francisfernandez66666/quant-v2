// t_cost_readout.go — §T-COST B1 的实盘成本分项只读聚合（2026-10-27）。
//
// 为什么在 server 侧按明细重算、而不是读文件里的"日汇总"：
// 实盘账本文件只有**明细**是真源（rounds 才是唯一事实），任何写盘时算好的汇总都可能是上一次
// 落库的旧值；接口重算一遍与既有 pnl 段同一条纪律，避免"面板显示的成本与明细对不上"。
// 纸面账本的成本段由引擎的 TRoundDayStats.cost 直接透出（那边本来就只有引擎能算）。
//
// 口径与 paper.TCostDaily 逐字一致（毛/佣金/印花/滑点/净 + 估算占比 + 参与率分位）：
//   - SlipCost = Σ |首腿成交 − 决策价| × 首腿股数（绝对值和，不区分买卖）；
//   - FeeEstimateShare = 费用来源非 broker 且有成交的轮数 / 有成交的轮数；
//   - ParticipationPct* 只统计读数可得的轮，不把"没有读数"补成 0 混进中位数。
//
// English: read-only per-day cost aggregation for the live T book, recomputed from the round details
// exactly like the existing pnl block (details are the only source of truth). Same field semantics as
// paper.TCostDaily; missing participation readings are excluded rather than filled with zero.
package server

import (
	"math"
	"sort"
)

// tCostView 当日成本分解（与 paper.TCostDaily 同名字段，前端一套渲染）。
type tCostView struct {
	Gross               float64 `json:"gross"`
	FeeBuy              float64 `json:"fee_buy"`
	FeeSell             float64 `json:"fee_sell"`
	StampTax            float64 `json:"stamp_tax"`
	SlipCost            float64 `json:"slip_cost"`
	Net                 float64 `json:"net"`
	Fills               int     `json:"fills"`
	Unfilled            int     `json:"unfilled"`
	FeeEstimateShare    float64 `json:"fee_estimate_share"`
	ParticipationPctP50 float64 `json:"participation_pct_p50"`
	ParticipationPctMax float64 `json:"participation_pct_max"`
}

// tCostAccumulator 累加器：中位数需要全部样本，故先收在切片里，finalize 时再算分位。
type tCostAccumulator struct {
	cost          tCostView
	filledRounds  int
	estimateRound int
	participation []float64
}

// add 累加一条实盘回转（参数已由调用方从 RawMessage 解出，server 不复制 engine 结构体）。
func (a *tCostAccumulator) add(filledFirst int, firstPrice, decision float64,
	feeBuy, feeSell, stamp, participation float64,
	tPnlGross, tPnlNet float64, pairFilled int, feeSource string, unfilled bool) {
	if filledFirst > 0 {
		a.cost.Fills++
	}
	if pairFilled > 0 {
		a.cost.Fills++
	}
	anyFill := filledFirst > 0 || pairFilled > 0
	if anyFill {
		a.filledRounds++
		if feeSource != "" && feeSource != "broker" {
			a.estimateRound++
		}
	}
	if unfilled {
		a.cost.Unfilled++
	}
	if decision > 0 && filledFirst > 0 && firstPrice > 0 {
		// 滑点成本只算首腿（decision_price 只锚首腿）；绝对值和，方向由 slip_bps 单独表达。
		a.cost.SlipCost += absF(firstPrice-decision) * float64(filledFirst)
	}
	if participation > 0 {
		a.participation = append(a.participation, participation)
		if participation > a.cost.ParticipationPctMax {
			a.cost.ParticipationPctMax = participation
		}
	}
	a.cost.Gross += tPnlGross
	a.cost.Net += tPnlNet
	a.cost.FeeBuy += feeBuy
	a.cost.FeeSell += feeSell
	a.cost.StampTax += stamp
}

// finalize 收口：估算占比、参与率中位数与两位小数（与面板展示口径一致）。
func (a *tCostAccumulator) finalize() tCostView {
	if a.filledRounds > 0 {
		a.cost.FeeEstimateShare = float64(a.estimateRound) / float64(a.filledRounds)
	}
	if n := len(a.participation); n > 0 {
		sort.Float64s(a.participation)
		if n%2 == 1 {
			a.cost.ParticipationPctP50 = a.participation[n/2]
		} else {
			a.cost.ParticipationPctP50 = (a.participation[n/2-1] + a.participation[n/2]) / 2
		}
	}
	a.cost.Gross, a.cost.Net = round2(a.cost.Gross), round2(a.cost.Net)
	a.cost.FeeBuy, a.cost.FeeSell = round2(a.cost.FeeBuy), round2(a.cost.FeeSell)
	a.cost.StampTax, a.cost.SlipCost = round2(a.cost.StampTax), round2(a.cost.SlipCost)
	a.cost.FeeEstimateShare = round4(a.cost.FeeEstimateShare)
	a.cost.ParticipationPctP50 = round4(a.cost.ParticipationPctP50)
	a.cost.ParticipationPctMax = round4(a.cost.ParticipationPctMax)
	return a.cost
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// round4 四位小数（参与率量级小；server 包不依赖 paper，故本地实现）。
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
