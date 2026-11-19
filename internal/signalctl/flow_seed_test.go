// flow_seed_test.go — §FLOW-FACTOR-DAILY 量比纯计算与 delta 桥接锁。
// 四条：① VolRatiosOfBars 不足 6 根/零负量弃权；② 正常序列算出 owner 伪码双比值；
// ③ withRegimeFlowDeltas 的 NaN=弃权不填、平量 0 有效填入、Meta 已有键不覆盖；
// ④ 平量 0 计入合成不 renormalize（跳过会静默放大其余项权重）。
package signalctl

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
)

func TestVolRatiosOfBars(t *testing.T) {
	// 6 根正常序列：last=150 prev=100 → r_yest=1.5；前5均=100 → r_ma5=1.5。
	got, ma, ok := VolRatiosOfBars([]float64{150, 100, 100, 100, 100, 100})
	if !ok || math.Abs(got-1.5) > 1e-9 || math.Abs(ma-1.5) > 1e-9 {
		t.Fatalf("正常序列应得 1.5/1.5, got ok=%v rYest=%v rMa5=%v", ok, got, ma)
	}
	if _, _, ok := VolRatiosOfBars([]float64{1, 1, 1, 1, 1}); ok {
		t.Fatal("不足 6 根应弃权")
	}
	if _, _, ok := VolRatiosOfBars([]float64{150, 0, 100, 100, 100, 100}); ok {
		t.Fatal("序列含 0（停市异常）应弃权")
	}
	if _, _, ok := VolRatiosOfBars(nil); ok {
		t.Fatal("空序列应弃权")
	}
}

func TestVolSurgeDeltaOf(t *testing.T) {
	if VolSurgeDeltaOf(VolStateSurge) != 1.0 || VolSurgeDeltaOf(VolStateShrink) != -1.0 {
		t.Fatal("放量/缩量 delta 映射错误")
	}
	if VolSurgeDeltaOf(VolStateNeutral) != 0.0 {
		t.Fatal("平量 delta 应为 0（有效中性，非弃权）")
	}
}

func TestWithRegimeFlowDeltas(t *testing.T) {
	// NaN 全弃权：Meta 不动（原 map 不被污染）。
	r := Regime{FlowMarketDelta: math.NaN(), FlowSectorDelta: math.NaN(), VolSurgeDelta: math.NaN()}
	orig := combat_agent.Signal{}
	got := withRegimeFlowDeltas(r, orig)
	if got.Meta != nil {
		t.Fatal("全 NaN 应不填 Meta")
	}
	if orig.Meta != nil {
		t.Fatal("原 sig.Meta 不得被污染（拷贝语义）")
	}

	// 平量 0 有效：VolSurgeDelta=0 应填入（不是弃权）。
	r.VolSurgeDelta = 0
	got = withRegimeFlowDeltas(r, combat_agent.Signal{})
	if got.Meta == nil || got.Meta[MetaVolSurgeDelta] != 0 {
		t.Fatalf("平量 0 应填入 Meta, got %+v", got.Meta)
	}

	// Meta 已有键不覆盖（逐票显式值优先）。
	r.VolSurgeDelta = 1
	existing := combat_agent.Signal{Meta: map[string]float64{MetaVolSurgeDelta: -1}}
	got = withRegimeFlowDeltas(r, existing)
	if got.Meta[MetaVolSurgeDelta] != -1 {
		t.Fatalf("Meta 已有键不得被 Regime 覆盖, got %v", got.Meta[MetaVolSurgeDelta])
	}
}

// TestBlendNeutralZeroCountedIn 平量 0 计入合成：大盘 +1、板块 +1、量能平量 0
// → b=(1/3+1/3+0)/1=2/3 → blend=1+0.2×2/3≈1.1333（若把 0 当弃权跳过则 b=1 → 1.2，偏大）。
func TestBlendNeutralZeroCountedIn(t *testing.T) {
	r := Regime{FlowMarketDelta: 1, FlowSectorDelta: 1, VolSurgeDelta: 0}
	sig := withRegimeFlowDeltas(r, combat_agent.Signal{})
	var res SizingResult
	ApplyFlowBlend(&res, sig)
	want := 1.0 + 0.20*(2.0/3.0)
	if math.Abs(res.FlowBlend-want) > 1e-9 {
		t.Fatalf("平量 0 应计入权重分母: want %.6f got %.6f", want, res.FlowBlend)
	}
}

// TestVolRatiosIntraday 盘中折算双比值（方案 B）：14:00 elapsed=180，今日 300 vs 历史各 100。
// 折算全天 = 300×240/180 = 400 → r_yest=r_ma5=4。盘前（elapsed=0）弃权，历史不足弃权。
func TestVolRatiosIntraday(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, cntime.Loc)
	hist := []float64{100, 100, 100, 100, 100}
	ry, rm, ok := VolRatiosIntraday(now, 300, hist)
	if !ok || math.Abs(ry-4.0) > 1e-9 || math.Abs(rm-4.0) > 1e-9 {
		t.Fatalf("14:00 应得 4/4, got ok=%v ry=%v rm=%v", ok, ry, rm)
	}
	// 盘前弃权（折算会放大 240x）。
	preOpen := time.Date(2026, 11, 20, 9, 20, 0, 0, cntime.Loc)
	if _, _, ok := VolRatiosIntraday(preOpen, 300, hist); ok {
		t.Fatal("盘前应弃权")
	}
	// 历史不足 5 根弃权。
	if _, _, ok := VolRatiosIntraday(now, 300, hist[:4]); ok {
		t.Fatal("历史不足 5 根应弃权")
	}
	// 历史含 0/负弃权。
	if _, _, ok := VolRatiosIntraday(now, 300, []float64{100, 0, 100, 100, 100}); ok {
		t.Fatal("历史含 0 应弃权")
	}
	// 当日累计 <=0 弃权。
	if _, _, ok := VolRatiosIntraday(now, 0, hist); ok {
		t.Fatal("当日累计 <=0 应弃权")
	}
}
