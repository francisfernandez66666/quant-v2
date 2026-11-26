// side_inflow_early_20261126_test.go — §SIDE-INFLOW-EARLY 纯函数锁（2026-11-26）。
//
// 背景：09:30–11:00 斜率成不了桶（需要两个相邻完整 30 分钟桶），冷启动分支一律返回
// `cold_start_bear` ⇒ 动态目标被压在 35%。可"净流入 + 放量"这两条读数当时是有的，
// 于是 owner 裁了一条有界早释：成立即落 `range`（ramp 恒 1.0），但不落 bull——
// bull 还要求斜率"改善"，而斜率此刻根本不存在。
//
// 这一批锁钉的是**边界**：缺量能/缺指数/已回落/当日已释放过，一个都不能放开；
// 而释放之后净流入仍在（≥ fade）不许被 bear 剧本收回。
//
// English: pure-function locks for the cold-start early release (inflow + volume surge),
// its hard prerequisites, the sticky fade flag and the latch precedence.
package signalctl

import (
	"math"
	"testing"
)

// earlyInput 造一份"资金流有数、斜率缺、指数有效"的冷启动输入（今天 09:30–11:00 的现场形状）。
func earlyInput(delta, vol float64) SidePolicyInput {
	in := sideInput() // 10:30，落在早释窗口内
	in.MarketDelta = delta
	in.SlopeDelta = math.NaN()
	in.VolSurgeDelta = vol
	in.VolValid = true
	return in
}

// TestSideInflowEarlyReleasesRange 「净流入 + 放量」成立 ⇒ range 100%。
func TestSideInflowEarlyReleasesRange(t *testing.T) {
	st := DecideSidePolicy(earlyInput(0.55, 1.0))
	if st.Mode != SidePolicyRange || st.TargetMultiplier != 1 || st.Reason != "inflow_reversal_early" {
		t.Fatalf("early release must land on range/1.0, got mode=%s tm=%v reason=%s",
			st.Mode, st.TargetMultiplier, st.Reason)
	}
	if !st.AllowNew || !st.AllowAdd {
		t.Fatalf("early release must not tighten the gate, got new=%v add=%v", st.AllowNew, st.AllowAdd)
	}
	// 刻意不是 bull：bull 的 AM 档只有 70%，且要求斜率确认"改善"。
	if st.Mode == SidePolicyBull {
		t.Fatal("early release must never be reported as bull")
	}
}

// TestSideInflowEarlyNeedsVolume 平量（vol=0）不构成"净流入 + 放量"。
func TestSideInflowEarlyNeedsVolume(t *testing.T) {
	st := DecideSidePolicy(earlyInput(0.55, 0))
	if st.Mode != SidePolicyBear || st.Reason != "side_policy_cold_start_bear" {
		t.Fatalf("flat volume must stay cold-start bear, got mode=%s reason=%s", st.Mode, st.Reason)
	}
}

// TestSideInflowEarlyNeedsIndex 指数读数缺失时早释也必须弃权（那是"抄底"判据的一半）。
func TestSideInflowEarlyNeedsIndex(t *testing.T) {
	in := earlyInput(0.55, 1.0)
	in.IndexValid = false
	if st := DecideSidePolicy(in); st.Mode != SidePolicyBear {
		t.Fatalf("missing index must keep bear, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestSideInflowFadedBlocksRelease 当日已回落 ⇒ 再满足触发条件也不放开（纯函数只认 bool）。
func TestSideInflowFadedBlocksRelease(t *testing.T) {
	in := earlyInput(0.55, 1.0)
	in.InflowFaded = true
	if st := DecideSidePolicy(in); st.Mode != SidePolicyBear {
		t.Fatalf("faded day must not release again, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestSideInflowHoldOverridesBear 已释放后净流入仍在（≥ fade）⇒ bear 剧本不许收回。
func TestSideInflowHoldOverridesBear(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.MarketDelta = 0.40
	in.SlopeDelta = 0.05 // 斜率已可用且不恶化
	in.InflowReleased = true
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyRange || st.Reason != "inflow_reversal_early" || st.TargetMultiplier != 1 {
		t.Fatalf("released day must hold range while inflow holds, got mode=%s reason=%s tm=%v",
			st.Mode, st.Reason, st.TargetMultiplier)
	}
}

// TestSideInflowFadeDropsBackToBear 释放后跌破 fade 阈值 ⇒ 回落 bear（Faded 由引擎置位）。
func TestSideInflowFadeDropsBackToBear(t *testing.T) {
	in := sideInput()
	in.MarketState = "bear"
	in.MarketDelta = 0.10
	in.SlopeDelta = 0.05
	in.InflowReleased = true
	if st := DecideSidePolicy(in); st.Mode != SidePolicyBear {
		t.Fatalf("inflow below fade must drop back to bear, got %s (%s)", st.Mode, st.Reason)
	}
}

// TestSideInflowEarlyRespectsLatch 13:00 的 no-new 是钱路径硬闸，早释不能穿透它。
func TestSideInflowEarlyRespectsLatch(t *testing.T) {
	in := earlyInput(0.55, 1.0)
	in.Now = rampAt("13:10")
	in.AsOf = in.Now
	in.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00", Reason: "pm1300_index_drop"}
	st := DecideSidePolicy(in)
	if st.AllowNew || st.Reason != "pm1300_latch" {
		t.Fatalf("latch must beat the early release, got new=%v reason=%s", st.AllowNew, st.Reason)
	}
}

// TestColdStartBearRespectsLatch 冷启动分支原本直接 AllowNew=true，会把 13:00 的锁静默丢掉。
func TestColdStartBearRespectsLatch(t *testing.T) {
	in := sideInput()
	in.Now = rampAt("13:10")
	in.AsOf = in.Now
	in.FlowValid = false
	in.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00", Reason: "pm1300_index_drop"}
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBear || st.AllowNew || st.Reason != "pm1300_latch" {
		t.Fatalf("cold start must honour the latch, got mode=%s new=%v reason=%s", st.Mode, st.AllowNew, st.Reason)
	}
}
