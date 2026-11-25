// side_ramp_test.go — D7 分时释放阶梯的纯函数锁（2026-11-25）。
//
// 为什么要这一枚：D7 把「今天允许持有多少 MS 派生目标」变成一个随钟点变化的倍率，
// 而倍率是**乘法**——错一档当天就少买/多买三成，读面上却只看到一个正常数字。
// 本文件钉的是 §17.3 的 ①②③④⑤⑥⑩ 六条：
//
//	① mode ∉ {bull,bear}（range/legacy/关闭）⇒ 1.0，逐字回到 D7 之前的行为；
//	② now <  pm_check_at ⇒ AM 档（bull 与 bear 各一档）；
//	③ pm_check_at <= now < full_at，latch 活跃 ⇒ **保持 AM 档**（"暂不释放剩余"）；
//	④ 同区间 latch 不活跃 ⇒ PM 档；
//	⑤ now >= full_at ⇒ 无条件 1.0（不查 latch——owner 原话"14:00 后不管有没有都释放"）；
//	⑥ 来源名（target_ramp_src）四档必须可分，否则资金帧上"为什么是 0.7"读不出来。
//	⑩ 表不合法（<=0 或 >1）⇒ 1.0 且 src=ramp_off（读坏了不砍仓的方向）。
//
// English: pure-function locks for the D7 intraday time-ramp multiplier: mode gate,
// AM/PM bands, latch hold, unconditional release at full_at, source attribution, and
// the fail-safe direction when the parameter table itself is invalid.
package signalctl

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// rampAt 造一个北京时间的钟点；日期随便取一个工作日（判据只看 HH:MM）。
func rampAt(hhmm string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", "2026-11-25 "+hhmm)
	if err != nil {
		panic(err)
	}
	return t
}

// rampState 造一个"总闸开着、模式指定"的市场状态。
func rampState(mode SidePolicyMode) SidePolicyState {
	return SidePolicyState{
		Enabled:          true,
		Mode:             mode,
		AllowNew:         mode == SidePolicyBull,
		AllowAdd:         true,
		TargetMultiplier: 1.0,
		LeftWeight:       1.0,
		RightWeight:      1.0,
	}
}

// ① 非 bull/bear 一律 1.0：range 是"没方向"，legacy 是"总闸关"，
// 两者都不该因为钟点而改变目标——否则"关掉侧策略"就不再等于旧行为。
func TestSideTimeRampOffForNonDirectionalModes(t *testing.T) {
	for _, mode := range []SidePolicyMode{SidePolicyRange, SidePolicyLegacy, "weird"} {
		for _, hhmm := range []string{"09:45", "13:30", "14:30"} {
			got, src := SideTimeRampWithSource(rampState(mode), rampAt(hhmm))
			if got != 1.0 {
				t.Fatalf("mode=%s at %s ramp=%v want 1.0", mode, hhmm, got)
			}
			if src != "ramp_off" {
				t.Fatalf("mode=%s at %s src=%s want ramp_off", mode, hhmm, src)
			}
		}
		// st.Enabled=false 也必须等价于"没有这一层"。
		off := rampState(mode)
		off.Enabled = false
		if v, _ := SideTimeRampWithSource(off, rampAt("10:00")); v != 1.0 {
			t.Fatalf("st.Enabled=false mode=%s ramp=%v want 1.0", mode, v)
		}
	}
}

// ② AM 档：09:45 在 pm_check_at(13:00) 之前，取 AM 值。
func TestSideTimeRampAMBand(t *testing.T) {
	am := rampAt("09:45")
	if v, src := SideTimeRampWithSource(rampState(SidePolicyBull), am); v != config.CarrierSideRampBullAM() || src != "ramp_bull_am" {
		t.Fatalf("bull AM got %v/%s want %v/ramp_bull_am", v, src, config.CarrierSideRampBullAM())
	}
	if v, src := SideTimeRampWithSource(rampState(SidePolicyBear), am); v != config.CarrierSideRampBearAM() || src != "ramp_bear_am" {
		t.Fatalf("bear AM got %v/%s want %v/ramp_bear_am", v, src, config.CarrierSideRampBearAM())
	}
}

// ③ 13:00–14:00 + latch 活跃 ⇒ 保持 AM 档。这是 owner「下午有坏信号就先别释放」
// 的唯一落点；写成"latch 只在 13:00 那一刻算数"会让它事实上永不生效。
func TestSideTimeRampPMChecksLatchAndHoldsAM(t *testing.T) {
	st := rampState(SidePolicyBull)
	st.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00", Reason: "pm_weak"}
	v, src := SideTimeRampWithSource(st, rampAt("13:30"))
	if v != config.CarrierSideRampBullAM() || src != "ramp_bull_am" {
		t.Fatalf("latch active got %v/%s want %v/ramp_bull_am", v, src, config.CarrierSideRampBullAM())
	}
	// 熊市镜像：上午 0.35，下午坏信号 ⇒ 仍然 0.35。
	bear := rampState(SidePolicyBear)
	bear.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00"}
	if v, src := SideTimeRampWithSource(bear, rampAt("13:30")); v != config.CarrierSideRampBearAM() || src != "ramp_bear_am" {
		t.Fatalf("bear latch active got %v/%s", v, src)
	}
}

// ④ 同区间 latch 不活跃 ⇒ PM 档。
func TestSideTimeRampPMWithoutLatch(t *testing.T) {
	if v, src := SideTimeRampWithSource(rampState(SidePolicyBull), rampAt("13:30")); v != config.CarrierSideRampBullPM() || src != "ramp_bull_pm" {
		t.Fatalf("bull PM got %v/%s want %v/ramp_bull_pm", v, src, config.CarrierSideRampBullPM())
	}
	if v, src := SideTimeRampWithSource(rampState(SidePolicyBear), rampAt("13:30")); v != config.CarrierSideRampBearPM() || src != "ramp_bear_pm" {
		t.Fatalf("bear PM got %v/%s want %v/ramp_bear_pm", v, src, config.CarrierSideRampBearPM())
	}
	// 过期 latch（Until 已过）不算活跃：13:30 时 Until=13:15 必须落到 PM 档。
	st := rampState(SidePolicyBull)
	st.Latch = SidePolicyLatch{Kind: "no_new", Until: "13:15"}
	if v, src := SideTimeRampWithSource(st, rampAt("13:30")); v != config.CarrierSideRampBullPM() || src != "ramp_bull_pm" {
		t.Fatalf("expired latch got %v/%s want PM", v, src)
	}
	// 非 no_new 的 latch（例如加急发布的其它 kind）不参与这一层。
	other := rampState(SidePolicyBull)
	other.Latch = SidePolicyLatch{Kind: "tighten", Until: "14:00"}
	if v, _ := SideTimeRampWithSource(other, rampAt("13:30")); v != config.CarrierSideRampBullPM() {
		t.Fatalf("kind!=no_new latch got %v want PM", v)
	}
}

// ⑤ full_at 之后无条件 1.0：latch 还在也不许压着（owner：「14:00 后不管有没有都释放剩余」）。
func TestSideTimeRampFullReleaseIgnoresLatch(t *testing.T) {
	st := rampState(SidePolicyBear)
	st.Latch = SidePolicyLatch{Kind: "no_new", Until: "14:00"}
	for _, hhmm := range []string{"14:00", "14:30", "14:59"} {
		v, src := SideTimeRampWithSource(st, rampAt(hhmm))
		if v != 1.0 || src != "ramp_full" {
			t.Fatalf("at %s got %v/%s want 1.0/ramp_full", hhmm, v, src)
		}
	}
}

// ⑥ 来源名互不相同：同一条资金帧上 bull/bear x am/pm 必须能分开读。
func TestSideTimeRampSourcesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	cases := []struct {
		mode SidePolicyMode
		at   string
	}{
		{SidePolicyBull, "09:45"},
		{SidePolicyBull, "13:30"},
		{SidePolicyBear, "09:45"},
		{SidePolicyBear, "13:30"},
		{SidePolicyBull, "14:30"},
		{SidePolicyRange, "10:00"},
	}
	for _, c := range cases {
		_, src := SideTimeRampWithSource(rampState(c.mode), rampAt(c.at))
		if src == "" {
			t.Fatalf("mode=%s at=%s produced an empty source", c.mode, c.at)
		}
		seen[src] = true
	}
	if len(seen) != 6 {
		t.Fatalf("expected 6 distinct source names, got %d: %v", len(seen), seen)
	}
}

// ⑩ 表不合法 ⇒ 不缩（1.0）。方向是"读坏了照着旧行为做"，不是照着坏表砍仓。
//
// 这里直接锁 `rampTable.valid()`：它是 SideTimeRamp 里那道 `if !t.valid()`
// 的唯一判据，而"整表回落"的语义正是靠它一个布尔位保证的。出厂表本身必须合法
// （否则这条链今天就在走下坡），所以两条一起断。
func TestSideTimeRampTableValidity(t *testing.T) {
	if !rampTableOf().valid() {
		t.Fatalf("shipped ramp table is invalid: %+v", rampTableOf())
	}
	for _, bad := range []rampTable{
		{BullAM: 0, BullPM: 1, BearAM: 0.35, BearPM: 0.5, PMCheckAt: "13:00", FullAt: "14:00"},
		{BullAM: 1.5, BullPM: 1, BearAM: 0.35, BearPM: 0.5, PMCheckAt: "13:00", FullAt: "14:00"},
		{BullAM: 0.7, BullPM: 1, BearAM: 0.35, BearPM: 0.5, PMCheckAt: "25:00", FullAt: "14:00"},
		{BullAM: 0.7, BullPM: 1, BearAM: 0.35, BearPM: 0.5, PMCheckAt: "13:00", FullAt: "nope"},
	} {
		if bad.valid() {
			t.Fatalf("invalid table %+v was accepted by valid()", bad)
		}
	}
}
