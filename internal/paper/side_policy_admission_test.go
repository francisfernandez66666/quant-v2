// side_policy_admission_test.go — 纸面自动 add 准入与逐票 style 金额锁。
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

func paperSideState(level int) signalctl.SidePolicyState {
	return signalctl.SidePolicyState{Enabled: true, Mode: signalctl.SidePolicyBear, Level: level,
		AllowNew: level == 0, AllowAdd: level == 0, TargetMultiplier: 1,
		LeftWeight: 0.8, RightWeight: 1.2, Reason: "test"}
}

func TestPaperSidePolicyDisabledKeepsHeldSkipAndManualUnchanged(t *testing.T) {
	e := New(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 10000}, "")
	if err := e.BuyEx("600000", "浦发", "龙头", 10, 10, 1000, nil); err != nil {
		t.Fatal(err)
	}
	before := e.positions["600000"].Qty
	// disabled 时即使没有准入回调，已持仓自动信号仍原样跳过。
	e.OnSignals([]combat_agent.Signal{{Code: "600000", Action: "buy", Direction: "做多", Price: 10}},
		map[string]*data.StockInfo{"600000": {Price: 10}})
	if got := e.positions["600000"].Qty; got != before {
		t.Fatalf("disabled held auto-buy must stay skipped: %d -> %d", before, got)
	}
	// 手动加仓不接侧策略，仍按用户意图执行。
	if err := e.BuyEx("600000", "浦发", "龙头", 10, 10, 100, nil); err != nil {
		t.Fatalf("manual add must not be gated by side policy: %v", err)
	}
	if got := e.positions["600000"].Qty; got != before+100 {
		t.Fatalf("manual add qty=%d want %d", got, before+100)
	}
}

func TestPaperSideAdmissionAllowsLegalAddAndFailClosesWithoutEvidence(t *testing.T) {
	e := New(dynPaperConfig(100000), "")
	if err := e.BuyEx("600000", "浦发", "龙头", 10, 10, 1000, nil); err != nil {
		t.Fatal(err)
	}
	e.SetSizingContext(1, 0.1, 0.3, 1, false, nil, 1, 1)
	st := paperSideState(2)
	e.SetSideAdmission(st, nil)
	sig := combat_agent.Signal{Code: "600000", Name: "浦发", Strategy: "因子", StrategyID: "fac_118",
		Direction: "做多", Action: "buy", Price: 10,
		EntryStyle: "left", Score: 90, Threshold: 70, Confidence: 0.8}
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600000": {Price: 10}})
	if got := e.positions["600000"].Qty; got != 1000 {
		t.Fatalf("missing evidence must fail closed, qty=%d", got)
	}
	if e.lastBuyReject["600000"] != "side_policy_evidence_unavailable" {
		t.Fatalf("missing evidence reason=%q", e.lastBuyReject["600000"])
	}

	// 第二轮安装可观测准入口，确认合法强左侧加仓确实进入撮合链。
	called := false
	e.SetSideAdmission(st, func(got combat_agent.Signal, intent string, held float64, sameDay bool, now time.Time) (bool, string) {
		called = true
		if intent != "add" || held != 1000 || sameDay {
			t.Fatalf("admission inputs intent=%s held=%.0f sameDay=%v", intent, held, sameDay)
		}
		if got.Confidence != 0.8 || got.SideWeight <= 0 {
			t.Fatalf("confidence/style must pass through: %+v", got)
		}
		return signalctl.BuyAdmission(st, intent, held, signalctl.SideSignal{EntryStyle: got.EntryStyle,
			Score: got.Score, Threshold: got.Threshold}, false, sameDay, true)
	})
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600000": {Price: 10}})
	if !called || e.positions["600000"].Qty <= 1000 {
		t.Fatalf("legal strong-left add must enter auto chain: called=%v qty=%d", called, e.positions["600000"].Qty)
	}
}

func TestPaperSideStyleAmountsAndBlendCap(t *testing.T) {
	e := New(dynPaperConfig(100000), "")
	e.SetSizingContext(1, 0.1, 0.3, 1, false, nil, 1, 1)
	st := paperSideState(0)
	e.SetSideAdmission(st, func(sig combat_agent.Signal, intent string, held float64, sameDay bool, now time.Time) (bool, string) {
		if sig.Confidence != 0.7 || e.sizingCtx.MarketScale != 1 {
			t.Fatalf("style must not alter confidence/MS: confidence=%.2f ms=%.2f", sig.Confidence, e.sizingCtx.MarketScale)
		}
		return true, ""
	})
	left := combat_agent.Signal{Code: "600000", Name: "左", Strategy: "因子", StrategyType: "", Direction: "做多", Action: "buy",
		Price: 10, EntryStyle: "left", Confidence: 0.7, SizeScale: 0.8, SideWeight: 0.8}
	right := combat_agent.Signal{Code: "600001", Name: "右", Strategy: "龙头", StrategyType: "", Direction: "做多", Action: "buy",
		Price: 10, EntryStyle: "right", Confidence: 0.7, SizeScale: 0.8, SideWeight: 1.2}
	e.OnSignals([]combat_agent.Signal{left, right}, map[string]*data.StockInfo{
		"600000": {Price: 10}, "600001": {Price: 10},
	})
	qtyLeft, qtyRight := e.positions["600000"].Qty, e.positions["600001"].Qty
	if qtyLeft <= 0 || qtyRight <= qtyLeft {
		t.Fatalf("same-account left/right amounts must differ: %d/%d", qtyLeft, qtyRight)
	}

	// 账户 FlowBlend 与逐票 StyleWeight 合成后仍封顶 1.20，不突破组合边界。
	e2 := New(dynPaperConfig(100000), "")
	e2.cfg.SingleStockCapPct = 100
	e2.singleStockLimitPct = 100
	e2.sectorLimitPct = 100
	e2.SetSizingContext(1, 0.1, 0.3, 1, false, nil, 1.1, 1)
	neutral, _ := e2.dynamicPaperQtyStyleLocked("", "600002", 10, 1, 0, 1)
	capped, _ := e2.dynamicPaperQtyStyleLocked("", "600002", 10, 1, 0, 1.2)
	if neutral != 3300 || capped != 3600 {
		t.Fatalf("combined blend must cap at 1.20: neutral=%d capped=%d", neutral, capped)
	}
}
