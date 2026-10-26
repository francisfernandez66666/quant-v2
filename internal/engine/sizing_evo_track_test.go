// sizing_evo_track_test.go — §SIZING-EVO 引擎侧影子落盘单测。
// 钉死：开关门控、只记 pass+已定权、按 (日,通道,配置戳,代码) 去重、灰度候选 shadow 照算
// （用候选配置，与 live 值不同）、前向收益按日K比值结算、缺K重试不伪造。
package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/sizing_evo"
)

// evoTestEngine 造一个只带影子库目录的引擎（d1Store 空 → 结算腿不可用，除非注入 bars）。
func evoTestEngine(t *testing.T) *Engine {
	t.Helper()
	return &Engine{evoStoreDir: t.TempDir()}
}

// evoTestPolicy 造 evo 开启的准入策略（sizing 也开，BuyScale 才有意义）。
func evoTestPolicy(t *testing.T) signalctl.Policy {
	t.Helper()
	on := true
	cfg := config.DefaultSizingConfig().WithDefaults()
	cfg.Enabled = &on
	cfg.Evo.Enabled = &on
	return signalctl.Policy{Regime: signalctl.Regime{
		SizeCfg: cfg, EmotionPhase: "发酵", MarketState: "bull", BreadthUpRatio: 0.6,
		Holiday: signalctl.HolidayWindow{CalendarLoaded: true},
	}}
}

// evoTestSignals 两只买入信号（价格/ATR 足够让 scale 有意义）。
func evoTestSignals() []combat_agent.Signal {
	return []combat_agent.Signal{
		{Code: "600519", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0, FlowRatio5D: 2},
		{Code: "000001", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0, FlowRatio5D: -2},
	}
}

// TestEvoRecordDecisionsGateOff 开关关闭 → 不落任何快照。
func TestEvoRecordDecisionsGateOff(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	off := false
	pol.Regime.SizeCfg.Evo.Enabled = &off
	sigs := evoTestSignals()
	decs := []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.8}, {Verdict: signalctl.VerdictPass, BuyScale: 0.9}}
	e.evoRecordDecisions(pol, decs, sigs, "live", "u1", time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local))
	tr := e.evoTrackerRef()
	if tr == nil {
		t.Fatal("开关关闭时也不该建落盘器")
	}
	if snaps, _ := tr.store.Load("20260901"); len(snaps) != 0 {
		t.Fatalf("关闭时不得落快照，得 %d", len(snaps))
	}
}

// TestEvoRecordDecisionsWritesLive 只记 pass+已定权，去重生效，字段完整。
func TestEvoRecordDecisionsWritesLive(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	sigs := append(evoTestSignals(),
		combat_agent.Signal{Code: "600000", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0},
		combat_agent.Signal{Code: "600036", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0},
	)
	decs := []signalctl.Decision{
		{Verdict: signalctl.VerdictPass, BuyScale: 0.8, SizingReasons: []string{"情绪相位×1.00"}},
		{Verdict: signalctl.VerdictHold, BuyScale: 0},    // 未通过 → 不记
		{Verdict: signalctl.VerdictPass, BuyScale: 0},    // 未定权 → 不记
		{Verdict: signalctl.VerdictPass, BuyScale: 1.05}, // 记
	}
	e.evoRecordDecisions(pol, decs, sigs, "live", "u1", now)
	// 再调一次（同轮重放）：去重后不新增。
	e.evoRecordDecisions(pol, decs, sigs, "live", "u1", now)

	tr := e.evoTrackerRef()
	snaps, err := tr.store.Load("20260901")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("应只记 2 条（pass+已定权、去重后），得 %d：%+v", len(snaps), snaps)
	}
	s := snaps[0]
	if s.Channel != "live" || s.Account != "u1" || s.CfgVer != "u1#base" || s.Code != "600519" {
		t.Fatalf("快照标识错：%+v", s)
	}
	if s.Scale != 0.8 || s.Sig.FlowRatio5D != 2 || s.Sig.Confidence != 0.8 || s.Sig.Price != 10 {
		t.Fatalf("快照画像错：%+v", s)
	}
	if s.Regime.MarketState != "bull" || s.Regime.EmotionPhase != "发酵" {
		t.Fatalf("Regime 快照错：%+v", s.Regime)
	}
	if len(s.Reasons) != 1 || s.Reasons[0] != "情绪相位×1.00" {
		t.Fatalf("理由未落盘：%+v", s.Reasons)
	}
	if s.FwdRet != nil {
		t.Fatalf("未结算时 FwdRet 必须为 nil，得 %v", *s.FwdRet)
	}
}

// TestEvoRecordDecisionsShadow 灰度候选 → shadow 通道按**候选配置**照算（与 live 值不同）。
func TestEvoRecordDecisionsShadow(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	// 显式钉 product：本例的因果链是「kelly_lambda 0.5→0.2 ⇒ 该因子从 ~1.0 压到 ~0.6 ⇒ scale 显著下降」，
	// 这是连乘路径的线性可推性。§DYN-SIZE v4 出厂默认已是 power_mean，同一改动会被组间幂平均稀释，
	// 断言"shadow < live"就测不到它想测的东西（灰度候选比较必须同模式才有意义）。
	pol.Regime.SizeCfg.Mode = "product"
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	// 状态库：一条灰度候选（kelly 压到 0.2，scale 应显著小于 live）。
	tr := e.evoTrackerRef()
	candID := "evo1234"
	if err := tr.state.AddGrayscale(sizing_evo.GrayscaleEntry{
		CandID: candID, Params: map[string]float64{"kelly_lambda": 0.2}, EnteredAt: "20260901",
	}); err != nil {
		t.Fatal(err)
	}
	sigs := evoTestSignals()[:1]
	decs := []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.9}}
	e.evoRecordDecisions(pol, decs, sigs, "live", "u1", now)

	snaps, err := tr.store.Load("20260901")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("应有 live+shadow 两条，得 %d：%+v", len(snaps), snaps)
	}
	var live, shadow *sizing_evo.SizingSnapshot
	for i := range snaps {
		switch snaps[i].Channel {
		case "live":
			live = &snaps[i]
		case "shadow":
			shadow = &snaps[i]
		}
	}
	if live == nil || shadow == nil {
		t.Fatalf("通道缺失：%+v", snaps)
	}
	if shadow.CfgVer != candID {
		t.Fatalf("shadow 配置戳应为候选 ID，得 %q", shadow.CfgVer)
	}
	if shadow.Scale <= 0 || math.Abs(shadow.Scale-live.Scale) < 1e-9 {
		t.Fatalf("shadow 应按候选配置照算出不同 scale：live=%v shadow=%v", live.Scale, shadow.Scale)
	}
	if shadow.Scale >= live.Scale {
		t.Fatalf("kelly 0.2 应压低 scale：live=%v shadow=%v", live.Scale, shadow.Scale)
	}
}

// TestEvoSettlePass 前向收益按日K close→close 比值结算（第 5 根/第 0 根）。
func TestEvoSettlePass(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	sigs := evoTestSignals()[:1]
	decs := []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.9}}
	e.evoRecordDecisions(pol, decs, sigs, "live", "u1", now)

	tr := e.evoTrackerRef()
	// 注入 40 根日K：20260901 起，Close 10.0 每根 +0.1 → 第 5 根 10.5（+5%）。
	tr.bars = func(code string, count int) ([]data.KLine, bool) {
		base, _ := time.ParseInLocation("20060102", "20260901", time.Local)
		bars := make([]data.KLine, 40)
		for i := range bars {
			bars[i] = data.KLine{Date: base.AddDate(0, 0, i), Close: 10 + 0.1*float64(i)}
		}
		return bars, true
	}
	tr.mu.Lock()
	tr.lastSettle = time.Time{} // 清掉节流，立即结算
	tr.mu.Unlock()
	e.evoSettlePass(tr, now)

	snaps, _ := tr.store.Load("20260901")
	if len(snaps) != 1 || snaps[0].FwdRet == nil {
		t.Fatalf("应已结算，得 %+v", snaps)
	}
	if got := *snaps[0].FwdRet; math.Abs(got-5) > 1e-9 {
		t.Fatalf("前向收益 = %v, want 5", got)
	}
	if snaps[0].SettledAt == nil {
		t.Fatal("结算时刻必须落盘")
	}
	tr.mu.Lock()
	n := len(tr.pending)
	tr.mu.Unlock()
	if n != 0 {
		t.Fatalf("结算后待结算队列应清空，得 %d", n)
	}
}

// TestEvoSettleInsufficientBars 目标日K未同步 → 保留待结算重试（不伪造收益）。
func TestEvoSettleInsufficientBars(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	e.evoRecordDecisions(pol, []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.9}}, evoTestSignals()[:1], "live", "u1", now)
	tr := e.evoTrackerRef()
	tr.bars = func(code string, count int) ([]data.KLine, bool) {
		base, _ := time.ParseInLocation("20060102", "20260901", time.Local)
		return []data.KLine{{Date: base, Close: 10}, {Date: base.AddDate(0, 0, 1), Close: 10.1}}, true
	}
	tr.mu.Lock()
	tr.lastSettle = time.Time{}
	tr.mu.Unlock()
	e.evoSettlePass(tr, now)

	snaps, _ := tr.store.Load("20260901")
	if snaps[0].FwdRet != nil {
		t.Fatalf("K线不足时不得结算，得 %v", *snaps[0].FwdRet)
	}
	tr.mu.Lock()
	p := tr.pending["20260901|600519"]
	tries := 0
	if p != nil {
		tries = p.tries
	}
	tr.mu.Unlock()
	if tries < 1 {
		t.Fatalf("应记重试次数（不伪造收益），得 %d", tries)
	}
}

// TestEvoTrackerNoStoreDir 纯内存模式（dataDir 空）→ 落盘器为 nil，调用零操作。
func TestEvoTrackerNoStoreDir(t *testing.T) {
	e := &Engine{}
	if tr := e.evoTrackerRef(); tr != nil {
		t.Fatalf("无影子库目录应返回 nil，得 %+v", tr)
	}
	pol := evoTestPolicy(t)
	e.evoRecordDecisions(pol, []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.9}}, evoTestSignals()[:1], "live", "u1", time.Now())
}

// TestEvoRecordDecisionsAppliesAppliedVer 生效条目存在 → live 快照的配置戳用生效候选 ID。
func TestEvoRecordDecisionsAppliesAppliedVer(t *testing.T) {
	e := evoTestEngine(t)
	pol := evoTestPolicy(t)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	tr := e.evoTrackerRef()
	if err := tr.state.SetApplied(&sizing_evo.AppliedEntry{CandID: "evo777", Params: map[string]float64{"kelly_lambda": 0.45}, CfgVer: "global#evo777", Auto: true}); err != nil {
		t.Fatal(err)
	}
	e.evoRecordDecisions(pol, []signalctl.Decision{{Verdict: signalctl.VerdictPass, BuyScale: 0.9}}, evoTestSignals()[:1], "live", "u1", now)
	snaps, _ := tr.store.Load("20260901")
	if len(snaps) != 1 || snaps[0].CfgVer != "u1#evo777" {
		t.Fatalf("配置戳应为生效候选（账号前缀本引擎），得 %+v", snaps)
	}
}
