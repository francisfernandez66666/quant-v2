// derive_carrier_flow.go — §FLOW-QUADRANT 的载体烘参出口（只读，零新数字）。
//
// 为什么需要这一层：四象限与后置乘子的阈值原本只存在于 flow_regime.go / flow_blend.go
// 的常量块里，载体侧只能"照着抄一份 1.2 / 1.0 / 0.9 / 1.3 / 0.20"——两处各写一次就是
// 会漂的第二份定义（本仓最贵的形状：改了一处、另一处继续按旧数交易，而两边看起来都对）。
// 这里把每一枚都**转调**自己包里的常量，不新增任何数值；derive_carrier_params 逐键搬进
// 参数文件，沙箱读 PARAMS，于是"同一枚数只有一个家"。
//
// English: read-only carrier readouts for the flow-quadrant constants. Every value is a
// re-export of the package's own constant; no new numbers are introduced here.
package signalctl

// CarrierFlowKnob 一枚载体烘参键值（顺序由导出函数给定，避免 map 迭代导致输出抖动）。
type CarrierFlowKnob struct {
	Key   string
	Value float64
}

// FlowQuadrantCarrierKnobs 四象限（量态三闸 + 仓位相对档）与后置乘子（幅度/上下界）的
// 全部出厂阈值，按固定顺序返回，供 cmd/derive_carrier_params 直接落参。
//
// 载体侧的键名与 Go 侧语义一一对应：
//
//	flow_r_yest_surge / flow_r_ma5_surge   放量双闸（VolStateOf）
//	flow_r_yest_shrink / flow_r_ma5_shrink 缩量双闸（更严：昨比<1.0 且 五日均比<0.9）
//	flow_posrel_high                       posRel 高档线（相对目标仓位，非绝对 70%）
//	flow_blend_pct / _lo / _hi             后置金额乘子幅度与硬边界（合成后总幅度）
//
// English: every flow-quadrant threshold the carrier bakes, in a stable order.
func FlowQuadrantCarrierKnobs() []CarrierFlowKnob {
	return []CarrierFlowKnob{
		{"flow_r_yest_surge", rYesSurge},
		{"flow_r_ma5_surge", rMa5Surge},
		{"flow_r_yest_shrink", rYesShrink},
		{"flow_r_ma5_shrink", rMa5Shrink},
		{"flow_posrel_high", posRelHighLow},
		{"flow_blend_pct", flowBlendPct},
		{"flow_blend_lo", flowBlendLo},
		{"flow_blend_hi", flowBlendHi},
	}
}
