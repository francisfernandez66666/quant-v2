package signalctl

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// baseRegime 基准态快照：配置为出厂默认，其余读数全部零值（= 全部因子弃权）。
func baseRegime() Regime { return Regime{SizeCfg: config.DefaultSizingConfig()} }

// baseSig 基准态信号：无置信度/无 ATR/无利空/无质量权重 → 个股侧因子全部弃权。
func baseSig() combat_agent.Signal {
	return combat_agent.Signal{Code: "600000", Name: "浦发银行", Strategy: "龙头", StrategyType: "dragon"}
}

func boolPtr(b bool) *bool { return &b }

// TestComputeSizingBaseline 基准态反证（方案 §5 第一锁）：全因子未命中 ⇒ Scale==1.0、不降级、无原因。
func TestComputeSizingBaseline(t *testing.T) {
	res := ComputeSizing(baseRegime(), baseSig())
	if res.Downgrade {
		t.Fatalf("基准态不应降级，reasons=%v", res.Reasons)
	}
	if res.Scale != 1.0 {
		t.Fatalf("基准态 Scale 应恒为 1.0，got %.4f（reasons=%v）", res.Scale, res.Reasons)
	}
	if len(res.Reasons) != 0 {
		t.Fatalf("基准态不应有命中因子，got %v", res.Reasons)
	}
}

// TestSizingHardBlocks 硬拦因子：命中即降级（不进金额连乘）。
func TestSizingHardBlocks(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Regime, *combat_agent.Signal)
	}{
		{"情绪冰点", func(r *Regime, _ *combat_agent.Signal) { r.EmotionPhase = "冰点" }},
		{"风险档Red", func(r *Regime, _ *combat_agent.Signal) { r.RiskTier = combat_agent.RiskTierRed }},
		{"个股LLM利空+Yellow", func(r *Regime, s *combat_agent.Signal) {
			r.RiskTier = combat_agent.RiskTierYellow
			s.D1Blocked = true
		}},
		{"连续止损熔断", func(r *Regime, _ *combat_agent.Signal) { r.Account.WindowStopLosses = 4 }},
		{"长假前最后一日禁开仓", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1}
			r.SizeCfg.HolidayBlockNew = boolPtr(true)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, s := baseRegime(), baseSig()
			c.mut(&r, &s)
			res := ComputeSizing(r, s)
			if !res.Downgrade {
				t.Fatalf("%s 应触发硬拦，got scale=%.2f reasons=%v", c.name, res.Scale, res.Reasons)
			}
		})
	}
}

// TestSizingWeightFactors 权重类因子：单因子生效时的取值。
func TestSizingWeightFactors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Regime, *combat_agent.Signal)
		want float64
	}{
		{"情绪退潮", func(r *Regime, _ *combat_agent.Signal) { r.EmotionPhase = "退潮" }, 0.50},
		{"情绪高潮", func(r *Regime, _ *combat_agent.Signal) { r.EmotionPhase = "高潮" }, 0.80},
		{"状态bear", func(r *Regime, _ *combat_agent.Signal) { r.MarketState = "bear" }, 0.40},
		{"状态range", func(r *Regime, _ *combat_agent.Signal) { r.MarketState = "range" }, 0.70},
		{"风险档Yellow", func(r *Regime, _ *combat_agent.Signal) { r.RiskTier = combat_agent.RiskTierYellow }, 0.60},
		{"大盘-1.5%", func(r *Regime, _ *combat_agent.Signal) { r.IndexChangePct = -1.5 }, 0.70},
		{"大盘-3%", func(r *Regime, _ *combat_agent.Signal) { r.IndexChangePct = -3 }, 0.40},
		{"广度0.25", func(r *Regime, _ *combat_agent.Signal) { r.BreadthUpRatio = 0.25 }, 0.70},
		{"广度0.40", func(r *Regime, _ *combat_agent.Signal) { r.BreadthUpRatio = 0.40 }, 0.85},
		{"LLM利空", func(_ *Regime, s *combat_agent.Signal) { s.D1Blocked = true }, 0.50},
		{"信号质量0.9", func(_ *Regime, s *combat_agent.Signal) { s.QualityWeight = 0.9 }, 0.90},
		{"节前长假1日", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1}
		}, 0.40},
		{"节前小长假1日", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 3, PreHolidayN: 1}
		}, 0.70},
		{"节前周末1日", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 2, PreHolidayN: 1}
		}, 0.85},
		{"宏观影响期high", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", InWindow: true, DaysToEvent: -1}}
		}, 0.50},
		{"宏观前瞻T-1", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: 1}}
		}, 0.50},
		{"宏观前瞻T-2", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: 2}}
		}, 0.70},
		{"宏观前瞻T-5", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: 5}}
		}, 0.90},
		{"宏观事件当日", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: 0}}
		}, 0.30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, s := baseRegime(), baseSig()
			c.mut(&r, &s)
			res := ComputeSizing(r, s)
			if res.Downgrade {
				t.Fatalf("%s 不应降级，reasons=%v", c.name, res.Reasons)
			}
			if math.Abs(res.Scale-c.want) > 1e-9 {
				t.Fatalf("%s 期望 %.2f，got %.4f（reasons=%v）", c.name, c.want, res.Scale, res.Reasons)
			}
		})
	}
}

// TestSizingAbstain 弃权纪律（方案 §5「弃权」锁）：缺失读数不进合成，也不当 0。
func TestSizingAbstain(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Regime, *combat_agent.Signal)
	}{
		{"情绪空串", func(r *Regime, _ *combat_agent.Signal) { r.EmotionPhase = "" }},
		{"状态空串", func(r *Regime, _ *combat_agent.Signal) { r.MarketState = "" }},
		{"大盘NaN", func(r *Regime, _ *combat_agent.Signal) { r.IndexChangePct = math.NaN() }},
		{"广度NaN", func(r *Regime, _ *combat_agent.Signal) { r.BreadthUpRatio = math.NaN() }},
		{"广度零值", func(r *Regime, _ *combat_agent.Signal) { r.BreadthUpRatio = 0 }},
		{"日历未加载", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: false, ClosedRun: 8, PreHolidayN: 1}
		}},
		{"节前窗口外", func(r *Regime, _ *combat_agent.Signal) {
			r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 0}
		}},
		{"置信度不可得", func(_ *Regime, s *combat_agent.Signal) { s.Confidence = 0 }},
		{"ATR不可得", func(_ *Regime, s *combat_agent.Signal) { s.ATR = 0; s.Price = 10 }},
		{"前瞻窗外", func(r *Regime, _ *combat_agent.Signal) {
			r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: 9}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, s := baseRegime(), baseSig()
			c.mut(&r, &s)
			res := ComputeSizing(r, s)
			if res.Downgrade {
				t.Fatalf("%s 不应降级", c.name)
			}
			if res.Scale != 1.0 {
				t.Fatalf("%s 应弃权（Scale=1.0），got %.4f reasons=%v", c.name, res.Scale, res.Reasons)
			}
		})
	}
}

// TestSizingLayeredSynthesis 分层合成（方案 §5 第二锁）：权重类连乘、上限类取 min，同一风险不双罚。
func TestSizingLayeredSynthesis(t *testing.T) {
	// 权重 0.5 × 0.5 = 0.25；上限 0.5 ⇒ 结果 = min(0.25, 0.5) = 0.25。
	r := baseRegime()
	r.EmotionPhase = "退潮"       // 0.50
	r.MarketState = "range"     // 0.70 → 0.50*0.70 = 0.35
	r.Account.TotalPosPct = 1.6 // 0.80/1.60 = 0.50
	res := ComputeSizing(r, baseSig())
	want := 0.50 * 0.70
	if math.Abs(res.Scale-want) > 1e-9 {
		t.Fatalf("期望 min(0.35, 0.50)=%.4f，got %.4f", want, res.Scale)
	}

	// 两个上限因子并存时取 min（不连乘）。
	r2 := baseRegime()
	r2.Account.TotalPosPct = 1.6     // 0.50
	r2.Account.EquityDrawdown = 0.40 // 0.10/0.40 = 0.25
	res2 := ComputeSizing(r2, baseSig())
	if math.Abs(res2.Scale-0.25) > 1e-9 {
		t.Fatalf("上限取 min 期望 0.25，got %.4f", res2.Scale)
	}
}

// TestSizingFloorCap 地板/上限（方案 §5 第四锁）。
func TestSizingFloorCap(t *testing.T) {
	// 多重利空叠加 → 触地板 0.15。
	r := baseRegime()
	r.EmotionPhase = "退潮"                                                         // 0.50
	r.MarketState = "bear"                                                        // 0.40
	r.RiskTier = combat_agent.RiskTierYellow                                      // 0.60
	r.IndexChangePct = -3                                                         // 0.40
	r.BreadthUpRatio = 0.10                                                       // 0.70
	r.Holiday = HolidayWindow{CalendarLoaded: true, ClosedRun: 8, PreHolidayN: 1} // 0.40
	res := ComputeSizing(r, baseSig())
	if res.Scale != 0.15 {
		t.Fatalf("期望触地板 0.15，got %.4f", res.Scale)
	}

	// 进攻档：强信号 + 低波动 → 但受 Cap 1.15 限制。
	r2 := baseRegime()
	s2 := baseSig()
	s2.Confidence = 1.0           // Kelly → 1.15（内部已 clamp）
	s2.ATR, s2.Price = 0.5, 100.0 // atrPct 0.5% → 2.0/0.5 = 4.0 → clamp 1.5
	res2 := ComputeSizing(r2, s2)
	if res2.Scale > 1.15 {
		t.Fatalf("合成不得超上限 1.15，got %.4f", res2.Scale)
	}
}

// TestSizingMonotonic 单调性（方案 §5 第三锁）：任一利空加重 ⇒ 合成 scale 不升。
func TestSizingMonotonic(t *testing.T) {
	steps := []func(*Regime){
		func(r *Regime) { r.EmotionPhase = "启动" },
		func(r *Regime) { r.EmotionPhase = "高潮" },
		func(r *Regime) { r.EmotionPhase = "背离" },
		func(r *Regime) { r.EmotionPhase = "退潮" },
	}
	prev := math.Inf(1)
	for i, apply := range steps {
		r := baseRegime()
		apply(&r)
		res := ComputeSizing(r, baseSig())
		if res.Scale > prev+1e-9 {
			t.Fatalf("第 %d 步利空加重后 scale 反而上升：%.4f > %.4f", i, res.Scale, prev)
		}
		prev = res.Scale
	}
}

// TestSizingKelly 信号强度（Fractional Kelly）。
func TestSizingKelly(t *testing.T) {
	mk := func(p float64) float64 {
		s := baseSig()
		s.Confidence = p
		return ComputeSizing(baseRegime(), s).Scale
	}
	if got := mk(0.0); got != 1.0 {
		t.Fatalf("p=0（置信度不可得）期望弃权 1.0，got %.4f", got)
	}
	if got := mk(0.50); math.Abs(got-0.60) > 1e-9 {
		t.Fatalf("p=0.50（无优势）期望 0.60，got %.4f", got)
	}
	if got := mk(0.51); math.Abs(got-0.60) > 1e-9 {
		t.Fatalf("p=0.51 低于最小优势概率期望 0.60，got %.4f", got)
	}
	if got := mk(1.0); math.Abs(got-1.15) > 1e-9 {
		t.Fatalf("p=1.0 期望触 1.15，got %.4f", got)
	}
	// 单调不降。
	if !(mk(0.7) < mk(0.8) && mk(0.8) < mk(0.9)) {
		t.Fatalf("Kelly 应随置信度单调不降：%.4f %.4f %.4f", mk(0.7), mk(0.8), mk(0.9))
	}
}

// TestSizingVol 个股波动率（vol targeting）：低波动放大、高波动缩小、限幅生效。
// 直接测 volFactor（合成终值会被 Cap 1.15 夹住，测不出 1.5 的上限）。
func TestSizingVol(t *testing.T) {
	cfg := config.DefaultSizingConfig()
	mk := func(atr, price float64) float64 {
		s := baseSig()
		s.ATR, s.Price = atr, price
		return volFactor(s, cfg)
	}
	if got := mk(1.0, 100); math.Abs(got-1.5) > 1e-9 { // 1% → 2/1=2 → clamp 1.5
		t.Fatalf("低波动期望触 1.5，got %.4f", got)
	}
	if got := mk(2.0, 100); math.Abs(got-1.0) > 1e-9 { // 2% → 1.0
		t.Fatalf("等于目标波动期望 1.0，got %.4f", got)
	}
	if got := mk(8.0, 100); math.Abs(got-0.5) > 1e-9 { // 8% → 0.25 → clamp 0.5
		t.Fatalf("高波动期望触 0.5，got %.4f", got)
	}
	if got := volFactor(baseSig(), cfg); got != 0 {
		t.Fatalf("ATR/现价不可得应弃权返回 0，got %.4f", got)
	}
	// 合成终值：低波动被 Cap 1.15 夹住。
	s := baseSig()
	s.ATR, s.Price = 1.0, 100
	if got := ComputeSizing(baseRegime(), s).Scale; math.Abs(got-1.15) > 1e-9 {
		t.Fatalf("低波动合成应被 Cap 夹到 1.15，got %.4f", got)
	}
}

// TestSizingTradingDayLeadCurve 前瞻曲线必须按交易日索引（自然日实现在此红）。
func TestSizingTradingDayLeadCurve(t *testing.T) {
	curve := config.DefaultSizingConfig().MacroLeadCurve
	if len(curve) != 5 {
		t.Fatalf("默认前瞻曲线应为 5 点，got %d", len(curve))
	}
	// T-5 必须等于曲线首点（0.90），T-1 等于尾点（0.50）。
	for d, want := range map[int]float64{5: 0.90, 4: 0.85, 3: 0.80, 2: 0.70, 1: 0.50} {
		r := baseRegime()
		r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: d}}
		if got := ComputeSizing(r, baseSig()).Scale; math.Abs(got-want) > 1e-9 {
			t.Fatalf("T-%d 期望 %.2f，got %.4f", d, want, got)
		}
	}
}

// TestAdmitSizingDisabledInert §DYN-SIZE P0 零行为变更：Enabled=false 时裁定与留痕均不变。
func TestAdmitSizingDisabledInert(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{}} // 两窗 ≤0 → 买入直通
	pol.Regime = baseRegime()                            // Enabled=false
	sig := baseSig()
	sig.Action, sig.Direction = "buy", "做多"
	d := c.Admit(ChannelLive, "u1", sig, pol, time.Now())
	if d.Verdict != VerdictPass {
		t.Fatalf("未启用时买入应 pass，got %s/%s", d.Verdict, d.Reason)
	}
	if d.BuyScale != 0 || d.SizingReasons != nil || d.Shadow {
		t.Fatalf("未启用时不应写任何定权字段：scale=%v reasons=%v shadow=%v", d.BuyScale, d.SizingReasons, d.Shadow)
	}
}

// TestAdmitSizingEnabledApplies 启用后：缩放写入 BuyScale，硬拦降级为 hold。
func TestAdmitSizingEnabledApplies(t *testing.T) {
	c := New()
	cfg := config.DefaultSizingConfig()
	cfg.Enabled, cfg.Shadow = true, boolPtr(false)
	pol := Policy{Discipline: config.DisciplineConfig{}}
	pol.Regime = Regime{SizeCfg: cfg, EmotionPhase: "退潮"}
	sig := baseSig()
	sig.Action, sig.Direction = "buy", "做多"
	d := c.Admit(ChannelLive, "u1", sig, pol, time.Now())
	if d.Verdict != VerdictPass {
		t.Fatalf("退潮不应降级，got %s/%s", d.Verdict, d.Reason)
	}
	if math.Abs(d.BuyScale-0.50) > 1e-9 {
		t.Fatalf("期望 BuyScale=0.50，got %.4f", d.BuyScale)
	}
	if len(d.SizingReasons) == 0 {
		t.Fatalf("应带定权原因")
	}

	// 硬拦：情绪冰点 → hold。
	pol.Regime = Regime{SizeCfg: cfg, EmotionPhase: "冰点"}
	d2 := c.Admit(ChannelLive, "u1", sig, pol, time.Now())
	if d2.Verdict != VerdictHold || d2.Stage != StageSizing {
		t.Fatalf("冰点应降级 hold@%s，got %s@%s", StageSizing, d2.Verdict, d2.Stage)
	}
}

// TestAdmitSizingShadow 影子模式：只标注，不改裁定、不写 BuyScale。
func TestAdmitSizingShadow(t *testing.T) {
	c := New()
	cfg := config.DefaultSizingConfig()
	cfg.Enabled, cfg.Shadow = true, boolPtr(true) // 影子
	pol := Policy{Discipline: config.DisciplineConfig{}}
	pol.Regime = Regime{SizeCfg: cfg, EmotionPhase: "冰点"}
	sig := baseSig()
	sig.Action, sig.Direction = "buy", "做多"
	d := c.Admit(ChannelLive, "u1", sig, pol, time.Now())
	if d.Verdict != VerdictPass {
		t.Fatalf("影子模式不得改裁定，got %s/%s", d.Verdict, d.Reason)
	}
	if !d.Shadow {
		t.Fatalf("影子命中应标注 Shadow=true")
	}
	if d.BuyScale != 0 {
		t.Fatalf("影子模式不得下发缩放，got %.4f", d.BuyScale)
	}
}

// TestComputeSizingZeroConfigSafe 未 WithDefaults 的零配置也不应算出 0（防御）。
func TestComputeSizingZeroConfigSafe(t *testing.T) {
	res := ComputeSizing(Regime{}, baseSig())
	if res.Scale != 1.0 {
		t.Fatalf("零配置快照应恒 1.0，got %.4f", res.Scale)
	}
}
