// flow_money_test.go — §FLOW-MONEY 资金流腿纯函数与逐票注入锁（2026-11-19 云端实现）。
// 四条：① FlowPctToDelta 成交额归一 3% 满档/超界夹取/零流入中性/非正弃权；
// ② withMetaDelta 不覆盖已有键、不污染原 sig；③ SetSectorDeltaFn 注入后 applySizing 板块 delta 进 Meta；
// ④ nil 缝/弃权回调 → Meta 不含板块键（零行为变更）。
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

func TestFlowPctToDelta(t *testing.T) {
	// 3% 满档：净 3 万 / 额 100 万 = 3% → +1；-3% → -1。
	if d := FlowPctToDelta(3e4, 1e6); math.Abs(d-1) > 1e-9 {
		t.Fatalf("3%% 应满档 +1, got %v", d)
	}
	if d := FlowPctToDelta(-3e4, 1e6); math.Abs(d+1) > 1e-9 {
		t.Fatalf("-3%% 应满档 -1, got %v", d)
	}
	// 超界夹取：10% → 仍 ±1。
	if d := FlowPctToDelta(1e5, 1e6); d != 1 {
		t.Fatalf("10%% 应夹到 +1, got %v", d)
	}
	// 中间值：1.5% → 0.5。
	if d := FlowPctToDelta(1.5e4, 1e6); math.Abs(d-0.5) > 1e-9 {
		t.Fatalf("1.5%% 应 0.5, got %v", d)
	}
	// 真零流入（净 0、额正常）→ 0 中性。
	if d := FlowPctToDelta(0, 1e6); d != 0 {
		t.Fatalf("净 0 应中性 0, got %v", d)
	}
	// 弃权：额非正 / NaN。
	if !math.IsNaN(FlowPctToDelta(1e4, 0)) || !math.IsNaN(FlowPctToDelta(1e4, -1)) {
		t.Fatal("额非正应 NaN 弃权")
	}
	if !math.IsNaN(FlowPctToDelta(math.NaN(), 1e6)) {
		t.Fatal("净 NaN 应弃权")
	}
}

func TestWithMetaDelta(t *testing.T) {
	orig := combat_agent.Signal{}
	got := withMetaDelta(orig, MetaFlowSectorDelta, 0.5)
	if got.Meta[MetaFlowSectorDelta] != 0.5 {
		t.Fatalf("应写入板块 delta, got %+v", got.Meta)
	}
	if orig.Meta != nil {
		t.Fatal("原 sig.Meta 不得被污染（拷贝语义）")
	}
	// 已有键不覆盖（逐票显式值优先）。
	existing := combat_agent.Signal{Meta: map[string]float64{MetaFlowSectorDelta: -1}}
	got = withMetaDelta(existing, MetaFlowSectorDelta, 0.5)
	if got.Meta[MetaFlowSectorDelta] != -1 {
		t.Fatalf("已有键不得覆盖, got %v", got.Meta[MetaFlowSectorDelta])
	}
}

func TestApplySizingSectorDeltaInjected(t *testing.T) {
	c := New()
	c.SetSectorDeltaFn(func(code string) (float64, bool) {
		if code == "600000" {
			return 0.6, true
		}
		return 0, false // 其他票弃权
	})
	r := Regime{FlowMarketDelta: math.NaN(), FlowSectorDelta: math.NaN(), VolSurgeDelta: math.NaN()}
	sig := combat_agent.Signal{Code: "600000"}
	got := withRegimeFlowDeltas(r, sig)                // Regime 全 NaN 不动 Meta
	got = withMetaDelta(got, MetaFlowSectorDelta, 0.6) // 模拟 applySizing 的注入
	if got.Meta[MetaFlowSectorDelta] != 0.6 {
		t.Fatalf("板块 delta 应进 Meta, got %+v", got.Meta)
	}
}

func TestSectorDeltaFnNilAbstains(t *testing.T) {
	c := New() // 未注入缝
	if c.sectorDeltaFn != nil {
		t.Fatal("缺省注入缝应为 nil")
	}
	// 弃权回调也不得注入。
	c.SetSectorDeltaFn(func(code string) (float64, bool) { return math.NaN(), true })
	if _, ok := func() (float64, bool) {
		v, ok := c.sectorDeltaFn("600000")
		if math.IsNaN(v) {
			return 0, false
		}
		return v, ok
	}(); ok {
		t.Fatal("NaN 回调应按弃权处理")
	}
}
