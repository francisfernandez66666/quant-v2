// sell_boost_test.go — §DYN-SIZE P2 卖出加权守护测试（方案 docs/DYN_SIZE_FINAL_PLAN_20261016.md §2.5）。
//
// 需求（owner）："宏观事件来临或者大盘情绪不佳 → 买入降权、卖出加权"；"节假日或周末前最后一两个
// 交易日严买入宽卖出，防止假期砸盘"。L2 档的三条兑现（owner 已裁决默认档）：
//   - 观察窗减半（下限 1 分钟）：缩短"再等等看"的暴露时间；
//   - 取消延持：不给"信号还在就再拿着"的额度（与 000063 事故的延持上限同向，但更严）；
//   - 单源利空即算 dual：放宽"触线即时硬清"的证据门槛（未验证仍不给资格）。
//
// L3 档（owner 2026-10-16 明示授权）：L2 全部 + "首触止损未深破的半平"升级为全平
// （trim→close，扩大自动卖出面）。升级只发生在**窗结算确认那一刻**；已确认处置的重放按
// ConfirmedAction 逐字节走（配置热切不翻案——执行层日级幂等键已按首结论下单）。
//
// 基准态反证（本文件最重要的锁）：`Enabled=false`（出厂默认）或档位显式关（off/none/0）或
// 无触发条件时，ComputeSellBoost 恒返回 ""，sellParams 三个加权字段保持零值，sellProbe 行为与
// 改造前**逐字一致**——删掉 boost 分支不影响任何既有测试。
//
// English: guard tests for §DYN-SIZE P2 sell-boost. L2 = half window + no extension + single-source
// bear counts as dual. L3 (owner-authorized 2026-10-16) additionally escalates the first-touch
// half-trim into a full close at confirmation time only — replays stay frozen on ConfirmedAction.
// The baseline (disabled / explicit off / no trigger) must leave every adjudication byte-identical
// to the pre-change kernel.
package signalctl

import (
	"math"
	"strings"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// boostRegime 构造"已启用 + 非影子 + L2 档"的卖出加权快照（各触发条件由调用方按需填）。
func boostRegime() Regime {
	shadow := false
	return Regime{
		SizeCfg: config.SizingConfig{
			Enabled:        true,
			Shadow:         &shadow,
			SellBoostLevel: "L2",
		}.WithDefaults(),
	}
}

// boostL3Regime 构造"已启用 + 非影子 + L3 档"的卖出加权快照。
func boostL3Regime() Regime {
	shadow := false
	return Regime{
		SizeCfg: config.SizingConfig{
			Enabled:        true,
			Shadow:         &shadow,
			SellBoostLevel: "L3",
		}.WithDefaults(),
	}
}

// boostPol 把快照包成策略（卖出加权只认 Policy.Regime，纪律参数走默认）。
func boostPol(r Regime) Policy { return Policy{Regime: r} }

// ── 1. ComputeSellBoost：六个触发条件 ─────────────────────────────────────────────

// TestSellBoostTriggers 六个触发条件逐一验证 + 条件缺失一律不触发。
func TestSellBoostTriggers(t *testing.T) {
	base := boostRegime()
	cases := []struct {
		name    string
		mut     func(r *Regime)
		wantHit bool
		wantIn  string // 命中的触发说明须包含该子串
	}{
		{"情绪冰点", func(r *Regime) { r.EmotionPhase = "冰点" }, true, "情绪相位=冰点"},
		{"情绪退潮", func(r *Regime) { r.EmotionPhase = "退潮" }, true, "情绪相位=退潮"},
		{"情绪高潮不触发", func(r *Regime) { r.EmotionPhase = "高潮" }, false, ""},
		{"风险档Red", func(r *Regime) { r.RiskTier = combat_agent.RiskTierRed }, true, "风险档=Red"},
		{"风险档Yellow", func(r *Regime) { r.RiskTier = combat_agent.RiskTierYellow }, true, "风险档=Yellow"},
		{"大盘-2.0%", func(r *Regime) { r.IndexChangePct = -2.0 }, true, "大盘-2.0%"},
		{"大盘-1.5%不触发", func(r *Regime) { r.IndexChangePct = -1.5 }, false, ""},
		{"大盘读数缺失(NaN)不触发", func(r *Regime) { r.IndexChangePct = nan() }, false, ""},
		{"节前第1日(长假)", func(r *Regime) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1}
		}, true, "节前第1日"},
		{"节前第2日(周末)", func(r *Regime) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 2, PreHolidayN: 2}
		}, true, "节前第2日"},
		{"日历未加载不触发", func(r *Regime) {
			r.Holiday = HolidayWindow{CalendarLoaded: false, ClosedRun: 8, PreHolidayN: 1}
		}, false, ""},
		{"节前第3日不触发", func(r *Regime) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 3}
		}, false, ""},
		{"连续止损熔断", func(r *Regime) { r.Account = AccountState{WindowStopLosses: 4} }, true, "连续止损4次"},
		{"止损3次不触发", func(r *Regime) { r.Account = AccountState{WindowStopLosses: 3} }, false, ""},
		{"宏观事件high影响期", func(r *Regime) {
			r.MacroEvents = []MacroEventView{{Title: "CPI", Impact: "high", InWindow: true}}
		}, true, "宏观事件影响期:CPI"},
		{"宏观事件medium不触发", func(r *Regime) {
			r.MacroEvents = []MacroEventView{{Title: "PMI", Impact: "medium", InWindow: true}}
		}, false, ""},
		{"宏观事件窗口外不触发", func(r *Regime) {
			r.MacroEvents = []MacroEventView{{Title: "CPI", Impact: "high", InWindow: false, DaysToEvent: 3}}
		}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			lvl, reasons := ComputeSellBoost(r)
			if tc.wantHit {
				if lvl != SellBoostL2 {
					t.Fatalf("应命中 L2，得 %q（%v）", lvl, reasons)
				}
				joined := strings.Join(reasons, "|")
				if !strings.Contains(joined, tc.wantIn) {
					t.Fatalf("触发说明应含 %q，得 %q", tc.wantIn, joined)
				}
				return
			}
			if lvl != "" {
				t.Fatalf("不应触发，得 %q（%v）", lvl, reasons)
			}
		})
	}
}

// TestSellBoostMultiTrigger 多条件同时命中：档位不变、理由全列（审计可归因）。
func TestSellBoostMultiTrigger(t *testing.T) {
	r := boostRegime()
	r.EmotionPhase = "退潮"
	r.RiskTier = combat_agent.RiskTierRed
	r.IndexChangePct = -3.1
	r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1}
	r.Account = AccountState{WindowStopLosses: 5}
	r.MacroEvents = []MacroEventView{{Title: "FOMC", Impact: "high", InWindow: true}}
	lvl, reasons := ComputeSellBoost(r)
	if lvl != SellBoostL2 {
		t.Fatalf("应命中 L2，得 %q", lvl)
	}
	if len(reasons) != 6 {
		t.Fatalf("六个触发应全列，得 %d 条：%v", len(reasons), reasons)
	}
}

// ── 2. ComputeSellBoost：关闭态与档位 ────────────────────────────────────────────

// TestSellBoostDisabledInert 出厂默认（Enabled=false）→ 即使满屏利空也恒返回 ""（零行为变更锁）。
func TestSellBoostDisabledInert(t *testing.T) {
	r := Regime{
		EmotionPhase:   "冰点",
		RiskTier:       combat_agent.RiskTierRed,
		IndexChangePct: -5,
		Holiday:        HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1},
		Account:        AccountState{WindowStopLosses: 9},
		MacroEvents:    []MacroEventView{{Title: "CPI", Impact: "high", InWindow: true}},
		SizeCfg:        config.DefaultSizingConfig(), // Enabled=false
	}
	if lvl, reasons := ComputeSellBoost(r); lvl != "" || reasons != nil {
		t.Fatalf("总开关关必须零加权，得 %q %v", lvl, reasons)
	}
	// 零值 Regime（未注入）同样零加权。
	if lvl, _ := ComputeSellBoost(Regime{}); lvl != "" {
		t.Fatalf("零值快照必须零加权，得 %q", lvl)
	}
}

// TestSellBoostExplicitOffLevel 档位显式关（off/none/0）→ 即使启用也不加权。
func TestSellBoostExplicitOffLevel(t *testing.T) {
	for _, off := range []string{"off", "OFF", " none ", "0"} {
		shadow := false
		r := boostRegime()
		r.SizeCfg = config.SizingConfig{Enabled: true, Shadow: &shadow, SellBoostLevel: off}.WithDefaults()
		r.EmotionPhase = "冰点" // 满屏利空
		r.RiskTier = combat_agent.RiskTierRed
		if lvl, _ := ComputeSellBoost(r); lvl != "" {
			t.Fatalf("档位 %q 应显式关闭，得 %q", off, lvl)
		}
	}
}

// TestSellBoostLevelL1L3 档位可配：L1 只标注（不改判定）；L2 达到判定面；L3 在判定面之上
// 独享 trim→close 升级轴（owner 2026-10-16 授权，此前该档与 L2 同真属灰度预留）。
func TestSellBoostLevelL1L3(t *testing.T) {
	for _, lvl := range []string{"L1", "L3"} {
		shadow := false
		r := boostRegime()
		r.SizeCfg = config.SizingConfig{Enabled: true, Shadow: &shadow, SellBoostLevel: lvl}.WithDefaults()
		r.EmotionPhase = "冰点"
		got, _ := ComputeSellBoost(r)
		if got != lvl {
			t.Fatalf("档位应原样回传 %q，得 %q", lvl, got)
		}
	}
	if SellBoostApplies(SellBoostL1) {
		t.Fatal("L1 不应达到 L2 的判定面")
	}
	if !SellBoostApplies(SellBoostL2) || !SellBoostApplies(SellBoostL3) {
		t.Fatal("L2/L3 应达到判定面")
	}
	// L3 独有轴：不到 L3 一律不许动 trim（半平口径必须保持）。
	if SellBoostEscalatesTrim(SellBoostL1) || SellBoostEscalatesTrim(SellBoostL2) {
		t.Fatal("L1/L2 不得升级 trim")
	}
	if !SellBoostEscalatesTrim(SellBoostL3) {
		t.Fatal("L3 应升级 trim→close")
	}
}

// ── 3. sellProbe 三条兑现 ────────────────────────────────────────────────────────

// TestSellBoostHalvesWindow L2 下首触观察窗减半：默认 15 分钟窗 → 7 分钟。结算点仍走 09:30
// 锚定的**固定栅格**（边界⑤），栅格步长即窗长——7 分钟栅格（09:30/09:37/…/09:58/10:05）下
// 10:00 首触取下一格 = 10:05。栅格语义与既有 exitMin=15 / trailMin=45 两套栅格同构。
func TestSellBoostHalvesWindow(t *testing.T) {
	r := boostRegime()
	r.EmotionPhase = "退潮"
	pol := boostPol(r)

	c := New()
	in := sellIn("600101", 9.3) // -7% 触 −6 止损线
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应即卖，得 %+v", d)
	}
	s := stateOf(t, c, "600101")
	if s.WindowMin != 7 {
		t.Fatalf("L2 应把 15 分钟窗减半为 7，得 %d", s.WindowMin)
	}
	if !s.SettleStart.Equal(st(10, 5)) {
		t.Fatalf("7 分钟栅格下一格应为 10:05，得 %s", s.SettleStart.Format("15:04:05"))
	}
	// 基准态反证：同一输入在未加权下窗口保持 15 分钟。
	c2 := New()
	if d := c2.JudgeSell(ChannelLive, "u1", in, Policy{}, st(10, 0)); d != nil {
		t.Fatalf("首触不应即卖，得 %+v", d)
	}
	if s2 := stateOf(t, c2, "600101"); s2.WindowMin != 15 || !s2.SettleStart.Equal(st(10, 15)) {
		t.Fatalf("未加权应保持 15 分钟窗/10:15 结算点，得 %d/%s", s2.WindowMin, s2.SettleStart.Format("15:04:05"))
	}
}

// TestSellBoostCancelsExtension L2 下窗结算即使有新鲜做多信号也不延持，直接按无信号处置，
// 且理由可归因（"卖出加权取消延持" ≠ "延持额度用尽"）。
func TestSellBoostCancelsExtension(t *testing.T) {
	r := boostRegime()
	r.RiskTier = combat_agent.RiskTierRed
	pol := boostPol(r)

	c := New()
	in := sellIn("600102", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	// 窗结算（7 分钟窗 → 10:05 栅格点）有新鲜做多信号：基准态会延持，加权态必须直接处置。
	in.Bull = freshBull(st(10, 5))
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 5))
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("加权态应取消延持直接处置（止损线=半平），得 %+v", d)
	}
	if !strings.Contains(d.Reason, "卖出加权") || !strings.Contains(d.Reason, "取消延持") {
		t.Fatalf("理由须可归因（含加权取消延持），得 %q", d.Reason)
	}
	if strings.Contains(d.Reason, "延持已达") {
		t.Fatalf("加权取消延持不得与'额度用尽'混淆，得 %q", d.Reason)
	}
	if s := stateOf(t, c, "600102"); s.Extends != 0 {
		t.Fatalf("加权态不应虚增延持计数，得 %d", s.Extends)
	}
	// 基准态反证：同一输入未加权时应延持。
	c2 := New()
	if d := c2.JudgeSell(ChannelLive, "u1", in, Policy{}, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	in2 := sellIn("600102", 9.3)
	in2.Bull = freshBull(st(10, 15))
	if d := c2.JudgeSell(ChannelLive, "u1", in2, Policy{}, st(10, 15)); d != nil {
		t.Fatalf("未加权应延持，得 %+v", d)
	}
}

// TestSellBoostSingleSourceBearHardClears L2 下"单源利空 + 触止损线"即算硬清资格；
// 未验证（LLM 提案）仍不给资格——那是"可能有利空"，不足以授权不等窗的即时清仓。
func TestSellBoostSingleSourceBearHardClears(t *testing.T) {
	r := boostRegime()
	r.EmotionPhase = "冰点"
	pol := boostPol(r)

	// 单源 + 触线 → 加权态即时全平。
	c := New()
	in := sellIn("600103", 9.3)
	in.Bear = BearConfirm{Hit: true, Verified: BearVerifiedSingle}
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0))
	if d == nil || d.Action != SellActionClose {
		t.Fatalf("加权态单源利空应即时硬清，得 %+v", d)
	}
	if !strings.Contains(d.Reason, "单源") || !strings.Contains(d.Reason, "卖出加权") {
		t.Fatalf("理由须标注单源+加权，得 %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "L2卖出加权") {
		t.Fatalf("理由须带档位前缀，得 %q", d.Reason)
	}

	// 基准态反证：单源利空无硬清资格（进观察窗不处置）。
	c2 := New()
	if d := c2.JudgeSell(ChannelLive, "u1", in, Policy{}, st(10, 0)); d != nil {
		t.Fatalf("未加权单源利空不应硬清，得 %+v", d)
	}

	// 未验证利空：加权态也不给硬清资格（只进观察窗）。
	c3 := New()
	in3 := sellIn("600103", 9.3)
	in3.Bear = BearConfirm{Hit: true, Verified: BearUnverified}
	if d := c3.JudgeSell(ChannelLive, "u1", in3, pol, st(10, 0)); d != nil {
		t.Fatalf("未验证利空不应硬清（加权态同样），得 %+v", d)
	}
}

// TestSellBoostL1KeepsKernelIntact L1 只标注：观察窗不减半、延持照常、单源利空无硬清资格。
func TestSellBoostL1KeepsKernelIntact(t *testing.T) {
	shadow := false
	r := boostRegime()
	r.SizeCfg = config.SizingConfig{Enabled: true, Shadow: &shadow, SellBoostLevel: "L1"}.WithDefaults()
	r.EmotionPhase = "冰点"
	pol := boostPol(r)

	c := New()
	in := sellIn("600104", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	if s := stateOf(t, c, "600104"); s.WindowMin != 15 || !s.SettleStart.Equal(st(10, 15)) {
		t.Fatalf("L1 不应改窗口，得 %d/%s", s.WindowMin, s.SettleStart.Format("15:04:05"))
	}
	in.Bull = freshBull(st(10, 15))
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 15)); d != nil {
		t.Fatalf("L1 延持照常，得 %+v", d)
	}
	// 留痕仍带档位（L1 的价值就在于标注）。
	found := false
	for _, rec := range c.Recent(10) {
		if rec.SellBoost == SellBoostL1 {
			found = true
		}
	}
	if !found {
		t.Fatal("L1 应把档位写入裁定留痕")
	}
}

// ── 4. 影子模式 ─────────────────────────────────────────────────────────────────

// TestSellBoostShadowAnnotatesOnly 影子模式（灰度）：档位照算、留痕标 shadow，但判定面不动
// （窗口不减半、延持照常）——与买入侧 applySizing 同纪律。
func TestSellBoostShadowAnnotatesOnly(t *testing.T) {
	shadow := true
	r := boostRegime()
	r.SizeCfg = config.SizingConfig{Enabled: true, Shadow: &shadow, SellBoostLevel: "L2"}.WithDefaults()
	r.EmotionPhase = "冰点"
	pol := boostPol(r)

	c := New()
	in := sellIn("600105", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	s := stateOf(t, c, "600105")
	if s.WindowMin != 15 || !s.SettleStart.Equal(st(10, 15)) {
		t.Fatalf("影子态不应改窗口，得 %d/%s", s.WindowMin, s.SettleStart.Format("15:04:05"))
	}
	found := false
	for _, rec := range c.Recent(10) {
		if rec.SellBoost == SellBoostL2 && rec.Shadow {
			found = true
		}
	}
	if !found {
		t.Fatal("影子态应把档位+shadow 标记写入裁定留痕")
	}
}

// ── 5. 通道一致（方案 §5 测试锁） ────────────────────────────────────────────────

// TestSellBoostChannelParity live/paper 两通道对同一快照得同一档位与同一判定面
// （通道只影响状态键与执行出口，不影响加权口径）。
func TestSellBoostChannelParity(t *testing.T) {
	r := boostRegime()
	r.RiskTier = combat_agent.RiskTierYellow
	pol := boostPol(r)

	c := New()
	in := sellIn("600106", 9.3)
	c.JudgeSellView(ChannelLive, "u1", in, pol, st(10, 0))
	c.JudgeSellView(ChannelPaper, "u1", in, pol, st(10, 0))
	live := c.sellStates[sellKey{ChannelLive, "u1", "600106"}]
	paper := c.sellStates[sellKey{ChannelPaper, "u1", "600106"}]
	if live == nil || paper == nil {
		t.Fatal("两通道状态应各自建立")
	}
	if live.WindowMin != paper.WindowMin || !live.SettleStart.Equal(paper.SettleStart) {
		t.Fatalf("通道口径应一致：live=%d/%s paper=%d/%s",
			live.WindowMin, live.SettleStart.Format("15:04:05"),
			paper.WindowMin, paper.SettleStart.Format("15:04:05"))
	}
	if live.WindowMin != 7 {
		t.Fatalf("两通道都应减半为 7，得 live=%d paper=%d", live.WindowMin, paper.WindowMin)
	}
}

// ── 6. 深破优先序不受加权影响 ────────────────────────────────────────────────────

// TestSellBoostDeepBreachStillWins 加权态下砸穿深破线仍无条件全平（边界④不被加权口径架空）。
func TestSellBoostDeepBreachStillWins(t *testing.T) {
	r := boostRegime()
	r.EmotionPhase = "冰点"
	pol := boostPol(r)
	c := New()
	in := sellIn("600107", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	in.CurPrice = 8.7 // -13% 深破
	in.Bull = freshBull(st(10, 7))
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 7))
	if d == nil || d.Action != SellActionClose || !strings.Contains(d.Reason, "深破") {
		t.Fatalf("深破应无条件全平，得 %+v", d)
	}
}

// nan 返回 NaN（大盘读数不可得的弃权态）。
func nan() float64 { return math.NaN() }

// ── 7. L3：trim→close 升级（owner 2026-10-16 授权） ──────────────────────────────

// TestSellBoostL3EscalatesTrimToClose L3 核心锁：止损首触窗结算（无信号）由半平**升级为全平**
// （trim→close）——"严买入宽卖出"的最高档，首触止损即整仓离场，不留半仓暴露。同输入在 L2 下
// 必须仍是半平：两档共享判定面，只有 L3 加码（SellBoostEscalatesTrim 只对 L3 为真）。
func TestSellBoostL3EscalatesTrimToClose(t *testing.T) {
	r := boostL3Regime()
	r.EmotionPhase = "退潮"
	pol := boostPol(r)

	c := New()
	in := sellIn("600201", 9.3) // -7% 触 −6 止损线
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	// L2+ 观察窗减半（15→7 分钟）→ 10:05 栅格点结算，无信号 → 处置。
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 5))
	if d == nil || d.Action != SellActionClose {
		t.Fatalf("L3 应把首触止损半平升级为全平，得 %+v", d)
	}
	if !strings.Contains(d.Reason, "trim→close") || !strings.Contains(d.Reason, "L3卖出加权") {
		t.Fatalf("理由须可归因（trim→close + 档位前缀），得 %q", d.Reason)
	}
	if s := stateOf(t, c, "600201"); s.ConfirmedAction != SellActionClose {
		t.Fatalf("冻结动作应为全平，得 %q", s.ConfirmedAction)
	}

	// 基准态反证：同一输入在 L2 档下仍是半平。
	r2 := boostRegime() // L2
	r2.EmotionPhase = "退潮"
	c2 := New()
	if d := c2.JudgeSell(ChannelLive, "u1", in, boostPol(r2), st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	d2 := c2.JudgeSell(ChannelLive, "u1", in, boostPol(r2), st(10, 5))
	if d2 == nil || d2.Action != SellActionTrim {
		t.Fatalf("L2 应保持半平，得 %+v", d2)
	}
}

// TestSellBoostL3EscalatesUnderNoExtend L3 下"取消延持"路径同样升级：窗结算有新鲜做多信号时，
// L2 取消延持但仍是半平，L3 必须全平——两条授权叠加时取更严的那个，不互相打折。
func TestSellBoostL3EscalatesUnderNoExtend(t *testing.T) {
	r := boostL3Regime()
	r.RiskTier = combat_agent.RiskTierRed
	pol := boostPol(r)

	c := New()
	in := sellIn("600202", 9.3)
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	in.Bull = freshBull(st(10, 5)) // 窗结算仍有新鲜做多信号
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 5))
	if d == nil || d.Action != SellActionClose {
		t.Fatalf("L3 取消延持路径也应升级全平，得 %+v", d)
	}
	if !strings.Contains(d.Reason, "取消延持") || !strings.Contains(d.Reason, "trim→close") {
		t.Fatalf("理由须同时可归因（取消延持 + trim→close），得 %q", d.Reason)
	}
	if s := stateOf(t, c, "600202"); s.Extends != 0 {
		t.Fatalf("加权态不应虚增延持计数，得 %d", s.Extends)
	}

	// 基准态反证：L2 同路径取消延持但保持半平。
	r2 := boostRegime() // L2
	r2.RiskTier = combat_agent.RiskTierRed
	c2 := New()
	c2.JudgeSell(ChannelLive, "u1", sellIn("600202", 9.3), boostPol(r2), st(10, 0))
	in2 := sellIn("600202", 9.3)
	in2.Bull = freshBull(st(10, 5))
	d2 := c2.JudgeSell(ChannelLive, "u1", in2, boostPol(r2), st(10, 5))
	if d2 == nil || d2.Action != SellActionTrim {
		t.Fatalf("L2 应保持半平，得 %+v", d2)
	}
}

// TestSellBoostL3ReplayKeepsFrozenAction 重放逐字节锁（与 §0926E2E-11A 同源的幂等保护）：
// 档位是配置轴，和价格深度一样不能在重放侧翻案——L2 确认的半平处置，同一天即使配置热切到
// L3，也必须按固化的 ConfirmedAction=trim 重放（执行层日级幂等键已按首结论下单，
// 翻成 close 会造成二次卖出）。
func TestSellBoostL3ReplayKeepsFrozenAction(t *testing.T) {
	l2 := boostRegime() // L2
	l2.RiskTier = combat_agent.RiskTierYellow

	c := New()
	in := sellIn("600203", 9.3)
	c.JudgeSell(ChannelLive, "u1", in, boostPol(l2), st(10, 0))
	d1 := c.JudgeSell(ChannelLive, "u1", in, boostPol(l2), st(10, 5))
	if d1 == nil || d1.Action != SellActionTrim {
		t.Fatalf("L2 下应确认半平，得 %+v", d1)
	}
	// 热切 L3（同一天、同代码）：重放必须仍是 trim，不得升级翻案。
	d2 := c.JudgeSell(ChannelLive, "u1", in, boostPol(boostL3Regime()), st(10, 10))
	if d2 == nil || d2.Action != SellActionTrim {
		t.Fatalf("重放不得因档位热切翻案为全平，得 %+v", d2)
	}
	if strings.Contains(d2.Reason, "trim→close") {
		t.Fatalf("重放理由不得出现升级文案，得 %q", d2.Reason)
	}
	// 反向：L3 确认的全平，热切回 L2 后重放同样是全平（冻结与档位无关）。
	l3 := boostL3Regime()
	l3.RiskTier = combat_agent.RiskTierYellow
	c2 := New()
	c2.JudgeSell(ChannelLive, "u1", sellIn("600204", 9.3), boostPol(l3), st(10, 0))
	if d := c2.JudgeSell(ChannelLive, "u1", sellIn("600204", 9.3), boostPol(l3), st(10, 5)); d == nil || d.Action != SellActionClose {
		t.Fatalf("L3 下应确认全平，得 %+v", d)
	}
	d3 := c2.JudgeSell(ChannelLive, "u1", sellIn("600204", 9.3), boostPol(boostRegime()), st(10, 10))
	if d3 == nil || d3.Action != SellActionClose {
		t.Fatalf("L3 确认的全平不因热切回 L2 而降级，得 %+v", d3)
	}
}

// TestSellBoostL3ProfitLinesUnaffected L3 只升级"止损半平"这一条：止盈/移动止盈/深破的基准
// 动作本来就是全平，加权不得改变它们（升级轴是单点的，不是"什么都往大改"）。
func TestSellBoostL3ProfitLinesUnaffected(t *testing.T) {
	r := boostL3Regime()
	r.EmotionPhase = "冰点"
	pol := boostPol(r)

	c := New()
	in := sellIn("600205", 11.6) // +16% 触 15 止盈线
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应处置，得 %+v", d)
	}
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 5))
	if d == nil || d.Action != SellActionClose || d.Line != SellLineTakeProfit {
		t.Fatalf("止盈线结算应全平且线型不变，得 %+v", d)
	}
	if !strings.Contains(d.Reason, "止盈离场") {
		t.Fatalf("理由应保持止盈口径，得 %q", d.Reason)
	}
}
