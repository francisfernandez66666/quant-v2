package signalctl

import (
	"math"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// baseRegime 基准态快照：配置为出厂默认，其余读数全部零值（= 全部因子弃权）。
func baseRegime() Regime { return Regime{SizeCfg: config.DefaultSizingConfig()} }

// productRegime 与 baseRegime 同构，但把合成模式钉回 **product**。
// 为什么需要它：§DYN-SIZE v4 把出厂默认切成了 power_mean（owner 裁决），
// 而 product 路径上的"连乘/取 min/地板"这一批旧锁必须按旧语义断言；
// 用 baseRegime 会让它们静默测到新路径，锁失去区分力。
// English: pins the legacy product synthesis so the byte-identity locks keep testing what they claim.
func productRegime() Regime {
	r := baseRegime()
	r.SizeCfg.Mode = "product"
	return r
}

// baseSig 基准态信号：无置信度/无 ATR/无利空/无质量权重 → 个股侧因子全部弃权。
func baseSig() combat_agent.Signal {
	return combat_agent.Signal{Code: "600000", Name: "浦发银行", Strategy: "龙头", StrategyType: "dragon"}
}

func boolPtr(b bool) *bool { return &b }

// TestComputeSizingBaseline 基准态反证（方案 §5 第一锁）：全因子未命中 ⇒ Scale==1.0、不降级、无原因。
func TestComputeSizingBaseline(t *testing.T) {
	r := baseRegime()
	// v4 出厂默认是 power_mean，其理由串恒定包含"市场组/个股组"三行留痕；
	// "无命中因子"这条旧锁在 product 路径上才有意义，故显式锁 product。
	r.SizeCfg.Mode = "product"
	res := ComputeSizing(r, baseSig())
	if res.Downgrade {
		t.Fatalf("基准态不应降级，reasons=%v", res.Reasons)
	}
	if res.Scale != 1.0 {
		t.Fatalf("基准态 Scale 应恒为 1.0，got %.4f（reasons=%v）", res.Scale, res.Reasons)
	}
	if len(res.Reasons) != 0 {
		t.Fatalf("基准态不应有命中因子，got %v", res.Reasons)
	}

	// 同一基准态在 power_mean（v4 出厂默认）下也必须 Scale=1.0、不降级、不触封顶：
	// 组行留痕允许存在，但**不许出现惩罚因子或封顶理由**——否则默认一切换就悄悄开始降权。
	r2 := baseRegime()
	res2 := ComputeSizing(r2, baseSig())
	if res2.Downgrade || math.Abs(res2.Scale-1.0) > 1e-12 || res2.MissingKeyInput {
		t.Fatalf("power_mean 基准态必须中性，got scale=%.4f downgrade=%v missing=%v", res2.Scale, res2.Downgrade, res2.MissingKeyInput)
	}
	for _, rs := range res2.Reasons {
		if strings.Contains(rs, "×0.") || strings.Contains(rs, "触地板") || strings.Contains(rs, "封顶") {
			t.Fatalf("power_mean 基准态不该出现惩罚/封顶留痕：%v", res2.Reasons)
		}
	}
}

// TestSizingHardBlocks 硬拦因子：命中即降级（不进金额连乘）。
func TestSizingHardBlocks(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Regime, *combat_agent.Signal)
	}{
		{"情绪冰点", func(r *Regime, _ *combat_agent.Signal) { r.EmotionPhase = "冰点" }},
		// §N6-RISK：Red 默认不硬拦（走 tier_scales.Red=0.30 权重收束），只有应急开关打开才整笔拒绝。
		{"风险档Red+red_block_new_buy", func(r *Regime, _ *combat_agent.Signal) {
			r.RiskTier = combat_agent.RiskTierRed
			r.RedBlockNewBuy = true
		}},
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
			r, s := productRegime(), baseSig()
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
		// §N6-RISK：广度 NaN 不再弃权，改为 breadth_missing_scale（默认 0.90）小幅降权，
		// 由 sizing_gate_test.go 的 TestBreadthFactorMissingHaircut 覆盖。
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
	r := productRegime()
	r.EmotionPhase = "退潮"   // 0.50
	r.MarketState = "range" // 0.70 → 0.50*0.70 = 0.35
	res := ComputeSizing(r, baseSig())
	want := 0.50 * 0.70
	if math.Abs(res.Scale-want) > 1e-9 {
		t.Fatalf("期望权重连乘 %.4f，got %.4f", want, res.Scale)
	}

	// 上限类与权重取 min（不连乘）；§DYN-SIZE v4 起总仓位上限不再参与合成。
	r2 := productRegime()
	r2.Account.EquityDrawdown = 0.40 // 0.10/0.40 = 0.25
	res2 := ComputeSizing(r2, baseSig())
	if math.Abs(res2.Scale-0.25) > 1e-9 {
		t.Fatalf("上限取 min 期望 0.25，got %.4f", res2.Scale)
	}
}

// TestSizingFloorCap 地板/上限（方案 §5 第四锁）。
func TestSizingFloorCap(t *testing.T) {
	// 多重利空叠加 → 触地板 0.15。
	r := productRegime()
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
	r2 := productRegime()
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
		return ComputeSizing(productRegime(), s).Scale
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
		r := productRegime()
		r.MacroEvents = []MacroEventView{{Level: "cpi", Impact: "high", DaysToEvent: d}}
		if got := ComputeSizing(r, baseSig()).Scale; math.Abs(got-want) > 1e-9 {
			t.Fatalf("T-%d 期望 %.2f，got %.4f", d, want, got)
		}
	}
}

// TestAdmitSizingDisabledInert §DYN-SIZE 零行为变更锁：Enabled=false（账号级显式关闭，
// §DYN-GLOBAL 后不再等于出厂默认）时裁定与留痕均不变。
func TestAdmitSizingDisabledInert(t *testing.T) {
	c := New()
	pol := Policy{Discipline: config.DisciplineConfig{}} // 两窗 ≤0 → 买入直通
	pol.Regime = baseRegime()
	pol.Regime.SizeCfg.Enabled = boolPtr(false) // 关闭态必须由账号显式写出（默认已是开）
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
	cfg.Enabled, cfg.Shadow = boolPtr(true), boolPtr(false)
	// 这条锁断言的是 product 的"连乘即缩放"语义；v4 出厂默认已是 power_mean，显式锁回 product。
	cfg.Mode = "product"
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
	// §DYN-SIZE v4：拆分读数必须**一并带出**，执行层靠它算目标仓位与单笔基准；
	// 漏带会让目标层拿不到市场组读数（退潮 0.50 → 目标应为 50%）。
	if math.Abs(d.MarketScale-0.50) > 1e-9 || math.Abs(d.StockScale-1.0) > 1e-9 {
		t.Fatalf("Decision 应带出拆分读数 MS/SS，got %.4f/%.4f", d.MarketScale, d.StockScale)
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
	cfg.Enabled, cfg.Shadow = boolPtr(true), boolPtr(true) // 影子
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

// TestSizingMarketStockSplit §DYN-SIZE v4（方案 §1.4）：市场组决定目标仓位、个股组只决定单笔大小。
// 根因回顾：旧 Scale 把两组混在一起，逐票漂移 ⇒ 账户级目标与 T 池都无法定义。
// English: the v4 split — market group drives the account target, stock group drives this trade only.
func TestSizingMarketStockSplit(t *testing.T) {
	base := productRegime()
	base.EmotionPhase = "退潮" // 市场组 0.50
	base.IndexChangePct = 0  // 已知中性（避免 NaN 触发缺失判定）
	baseRes := ComputeSizing(base, baseSig())
	if math.Abs(baseRes.MarketScale-0.50) > 1e-12 {
		t.Fatalf("市场组应为 0.50，got %.4f reasons=%v", baseRes.MarketScale, baseRes.Reasons)
	}

	// 同一市场下换成高波动票：StockScale 显著缩小，MarketScale 与目标仓位一分不动。
	s := baseSig()
	s.Price = 10
	s.ATR = 0.6 // 6% 波动 → volFactor = VolTargetPct(2)/6 ≈ 0.333
	wigRes := ComputeSizing(base, s)
	if math.Abs(wigRes.MarketScale-baseRes.MarketScale) > 1e-12 {
		t.Fatalf("个股因子不得拖动市场组：base=%.4f got=%.4f", baseRes.MarketScale, wigRes.MarketScale)
	}
	if math.Abs(wigRes.StockScale-1.0) < 1e-9 {
		t.Fatalf("高波动票的 StockScale 必须小于 1.0，got %.4f", wigRes.StockScale)
	}
	// 期望值＝波动率目标因子按 [VolClampLo, VolClampHi] 夹取后的结果（夹取是刻意的，写死数字会随默认档漂移）
	c := base.SizeCfg
	want := c.VolTargetPct / 6.0
	if want < c.VolClampLo {
		want = c.VolClampLo
	}
	if want > c.VolClampHi {
		want = c.VolClampHi
	}
	if math.Abs(wigRes.StockScale-want) > 1e-6 {
		t.Fatalf("StockScale 应等于夹取后的波动率因子 %.4f，got %.4f", want, wigRes.StockScale)
	}
}

// TestSizingBuyScaleMatchesLegacyScale §6.1 回归锁：product 路径在上限未触发时 BuyScale ≡ Scale。
func TestSizingBuyScaleMatchesLegacyScale(t *testing.T) {
	cases := []struct{ emotion, state string }{
		{"", ""},
		{"退潮", "range"},
		{"发酵", "bull"},
		{"高潮", "bear"},
	}
	for _, c := range cases {
		r := productRegime()
		r.EmotionPhase, r.MarketState, r.IndexChangePct = c.emotion, c.state, 0
		res := ComputeSizing(r, baseSig())
		if math.Abs(res.BuyScale()-res.Scale) > 1e-12 {
			t.Fatalf("%s/%s BuyScale 应与改造前 Scale 逐字一致：buy=%.6f scale=%.6f",
				c.emotion, c.state, res.BuyScale(), res.Scale)
		}
	}
}

// TestSizingMissingKeyInputCapsTarget §1.4 覆盖检查：关键市场读数缺失 ⇒ 目标封顶 0.50。
// 反例（不封顶）：维度被 owner 主动关闭、或只是广度缺失（已另有 0.90 小降权，再计一次即双罚）。
func TestSizingMissingKeyInputCapsTarget(t *testing.T) {
	// 大盘断供（NaN）。
	r := baseRegime()
	r.IndexChangePct = math.NaN()
	res := ComputeSizing(r, baseSig())
	if !res.MissingKeyInput || math.Abs(res.MarketScale-0.50) > 1e-12 {
		t.Fatalf("大盘 NaN 应封顶 0.50，got missing=%v ms=%.4f reasons=%v", res.MissingKeyInput, res.MarketScale, res.Reasons)
	}
	if math.Abs(res.StockScale-1.0) > 1e-12 {
		t.Fatalf("封顶只作用于市场组，不该动个股组，got %.4f", res.StockScale)
	}

	// 状态机已启用却没有读数（真盲区）。
	r2 := baseRegime()
	r2.IndexChangePct, r2.MarketStateEnabled = 0, true
	res2 := ComputeSizing(r2, baseSig())
	if !res2.MissingKeyInput || math.Abs(res2.MarketScale-0.50) > 1e-12 {
		t.Fatalf("状态机开启但无读数应封顶，got missing=%v ms=%.4f", res2.MissingKeyInput, res2.MarketScale)
	}

	// 情绪探测已配置但本轮未产出。
	r3 := baseRegime()
	r3.IndexChangePct, r3.EmotionEnabled = 0, true
	res3 := ComputeSizing(r3, baseSig())
	if !res3.MissingKeyInput {
		t.Fatalf("情绪维度启用且缺失应标记 MissingKeyInput")
	}

	// 广度缺失不计关键缺失。
	r4 := baseRegime()
	r4.IndexChangePct, r4.BreadthUpRatio = 0, math.NaN()
	res4 := ComputeSizing(r4, baseSig())
	if res4.MissingKeyInput {
		t.Fatalf("广度缺失不该触发关键输入封顶：reasons=%v", res4.Reasons)
	}

	// 两维都被主动关闭 ⇒ 不封顶（关功能 ≠ 看不清水位）。
	r5 := baseRegime()
	r5.IndexChangePct = 0
	res5 := ComputeSizing(r5, baseSig())
	if res5.MissingKeyInput || math.Abs(res5.MarketScale-1.0) > 1e-12 {
		t.Fatalf("未启用的维度不应封顶，got missing=%v ms=%.4f", res5.MissingKeyInput, res5.MarketScale)
	}
}

// TestSizingTotalPosCapMovedOut §1.6：总仓位上限不再是缩放乘子。
// 它现在是目标仓位的定义（Tgt＝总资产×min(MS,1)），账户级拦截由 ordbudget 的 TargetRoom 负责；
// 留在合成里会让半仓时恒等于 1.0（永不约束）而满仓时又把同一件事罚两次。
func TestSizingTotalPosCapMovedOut(t *testing.T) {
	r := baseRegime()
	r.Account.TotalPosPct = 1.6 // 旧口径会把缩放压到 0.50
	res := ComputeSizing(r, baseSig())
	if math.Abs(res.Scale-1.0) > 1e-12 {
		t.Fatalf("总仓位上限已移出合成，Scale 应保持 1.0，got %.4f reasons=%v", res.Scale, res.Reasons)
	}
	if strings.Contains(strings.Join(res.Reasons, "|"), "总仓位上限") {
		t.Fatalf("理由串不应再出现总仓位上限：%v", res.Reasons)
	}
}
