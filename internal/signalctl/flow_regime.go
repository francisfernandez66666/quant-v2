// flow_regime.go — §FLOW-QUADRANT 资金流/相对放量四象限决策（纯函数，无 IO）。
//
// 为什么单列一个中央层：四象限判据（趋势/量态/仓位相对档）原本会在拒买闸、执行闸、
// 减仓通道三处各算一遍，三份实现必然漂移。这里收敛成唯一纯函数，三处共用同一输出。
// owner 裁决（2026-11-19）：① ±20% 全在金额层、置信度不独立放大；③ 禁止抄底用相对口径
// （TotalPosPct/目标档），不用绝对 70%；④ range 态不做四象限硬决策，交做T联动处理。
//
// English: single pure decision layer for the flow/volume quadrant; shared by sizing veto,
// execution gate and the reduce channel so the judgement cannot drift across three call sites.
package signalctl

import "math"

// 量态三态。owner 裁决5：放量/缩量双闸门，其余归平量（修复原矩阵只有两态的缺口）。
const (
	VolStateSurge   = "surge"   // 放量：r_yest>rYesSurge 且 r_ma5>rMa5Surge
	VolStateShrink  = "shrink"  // 缩量：r_yest<rYesShrink 且 r_ma5<rMa5Shrink
	VolStateNeutral = "neutral" // 平量：两者之间（原矩阵无此态，此处补齐）
)

// 量态判据阈值。放量为 owner 定稿；缩量为 owner 暂定（市面主流量比<1，本口径更严：
// 同时要求昨比<1.0 与 5日均比<0.9，避免平量被误判缩量）。
const (
	rYesSurge  = 1.2
	rMa5Surge  = 1.0
	rYesShrink = 1.0
	rMa5Shrink = 0.9
)

// 仓位相对档阈值（owner 裁决3：相对目标档，非绝对仓位）。
// posRel = TotalPosPct / MaxPosPct（目标档来自状态机 bull 0.60/range 0.35/bear 0.15）。
const (
	posRelLowHigh = 0.8 // ≤ 低档
	posRelHighLow = 1.3 // ≥ 高档
)

// 象限决策三态。
const (
	QuadrantNeutral  = "neutral"   // 不干预
	QuadrantBlockBuy = "block_buy" // 硬拒买（禁抄底 / 禁加仓）
	QuadrantReduce   = "reduce"    // 减仓闸门（卖出动作）
)

// QuadrantInput 四象限决策输入（引擎装配，纯值传递）。
type QuadrantInput struct {
	MarketState  string  // bull / range / bear（空=弃权→Neutral）
	TotalPosPct  float64 // 当前持仓市值/总资产（0~1）
	TargetPosPct float64 // 状态机目标档（bull .60/range .35/bear .15；0=弃权→Neutral）
	RYest        float64 // 昨日量比 v[-1]/v[-2]（NaN/≤0=弃权→平量）
	RMa5         float64 // 五日均量比 v[-1]/mean(v[-6:-1])（NaN/≤0=弃权→平量）
}

// QuadrantDecision 决策输出。
type QuadrantDecision struct {
	Decision string  // Neutral / BlockBuy / Reduce
	VolState string  // surge / shrink / neutral
	PosRel   float64 // TotalPosPct/TargetPosPct（弃权时 0）
	Reason   string  // 落痕（机读短串）
}

// VolStateOf 由双闸门判量态。任一读数不可得（NaN/≤0）→ 平量弃权，绝不把未知当放量。
func VolStateOf(rYest, rMa5 float64) string {
	if math.IsNaN(rYest) || math.IsNaN(rMa5) || rYest <= 0 || rMa5 <= 0 {
		return VolStateNeutral
	}
	if rYest > rYesSurge && rMa5 > rMa5Surge {
		return VolStateSurge
	}
	if rYest < rYesShrink && rMa5 < rMa5Shrink {
		return VolStateShrink
	}
	return VolStateNeutral
}

// PosRelOf 仓位相对档 = 当前仓位/目标档。目标档不可得（≤0）→ 返回 0 表示弃权。
func PosRelOf(totalPosPct, targetPosPct float64) float64 {
	if targetPosPct <= 0 || totalPosPct <= 0 {
		return 0
	}
	return totalPosPct / targetPosPct
}

// DecideQuadrant 四象限中央决策。range/弃权一律 Neutral：
//   - 牛+放量+高档 → Reduce（查滞涨→减仓闸门）
//   - 牛+缩量+高档 → BlockBuy（顶背离预警，禁止加仓）
//   - 熊+放量+高档 → BlockBuy（出货/主跌，禁止抄底；owner 裁决硬闸）
//   - 其余（含低/中档、平量、range、弃权）→ Neutral
//
// 高档判据用相对口径 posRel≥1.3（owner 裁决3），不再用绝对 70%——熊市目标档仅 15%，
// 绝对 70% 在熊市几乎不可能触发，硬闸会在最该防的场景失效。
func DecideQuadrant(in QuadrantInput) QuadrantDecision {
	vol := VolStateOf(in.RYest, in.RMa5)
	posRel := PosRelOf(in.TotalPosPct, in.TargetPosPct)
	d := QuadrantDecision{VolState: vol, PosRel: posRel, Decision: QuadrantNeutral, Reason: "neutral"}
	// range 态交做T联动，不进四象限硬决策（owner 裁决4）。
	if in.MarketState != "bull" && in.MarketState != "bear" {
		d.Reason = "regime_not_quadrant"
		return d
	}
	// 高档才触发硬决策；posRel=0（弃权）或低于阈值不触发。
	if posRel < posRelHighLow {
		d.Reason = "pos_not_high"
		return d
	}
	switch {
	case in.MarketState == "bull" && vol == VolStateSurge:
		d.Decision, d.Reason = QuadrantReduce, "bull_surge_high_reduce"
	case in.MarketState == "bull" && vol == VolStateShrink:
		d.Decision, d.Reason = QuadrantBlockBuy, "bull_shrink_high_noadd"
	case in.MarketState == "bear" && vol == VolStateSurge:
		d.Decision, d.Reason = QuadrantBlockBuy, "bear_surge_high_nobottom"
	}
	return d
}
