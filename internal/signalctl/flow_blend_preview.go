// flow_blend_preview.go — §FLOW-BLEND 后置乘子的**只读预览**入口（仪表盘读数用）。
//
// 为什么需要它：首页要显示"当前合成的金额乘子"（±20% 封顶），而那个数只能由
// ApplyFlowBlend 算出来。如果展示层照抄一份 `1+Pct×b`，就出现了第二份公式真源——
// 本仓反复出事的形状正是这类"两处各自实现同一把尺"，下次改权重/改封顶时只有一处动。
// 所以预览入口不写公式：它把三项 delta 包成一个临时 Signal，**直接转调**
// ApplyFlowBlend，与下单链逐字同源。
//
// English: read-only preview of the post-composition amount multiplier. It deliberately does
// not re-implement the formula — it builds a throwaway Signal and delegates to ApplyFlowBlend,
// so the dashboard and the order path can never drift apart.
package signalctl

import (
	"math"

	"quant-trading-v2/internal/combat_agent"
)

// FlowBlendPreview 预览三项 delta 的合成金额乘子。
//
// market/sector/vol 传 math.NaN() 表示该腿弃权（缺数），与 Regime 上的 NaN 口径一致；
// 三项全弃权 → 返回 (1.0, false)，调用方据此显示"弃权/不参与"，**不许把它画成中性 0**。
// 返回的 ok=true 只表示"至少一项有效"，不表示方向。
// English: preview the blend from three deltas; NaN = abstain. All-abstain → (1.0, false).
func FlowBlendPreview(marketDelta, sectorDelta, volDelta float64) (blend float64, ok bool) {
	// 只把非 NaN 的腿放进 Meta：ApplyFlowBlend 用"键在不在"区分弃权与真零，
	// 所以在 NaN 时把键省掉（而不是塞 0）才是同一条语义。
	meta := make(map[string]float64, 3)
	for key, v := range map[string]float64{
		MetaFlowMarketDelta: marketDelta,
		MetaFlowSectorDelta: sectorDelta,
		MetaVolSurgeDelta:   volDelta,
	} {
		if math.IsNaN(v) {
			continue
		}
		meta[key] = v
	}
	if len(meta) == 0 {
		return 1.0, false
	}
	res := SizingResult{}
	ApplyFlowBlend(&res, combat_agent.Signal{Meta: meta})
	return res.FlowBlend, true
}
