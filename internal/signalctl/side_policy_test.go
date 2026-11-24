// side_policy_test.go — §SIDE-POLICY-20261124 纯策略行为锁。
package signalctl

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func sideInput() SidePolicyInput {
	now := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	return SidePolicyInput{Enabled: true, Now: now, AsOf: now, IndexValid: true, FlowValid: true}
}

// TestSidePolicyJSONForPython 由跨语言用例驱动，输出真实 Go JSON，不提供手写夹具。
func TestSidePolicyJSONForPython(t *testing.T) {
	if os.Getenv("SIDE_POLICY_JSON_FOR_PYTHON") != "1" {
		t.Skip("only used by the Go-to-Python decode contract test")
	}
	in := sideInput()
	st := DecideSidePolicy(in)
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"latch_until_ts":0`) {
		t.Fatalf("no-latch state must explicitly carry numeric zero: %s", raw)
	}
	t.Logf("SIDE_POLICY_JSON=%s", raw)
}

func TestSidePolicyDisabledEqualsLegacy(t *testing.T) {
	in := sideInput()
	in.Enabled = false
	in.MarketDelta = -1
	st := DecideSidePolicy(in)
	if st.Enabled || st.Mode != SidePolicyLegacy || !st.AllowNew || !st.AllowAdd ||
		st.TargetMultiplier != 1 || st.LeftWeight != 1 || st.RightWeight != 1 {
		t.Fatalf("disabled must be byte-equivalent legacy, got %+v", st)
	}
}

func TestSidePolicyBearStrongOutflowBlocksNewOnly(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.MarketDelta = -config.CarrierSideFlowStrongDelta()
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBear || st.AllowNew || !st.AllowAdd || st.TargetMultiplier != 1 {
		t.Fatalf("strong outflow must be L1 (new only), got %+v", st)
	}
}

// TestSidePolicyBearSlopeTightensTargetAndBlocksAdd 验证斜率触发 L2，深度只改变目标档。
func TestSidePolicyBearSlopeTightensTargetAndBlocksAdd(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.SlopeDelta = -config.CarrierSideFlowSlopeStep()
	st := DecideSidePolicy(in)
	if st.Level != 2 || st.AllowNew || st.AllowAdd || st.TargetMultiplier != config.CarrierSideBearTargetL1() {
		t.Fatalf("worsening slope must select L2 first target tier, got %+v", st)
	}
	in.SlopeDelta = -2 * config.CarrierSideFlowSlopeStep()
	st = DecideSidePolicy(in)
	if st.Level != 2 || st.AllowAdd || st.TargetMultiplier != config.CarrierSideBearTargetL2() {
		t.Fatalf("deep worsening slope must keep L2 and select second target tier, got %+v", st)
	}
}

func TestSidePolicyBearHighGapDownweightsRight(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.GapPct = config.CarrierSideGapHighPct()
	st := DecideSidePolicy(in)
	if st.Level != 2 || st.AllowAdd || st.TargetMultiplier != config.CarrierSideBearTargetL1() ||
		st.RightWeight != config.CarrierSideWeightLo() || st.LeftWeight != 1 {
		t.Fatalf("high gap without inflow confirmation must select L2 and penalize right, got %+v", st)
	}
}

func TestSidePolicyLowGapInflowBoostsLeft(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.GapPct = config.CarrierSideGapLowPct()
	in.MarketDelta = 0.1
	st := DecideSidePolicy(in)
	if st.LeftWeight != config.CarrierSideWeightHi() || st.RightWeight != 1 {
		t.Fatalf("low gap + positive flow must boost left only, got %+v", st)
	}
}

// TestSidePolicyBullMirrorRequiresFullConfirmation 锁定牛市镜像必须多项共振。
func TestSidePolicyBullMirrorRequiresFullConfirmation(t *testing.T) {
	in := sideInput()
	in.MarketState = "bull"
	in.MarketDelta = config.CarrierSideFlowStrongDelta()
	in.SlopeDelta = config.CarrierSideFlowSlopeStep()
	in.GapPct = config.CarrierSideGapHighPct()
	in.IndexChangePct = config.CarrierSidePMIndexRisePct()
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBull || st.Level != 3 || st.TargetMultiplier != config.CarrierSideBullTargetL3() ||
		st.RightWeight != config.CarrierSideWeightHi() || !st.AllowNew || !st.AllowAdd {
		t.Fatalf("full bull mirror must loosen and boost right, got %+v", st)
	}
	in.MarketDelta = 0.1
	st = DecideSidePolicy(in)
	if st.Mode == SidePolicyBull {
		t.Fatalf("single positive reading must not create bull mirror, got %+v", st)
	}
}

// TestSidePolicyBullLevelsUseIndexRiseWithoutNewGate 验证指数上涨只分档，不另设准入门。
func TestSidePolicyBullLevelsUseIndexRiseWithoutNewGate(t *testing.T) {
	in := sideInput()
	in.MarketState = "bull"
	in.MarketDelta = config.CarrierSideFlowStrongDelta()
	in.SlopeDelta = config.CarrierSideFlowSlopeStep()
	in.IndexChangePct = config.CarrierSidePMIndexRisePct() - 0.01
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBull || st.Level != 1 || st.TargetMultiplier != config.CarrierSideBullTargetL1() ||
		!st.AllowNew || !st.AllowAdd {
		t.Fatalf("flow-confirmed bull without index rise must reach L1, got %+v", st)
	}
	in.IndexChangePct = config.CarrierSidePMIndexRisePct()
	st = DecideSidePolicy(in)
	if st.Level != 2 || st.TargetMultiplier != config.CarrierSideBullTargetL2() {
		t.Fatalf("index rise must select L2 without changing admission, got %+v", st)
	}
	in.GapPct = config.CarrierSideGapHighPct()
	st = DecideSidePolicy(in)
	if st.Level != 3 || st.TargetMultiplier != config.CarrierSideBullTargetL3() {
		t.Fatalf("index rise + high gap must select L3, got %+v", st)
	}
}

// TestSidePolicyInvalidInputsAbstainAndPartialConfirmationDoesNotFlip 验证无效输入弃权且部分确认不翻转。
func TestSidePolicyInvalidInputsAbstainAndPartialConfirmationDoesNotFlip(t *testing.T) {
	in := sideInput()
	in.MarketState = "bull"
	in.MarketDelta = config.CarrierSideFlowStrongDelta()
	in.SlopeDelta = config.CarrierSideFlowSlopeStep()
	in.GapPct = config.CarrierSideGapHighPct()
	in.IndexChangePct = config.CarrierSidePMIndexRisePct()
	in.PMFlowIntensityDelta = -1
	in.PMAnchorValid = true
	in.FlowValid = false
	st := DecideSidePolicy(in)
	if st.Mode == SidePolicyBull || st.MarketDelta != 0 || st.SlopeDelta != 0 || st.PMFlowIntensityDelta != 0 {
		t.Fatalf("invalid flow must abstain even when cached numbers are finite: %+v", st)
	}
	in = sideInput()
	in.MarketState = "bull"
	in.MarketDelta = config.CarrierSideFlowStrongDelta()
	in.SlopeDelta = config.CarrierSideFlowSlopeStep()
	in.GapPct = config.CarrierSideGapHighPct()
	in.IndexChangePct = config.CarrierSidePMIndexRisePct()
	in.IndexValid = false
	st = DecideSidePolicy(in)
	if st.Mode != SidePolicyBull || st.Level != 1 || st.GapPct != 0 || st.RightWeight != 1 {
		t.Fatalf("invalid index must not raise bull tier or apply gap weight: %+v", st)
	}
	in = sideInput()
	in.IndexChangePct = config.CarrierSidePMIndexRisePct()
	in.FlowValid = false
	st = DecideSidePolicy(in)
	if st.Mode == SidePolicyBull || !st.AllowNew || !st.AllowAdd {
		t.Fatalf("single index rise must not flip mode or admission: %+v", st)
	}
}

func TestSidePolicyBearWinsConflict(t *testing.T) {
	in := sideInput()
	in.MarketState = "bull"
	in.MarketDelta = -config.CarrierSideFlowStrongDelta()
	in.SlopeDelta = config.CarrierSideFlowSlopeStep()
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBear || st.AllowNew {
		t.Fatalf("bear hard condition must win conflict, got %+v", st)
	}
}

func TestSidePolicyLatchBlocksNewUntil1400(t *testing.T) {
	in := sideInput()
	in.Now = time.Date(2026, 11, 24, 13, 30, 0, 0, in.Now.Location())
	in.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00", Reason: "pm1300_index_drop"}
	st := DecideSidePolicy(in)
	if st.AllowNew || !st.AllowAdd {
		t.Fatalf("latch must block new only, got %+v", st)
	}
	in.Now = time.Date(2026, 11, 24, 14, 0, 0, 0, in.Now.Location())
	st = DecideSidePolicy(in)
	if !st.AllowNew {
		t.Fatalf("latch must expire at 14:00, got %+v", st)
	}
}

func TestSidePolicyStaleAggressiveFailsClosed(t *testing.T) {
	in := sideInput()
	in.MarketState, in.MarketDelta, in.SlopeDelta = "bull", 0.8, 0.2
	st := DecideSidePolicy(in)
	st.AsOf = in.Now.Add(-time.Duration(config.CarrierSidePolicyTTLSec()+1) * time.Second)
	st = ApplySidePolicyFreshness(st, in.Now)
	if st.Mode != SidePolicyBear || st.AllowNew || st.Reason != "side_policy_stale" {
		t.Fatalf("stale aggressive state must fail closed to no-new, got %+v", st)
	}
}

// TestSidePolicyRestrictiveStateExpiresToL1KeepingAdd 验证过期限制态当日保留且不加权。
func TestSidePolicyRestrictiveStateExpiresToL1KeepingAdd(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.MarketDelta = -config.CarrierSideFlowStrongDelta()
	st := DecideSidePolicy(in)
	st.AsOf = in.Now.Add(-2 * time.Hour)
	st = ApplySidePolicyFreshness(st, in.Now)
	if st.Mode != SidePolicyBear || st.Level != 1 || st.AllowNew || !st.AllowAdd || st.Reason != "bear_no_new" {
		t.Fatalf("expired restrictive state must persist for the day without loosening, got %+v", st)
	}
}

// TestSidePolicyFreshnessMissingAsOfFailsClosed 验证启用态缺时刻或未来时刻均 fail-closed。
func TestSidePolicyFreshnessMissingAsOfFailsClosed(t *testing.T) {
	in := sideInput()
	in.MarketState = "bull"
	in.MarketDelta = 0.8
	in.SlopeDelta = 0.2
	st := DecideSidePolicy(in)
	st.AsOf = time.Time{}
	st = ApplySidePolicyFreshness(st, in.Now)
	if st.Level != 1 || st.AllowNew || !st.AllowAdd || st.Reason != "side_policy_asof_missing" {
		t.Fatalf("enabled state without AsOf must fail closed to L1, got %+v", st)
	}
	st.AsOf = in.Now.Add(time.Duration(config.CarrierSidePolicyTTLSec() * float64(time.Second)))
	if got := ApplySidePolicyFreshness(st, in.Now); got.Reason != "side_policy_asof_invalid" {
		t.Fatalf("future AsOf must fail closed, got %+v", got)
	}
}

func TestSidePolicyTargetMultiplierDoesNotTouchMarketScale(t *testing.T) {
	in := sideInput()
	in.SlopeDelta = -0.2
	st := DecideSidePolicy(in)
	if got := TargetMultiplierForIntent(st, "new"); got != config.CarrierSideBearTargetL1() {
		t.Fatalf("strategic new must use policy TM, got %.3f", got)
	}
	if got := TargetMultiplierForIntent(st, "add"); got != config.CarrierSideBearTargetL1() {
		t.Fatalf("strategic add must use policy TM, got %.3f", got)
	}
	if got := TargetMultiplierForIntent(st, "t_leg"); got != 1 {
		t.Fatalf("T must force TM=1, got %.3f", got)
	}
}

func TestSidePolicyStyleWeightCombinedAndCapped(t *testing.T) {
	if got := CombineSideWeight(1.2, 1.2); got != config.CarrierSideWeightHi() {
		t.Fatalf("combined upside must cap at %.2f, got %.3f", config.CarrierSideWeightHi(), got)
	}
	if got := CombineSideWeight(0.8, 0.8); got != config.CarrierSideWeightLo() {
		t.Fatalf("combined downside must cap at %.2f, got %.3f", config.CarrierSideWeightLo(), got)
	}
	if got := CombineSideWeight(1.1, 1); got != 1.1 {
		t.Fatalf("neutral style must preserve flow blend, got %.3f", got)
	}
}

// TestStrongLeftSignalScoreAndStability 验证分数强度和连续观察缺一不可。
func TestStrongLeftSignalScoreAndStability(t *testing.T) {
	now := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	sig := SideSignal{EntryStyle: "left", Score: 85, Threshold: 70}
	obs := []SideObservation{{At: now.Add(-time.Minute), Score: 86}, {At: now, Score: 85}}
	if !StrongLeftSignal(sig, 100, false, obs, now) {
		t.Fatal("strong stable left signal should pass")
	}
	if StrongLeftSignal(sig, 100, true, obs, now) {
		t.Fatal("TP-fired name must not pass")
	}
	if StrongLeftSignal(SideSignal{EntryStyle: "left", Score: 85}, 100, false, obs, now) {
		t.Fatal("missing threshold must not pass")
	}
	obs[1].At = now.Add(-30 * time.Second)
	if StrongLeftSignal(sig, 100, false, obs, now) {
		t.Fatal("insufficient confirmation gap must not pass")
	}
}

func TestStrongLeftSignalRejectsNoScoreOrTP(t *testing.T) {
	now := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	obs := []SideObservation{{At: now.Add(-time.Minute), Score: 90}, {At: now, Score: 90}}
	if StrongLeftSignal(SideSignal{EntryStyle: "left", Threshold: 70}, 100, false, obs, now) {
		t.Fatal("missing score must not pass")
	}
	if StrongLeftSignal(SideSignal{EntryStyle: "right", Score: 90, Threshold: 70}, 100, false, obs, now) {
		t.Fatal("right style must not pass left exception")
	}
}

func TestBuyAdmissionCannotBypassHardGates(t *testing.T) {
	st := DecideSidePolicy(sideInput())
	sig := SideSignal{EntryStyle: "left", Score: 90, Threshold: 70}
	if ok, why := BuyAdmission(st, "add", 100, sig, true, false, true); ok || why != "tp_rebuy_block" {
		t.Fatalf("TP hard gate must win, got ok=%v why=%s", ok, why)
	}
	if ok, why := BuyAdmission(st, "add", 100, sig, false, true, true); ok || why != "same_symbol_day" {
		t.Fatalf("same-symbol hard gate must win, got ok=%v why=%s", ok, why)
	}
	if ok, _ := BuyAdmission(st, "t_leg", 100, sig, true, true, false); !ok {
		t.Fatal("T leg must remain exempt")
	}
}

// TestComputeSizingWithBlendCarriesSidePolicy 锁定目标乘子与风格权重进入定权结果。
func TestComputeSizingWithBlendCarriesSidePolicy(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.SlopeDelta = -0.2
	in.GapPct = config.CarrierSideGapHighPct()
	st := DecideSidePolicy(in)
	r := Regime{SizeCfg: config.DefaultSizingConfig(), SidePolicy: st}
	res := ComputeSizingWithBlend(r, combat_agent.Signal{EntryStyle: "right"})
	if res.TargetMultiplier != config.CarrierSideBearTargetL1() || res.SidePolicyMode != string(SidePolicyBear) || res.SidePolicyLevel != 2 {
		t.Fatalf("sizing result must carry policy target/mode, got %+v", res)
	}
	if res.StyleWeight != config.CarrierSideWeightLo() {
		t.Fatalf("right style must use bear penalty, got %.3f", res.StyleWeight)
	}
	if res.FlowBlend < config.CarrierSideWeightLo() || res.FlowBlend > config.CarrierSideWeightHi() {
		t.Fatalf("combined blend must stay capped, got %.3f", res.FlowBlend)
	}
}
