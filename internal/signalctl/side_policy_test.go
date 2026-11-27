// side_policy_test.go — §SIDE-POLICY-20261124 纯策略行为锁。
package signalctl

import (
	"encoding/json"
	"math"
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
	// 2026-11-25 owner：资金流未齐 ⇒ 默认 bear L1（禁新仓），指数单腿绝不能
	// 把剧本翻成 bull，也不能借"range"名义把动态仓位打满。
	// 2026-11-25 owner 修正：资金流未齐 ⇒ 冷启动 bear 剧本，但允许 new/add（35% 动态仓位）。
	if st.Mode != SidePolicyBear || !st.AllowNew || !st.AllowAdd || st.TargetMultiplier != 1.0 {
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

// ---------------------------------------------------------------------------
// D6-a 2026-11-25：极强右侧回买（owner：「极强的一次买入机会出现，可以重新买入」）
//
// 这七枚锁的是一个**只解一条闸**的例外，所以每一条都在问同一个问题：
// 「它解掉的是 tp_rebuy_block，还是顺手把别的闸也解了？」
// 允许多解一条，就等于当天把同一只票买两次（止盈过的票原价回买是最常见的亏钱形状）。
// ---------------------------------------------------------------------------

// sideBullState 造一个 L1（只禁 new、放行 add）的牛市状态：
// 这样"tp_rebuy_block 被解掉"与"side_policy_no_new 仍在"能同时观察到。
func sideBullL1State() SidePolicyState {
	return SidePolicyState{
		Enabled: true, Mode: SidePolicyBull, Level: 1,
		AllowNew: true, AllowAdd: true,
		TargetMultiplier: config.CarrierSideBullTargetL1(),
		LeftWeight:       1.0, RightWeight: 1.0,
		Reason: "bull_l1",
	}
}

// ⑪ 极强右侧 + new + 当天止盈过 + sameSymbolDay=false ⇒ 放行。
// 这是 owner 要的那一次「重新买入」，也是这一批唯一放宽的行为。
func TestStrongRightRebuyReleasesTPBlockForNew(t *testing.T) {
	st := sideBullL1State()
	sig := SideSignal{EntryStyle: "right", Score: 85, Threshold: 70}
	// 门槛 + margin：判据是"高出多少"，不是"绝对值多少"——阈值 90 时 85 分不算极强。
	ok, why := BuyAdmission(st, "new", 0, sig, true, false, false)
	if !ok {
		t.Fatalf("strong right rebuy must be admitted, got why=%s", why)
	}
	if !strongRightRebuy(sig) {
		t.Fatal("strongRightRebuy helper disagrees with BuyAdmission")
	}
}

// ⑫ 分数只差一分就不算极强：门槛必须真的被用上，不许写成 >= Threshold。
func TestStrongRightRebuyNeedsTheMargin(t *testing.T) {
	sig := SideSignal{EntryStyle: "right", Score: 70 + config.CarrierSideRightRebuyMargin() - 0.5, Threshold: 70}
	if strongRightRebuy(sig) {
		t.Fatalf("score %.1f must stay below threshold+margin", sig.Score)
	}
	if ok, why := BuyAdmission(sideBullL1State(), "new", 0, sig, true, false, false); ok || why != "tp_rebuy_block" {
		t.Fatalf("sub-margin score must keep the TP block, got ok=%v why=%s", ok, why)
	}
}

// ⑬ 左侧入场再强也不走这条：稳定性是左侧的概念，右侧买点要的是强度。
// （强左侧已有自己的 add 例外，混用会让"左侧 add"变成"任一侧 new"。）
func TestStrongRightRebuyRejectsLeftStyle(t *testing.T) {
	sig := SideSignal{EntryStyle: "left", Score: 99, Threshold: 70}
	if strongRightRebuy(sig) {
		t.Fatal("left style must never take the right-side escape hatch")
	}
	if ok, why := BuyAdmission(sideBullL1State(), "new", 0, sig, true, false, true); ok || why != "tp_rebuy_block" {
		t.Fatalf("left style must keep the TP block, got ok=%v why=%s", ok, why)
	}
}

// ⑭ 只对 intent == "new"：add 的例外是强左侧那条，右侧不许再开一扇门
// （否则 L2 会退化成"任一侧、分数够"）。
func TestStrongRightRebuyOnlyForNewIntent(t *testing.T) {
	sig := SideSignal{EntryStyle: "right", Score: 99, Threshold: 70}
	if ok, why := BuyAdmission(sideBullL1State(), "add", 100, sig, true, false, false); ok || why != "tp_rebuy_block" {
		t.Fatalf("add intent must keep the TP block, got ok=%v why=%s", ok, why)
	}
}

// ⑮ same_symbol_day 排在 tpFired 外面：同一天已经买过的票，极强也不用再买一次。
// 这是"当天不许重复下单"这条硬闸，与止盈无关。
func TestStrongRightRebuyCannotBypassSameSymbolDay(t *testing.T) {
	sig := SideSignal{EntryStyle: "right", Score: 99, Threshold: 70}
	if ok, why := BuyAdmission(sideBullL1State(), "new", 0, sig, true, true, false); ok || why != "same_symbol_day" {
		t.Fatalf("same_symbol_day must win over the rebuy hatch, got ok=%v why=%s", ok, why)
	}
}

// ⑯ 熊市 L1 禁 new 仍然生效：例外只解 tp_rebuy_block，解不掉侧策略自己的准入。
// 这一枚是 §239 ㊳ 的 Go 侧对应物——bear 当天绝不因为"分数高"就放开新开仓。
func TestBearL1NeverReleasesNew(t *testing.T) {
	bear := SidePolicyState{
		Enabled: true, Mode: SidePolicyBear, Level: 1,
		AllowNew: false, AllowAdd: true,
		TargetMultiplier: config.CarrierSideBearTargetL1(),
		LeftWeight:       config.CarrierSideWeightLo(),
		RightWeight:      config.CarrierSideWeightLo(),
		Reason:           "bear_l1",
	}
	strongest := SideSignal{EntryStyle: "right", Score: 100, Threshold: 60}
	// 没止盈过也一样被拦：这条闸与 tpFired 无关。
	if ok, why := BuyAdmission(bear, "new", 0, strongest, false, false, false); ok || why != "side_policy_no_new" {
		t.Fatalf("bear L1 must block new, got ok=%v why=%s", ok, why)
	}
	// 止盈过时也绝不允许一路放行。注意这里的 why 会是 `side_policy_no_new` 而不是
	// `tp_rebuy_block`：D6-a 的例外只是**穿过**了止盈那条闸，紧接着就撞上 bear L1 自己
	// 的准入闸 ⇒ 说明两层是串联的、不是互相顶替的（谁先说"不"都算拦下）。
	if ok, why := BuyAdmission(bear, "new", 0, strongest, true, false, false); ok ||
		(why != "tp_rebuy_block" && why != "side_policy_no_new") {
		t.Fatalf("bear L1 + tpFired must block, got ok=%v why=%s", ok, why)
	}
}

// ⑰ 开关与读数缺失都要 fail-closed：关掉 D6-a ⇒ 例外消失；
// 缺 score / 缺 threshold / threshold<=0 ⇒ 一律不放行（"看不见"不许变成"可以买"）。
func TestStrongRightRebuyFailsClosed(t *testing.T) {
	if !config.CarrierSideRightRebuyEnabled() {
		t.Skip("D6-a is shipped ON (owner 2026-11-25); this test documents the OFF direction")
	}
	for name, sig := range map[string]SideSignal{
		"missing_score":     {EntryStyle: "right", Threshold: 70},
		"missing_threshold": {EntryStyle: "right", Score: 99},
		"zero_threshold":    {EntryStyle: "right", Score: 99, Threshold: 0},
		"empty_style":       {Score: 99, Threshold: 70},
	} {
		if strongRightRebuy(sig) {
			t.Fatalf("%s must fail closed", name)
		}
	}
}

// 2026-11-25 owner：开盘默认 bear 只是数据未齐的临时剧本——允许 new/add，
// 总动态仓位封 35%（bear AM ramp 0.35 × TM 1.0）；缺失输入一律按弃权，
// 绝不落 range 满仓。
func TestColdStartDefaultsToBear(t *testing.T) {
	now := time.Date(2026, 11, 25, 9, 35, 0, 0, time.FixedZone("CST", 8*3600))
	cases := []SidePolicyInput{
		// 指数缺口有读数，但资金流腿未回（fflow 熔断时的现场形状）。
		{Enabled: true, Now: now, AsOf: now, IndexValid: true, GapPct: -0.46},
		// 资金流刚回但斜率还没凑够两桶。
		{Enabled: true, Now: now, AsOf: now, FlowValid: true, MarketDelta: 0.3,
			SlopeDelta: math.NaN(), IndexValid: true},
	}
	for i, in := range cases {
		st := DecideSidePolicy(in)
		// 2026-11-27 F1：case 0 仍是真缺数（bear/35%）；case 1 有读数但没斜率，
		// 不再误判成冷启动，按 range_regime 走（AllowNew/Add 与 TM 不变）。
		if i == 0 {
			if st.Mode != SidePolicyBear || !st.AllowNew || !st.AllowAdd || st.TargetMultiplier != 1.0 ||
				st.Reason != "side_policy_cold_start_bear" {
				t.Fatalf("case %d: cold start got mode=%s new=%v add=%v tm=%v reason=%s",
					i, st.Mode, st.AllowNew, st.AllowAdd, st.TargetMultiplier, st.Reason)
			}
			continue
		}
		if st.Mode != SidePolicyRange || st.Reason != "range_regime" || !st.AllowNew || !st.AllowAdd {
			t.Fatalf("case %d: flow without slope must be range_regime, got mode=%s new=%v add=%v reason=%s",
				i, st.Mode, st.AllowNew, st.AllowAdd, st.Reason)
		}
	}
}
