// side_pm_gate_20261126_test.go — §SIDE-PM-GATE 纯函数锁（2026-11-26）。
//
// 13:00 那一档原来是个**时钟事件**：到点就从上午档（bear 0.35 / bull 0.70）放到下午档
// （0.50 / 1.00），不管下午的读数有没有到位。今天 13:00 的现场是"斜率仍缺、资金流间歇"，
// 于是 0.50 这一档等于是凭钟点放出来的。
//
// owner 的裁决：改成**数据事件**——三项读数（净流入、指数、上午锚）齐了才放；
// 放出去之后当日粘滞（下午读数抖动不许翻面，每次翻面都改 digest ⇒ 一次客户端有序重启）。
// 这一批锁钉三件事：三项缺一不放、粘滞真的短路后面三项、fail-closed 之后确认位仍在。
//
// English: pure-function locks for the 13:00 data gate (all-three prerequisite, sticky
// release, and the release surviving a fail-closed freshness downgrade).
package signalctl

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// pmInput 造一份"下午三项读数齐全"的输入：bear 剧本、净流入轻度流出（不进 L2）、
// 斜率不恶化、指数有效、上午锚有效。
func pmInput(now time.Time) SidePolicyInput {
	return SidePolicyInput{
		Enabled: true, Now: now, AsOf: now,
		MarketState: "bear", MarketDelta: -0.10, SlopeDelta: 0.02,
		IndexChangePct: -0.30, IndexValid: true, GapPct: 0.10,
		FlowValid: true, PMAnchorValid: true, PMFlowIntensityDelta: -0.05,
	}
}

// TestSidePMGateNeedsAllThree 三项缺一 ⇒ 不放；三项全真 + 13:05 ⇒ 放。
func TestSidePMGateNeedsAllThree(t *testing.T) {
	now := rampAt("13:05")
	if st := DecideSidePolicy(pmInput(now)); !st.PMRelease {
		t.Fatalf("all three readings present must release, got %+v", st)
	}
	cases := []struct {
		name string
		mut  func(*SidePolicyInput)
	}{
		{"market", func(in *SidePolicyInput) { in.FlowValid = false }},
		{"index", func(in *SidePolicyInput) { in.IndexValid = false }},
		{"anchor", func(in *SidePolicyInput) { in.PMAnchorValid = false }},
	}
	for _, c := range cases {
		in := pmInput(now)
		c.mut(&in)
		if st := DecideSidePolicy(in); st.PMRelease {
			t.Fatalf("missing %s reading must not release, got %+v", c.name, st)
		}
	}
	// 11:00 之前不该放（确认位不能提前生效）。
	if st := DecideSidePolicy(pmInput(rampAt("11:00"))); st.PMRelease {
		t.Fatal("PM release must not fire before pm_check_at")
	}
	// range 档不置位：ramp 恒 1.0，翻面只会白多一次客户端重启。
	in := pmInput(now)
	in.MarketState = ""
	if st := DecideSidePolicy(in); st.Mode != SidePolicyRange || st.PMRelease {
		t.Fatalf("range mode must not carry pm_release, got mode=%s release=%v", st.Mode, st.PMRelease)
	}
}

// TestSidePMReleaseLatchedSticky 粘性位短路后面三项：下午读数坏了也不许翻回上午档。
func TestSidePMReleaseLatchedSticky(t *testing.T) {
	in := pmInput(rampAt("13:30"))
	in.PMReleaseLatched = true
	in.IndexValid = false
	in.GapPct = 0 // 顺带把缺口也弄成"无读数"
	if st := DecideSidePolicy(in); !st.PMRelease {
		t.Fatalf("latched release must survive a bad afternoon reading, got %+v", st)
	}
}

// TestSidePMReleaseSurvivesColdStart 冷启动分支也必须照抄粘性位：13:00 已经由三项读数
// 确认过，13:20 资金流突然掉线（marketOK=false ⇒ 走 cold 分支）时，不能把确认位算丢。
// 这是 §245 ⑩ 的行为腿——静态锁只拦"字面量 true"，拦不住把粘滞位误写成读数判据
// （例如 cold.PMRelease = marketOK）这一类就地推导；没有本例，13:30 会静默退回 0.35。
func TestSidePMReleaseSurvivesColdStart(t *testing.T) {
	now := rampAt("13:30")
	in := pmInput(now)
	in.FlowValid = false       // 资金流掉线 ⇒ marketOK=false ⇒ 进冷启动分支
	in.PMReleaseLatched = true // 13:00 三项齐全时已经确认过
	st := DecideSidePolicy(in)
	if st.Reason != "side_policy_cold_start_bear" {
		t.Fatalf("fixture must enter cold-start branch, got reason=%s", st.Reason)
	}
	if !st.PMRelease {
		t.Fatal("latched PM release must survive cold start, otherwise 13:30 silently reverts to 0.35")
	}
	if v, src := SideTimeRampWithSource(st, now); v != config.CarrierSideRampBearPM() || src != "ramp_bear_pm" {
		t.Fatalf("cold start + released must ramp %v/ramp_bear_pm, got %v/%s",
			config.CarrierSideRampBearPM(), v, src)
	}
}

// TestSidePMReleaseSurvivesFailClosed 这是本组最关键的一条：没有它，"粘性"就只是注释。
// 已确认的 bull 态在 TTL 过期后必须回落 bear L1，但 PMRelease 仍为 true ⇒ ramp 走 0.50。
func TestSidePMReleaseSurvivesFailClosed(t *testing.T) {
	now := rampAt("13:30")
	in := pmInput(now)
	in.MarketState = "bull"
	in.MarketDelta = 0.60
	in.SlopeDelta = 0.20
	in.IndexChangePct = 1.20
	st := DecideSidePolicy(in)
	if st.Mode != SidePolicyBull || !st.PMRelease {
		t.Fatalf("fixture must be a released bull state, got mode=%s release=%v", st.Mode, st.PMRelease)
	}
	st.AsOf = now.Add(-time.Duration(config.CarrierSidePolicyTTLSec()+60) * time.Second)
	st = ApplySidePolicyFreshness(st, now)
	if st.Mode != SidePolicyBear || st.Level != 1 || st.AllowNew || !st.AllowAdd {
		t.Fatalf("expired aggressive state must fall back to bear L1, got %+v", st)
	}
	if !st.PMRelease {
		t.Fatal("fail-closed must keep the sticky release, otherwise 13:30 silently reverts to 0.35")
	}
	if v, src := SideTimeRampWithSource(st, now); v != config.CarrierSideRampBearPM() || src != "ramp_bear_pm" {
		t.Fatalf("fail-closed + released must ramp %v/ramp_bear_pm, got %v/%s",
			config.CarrierSideRampBearPM(), v, src)
	}
}
