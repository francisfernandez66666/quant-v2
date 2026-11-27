// side_flow_fallback_20261127_test.go — §SIDE-FLOW-FALLBACK 引擎侧行为腿（2026-11-27）。
//
// 现场：东财 fflow/clist 交替熔断，库里 `sector_flow_pct=2.83` 却没有任何路径进侧策略；
// ProxyMiss 每次大盘腿成功就清零，50% 占空比下"连续 2 次失败"永远锁不上；
// `!marketOK || !slopeOK` 把"有读数没斜率"当缺数，全天 35%。这些用例逐条钉住修法。
package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

func sideFallbackEngine(t *testing.T) *Engine {
	t.Helper()
	return sidePolicyEngine(t, true)
}

// fallbackInput 造"有读数、斜率缺"的输入（engine 包看不到 signalctl 测试里的 sideInput）。
func fallbackInput() signalctl.SidePolicyInput {
	now := time.Date(2026, 11, 27, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	return signalctl.SidePolicyInput{Enabled: true, Now: now, AsOf: now,
		IndexValid: true, FlowValid: true, MarketState: "range"}
}

// TestFallbackPersistedSectorAggFeedsPolicy 大盘失败 + clist 失败 + 落库聚合新鲜 ⇒ flowValid。
func TestFallbackPersistedSectorAggFeedsPolicy(t *testing.T) {
	e := sideFallbackEngine(t)
	now := sideTestTime(11, 22)
	net, amt := 2.83e9, 100e9
	delta := signalctl.FlowPctToDelta(net, amt)
	e.sideMu.Lock()
	e.sideFlow.AggDelta, e.sideFlow.AggNet, e.sideFlow.AggAmount, e.sideFlow.AggAt = delta, net, amt, now
	e.sideMu.Unlock()
	e.sampleSideFlow(now, sideFlowSample{}, false, 0, 0, 3)
	e.sideMu.RLock()
	last := e.sideFlow.Last
	e.sideMu.RUnlock()
	if last == nil || last.Source != "em:sector_agg" || math.Abs(last.Delta-delta) > 1e-9 {
		t.Fatalf("persisted agg must feed the proxy, got %+v", last)
	}
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "range", FlowMarketDelta: math.NaN()})
	if st := e.SidePolicySnapshot(); st.Reason == "side_policy_cold_start_bear" {
		t.Fatalf("fresh persisted agg must not fall back to cold start, got %s", st.Reason)
	}
}

// TestFallbackStaleAggFailsClosed Agg 超龄（901 秒）⇒ 仍冷启动 bear。
func TestFallbackStaleAggFailsClosed(t *testing.T) {
	e := sideFallbackEngine(t)
	now := sideTestTime(11, 22)
	net, amt := 2.83e9, 100e9
	e.sideMu.Lock()
	e.sideFlow.AggDelta, e.sideFlow.AggNet, e.sideFlow.AggAmount = signalctl.FlowPctToDelta(net, amt), net, amt
	e.sideFlow.AggAt = now.Add(-901 * time.Second)
	e.sideMu.Unlock()
	e.sampleSideFlow(now, sideFlowSample{}, false, 0, 0, 3)
	setSideIndexCache(e, now, 0.5, false)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "range"})
	if st := e.SidePolicySnapshot(); st.Mode != signalctl.SidePolicyBear {
		t.Fatalf("stale agg must fail closed, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestFallbackProxyMissUsesTimeWindow 成/败各 50% 占空比时仍会在时间窗内锁定，且成功后不解锁。
func TestFallbackProxyMissUsesTimeWindow(t *testing.T) {
	e := sideFallbackEngine(t)
	for i, hm := range [][2]int{{10, 30}, {10, 31}, {10, 32}, {10, 33}, {10, 34}, {10, 35}} {
		at := sideTestTime(hm[0], hm[1])
		mktOK := i%2 == 0
		mkt := sideFlowSample{}
		if mktOK {
			mkt = sideFlowSample{Net: 1e8, Amount: 1e9, Source: "em:000001.SH", At: at, Delta: 0.1}
		}
		e.sampleSideFlow(at, mkt, mktOK, proxyNetSum, proxyAmtSum, 40)
	}
	e.sideMu.RLock()
	locked := e.sideFlow.ProxyLocked
	e.sideMu.RUnlock()
	if !locked {
		t.Fatal("two failures within the window must latch the proxy even with interleaved successes")
	}
	// 锁定后即使大盘腿恢复也不切回（现有语义不变）。
	at := sideTestTime(10, 36)
	e.sampleSideFlow(at, sideFlowSample{Net: 1e8, Amount: 1e9, Source: "em:000001.SH", At: at, Delta: 0.1}, true, proxyNetSum, proxyAmtSum, 40)
	e.sideMu.RLock()
	last := e.sideFlow.Last
	locked = e.sideFlow.ProxyLocked
	e.sideMu.RUnlock()
	if !locked || last == nil || last.Source != "em:sector_agg" {
		t.Fatalf("recovered market must not unlock the proxy, got locked=%v last=%+v", locked, last)
	}
}

// TestFallbackStickyReleaseCannotBypassFailClosed 真缺数时"曾经释放过"不许保持 100%。
func TestFallbackStickyReleaseCannotBypassFailClosed(t *testing.T) {
	in := fallbackInput()
	in.FlowValid = false
	in.MarketDelta = math.NaN()
	in.InflowReleased = true
	in.InflowFaded = false
	if st := signalctl.DecideSidePolicy(in); st.Reason != "side_policy_cold_start_bear" {
		t.Fatalf("sticky release must not bypass fail-closed, got %s (%s)", st.Mode, st.Reason)
	}
	in.InflowFaded = true
	if st := signalctl.DecideSidePolicy(in); st.Reason != "side_policy_cold_start_bear" {
		t.Fatalf("faded + no data must stay cold, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestFallbackValidDeltaWithoutSlopeIsRange 有读数没斜率不再被当缺数。
func TestFallbackValidDeltaWithoutSlopeIsRange(t *testing.T) {
	in := fallbackInput()
	in.MarketState = "range"
	in.MarketDelta = 0.40
	in.SlopeDelta = math.NaN()
	if st := signalctl.DecideSidePolicy(in); st.Mode != signalctl.SidePolicyRange || st.Reason != "range_regime" {
		t.Fatalf("valid delta without slope must be range_regime, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestFallbackStrongOutWithoutSlopeIsBear 强流出即使没斜率也要标 bear_regime（不是"冷启动允许新仓"）。
func TestFallbackStrongOutWithoutSlopeIsBear(t *testing.T) {
	in := fallbackInput()
	in.MarketDelta = -0.80
	in.SlopeDelta = math.NaN()
	if st := signalctl.DecideSidePolicy(in); st.Mode != signalctl.SidePolicyBear || st.Reason == "side_policy_cold_start_bear" {
		t.Fatalf("strong outflow without slope must be bear (not cold start), got %s (%s)", st.Mode, st.Reason)
	}
}

// TestFallbackInvalidAggDoesNotOverwriteCached 聚合无效那一轮不许覆盖上一次的有效缓存。
func TestFallbackInvalidAggDoesNotOverwriteCached(t *testing.T) {
	e := sideFallbackEngine(t)
	now := sideTestTime(11, 22)
	net, amt := 2.83e9, 100e9
	delta := signalctl.FlowPctToDelta(net, amt)
	e.sideMu.Lock()
	e.sideFlow.Day = data.TradingDayDate(now)
	e.sideFlow.AggDelta, e.sideFlow.AggNet, e.sideFlow.AggAmount, e.sideFlow.AggAt = delta, net, amt, now
	e.sideMu.Unlock()
	e.sampleSideFlow(now.Add(time.Minute), sideFlowSample{}, false, 0, 0, 3)
	e.sideMu.RLock()
	after := e.sideFlow
	e.sideMu.RUnlock()
	if after.AggNet != net || after.AggAmount != amt || math.Abs(after.AggDelta-delta) > 1e-9 ||
		!after.AggAt.Equal(now) {
		t.Fatalf("invalid aggregate must not overwrite the cached reading, got %+v", after)
	}
}
