// flow_blend.go — §FLOW-BLEND 资金流/放量后置金额乘子（纯函数，无 IO）。
//
// 为什么后置而不是加进定权因子：金额层实跑 power_mean（sizing.go:175-177），因子经幂平均
// 合成、Cap=1.15 夹的是 Scale；把新信号加进因子组会被 de-stack 稀释、正幅度还可能被 Cap 吃掉，
// 失去区分度。故三项合成一个**后置乘子**，只在单笔金额占比出口乘，恒定封顶 ±20%，
// 不随因子数累加——owner 裁决（2026-11-19）：① ±20% 全在金额层；② 20% 是**合成后总幅度**。
//
// 与 Cap=1.15 的关系（不冲突）：Cap 夹的是 Scale（目标仓位缩放），本乘子作用在 perTradePct
// （单笔占比，硬边界 0.5）。0.30×1.2=0.36<0.5，故 ±20% 可完整生效，不受 Scale 的 Cap 约束。
// 置信度层不参与（owner 裁决）：改 Confidence 会经 kellyFactor 二次放大，两层都调会破总幅度。
//
// English: a single post-composition amount multiplier for the three flow/volume signals;
// capped at +/-20% in total (not per factor), applied on the per-trade share, not the sizing group.
package signalctl

import (
	"fmt"
	"math"

	"quant-trading-v2/internal/combat_agent"
)

// 三项 delta 在 Signal.Meta 里的键（每项的"信号强度"∈[-1,1]；缺键=弃权，不当 0 也不当中性）。
const (
	MetaFlowMarketDelta = "flow_market_delta" // 大盘净流入方向强度
	MetaFlowSectorDelta = "flow_sector_delta" // 板块净流入方向强度
	MetaVolSurgeDelta   = "vol_surge_delta"   // 相对放量方向强度（放量+1/缩量-1/平量0）
)

// blend 幅度与硬边界。Pct=0.20 是合成后总幅度；权重三项均分（后续可配置）。
const flowBlendPct = 0.20
const flowBlendLo = 0.80
const flowBlendHi = 1.20

// flowBlendWeight 三项权重（合计 1.0，均分）。缺项时按有效项 renormalize，缺全部→弃权(1.0)。
var flowBlendWeight = map[string]float64{
	MetaFlowMarketDelta: 1.0 / 3,
	MetaFlowSectorDelta: 1.0 / 3,
	MetaVolSurgeDelta:   1.0 / 3,
}

// metaDelta 从信号 Meta 读一项 delta，夹到 [-1,1]；缺失/NaN → (0,false) 表示弃权。
func metaDelta(meta map[string]float64, key string) (float64, bool) {
	if meta == nil {
		return 0, false
	}
	v, ok := meta[key]
	if !ok || math.IsNaN(v) {
		return 0, false
	}
	if v < -1 {
		v = -1
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

// ApplyFlowBlend 合成三项 delta 成一个后置乘子，写入结果。
// b = Σ(w_i·delta_i)/Σ(w_i有效)，blend = clamp(1+Pct·b, 0.80, 1.20)。
// 全弃权 → blend=1.0（零行为变更）。恒定封顶：三项同向极值也只到 ±20%，不随项数累加。
func ApplyFlowBlend(res *SizingResult, sig combat_agent.Signal) {
	if res == nil {
		return
	}
	res.FlowBlend = 1.0
	sumW, sumWD := 0.0, 0.0
	for key, w := range flowBlendWeight {
		d, ok := metaDelta(sig.Meta, key)
		if !ok {
			continue
		}
		sumW += w
		sumWD += w * d
	}
	if sumW <= 0 {
		return // 三项全弃权：中性，零行为变更
	}
	b := sumWD / sumW
	blend := 1.0 + flowBlendPct*b
	if blend < flowBlendLo {
		blend = flowBlendLo
	}
	if blend > flowBlendHi {
		blend = flowBlendHi
	}
	res.FlowBlend = blend
	res.Reasons = append(res.Reasons, fmt.Sprintf("资金流放量×%+.0f%%", (blend-1.0)*100))
}

// ComputeSizingWithBlend 对外入口：先算原始 SizingResult，再叠加后置 blend。
// 为什么包一层而不是改 ComputeSizing：ComputeSizing 的 Scale 有"与改造前逐字一致"回归锁
// （sizing.go:74-77、sizing_test.go:464），且统计类调用（engine.go:7289 / funds_today.go:103）
// 不需要 blend。只有真正下单的 applySizing 改调本入口，影响面收敛到一条链。
func ComputeSizingWithBlend(r Regime, sig combat_agent.Signal) SizingResult {
	res := ComputeSizing(r, sig)
	if !res.Downgrade {
		ApplyFlowBlend(&res, sig)
	}
	return res
}

// PerTradePctOfBlend 带后置 blend 的单笔占比：先按原口径算 eff，再乘 blend，
// 最后夹到 [0, 0.5]（单笔永不过半仓；下限 0 交给 ordbudget 的不可得判据兜住）。
// blend=1.0 时与原 PerTradePctOf 逐字一致（向后兼容）。
func PerTradePctOfBlend(k, lo, hi, blend float64) float64 {
	eff := PerTradePctOf(k, lo, hi) // 复用原函数全部归一化/夹取护栏
	if math.IsNaN(blend) || blend <= 0 {
		blend = 1.0
	}
	eff *= blend
	if eff < 0 {
		eff = 0
	}
	if eff > 0.5 {
		eff = 0.5
	}
	return eff
}

// PerTradePctBlend 是 PerTradePctOfBlend 的 (ms,ss) 包装，与原 PerTradePct 对称。
func PerTradePctBlend(ms, ss, lo, hi, blend float64) float64 {
	return PerTradePctOfBlend(ms*ss, lo, hi, blend)
}
