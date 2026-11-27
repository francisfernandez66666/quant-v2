// side_inflow_urgent_20261126_test.go — §SIDE-INFLOW-EARLY 的引擎侧锁（2026-11-26）。
//
// 纯函数只管"该不该放开"；"当日只写一次加急"住在引擎里。这一批锁钉的是**次数**：
// 释放一条、收回一条，各当天最多一次。判据用"删掉 req 文件后再跑一轮，看它会不会再出现"——
// 比 mtime 更硬，也正好是"每轮都写"这种形态唯一能被抓住的地方。
//
// English: engine-side locks for the once-per-day urgent requests of the early release
// and its fade-back, plus the "already released ⇒ don't re-bake data_complete" rule.
package engine

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"quant-trading-v2/internal/signalctl"
)

// flowRound 写一枚大盘腿样本并把 regime 对齐：主路读 regime.FlowMarketDelta，
// 而 flowValid 只认**样本**的新鲜度（P3 之后这两个不再是同一件事）。
func flowRound(e *Engine, now time.Time, delta, vol float64) *signalctl.Regime {
	e.sampleSideFlow(now, sideFlowSample{Net: 1e8, Amount: 1e9, Source: "em:000001.SH", At: now, Delta: delta}, true, 0, 0, 0)
	setSideIndexCache(e, now, 0.5, false)
	return &signalctl.Regime{MarketState: "range", FlowMarketDelta: delta, VolSurgeDelta: vol}
}

// readUrgentReq 读回加急请求（不存在时返回 nil）。
func readUrgentReq(t *testing.T, e *Engine) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(e.sideUrgentReqPath)
	if err != nil {
		return nil
	}
	var req map[string]interface{}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("urgent req must be valid json: %v", err)
	}
	return req
}

// releaseEngine 把"开盘首烘"这一条先消费掉，返回已经写过一次释放的引擎。
func releaseEngine(t *testing.T) *Engine {
	t.Helper()
	e := sidePolicyEngine(t, true)
	warm := sideTestTime(10, 29)
	e.updateSidePolicy(warm, flowRound(e, warm, 0, 0))
	_ = os.Remove(e.sideUrgentReqPath)
	now := sideTestTime(10, 30)
	e.updateSidePolicy(now, flowRound(e, now, 0.55, 1.0))
	req := readUrgentReq(t, e)
	if req == nil || req["reason"] != "inflow_reversal_early" {
		t.Fatalf("release round must write inflow_reversal_early, got %v", req)
	}
	return e
}

// TestSideInflowUrgentOncePerDay 释放那条加急当天只写一次。
func TestSideInflowUrgentOncePerDay(t *testing.T) {
	e := releaseEngine(t)
	e.sideMu.RLock()
	released := e.sideFlow.InflowReleased
	e.sideMu.RUnlock()
	if !released {
		t.Fatal("release round must set InflowReleased")
	}
	// 删掉文件再跑一轮：如果代码每轮都写，文件会重新出现。
	_ = os.Remove(e.sideUrgentReqPath)
	now := sideTestTime(10, 35)
	e.updateSidePolicy(now, flowRound(e, now, 0.55, 1.0))
	if req := readUrgentReq(t, e); req != nil {
		t.Fatalf("a second qualifying round must not rewrite the release req, got %v", req)
	}
}

// TestSideInflowFadeSetsFlag 释放后跌破 fade 阈值 ⇒ 当日回落，且此后再高的净流入也不放开。
func TestSideInflowFadeSetsFlag(t *testing.T) {
	e := releaseEngine(t)
	now := sideTestTime(10, 31)
	e.updateSidePolicy(now, flowRound(e, now, 0.10, 1.0))
	e.sideMu.RLock()
	faded := e.sideFlow.InflowFaded
	e.sideMu.RUnlock()
	if !faded {
		t.Fatal("inflow below fade must set InflowFaded")
	}
	if st := e.SidePolicySnapshot(); st.Reason == "inflow_reversal_early" || st.Mode != signalctl.SidePolicyRange {
		t.Fatalf("faded day must not early-release; with valid flow it falls through to range_regime, got %s (%s)", st.Mode, st.Reason)
	}
	_ = os.Remove(e.sideUrgentReqPath)
	now = sideTestTime(10, 32)
	e.updateSidePolicy(now, flowRound(e, now, 0.80, 1.0))
	if st := e.SidePolicySnapshot(); st.Mode != signalctl.SidePolicyRange || st.Reason == "inflow_reversal_early" {
		t.Fatalf("faded day must never release again, got %s (%s)", st.Mode, st.Reason)
	}
	if req := readUrgentReq(t, e); req != nil {
		t.Fatalf("faded day must not write another reversal req, got %v", req)
	}
}

// TestSideInflowReleaseSkipsDataComplete 已释放的日子不许再烘一次 data_complete
// （同一份 range 重复投递＝白多一次客户端有序重启）。
func TestSideInflowReleaseSkipsDataComplete(t *testing.T) {
	run := func(released bool) bool {
		e := sidePolicyEngine(t, true)
		// 攒出斜率：三个样本收口两个相邻完整桶（早释那条腿要求斜率缺，这条用例要斜率在）。
		net, amt := 0.0, 100.0
		for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}} {
			at := sideTestTime(hm[0], hm[1])
			net += 10
			amt += 100
			e.sampleSideFlow(at, sideFlowSample{Net: net, Amount: amt, Source: "em:000001.SH", At: at, Delta: 0.6}, true, 0, 0, 0)
		}
		e.sideMu.Lock()
		e.sideFlow.InflowReleased = released
		e.sideMu.Unlock()
		_ = os.Remove(e.sideUrgentReqPath)
		now := sideTestTime(11, 1)
		e.sampleSideFlow(now, sideFlowSample{Net: net + 10, Amount: amt + 100, Source: "em:000001.SH", At: now, Delta: 0.6}, true, 0, 0, 0)
		setSideIndexCache(e, now, 0.5, false)
		e.updateSidePolicy(now, &signalctl.Regime{MarketState: "range", FlowMarketDelta: 0.6})
		e.sideMu.RLock()
		baked := e.sideFlow.DataCompleteBaked
		e.sideMu.RUnlock()
		return baked
	}
	if !run(false) {
		t.Fatal("control: a healthy non-released round must bake data_complete")
	}
	if run(true) {
		t.Fatal("an already released day must not re-bake data_complete")
	}
}

// TestSideInflowFadeUrgentOncePerDay 收回那条加急当天也只写一次，且与释放那条不撞 request_id。
func TestSideInflowFadeUrgentOncePerDay(t *testing.T) {
	e := releaseEngine(t)
	releaseReq := readUrgentReq(t, e)
	_ = os.Remove(e.sideUrgentReqPath)
	now := sideTestTime(10, 31)
	e.updateSidePolicy(now, flowRound(e, now, 0.10, 1.0))
	req := readUrgentReq(t, e)
	if req == nil || req["reason"] != "inflow_fade" {
		t.Fatalf("fade round must write inflow_fade, got %v", req)
	}
	if releaseReq["request_id"] == req["request_id"] {
		t.Fatalf("release and fade must use different request ids, both %v", req["request_id"])
	}
	_ = os.Remove(e.sideUrgentReqPath)
	now = sideTestTime(10, 32)
	e.updateSidePolicy(now, flowRound(e, now, 0.10, 1.0))
	if req := readUrgentReq(t, e); req != nil {
		t.Fatalf("fade req must be written at most once per day, got %v", req)
	}
}
