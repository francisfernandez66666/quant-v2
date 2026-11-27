// carrier_knobs_pending_20261126_test.go — §BUY-PENDING / §SIDE-FLOW-PROXY 九枚旋钮的出厂长锁（2026-11-26）。
//
// 九枚旋钮分成两组：P0/P1/P2 四枚（在途买单占额度、进程内买单账、确认窗时间跨度、不追刀）
// 与 P3/P4 五枚（资金流替补两枚 + 早释三枚）。它们的唯一家是 carrier_knobs.go，
// 派生链把它们烘进载体 params —— 但**载体不一定读**，所以这里既锁默认值，也锁"语义护栏"
// （fade 必须小于强流入阈值，否则"释放即回落"，那条腿等于没有）。
//
// English: default-value and semantic-guard locks for the nine 2026-11-26 carrier knobs.
package config

import "testing"

func TestCarrierPendingKnobs(t *testing.T) {
	// P1：进程内买单账的判死等待。必须**大于**一轮轮询周期（6–45 s）且小于确认窗重置，
	// 否则要么每轮都判死（等于没记），要么一直占着额度不放。
	if CarrierOpenBuysGraceSec() <= 0 || CarrierOpenBuysGraceSec() > CarrierBuyConfirmResetSec() {
		t.Fatalf("open_buys_grace=%v must be in (0, reset=%v]", CarrierOpenBuysGraceSec(), CarrierBuyConfirmResetSec())
	}
	// P2：确认窗的最小真实时间跨度。60 秒是"两轮"的历史语义，不是两轮 6 秒。
	if CarrierBuyConfirmMinSpanSec() < 60 {
		t.Fatalf("buy_confirm_min_span_sec=%v is shorter than the established 60 s window", CarrierBuyConfirmMinSpanSec())
	}
	// P2：断档重置必须长于最小跨度（否则刚满足就被判成断档）。
	if CarrierBuyConfirmResetSec() <= CarrierBuyConfirmMinSpanSec() {
		t.Fatalf("reset=%v must exceed min_span=%v", CarrierBuyConfirmResetSec(), CarrierBuyConfirmMinSpanSec())
	}
	// P2：不追刀闸缺省开（关掉就会回到"信号价已经在目标下方也照买"）。
	if !CarrierEntryPullbackChaseGuard() {
		t.Fatal("entry_pullback_chase_guard must default on")
	}
}

// TestCarrierSideFlowProxyKnobs 钉住 side_flow_proxy 的出厂值是「开」：
// 这条腿缺省关闭时载体拿不到板块合计替补，净流入腿会在板块缺口那天整天弃权。
func TestCarrierSideFlowProxyKnobs(t *testing.T) {
	if !CarrierSideFlowProxyEnabled() {
		t.Fatal("side_flow_proxy must default on")
	}
	if CarrierSideFlowProxyAfterRounds() != 2 {
		t.Fatalf("side_flow_proxy_after_rounds=%d want 2", CarrierSideFlowProxyAfterRounds())
	}
	if !CarrierSideInflowReversalEnabled() {
		t.Fatal("side_inflow_reversal must default on")
	}
	if CarrierSideInflowVolMin() != 1.0 {
		t.Fatalf("side_inflow_vol_min=%v want 1.0 (same scale as VolSurgeDeltaOf)", CarrierSideInflowVolMin())
	}
	if CarrierSideInflowFadeDelta() != 0.20 {
		t.Fatalf("side_inflow_fade_delta=%v want 0.20", CarrierSideInflowFadeDelta())
	}
	// 语义护栏：fade 必须**小于**强流入阈值，否则"释放即回落"。
	if CarrierSideInflowFadeDelta() >= CarrierSideFlowStrongDelta() {
		t.Fatal("fade must be below the strong-inflow threshold")
	}
	// 早释的放量下限与量能三态的放量档同源（不新造"放量"定义）。
	if CarrierSideInflowVolMin() != 1.0 {
		t.Fatal("vol_min must stay on the VolSurgeDeltaOf scale")
	}
}
