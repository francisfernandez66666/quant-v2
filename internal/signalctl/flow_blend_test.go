// flow_blend_test.go — §FLOW-BLEND 后置金额乘子锁。
//
// 三条：① 合成后总幅度恒定 ±20%（三项同向极值也只到 1.20/0.80，不随项数累加）；
// ② 缺项弃权（全缺→1.0 零行为变更，单项缺→按有效项 renormalize）；
// ③ PerTradePctOfBlend：blend=1 与原函数逐字一致，正向可突破 hi 但不过半仓硬边界。
// 反证：把 blend 加进定权因子组（幂平均稀释）或叠加两次，用例1/3 会红。
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

func flowSig(meta map[string]float64) combat_agent.Signal {
	return combat_agent.Signal{Meta: meta}
}

// TestFlowBlendTotalCap 三项同向极值：合计只到 ±20%，不累加成 ±60%。
func TestFlowBlendTotalCap(t *testing.T) {
	all := map[string]float64{
		MetaFlowMarketDelta: 1, MetaFlowSectorDelta: 1, MetaVolSurgeDelta: 1,
	}
	var res SizingResult
	ApplyFlowBlend(&res, flowSig(all))
	if math.Abs(res.FlowBlend-1.20) > 1e-9 {
		t.Fatalf("三项全强应封顶 1.20，实际 %.4f", res.FlowBlend)
	}
	allNeg := map[string]float64{
		MetaFlowMarketDelta: -1, MetaFlowSectorDelta: -1, MetaVolSurgeDelta: -1,
	}
	res = SizingResult{}
	ApplyFlowBlend(&res, flowSig(allNeg))
	if math.Abs(res.FlowBlend-0.80) > 1e-9 {
		t.Fatalf("三项全弱应封顶 0.80，实际 %.4f", res.FlowBlend)
	}
}

// TestFlowBlendNotStacked 单项 vs 三项同向：三项同向不比单项更极端（renormalize 后 b 相同）。
func TestFlowBlendNotStacked(t *testing.T) {
	one := SizingResult{}
	ApplyFlowBlend(&one, flowSig(map[string]float64{MetaFlowMarketDelta: 1}))
	three := SizingResult{}
	ApplyFlowBlend(&three, flowSig(map[string]float64{
		MetaFlowMarketDelta: 1, MetaFlowSectorDelta: 1, MetaVolSurgeDelta: 1,
	}))
	if math.Abs(one.FlowBlend-three.FlowBlend) > 1e-9 {
		t.Fatalf("三项同向不应比单项更极端：one=%.4f three=%.4f", one.FlowBlend, three.FlowBlend)
	}
}

// TestFlowBlendAbstain 全弃权→中性；缺一项按有效项算，不把缺项当 0 拉低。
func TestFlowBlendAbstain(t *testing.T) {
	none := SizingResult{}
	ApplyFlowBlend(&none, flowSig(nil))
	if none.FlowBlend != 1.0 {
		t.Fatalf("全缺应中性 1.0，实际 %.4f", none.FlowBlend)
	}
	partial := SizingResult{}
	ApplyFlowBlend(&partial, flowSig(map[string]float64{MetaFlowMarketDelta: 1}))
	if math.Abs(partial.FlowBlend-1.20) > 1e-9 {
		t.Fatalf("单项强应仍到 1.20（缺项弃权不当0），实际 %.4f", partial.FlowBlend)
	}
	nan := SizingResult{}
	ApplyFlowBlend(&nan, flowSig(map[string]float64{MetaFlowMarketDelta: math.NaN()}))
	if nan.FlowBlend != 1.0 {
		t.Fatalf("NaN 应弃权 1.0，实际 %.4f", nan.FlowBlend)
	}
}

// TestPerTradePctOfBlendCompat blend=1 与原函数逐字一致（向后兼容）。
func TestPerTradePctOfBlendCompat(t *testing.T) {
	for _, k := range []float64{0, 0.3, 0.67, 1.0, 3.0, -1} {
		base := PerTradePctOf(k, 0.10, 0.30)
		got := PerTradePctOfBlend(k, 0.10, 0.30, 1.0)
		if math.Abs(got-base) > 1e-12 {
			t.Fatalf("blend=1 应逐字一致：k=%v base=%.6f got=%.6f", k, base, got)
		}
	}
}

// TestPerTradePctOfBlendPositive 正 blend 可突破 hi（0.30×1.2=0.36），但不过半仓硬边界。
func TestPerTradePctOfBlendPositive(t *testing.T) {
	neutral := PerTradePctOf(1.0, 0.10, 0.30) // 0.30 顶格
	up := PerTradePctOfBlend(1.0, 0.10, 0.30, 1.2)
	if math.Abs(up-neutral*1.2) > 1e-9 {
		t.Fatalf("正 blend 应放大到中性×1.2：neutral=%.4f up=%.4f", neutral, up)
	}
	if up > 0.5 {
		t.Fatalf("单笔不得超过半仓：up=%.4f", up)
	}
	// 极端：lo/hi 被配置到 0.9 时，原函数夹到 0.5，再乘 1.2 仍不破 0.5。
	extreme := PerTradePctOfBlend(1.0, 0.9, 0.9, 1.2)
	if extreme > 0.5 {
		t.Fatalf("极端配置正 blend 不得破半仓：%.4f", extreme)
	}
}

// TestComputeSizingWithBlendNoDowngrade 硬拦（情绪冰点）时不叠加 blend：被拒买的单不谈金额调节。
func TestComputeSizingWithBlendNoDowngrade(t *testing.T) {
	s := flowSig(map[string]float64{MetaFlowMarketDelta: 1})
	res := ComputeSizingWithBlend(Regime{EmotionPhase: phaseIce}, s)
	if !res.Downgrade {
		t.Fatal("情绪冰点应硬拦")
	}
	if res.FlowBlend != 0 {
		t.Fatalf("硬拦时不应叠加 blend，实际 %.4f", res.FlowBlend)
	}
}
