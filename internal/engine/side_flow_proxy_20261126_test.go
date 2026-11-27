// side_flow_proxy_20261126_test.go — §SIDE-FLOW-PROXY / §SIDE-INFLOW-EARLY 的引擎侧锁（2026-11-26）。
//
// 为什么要这一批：`sideFlow.observe` 原本只有**一个**调用点，且嵌在"大盘 fflow 腿成功"分支里。
// 于是那条腿抖一轮（今天 10:02–10:36 反复出现），样本 90 秒后就过期，`flowValid=false`，
// 侧策略整天回 `cold_start_bear`，动态目标被压在 35%——而板块合计净流入当时是**有数**的
// （pct 1.607~1.816 ⇒ delta 0.54~0.61）。这批锁钉的就是"替补腿什么时候接管、接管后不切回、
// 锚不跨源相减、样本过期仍弃权"四件事。
//
// English: engine-side locks for the sector-aggregate flow proxy (fallback source, day latch,
// same-source morning anchor) and for the sample-driven `flowValid` semantics.
package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/signalctl"
)

// proxyBoardTotals 造一组"板块合计净流入 +1.816% / 成交额 1000 亿"的累计值，
// 归一后 delta = 0.6053（与 2026-11-26 现网 11:0x 实读同档）。
const (
	proxyNetSum = 1.816e9
	proxyAmtSum = 100e9
)

func proxyEngine(t *testing.T) *Engine {
	t.Helper()
	e := sidePolicyEngine(t, true)
	// 先攒出两条可配对的历史桶（大盘腿），否则"换源清桶"这条判据没有可清的东西。
	net, amt := 0.0, 100.0
	for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}} {
		at := sideTestTime(hm[0], hm[1])
		net += 10
		amt += 100
		e.sampleSideFlow(at, sideFlowSample{Net: net, Amount: amt, Source: "em:000001.SH", At: at, Delta: net / amt}, true, 0, 0, 0)
	}
	return e
}

// TestSideFlowProxyTakesOverAfterRounds 大盘腿连续失败 side_flow_proxy_after_rounds 轮 ⇒ 切板块合计。
func TestSideFlowProxyTakesOverAfterRounds(t *testing.T) {
	e := proxyEngine(t)
	at := sideTestTime(11, 5)
	e.sampleSideFlow(at, sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	e.sideMu.RLock()
	locked, miss, src := e.sideFlow.ProxyLocked, e.sideFlow.ProxyMiss, e.sideFlow.Last.Source
	e.sideMu.RUnlock()
	if locked || miss != 1 || src != "em:000001.SH" {
		t.Fatalf("first miss must not take over: locked=%v miss=%d src=%s", locked, miss, src)
	}
	at = sideTestTime(11, 6)
	e.sampleSideFlow(at, sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	e.sideMu.RLock()
	last := e.sideFlow.Last
	locked = e.sideFlow.ProxyLocked
	e.sideMu.RUnlock()
	if !locked || last == nil || last.Source != "em:sector_agg" {
		t.Fatalf("second miss must latch the proxy, got locked=%v last=%+v", locked, last)
	}
	if math.Abs(last.Delta-signalctl.FlowPctToDelta(proxyNetSum, proxyAmtSum)) > 1e-9 {
		t.Fatalf("proxy sample must carry the same delta the read-out shows, got %.6f", last.Delta)
	}
}

// TestSideFlowProxyWaitsForThreshold 只失败一轮时旧样本不能被顶掉（阈值就是 2 轮）。
func TestSideFlowProxyWaitsForThreshold(t *testing.T) {
	e := proxyEngine(t)
	e.sampleSideFlow(sideTestTime(11, 5), sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	e.sideMu.RLock()
	last := e.sideFlow.Last
	e.sideMu.RUnlock()
	if last == nil || last.Source != "em:000001.SH" {
		t.Fatalf("a single miss must keep the market sample, got %+v", last)
	}
}

// TestSideFlowProxyStaysLocked 锁定后大盘腿恢复也不切回：换源会清空桶历史，
// 源来回抖 = 斜率永远攒不出（这是今天 11:00 之后仍回 35% 的第二个成因）。
func TestSideFlowProxyStaysLocked(t *testing.T) {
	e := proxyEngine(t)
	e.sampleSideFlow(sideTestTime(11, 5), sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	e.sampleSideFlow(sideTestTime(11, 6), sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	at := sideTestTime(11, 7)
	e.sampleSideFlow(at, sideFlowSample{Net: 5, Amount: 500, Source: "em:000001.SH", At: at, Delta: 0.5}, true, proxyNetSum, proxyAmtSum, 40)
	e.sideMu.RLock()
	last, locked := e.sideFlow.Last, e.sideFlow.ProxyLocked
	e.sideMu.RUnlock()
	if !locked || last == nil || last.Source != "em:sector_agg" {
		t.Fatalf("a recovered market leg must not flip the source back, got locked=%v last=%+v", locked, last)
	}
}

// TestSideFlowProxyDisabledKeepsOldBehaviour §17.1 的退路锁：
// side_flow_proxy_enabled=false 时，大盘腿连续失败**不**触发替补采样（回到旧行为）——
// 替补是新加的兜底，不许在总闸关掉后还偷偷生效。总闸在生产是编译期常量，
// 这里用引擎字段注入 false，只影响本例的引擎实例。
func TestSideFlowProxyDisabledKeepsOldBehaviour(t *testing.T) {
	e := proxyEngine(t)
	e.sideProxyEnabledFn = func() bool { return false }
	for i := 0; i < 5; i++ {
		e.sampleSideFlow(sideTestTime(11, 5+i), sideFlowSample{}, false, proxyNetSum, proxyAmtSum, 40)
	}
	e.sideMu.RLock()
	last, locked, miss := e.sideFlow.Last, e.sideFlow.ProxyLocked, e.sideFlow.ProxyMiss
	e.sideMu.RUnlock()
	if locked || last == nil || last.Source != "em:000001.SH" {
		t.Fatalf("proxy disabled must keep the last market sample, got locked=%v last=%+v", locked, last)
	}
	// 关掉总闸时连 miss 计数都不该涨——那本是为替补接管服务的。
	if miss != 0 {
		t.Fatalf("proxy disabled must not accumulate proxy misses, got %d", miss)
	}
}

// TestSideFlowProxySkipsThinBoards 样本数不足 sectorAggMinBoards ⇒ 不采样；
// 但 2026-11-27 起失败次数仍按时间窗累计（锁定替补源，不制造假样本）。
func TestSideFlowProxySkipsThinBoards(t *testing.T) {
	e := proxyEngine(t)
	for i := 0; i < 5; i++ {
		e.sampleSideFlow(sideTestTime(11, 5+i), sideFlowSample{}, false, proxyNetSum, proxyAmtSum, sectorAggMinBoards-1)
	}
	e.sideMu.RLock()
	last, locked, miss := e.sideFlow.Last, e.sideFlow.ProxyLocked, e.sideFlow.ProxyMiss
	e.sideMu.RUnlock()
	if !locked || miss < 2 || last == nil || last.Source != "em:000001.SH" {
		t.Fatalf("thin boards must not sample but still count misses, got locked=%v miss=%d last=%+v", locked, miss, last)
	}
}

// seedProxyBuckets 直接铺两条可配对的**替补源**历史桶（斜率成桶需要三个样本）。
// 刻意不走 sampleSideFlow：那条路在换源时会清桶，而本用例要判的是"锚与当期同源"，
// 不是"接管时机"（后者由上面四条锁负责）。
func seedProxyBuckets(t *testing.T, e *Engine) {
	t.Helper()
	net, amt := 0.0, 100.0
	// 最后一枚 11:05 的样本是为了让 Last 在 updateSidePolicy(11:06) 时仍新鲜（90 秒窗）；
	// 它会顺手把 11:00–11:30 的锚写成 0.6，调用方随后按用例覆盖锚与锚来源。
	for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}, {11, 5}} {
		at := sideTestTime(hm[0], hm[1])
		net += 10
		amt += 100
		e.sideFlow.observe(at, sideFlowSample{Net: net, Amount: amt, Source: "em:sector_agg", At: at}, 0.6)
	}
}

// TestSideFlowAnchorNotComparedAcrossSources §14.8：11:00 的锚与 13:00 的当期必须同源，
// 否则 pmDelta 是"大盘锚减板块合计"的跨源差值，不是趋势。
func TestSideFlowAnchorNotComparedAcrossSources(t *testing.T) {
	seed := func(src string) signalctl.SidePolicyState {
		e := sidePolicyEngine(t, true)
		seedProxyBuckets(t, e)
		e.sideMu.Lock()
		e.sideFlow.MorningAnchor = -0.2
		e.sideFlow.MorningAnchorAt = sideTestTime(11, 5)
		e.sideFlow.MorningAnchorSource = src
		e.sideMu.Unlock()
		e.updateSidePolicy(sideTestTime(11, 6), &signalctl.Regime{MarketState: "range"})
		return e.SidePolicySnapshot()
	}
	if st := seed("em:000001.SH"); st.PMFlowIntensityDelta != 0 || st.DeltaSource != "sector_agg" {
		t.Fatalf("cross-source anchor must abstain, got pm=%.4f src=%s", st.PMFlowIntensityDelta, st.DeltaSource)
	}
	if st := seed("em:sector_agg"); math.Abs(st.PMFlowIntensityDelta-(0.6+0.2)) > 1e-9 {
		t.Fatalf("same-source anchor must produce the difference, got %.6f want %.6f", st.PMFlowIntensityDelta, 0.8)
	}
}

// TestSideFlowProxyFeedsFlowValid 替补样本必须真的喂进 flowValid / delta_source，
// 否则"切了源"与"没切"在读面上同形（今天的读面就是这么骗人的）。
func TestSideFlowProxyFeedsFlowValid(t *testing.T) {
	e := sidePolicyEngine(t, true)
	net, amt := 0.0, 100.0
	for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}} {
		at := sideTestTime(hm[0], hm[1])
		net += 10
		amt += 100
		e.sideFlow.observe(at, sideFlowSample{Net: net, Amount: amt, Source: "em:sector_agg", At: at},
			signalctl.FlowPctToDelta(proxyNetSum, proxyAmtSum))
	}
	e.updateSidePolicy(sideTestTime(11, 1), &signalctl.Regime{MarketState: "range"})
	st := e.SidePolicySnapshot()
	if st.DeltaSource != "sector_agg" || math.Abs(st.MarketDelta-signalctl.FlowPctToDelta(proxyNetSum, proxyAmtSum)) > 1e-9 {
		t.Fatalf("proxy sample must drive market delta, got src=%s delta=%.6f", st.DeltaSource, st.MarketDelta)
	}
	if st.Mode != signalctl.SidePolicyRange {
		t.Fatalf("flat proxy flow with a healthy slope must stay range, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestSideFlowSampleExpiryStillAbstains 回归锁：替补腿也不能绕过新鲜度——
// 91 秒前的样本照样 flowValid=false，方向仍是 fail-closed（bear）。
func TestSideFlowSampleExpiryStillAbstains(t *testing.T) {
	e := sidePolicyEngine(t, true)
	net, amt := 0.0, 100.0
	for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}} {
		at := sideTestTime(hm[0], hm[1])
		net += 10
		amt += 100
		e.sideFlow.observe(at, sideFlowSample{Net: net, Amount: amt, Source: "em:sector_agg", At: at}, 0.6)
	}
	e.updateSidePolicy(sideTestTime(11, 2), &signalctl.Regime{MarketState: "range"})
	st := e.SidePolicySnapshot()
	if st.DeltaSource != "" || st.Mode != signalctl.SidePolicyBear {
		t.Fatalf("stale sample must abstain and fall back to bear, got src=%q mode=%s", st.DeltaSource, st.Mode)
	}
}

var _ = time.Second
