// side_pm_release_urgent_20261126_test.go — §SIDE-PM-GATE 的引擎侧锁（2026-11-26）。
//
// 13:00 那一档从"时钟事件"改成"数据事件"之后，多出一条加急：确认放开的**那一刻**立刻重烘，
// 不必等常规班次。三件事必须钉住：一天只写一次、range 档不写（ramp 恒 1.0，写了只是白重启一次
// 客户端）、以及确认过之后读数抖动不许把位翻回去。
//
// English: engine-side locks for the 13:00 data-gate urgent request (once per day, skipped in
// range mode, and the sticky latch surviving a later data blackout).
package engine

import (
	"os"
	"testing"

	"quant-trading-v2/internal/signalctl"
)

// pmEngine 造一个"斜率已成桶 + 上午锚在同源腿 + 下午样本新鲜"的引擎（13:00 判据齐的前提）。
func pmEngine(t *testing.T, delta float64) *Engine {
	t.Helper()
	e := sidePolicyEngine(t, true)
	net, amt := 0.0, 100.0
	for _, hm := range [][2]int{{9, 31}, {10, 0}, {10, 30}, {11, 0}} {
		at := sideTestTime(hm[0], hm[1])
		net += 10
		amt += 100
		// 11:00 那枚会顺手写下上午锚（-0.05），锚来源记成同一条腿。
		e.sampleSideFlow(at, sideFlowSample{Net: net, Amount: amt, Source: "em:000001.SH", At: at, Delta: -0.05}, true, 0, 0, 0)
	}
	now := sideTestTime(13, 1)
	e.sampleSideFlow(now, sideFlowSample{Net: net + 10, Amount: amt + 100, Source: "em:000001.SH", At: now, Delta: delta}, true, 0, 0, 0)
	setSideIndexCache(e, now, 0.5, false)
	// 09:30 的首烘与 11:00 的 data_complete 都已经发生。本组只判 pm_release 这条腿，
	// 不让同轮的另一条加急把 req 文件覆盖掉（三条写的是同一个路径，后写者赢）。
	e.sideFlow.ColdStartBakeWritten = true
	e.sideFlow.DataCompleteBaked = true
	return e
}

// TestSidePMReleaseUrgentOncePerDay 第一轮确认 ⇒ 写 pm_release；第二轮不再写。
func TestSidePMReleaseUrgentOncePerDay(t *testing.T) {
	e := pmEngine(t, -0.10)
	now := sideTestTime(13, 1)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.10})
	req := readUrgentReq(t, e)
	if req == nil || req["reason"] != "pm_release" {
		t.Fatalf("confirmed 13:00 must write pm_release, got %v", req)
	}
	e.sideMu.RLock()
	latched, written := e.sideFlow.PMReleaseLatched, e.sideFlow.PMReleaseWritten
	e.sideMu.RUnlock()
	if !latched || !written {
		t.Fatalf("confirmation must latch and mark written, got latched=%v written=%v", latched, written)
	}
	if st := e.SidePolicySnapshot(); !st.PMRelease {
		t.Fatalf("confirmed state must carry pm_release, got %+v", st)
	}
	// 删文件再跑一轮：每轮都写的形态会让它重新出现。
	_ = os.Remove(e.sideUrgentReqPath)
	now = sideTestTime(13, 2)
	e.sampleSideFlow(now, sideFlowSample{Net: 500, Amount: 1000, Source: "em:000001.SH", At: now, Delta: -0.10}, true, 0, 0, 0)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.10})
	if req := readUrgentReq(t, e); req != nil {
		t.Fatalf("pm_release must be written at most once per day, got %v", req)
	}
}

// TestSidePMReleaseSkipsRangeMode range 档的 ramp 恒 1.0 ⇒ 不为这一位多烘一次。
func TestSidePMReleaseSkipsRangeMode(t *testing.T) {
	e := pmEngine(t, -0.10)
	now := sideTestTime(13, 1)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "", FlowMarketDelta: -0.10})
	if st := e.SidePolicySnapshot(); st.Mode != signalctl.SidePolicyRange {
		t.Fatalf("fixture must land on range, got %s (%s)", st.Mode, st.Reason)
	}
	if req := readUrgentReq(t, e); req != nil {
		t.Fatalf("range mode must not write a pm_release req, got %v", req)
	}
	e.sideMu.RLock()
	written := e.sideFlow.PMReleaseWritten
	e.sideMu.RUnlock()
	if written {
		t.Fatal("range mode must not mark the release as written")
	}
}

// TestSidePMReleaseStaysLatched 确认过之后读数整条腿掉了，也不许把位翻回 0.35
// （每次翻面都改 digest ⇒ 一次客户端有序重启）。
func TestSidePMReleaseStaysLatched(t *testing.T) {
	e := pmEngine(t, -0.10)
	now := sideTestTime(13, 1)
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.10})
	if !e.SidePolicySnapshot().PMRelease {
		t.Fatal("fixture must start confirmed")
	}
	// 资金流整条腿掉了：走冷启动分支，但粘性位必须跟着走。
	now = sideTestTime(13, 20)
	e.sideMu.Lock()
	e.sideFlow.Last = nil // 模拟样本过期（flowValid=false）
	e.sideMu.Unlock()
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear"})
	if st := e.SidePolicySnapshot(); !st.PMRelease {
		t.Fatalf("a data blackout must keep the confirmed release, got %+v", st)
	}
	// 指数掉了但资金流在：仍要保住（主路的 PMReleaseLatched 短路）。
	now = sideTestTime(13, 30)
	e.sampleSideFlow(now, sideFlowSample{Net: 500, Amount: 1000, Source: "em:000001.SH", At: now, Delta: -0.10}, true, 0, 0, 0)
	e.mu.Lock()
	e.idxQuoteDay = "19990101" // 让 indexValid=false
	e.mu.Unlock()
	e.updateSidePolicy(now, &signalctl.Regime{MarketState: "bear", FlowMarketDelta: -0.10})
	if st := e.SidePolicySnapshot(); !st.PMRelease {
		t.Fatalf("a bad index reading must keep the confirmed release, got %+v", st)
	}
}
