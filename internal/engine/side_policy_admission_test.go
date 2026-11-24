// side_policy_admission_test.go — live 自动买入侧策略准入、硬闸证据与逐票金额锁。
package engine

import (
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// enableSidePolicyForTest 装配带当日新鲜快照的测试引擎，避免真实时钟造成假红。
func enableSidePolicyForTest(e *Engine, st signalctl.SidePolicyState) {
	e.SetMembers([]string{"u_1"})
	on := true
	e.SetCfgMgr(config.NewManagerWithRules(&config.Rules{
		Runtime: config.RuntimeConfig{SidePolicyEnabled: &on},
		Sizing:  config.SizingConfig{Enabled: dynBoolPtr(false), BudgetMode: "legacy"},
	}))
	now := time.Now()
	st.Enabled = true
	st.Day = data.TradingDayDate(now)
	st.AsOf, st.AsOfUnix = now, now.Unix()
	e.sideMu.Lock()
	e.sideState = st
	e.sideMu.Unlock()
}

func sideAdmissionState(level int) signalctl.SidePolicyState {
	return signalctl.SidePolicyState{
		Mode: signalctl.SidePolicyBear, Level: level, AllowNew: level == 0, AllowAdd: level == 0,
		TargetMultiplier: 1, LeftWeight: 0.8, RightWeight: 1.2, Reason: "test",
	}
}

func TestLiveSideAdmissionLevelsAndPendingNotHeld(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, nil)
	enableSidePolicyForTest(e, sideAdmissionState(2))
	now := time.Now()
	day := data.TradingDayDate(now)
	// 在途买单不是 held；L2 必须把它当 new 禁掉，而不是给强左侧例外。
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "PEND-SIDE", SignalID: "SIG-PEND-SIDE",
		Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, UserID: "u_1"}); err != nil {
		t.Fatal(err)
	}
	held, known := e.liveSideHeldQty("u_1", "600000")
	if !known || held != 0 {
		t.Fatalf("pending buy must not count as held: qty=%.0f known=%v", held, known)
	}
	sig := combat_agent.Signal{Code: "600000", StrategyID: "fac_118", StrategyType: "fac_118",
		EntryStyle: "left", Score: 90, Threshold: 70}
	ok, reason := e.admitSideBuy("u_1", sig, "new", held, known, now, "live", false)
	if ok || reason != "side_policy_no_new" {
		t.Fatalf("L2 pending-only new must fail: ok=%v reason=%s", ok, reason)
	}
	e.autoPlace(sig, map[string]*data.StockInfo{"600000": {Price: 10}})
	if len(*orders) != 0 {
		t.Fatalf("L2 new must not send, got %d orders", len(*orders))
	}
	if d := e.liveDecisionOf(sig); d == nil || d.Reason != "side_policy_no_new" {
		t.Fatalf("block reason must be observable, got %+v", d)
	}

	// L1 同样禁新，但允许普通 add。
	e.sideMu.Lock()
	e.sideState = sideAdmissionState(1)
	e.sideState.Day = day
	e.sideMu.Unlock()
	ok, reason = e.admitSideBuy("u_1", sig, "new", 0, true, now, "live", false)
	if ok || reason != "side_policy_no_new" {
		t.Fatalf("L1 new must fail: ok=%v reason=%s", ok, reason)
	}
	ok, reason = e.admitSideBuy("u_1", sig, "add", 100, true, now, "live", false)
	if !ok || reason != "" {
		t.Fatalf("L1 ordinary add must pass: ok=%v reason=%s", ok, reason)
	}

	// 异步买单尚未落库时，同轮预占仍建立同票意图。
	e.reserveBuyBudget("u_1", fmt.Sprintf("buy:600002:dragon:%s", day), 1000)
	sig.Code = "600002"
	ok, reason = e.admitSideBuy("u_1", sig, "new", 0, true, now, "live", false)
	if ok || reason != "same_symbol_day" {
		t.Fatalf("same-round reservation must block: ok=%v reason=%s", ok, reason)
	}
}

func TestLiveSideHardGatesPriorityAndEvidenceUnavailable(t *testing.T) {
	e, db, _, _ := newQMTEngine(t, nil)
	enableSidePolicyForTest(e, sideAdmissionState(2))
	now := time.Now()
	day := data.TradingDayDate(now)
	rows := []store.RealOrder{
		{OrderID: "TP-SIDE", SignalID: fmt.Sprintf("sell:600000:tp:%s", day), Code: "600000.SH", Side: "卖出", Status: "已撤", UserID: "u_1"},
		{OrderID: "BUY-SIDE", SignalID: fmt.Sprintf("buy:600000:dragon:%s", day), Code: "600000.SH", Side: "买入", Status: "已报", UserID: "u_1"},
	}
	for _, row := range rows {
		if _, err := db.UpsertRealOrder(row); err != nil {
			t.Fatal(err)
		}
	}
	sig := combat_agent.Signal{Code: "600000", StrategyID: "fac_118", StrategyType: "fac_118",
		EntryStyle: "left", Score: 90, Threshold: 70}
	ok, reason := e.admitSideBuy("u_1", sig, "add", 100, true, now, "live", false)
	if ok || reason != "same_symbol_day" {
		t.Fatalf("same_symbol_day must precede TP and strong-left: ok=%v reason=%s", ok, reason)
	}

	// 仅有 TP 证据时，强左侧也不能穿透回买闸。
	sig.Code = "600001"
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "TP-SIDE-2",
		SignalID: fmt.Sprintf("sell:600001:tt:%s", day), Code: "600001.SH", Side: "卖出", Status: "已成", UserID: "u_1"}); err != nil {
		t.Fatal(err)
	}
	ok, reason = e.admitSideBuy("u_1", sig, "add", 100, true, now, "live", false)
	if ok || reason != "tp_rebuy_block" {
		t.Fatalf("TP must beat strong-left: ok=%v reason=%s", ok, reason)
	}

	// 开关打开但没有柜台账本时，不能把“读不到”伪造为“当日未止盈”。
	e2 := newPinnedEngine()
	enableSidePolicyForTest(e2, sideAdmissionState(0))
	ok, reason = e2.admitSideBuy("u_1", sig, "new", 0, true, now, "live", false)
	if ok || reason != "side_policy_evidence_unavailable" {
		t.Fatalf("missing hard-gate evidence must fail closed: ok=%v reason=%s", ok, reason)
	}
}

func TestLiveSideStrongLeftHeldExceptionAndStability(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, nil)
	enableSidePolicyForTest(e, sideAdmissionState(2))
	now := time.Now()
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600000.SH", Name: "浦发", Qty: 100,
		CostPrice: 10, CurPrice: 10, UserID: "u_1", UpdatedAt: now.Format("2006-01-02 15:04:05")}}); err != nil {
		t.Fatal(err)
	}
	sig := combat_agent.Signal{ID: "SIG-STRONG", Code: "600000", Name: "浦发", Strategy: "因子", StrategyID: "fac_118",
		StrategyType: "fac_118", Direction: "做多", Price: 10, EntryStyle: "left", Score: 90, Threshold: 70}
	key := data.TradingDayDate(now) + "|live|600000|fac_118"
	e.sideMu.Lock()
	e.sideLeftObs = map[string][]signalctl.SideObservation{key: {
		{At: now.Add(-61 * time.Second), Score: 95}, {At: now, Score: 90},
	}}
	e.sideMu.Unlock()
	ok, reason := e.admitSideBuy("u_1", sig, "add", 100, true, now, "live", false)
	if ok || reason != "side_policy_tighten" {
		t.Fatalf("unstable strong-left must fail: ok=%v reason=%s", ok, reason)
	}

	// 分数回落不超过 2 分且间隔足够时，held 强左侧 add 才例外放行。
	e.sideMu.Lock()
	e.sideLeftObs[key] = []signalctl.SideObservation{{At: now.Add(-61 * time.Second), Score: 91}, {At: now, Score: 90}}
	e.sideMu.Unlock()
	e.autoPlace(sig, map[string]*data.StockInfo{"600000": {Price: 10}})
	if len(*orders) != 1 {
		t.Fatalf("stable strong-left held add must send, got %d", len(*orders))
	}
}

func TestLiveLegacyStyleAmountsAndFlowBlend(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	enableSidePolicyForTest(e, sideAdmissionState(0))
	now := time.Now()
	if !e.sidePolicyEnabled() || !e.currentSidePolicy(now).Enabled {
		t.Fatalf("test side policy must be enabled: snapshot=%+v", e.SidePolicySnapshot())
	}
	left := combat_agent.Signal{Code: "600000", Name: "左", Strategy: "因子", StrategyID: "fac_118", StrategyType: "fac_118", Direction: "做多", Action: "buy", Price: 10, EntryStyle: "left"}
	right := combat_agent.Signal{Code: "600001", Name: "右", Strategy: "龙头", Direction: "做多", Action: "buy", Price: 10, EntryStyle: "right"}
	sigs := []combat_agent.Signal{left, right}
	e.annotateSideWeights(sigs, now)
	left, right = sigs[0], sigs[1]
	if left.SideWeight >= right.SideWeight {
		t.Fatalf("left/right transient weights must differ: %.2f/%.2f", left.SideWeight, right.SideWeight)
	}
	for _, sig := range []combat_agent.Signal{left, right} {
		e.autoPlaceScaled(sig, map[string]*data.StockInfo{sig.Code: {Price: 10}}, 0, false, dynBudgetCtx{FlowBlend: 1})
	}
	if len(*orders) != 2 {
		t.Fatalf("two different symbols must send, got %d", len(*orders))
	}
	qtyLeft, qtyRight := (*orders)[0]["qty"].(float64), (*orders)[1]["qty"].(float64)
	if qtyLeft != 800 || qtyRight != 1200 {
		t.Fatalf("legacy amounts must use per-symbol style: left=%.0f right=%.0f", qtyLeft, qtyRight)
	}

	// 账户级 FlowBlend 也进入 legacy 金额路径；SideWeight=0 时不改变它。
	e2, _, _, orders2 := newQMTEngine(t, nil)
	sig := combat_agent.Signal{Code: "600002", Name: "流量", Strategy: "龙头", Direction: "做多", Price: 10}
	e2.autoPlaceScaled(sig, map[string]*data.StockInfo{"600002": {Price: 10}}, 0, false, dynBudgetCtx{FlowBlend: 0.8})
	if len(*orders2) != 1 || (*orders2)[0]["qty"].(float64) != 800 {
		t.Fatalf("legacy FlowBlend must scale amount, got %+v", *orders2)
	}
}
