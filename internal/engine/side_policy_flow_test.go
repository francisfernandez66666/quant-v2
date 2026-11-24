// side_policy_flow_test.go — §SIDE-POLICY 固定桶、午休断斜率、CNY 分母与 13:00 独立加急锁。
package engine

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

func sideTestTime(h, m int) time.Time {
	return time.Date(2026, 11, 24, h, m, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestSideFlowBucketsExcludeLunchAndFirstPartial 锁定残桶与午休不污染斜率。
func TestSideFlowBucketsExcludeLunchAndFirstPartial(t *testing.T) {
	s := newSideFlowState()
	net := 0.0
	amt := 100.0
	for _, tm := range []struct{ h, m int }{{9, 31}, {10, 0}, {10, 30}, {11, 0}, {11, 30}, {13, 0}, {13, 30}} {
		net += 10
		amt += 100
		s.observe(sideTestTime(tm.h, tm.m), sideFlowSample{Net: net, Amount: amt, Source: "em:000001.SH", At: sideTestTime(tm.h, tm.m)}, net/amt)
	}
	if len(s.Buckets) != 5 || !s.Buckets[0].Partial {
		t.Fatalf("want 5 buckets with first partial, got %+v", s.Buckets)
	}
	if !math.IsNaN(s.slopeDelta()) {
		t.Fatalf("slope after lunch must abstain, got %.4f", s.slopeDelta())
	}
	s.observe(sideTestTime(14, 0), sideFlowSample{Net: 90, Amount: 900, Source: "em:000001.SH", At: sideTestTime(14, 0)}, 0.1)
	if len(s.Buckets) != 6 || math.IsNaN(s.slopeDelta()) {
		t.Fatalf("slope must resume on adjacent afternoon buckets, got %+v", s.Buckets)
	}
}

func TestSideFlowBucketLengthAndToleranceFollowKnob(t *testing.T) {
	idx, start, end, ok := sideBucketWindowAt(sideTestTime(10, 1), 15)
	if !ok || idx != 2 || !start.Equal(sideTestTime(10, 0)) || !end.Equal(sideTestTime(10, 15)) {
		t.Fatalf("15-minute knob must move bucket boundary, got idx=%d start=%s end=%s ok=%v", idx, start, end, ok)
	}
	if !sideBucketDurationOK(14*time.Minute, 15) || sideBucketDurationOK(10*time.Minute, 15) {
		t.Fatal("15-minute bucket tolerance must scale with the knob")
	}
	if !sideBucketDurationOK(25*time.Minute, 30) || !sideBucketDurationOK(35*time.Minute, 30) ||
		sideBucketDurationOK(24*time.Minute, 30) || sideBucketDurationOK(36*time.Minute, 30) {
		t.Fatal("30-minute bucket must keep the established +/-5 minute tolerance")
	}
}

// TestSideFlowBucketsDoNotRequireExactBoundarySamples 验证边界两侧样本也能收口相邻桶。
func TestSideFlowBucketsDoNotRequireExactBoundarySamples(t *testing.T) {
	s := newSideFlowState()
	for _, x := range []struct {
		t           time.Time
		net, amount float64
	}{
		{sideTestTime(9, 31), 0, 100},
		{sideTestTime(9, 59), 10, 200},
		{sideTestTime(10, 1), 30, 400},
		{sideTestTime(10, 29), 40, 500},
		{sideTestTime(10, 31), 70, 800},
		{sideTestTime(10, 59), 80, 900},
		{sideTestTime(11, 1), 110, 1100},
	} {
		s.observe(x.t, sideFlowSample{Net: x.net, Amount: x.amount, Source: "em:000001.SH", At: x.t}, 0)
	}
	if len(s.Buckets) != 3 || !s.Buckets[0].Partial || s.Buckets[1].Partial || s.Buckets[2].Partial {
		t.Fatalf("09:59/10:01 straddling samples must close three buckets, got %+v", s.Buckets)
	}
	if !s.Buckets[0].End.Equal(sideTestTime(10, 0)) || !s.Buckets[1].Start.Equal(sideTestTime(10, 0)) ||
		!s.Buckets[1].End.Equal(sideTestTime(10, 30)) || !s.Buckets[2].End.Equal(sideTestTime(11, 0)) {
		t.Fatalf("bucket windows must use configured half-open boundaries, got %+v", s.Buckets)
	}
	if math.IsNaN(s.slopeDelta()) {
		t.Fatalf("adjacent complete buckets must resume slope without exact boundary samples: %+v", s.Buckets)
	}
}

// TestSideFlowLunchBoundaryWithoutExactSamples 验证午休断斜率且下午相邻桶能恢复。
func TestSideFlowLunchBoundaryWithoutExactSamples(t *testing.T) {
	s := newSideFlowState()
	for _, x := range []struct {
		t           time.Time
		net, amount float64
	}{
		{sideTestTime(11, 0), 0, 100},
		{sideTestTime(11, 29), 10, 200},
		{sideTestTime(13, 0), 30, 400},
	} {
		s.observe(x.t, sideFlowSample{Net: x.net, Amount: x.amount, Source: "em:000001.SH", At: x.t}, 0)
	}
	if len(s.Buckets) != 1 || !s.Buckets[0].End.Equal(sideTestTime(11, 30)) {
		t.Fatalf("11:29/13:00 must close the morning bucket at 11:30, got %+v", s.Buckets)
	}
	for _, x := range []struct {
		t           time.Time
		net, amount float64
	}{
		{sideTestTime(13, 29), 40, 500},
		{sideTestTime(13, 31), 70, 800},
	} {
		s.observe(x.t, sideFlowSample{Net: x.net, Amount: x.amount, Source: "em:000001.SH", At: x.t}, 0)
	}
	if !math.IsNaN(s.slopeDelta()) {
		t.Fatalf("lunch gap must not become a slope, got %+v", s.Buckets)
	}
	for _, x := range []struct {
		t           time.Time
		net, amount float64
	}{
		{sideTestTime(13, 59), 80, 900},
		{sideTestTime(14, 0), 100, 1000},
	} {
		s.observe(x.t, sideFlowSample{Net: x.net, Amount: x.amount, Source: "em:000001.SH", At: x.t}, 0)
	}
	if math.IsNaN(s.slopeDelta()) {
		t.Fatalf("slope must resume on adjacent afternoon buckets, got %+v", s.Buckets)
	}
}

// TestSideFlowSourceChangeRebuildsBaseline 换源不得把两个指数的累计值做差；
// 上午锚与 latch 是独立证据，不随桶历史一起清掉。
func TestSideFlowSourceChangeRebuildsBaseline(t *testing.T) {
	s := newSideFlowState()
	s.Day = data.TradingDayDate(sideTestTime(10, 0))
	s.observe(sideTestTime(9, 31), sideFlowSample{Net: 10, Amount: 100, Source: "em:000001.SH", At: sideTestTime(9, 31)}, 0)
	s.observe(sideTestTime(10, 0), sideFlowSample{Net: 30, Amount: 300, Source: "em:000001.SH", At: sideTestTime(10, 0)}, 0)
	s.MorningAnchor, s.MorningAnchorAt = -0.2, sideTestTime(11, 0)
	s.Latch = signalctl.SidePolicyLatch{Kind: "no_new", Until: "14:00", Reason: "test"}
	s.observe(sideTestTime(10, 1), sideFlowSample{Net: 900, Amount: 9000, Source: "em:000300.SH", At: sideTestTime(10, 1)}, 0)
	if len(s.Buckets) != 0 || !math.IsNaN(s.slopeDelta()) || s.Baseline.Source != "em:000300.SH" ||
		s.BucketStart.Source != "em:000300.SH" || s.BucketStart.Net != 900 {
		t.Fatalf("source change must discard old buckets and seed the new source, got %+v", s)
	}
	if s.MorningAnchor != -0.2 || s.Latch.Reason != "test" {
		t.Fatalf("source change must preserve independent morning anchor and latch, got %+v", s)
	}
}

func TestSideFlowRejectsNonCNYAmount(t *testing.T) {
	s := newSideFlowState()
	s.observe(sideTestTime(10, 0), sideFlowSample{Net: 10, Amount: 100, Source: "sina:000001.SH", At: sideTestTime(10, 0)}, 0.1)
	if s.Baseline != nil || len(s.Buckets) != 0 {
		t.Fatalf("Sina volume must not seed money-flow buckets, got %+v", s)
	}
}

// sidePolicyEngine 造带 runtime 开关和临时状态文件的最小引擎。
func sidePolicyEngine(t *testing.T, enabled bool) *Engine {
	t.Helper()
	on := enabled
	cm := config.NewManagerWithRules(&config.Rules{Runtime: config.RuntimeConfig{SidePolicyEnabled: &on}})
	dir := t.TempDir()
	return &Engine{
		cfgMgr: cm, dataDir: dir,
		sideFlow: newSideFlowState(),
		sideState: signalctl.SidePolicyState{Mode: signalctl.SidePolicyLegacy, AllowNew: true, AllowAdd: true,
			TargetMultiplier: 1, LeftWeight: 1, RightWeight: 1, Reason: "side_policy_disabled"},
		sideLatestPath:      filepath.Join(dir, "side_policy_latest.json"),
		sideUrgentReqPath:   filepath.Join(dir, "side_policy_urgent.req"),
		indexAmountSeriesFn: func(int, string) ([]data.IndexAmountDay, string, error) { return nil, "", errors.New("no gap") },
	}
}

// TestTPolicyUsesCurrentFreshnessAndRuntimeSwitch runtime 关闭时旧 bear 快照不能继续放行 T；
// 开启时过期进攻态必须回落 bear L1，与战略买入使用同一 currentSidePolicy 出口。
func TestTPolicyUsesCurrentFreshnessAndRuntimeSwitch(t *testing.T) {
	now := sideTestTime(10, 30)
	e := sidePolicyEngine(t, false)
	e.clockFn = func() time.Time { return now }
	e.sideState = signalctl.SidePolicyState{Enabled: true, Day: data.TradingDayDate(now),
		Mode: signalctl.SidePolicyBear, Level: 1, AllowNew: false, AllowAdd: true, AsOf: now}
	p, _, _ := e.tParamsFor("u_1", config.TConfig{}, false)
	if p.SidePolicyEnabled || p.SidePolicyMode != "legacy" {
		t.Fatalf("runtime-off must not let stale bear snapshot authorize T, got enabled=%v mode=%s",
			p.SidePolicyEnabled, p.SidePolicyMode)
	}

	e = sidePolicyEngine(t, true)
	e.clockFn = func() time.Time { return now }
	e.sideState = signalctl.SidePolicyState{Enabled: true, Day: data.TradingDayDate(now),
		Mode: signalctl.SidePolicyBull, Level: 1, AllowNew: true, AllowAdd: true,
		TargetMultiplier: config.CarrierSideBullTargetL1(), AsOf: now.Add(-time.Hour)}
	p, _, _ = e.tParamsFor("u_1", config.TConfig{}, false)
	if !p.SidePolicyEnabled || p.SidePolicyMode != "bear" {
		t.Fatalf("T must consume freshness-filtered current policy, got enabled=%v mode=%s",
			p.SidePolicyEnabled, p.SidePolicyMode)
	}
}

// TestExecutionFlowBlendDoesNotDoubleCountStyle 生产定权链已含风格权重；legacy 才补乘一次。
func TestExecutionFlowBlendDoesNotDoubleCountStyle(t *testing.T) {
	if got := executionFlowBlend(dynBudgetCtx{MarketScale: 0.8, StockScale: 1, FlowBlend: 0.96}, 1.2); got != 0.96 {
		t.Fatalf("injected Decision.FlowBlend must pass through once, got %.4f", got)
	}
	if got := executionFlowBlend(dynBudgetCtx{FlowBlend: 0.8}, 1.2); math.Abs(got-0.96) > 1e-9 {
		t.Fatalf("legacy flow=.8 and left=1.2 must synthesize .96, got %.4f", got)
	}
}

func setSideIndexCache(e *Engine, now time.Time, pct float64, stale bool) {
	e.mu.Lock()
	e.idxQuoteDay, e.idxQuotePct, e.idxQuoteAt, e.idxQuoteStale = data.TradingDayDate(now), pct, now, stale
	e.mu.Unlock()
}

func TestSidePolicyPM1300IndexIndependentUrgent(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(13, 0)
	setSideIndexCache(e, now, -2.0, false)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear"})
	if _, err := os.Stat(e.sideUrgentReqPath); err != nil {
		t.Fatalf("index-only hit must write urgent req: %v", err)
	}
	st := e.SidePolicySnapshot()
	if st.AllowNew || st.Latch.Reason != "pm1300_index_drop" {
		t.Fatalf("index-only hit must latch no-new, got %+v", st)
	}
}

// TestSidePolicyPM1300FlowIndependentUrgent 证明资金流单腿也能独立触发加急。
func TestSidePolicyPM1300FlowIndependentUrgent(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(13, 0)
	setSideIndexCache(e, now, math.NaN(), true)
	e.sideFlow.Day = data.TradingDayDate(now)
	e.sideFlow.Last = &sideFlowSample{Net: -100, Amount: 1000, Source: "em:000001.SH", At: now}
	e.sideFlow.MorningAnchor = -0.1
	e.sideFlow.MorningAnchorAt = sideTestTime(11, 29)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.5})
	if _, err := os.Stat(e.sideUrgentReqPath); err != nil {
		t.Fatalf("flow-only hit must write urgent req: %v", err)
	}
	st := e.SidePolicySnapshot()
	if st.AllowNew || st.Latch.Reason != "pm1300_flow_drop" {
		t.Fatalf("flow-only hit must latch no-new, got %+v", st)
	}
}

func TestSidePolicyPM1300StaleIndexDoesNotFire(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(13, 0)
	setSideIndexCache(e, now, -2.0, true)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear"})
	if _, err := os.Stat(e.sideUrgentReqPath); !os.IsNotExist(err) {
		t.Fatalf("stale index must not fire urgent, err=%v", err)
	}
}

func TestSidePolicyPM1300LateSampleDoesNotFire(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(13, 6)
	setSideIndexCache(e, now, -2.0, false)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear"})
	if _, err := os.Stat(e.sideUrgentReqPath); !os.IsNotExist(err) {
		t.Fatalf("sample after 13:05 must not create latch, err=%v", err)
	}
}

// TestSidePolicyPM1300WaitsForMissingFlowBranch 锁定另一路稍后到达仍能触发 OR 加急。
func TestSidePolicyPM1300WaitsForMissingFlowBranch(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(13, 0)
	setSideIndexCache(e, now, -0.5, false)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear"})
	if _, err := os.Stat(e.sideUrgentReqPath); !os.IsNotExist(err) {
		t.Fatalf("index branch alone must not finish checkpoint before flow branch is ready: %v", err)
	}
	e.sideMu.Lock()
	if e.sideFlow.PMChecked {
		t.Fatal("checkpoint must wait for the missing flow branch")
	}
	e.sideMu.Unlock()
	now = sideTestTime(13, 1)
	e.sideFlow.Last = &sideFlowSample{Net: -100, Amount: 1000, Source: "em:000001.SH", At: now}
	e.sideFlow.MorningAnchor = -0.1
	e.sideFlow.MorningAnchorAt = sideTestTime(11, 29)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.5})
	if _, err := os.Stat(e.sideUrgentReqPath); err != nil {
		t.Fatalf("flow branch arriving later must still trigger urgent req: %v", err)
	}
}

// TestSidePolicyInvalidIndexOnlyAbstainsFromBullTier 验证无效指数只弃权指数分档，不伪造突破。
func TestSidePolicyInvalidIndexOnlyAbstainsFromBullTier(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(10, 30)
	setSideIndexCache(e, now, 2.0, true)
	e.sideFlow.Day = data.TradingDayDate(now)
	e.sideFlow.Last = &sideFlowSample{Net: 100, Amount: 1000, Source: "em:000001.SH", At: now}
	e.sideFlow.Buckets = []sideFlowBucket{
		{Index: 1, Start: sideTestTime(10, 0), End: sideTestTime(10, 30), Delta: 0},
		{Index: 2, Start: sideTestTime(10, 30), End: sideTestTime(11, 0), Delta: 0.2},
	}
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bull", FlowMarketDelta: 0.8})
	st := e.SidePolicySnapshot()
	if st.Mode != signalctl.SidePolicyBull || st.Level != 1 || st.TargetMultiplier != config.CarrierSideBullTargetL1() {
		t.Fatalf("stale index must not raise bull tier, got %+v", st)
	}
}

// TestSidePolicyInvalidFlowDoesNotConfirmBull 验证缓存有限值不能替代有效资金流确认。
func TestSidePolicyInvalidFlowDoesNotConfirmBull(t *testing.T) {
	e := sidePolicyEngine(t, true)
	now := sideTestTime(10, 30)
	setSideIndexCache(e, now, 2.0, false)
	e.sideFlow.Day = data.TradingDayDate(now)
	e.sideFlow.Buckets = []sideFlowBucket{
		{Index: 1, Start: sideTestTime(10, 0), End: sideTestTime(10, 30), Delta: 0},
		{Index: 2, Start: sideTestTime(10, 30), End: sideTestTime(11, 0), Delta: 0.2},
	}
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bull", FlowMarketDelta: 0.8})
	st := e.SidePolicySnapshot()
	if st.Mode == signalctl.SidePolicyBull || st.MarketDelta != 0 || st.SlopeDelta != 0 {
		t.Fatalf("missing flow sample must abstain despite cached regime numbers, got %+v", st)
	}
}

func TestSidePolicyDisabledWritesLegacy(t *testing.T) {
	e := sidePolicyEngine(t, false)
	now := sideTestTime(10, 30)
	setSideIndexCache(e, now, -2.0, false)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.8})
	st := e.SidePolicySnapshot()
	if st.Enabled || st.Mode != signalctl.SidePolicyLegacy || !st.AllowNew || !st.AllowAdd || st.Day != data.TradingDayDate(now) {
		t.Fatalf("disabled policy must remain legacy and carry day identity, got %+v", st)
	}
	if _, err := os.Stat(e.sideUrgentReqPath); !os.IsNotExist(err) {
		t.Fatalf("disabled policy must not write urgent req, err=%v", err)
	}
}
