// flow_factor_test.go — §FLOW-FACTOR 净流入因子单测
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.3~§2.5）。
//
// 钉死四类语义：
//  1. 档位边界（含阈值点的归档方向：达到即算，归严不归松）；
//  2. 卖出权重=买入偏离折半（strong_out 买 0.70 ↔ 卖 0.85）且取最严不连乘；
//  3. 弃权纪律：比值 0/开关关 → 该维不进合成（两条路径口径一致）；
//  4. 卖出窗长×权重与 L2+ 减半两维叠加（互不覆盖）。
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// TestFlowBandBoundaries 分档边界：阈值点按"达到即算"归更有利档。
// English: band thresholds are inclusive lower bounds.
func TestFlowBandBoundaries(t *testing.T) {
	d := config.DefaultFlowFactorConfig()
	b := d.Buy5DBands
	cases := []struct {
		ratio float64
		want  string
	}{
		{10, config.FlowBandStrongIn},
		{3, config.FlowBandStrongIn}, // 恰好压在强入线 → 强入（归严）
		{2.99, config.FlowBandMildIn},
		{1, config.FlowBandMildIn}, // 恰好压在轻入线 → 轻入
		{0.99, config.FlowBandNeutral},
		{0, ""}, // 未解析 → 弃权（不是中性！）
		{-0.5, config.FlowBandNeutral},
		{-1, config.FlowBandMildOut}, // 恰好压在轻出线 → 轻出（归严）
		{-2, config.FlowBandMildOut},
		{-3, config.FlowBandStrongOut}, // 恰好压在强出线 → 强出
		{-10, config.FlowBandStrongOut},
		{math.NaN(), ""}, // 读数不可得 → 弃权
	}
	for _, c := range cases {
		if got := flowBandOf(c.ratio, b); got != c.want {
			t.Errorf("flowBandOf(%v) = %q, want %q", c.ratio, got, c.want)
		}
	}
}

// TestFlowBuyFactors 买入两因子的档位乘子 + 弃权纪律。
// English: buy multipliers per band, plus the abstain discipline.
func TestFlowBuyFactors(t *testing.T) {
	cfg := config.SizingConfig{FlowFactor: config.DefaultFlowFactorConfig()}.WithDefaults()
	cases := []struct {
		name    string
		ratio5D float64
		ratioTD float64
		want5D  float64
		wantTD  float64
	}{
		{"双未解析", 0, 0, 0, 0},       // 都弃权
		{"5日强入", 5, 0, 1.10, 0},    // 当日缺 → 当日维弃权
		{"5日强出", -5, 0, 0.70, 0},   // 主因子惩罚档
		{"当日轻入", 0, 1.5, 0, 1.025}, // 辅因子独立解析
		{"当日强入", 0, 5, 0, 1.05},    // 当日档幅度减半（1.05 vs 5日 1.10）
		{"双命中", 5, 5, 1.10, 1.05},  // 两维都进合成
		{"当日中性", 0, 0.5, 0, 1.00},  // |ratio|<1% → 中性 1.0（合法读数，不是弃权）
		{"当日轻出", 0, -2, 0, 0.95},   // 当日流出惩罚轻一档
	}
	for _, c := range cases {
		sig := combat_agent.Signal{FlowRatio5D: c.ratio5D, FlowRatioToday: c.ratioTD}
		if got := flow5DFactor(sig, cfg); got != c.want5D {
			t.Errorf("%s: flow5DFactor(%v) = %v, want %v", c.name, c.ratio5D, got, c.want5D)
		}
		if got := flowTodayFactor(sig, cfg); got != c.wantTD {
			t.Errorf("%s: flowTodayFactor(%v) = %v, want %v", c.name, c.ratioTD, got, c.wantTD)
		}
	}
}

// TestFlowBuyFactorsSwitchOff 总开关关 → 两个买入因子全弃权（与"没有这一维"逐字一致）。
// English: master switch off abstains both buy factors.
func TestFlowBuyFactorsSwitchOff(t *testing.T) {
	off := false
	cfg := config.SizingConfig{FlowFactor: config.FlowFactorConfig{Enabled: &off}}.WithDefaults()
	sig := combat_agent.Signal{FlowRatio5D: 5, FlowRatioToday: 5}
	if got := flow5DFactor(sig, cfg); got != 0 {
		t.Errorf("flow5DFactor with Enabled=false = %v, want 0 (abstain)", got)
	}
	if got := flowTodayFactor(sig, cfg); got != 0 {
		t.Errorf("flowTodayFactor with Enabled=false = %v, want 0 (abstain)", got)
	}
	// nil=开（出厂默认）
	cfg2 := config.SizingConfig{}.WithDefaults()
	if got := flow5DFactor(sig, cfg2); got != 1.10 {
		t.Errorf("flow5DFactor with nil Enabled = %v, want 1.10", got)
	}
}

// TestFlowSellWeightHalved 卖出权重=买入偏离折半 + 取最严不连乘。
// English: sell weights are half the buy deviation; the strictest band wins (never multiplied).
func TestFlowSellWeightHalved(t *testing.T) {
	cfg := config.SizingConfig{FlowFactor: config.DefaultFlowFactorConfig()}.WithDefaults()
	cases := []struct {
		name    string
		ratio5D float64
		ratioTD float64
		want    float64
	}{
		{"5日强出→0.85（买0.70的折半中和）", -5, 0, 0.85},
		{"5日轻出→0.95（买0.85的折半中和）", -2, 0, 0.95},
		{"当日强出→0.95", 0, -5, 0.95},
		{"当日轻出→0.975", 0, -2, 0.975},
		{"两档都命中取最严（0.85，不连乘成0.81）", -5, -5, 0.85},
		{"5日中性+当日轻出", 0, -2, 0.975},
		{"未解析→不加权", 0, 0, 1.0},
		{"净流入→不加权", 5, 5, 1.0},
	}
	for _, c := range cases {
		in := SellInput{FlowRatio5D: c.ratio5D, FlowRatioToday: c.ratioTD}
		if got := flowSellWeight(in, cfg); got != c.want {
			t.Errorf("%s: flowSellWeight(%v,%v) = %v, want %v", c.name, c.ratio5D, c.ratioTD, got, c.want)
		}
	}
}

// TestFlowSellWeightSwitch 卖出侧开关可独立关（买入侧照常）。
// English: the sell switch is independent of the buy side.
func TestFlowSellWeightSwitch(t *testing.T) {
	off := false
	cfg := config.SizingConfig{FlowFactor: config.FlowFactorConfig{SellEnabled: &off}}.WithDefaults()
	in := SellInput{FlowRatio5D: -5}
	if got := flowSellWeight(in, cfg); got != 0 {
		t.Errorf("flowSellWeight with SellEnabled=false = %v, want 0 (not injected)", got)
	}
	// 买入侧不受影响
	sig := combat_agent.Signal{FlowRatio5D: 5}
	if got := flow5DFactor(sig, cfg); got != 1.10 {
		t.Errorf("buy side must stay on when only sell is off, got %v", got)
	}
}

// TestFlowWindowMin 窗长×权重（下限 1 分钟；未注入/>=1 原样）。
// English: window × weight floored at 1; unset or >=1 weights pass through.
func TestFlowWindowMin(t *testing.T) {
	cases := []struct {
		min int
		w   float64
		out int
	}{
		{15, 0.85, 12}, // int(15*0.85)=12
		{45, 0.95, 42}, // int(45*0.95)=42
		{2, 0.5, 1},    // 下限 1
		{15, 0, 15},    // 未注入
		{15, 1.0, 15},  // 中性
		{15, 1.5, 15},  // >1 不放宽（权重只缩短）
	}
	for _, c := range cases {
		if got := flowWindowMin(c.min, c.w); got != c.out {
			t.Errorf("flowWindowMin(%d,%v) = %d, want %d", c.min, c.w, got, c.out)
		}
	}
}

// TestComputeSizingFlowAbstainNeutral 因子未挂载时合成结果与改造前逐字一致（反证锁）。
// English: with no flow ratios resolved, ComputeSizing is byte-identical to pre-factor behavior.
func TestComputeSizingFlowAbstainNeutral(t *testing.T) {
	r := Regime{SizeCfg: config.DefaultSizingConfig().WithDefaults(), EmotionPhase: "高潮"}
	base := ComputeSizing(r, combat_agent.Signal{})
	sig := combat_agent.Signal{FlowRatio5D: 0, FlowRatioToday: 0}
	got := ComputeSizing(r, sig)
	if base.Scale != got.Scale {
		t.Fatalf("unresolved flow must be a no-op: base=%v got=%v", base.Scale, got.Scale)
	}
	if len(got.Reasons) != 0 {
		// 只允许既有因子（情绪相位等）出现；净流入两个维度绝不出现在理由里。
		for _, x := range got.Reasons {
			if contains(x, "净流入") {
				t.Errorf("abstain factors must not appear in reasons: %v", got.Reasons)
			}
		}
	}
}

// TestComputeSizingFlowBuyScales 买入因子经两条合成路径真实改变 Scale。
// English: resolved buy factors move the scale on both synthesis paths.
func TestComputeSizingFlowBuyScales(t *testing.T) {
	r := Regime{SizeCfg: config.DefaultSizingConfig().WithDefaults(), EmotionPhase: "高潮"}
	neutral := ComputeSizing(r, combat_agent.Signal{FlowRatio5D: 0, FlowRatioToday: 0})
	strongIn := ComputeSizing(r, combat_agent.Signal{FlowRatio5D: 5, FlowRatioToday: 5})
	strongOut := ComputeSizing(r, combat_agent.Signal{FlowRatio5D: -5, FlowRatioToday: -5})
	if !(strongIn.Scale > neutral.Scale) {
		t.Errorf("strong inflow should raise the scale: neutral=%v in=%v", neutral.Scale, strongIn.Scale)
	}
	if !(strongOut.Scale < neutral.Scale) {
		t.Errorf("strong outflow should lower the scale: neutral=%v out=%v", neutral.Scale, strongOut.Scale)
	}
	// 留痕可归因：两个维度都点名
	joined := ""
	for _, x := range strongIn.Reasons {
		joined += x + "|"
	}
	for _, want := range []string{"5日净流入", "当日净流入"} {
		if !contains(joined, want) {
			t.Errorf("reasons missing %s: %v", want, strongIn.Reasons)
		}
	}
}

// TestComputeSizingFlowLegacyPath legacy product 路径同样吃两个因子。
// English: the legacy product path also consumes both factors.
func TestComputeSizingFlowLegacyPath(t *testing.T) {
	cfg := config.DefaultSizingConfig().WithDefaults()
	cfg.Mode = "legacy"
	r := Regime{SizeCfg: cfg, EmotionPhase: "高潮"}
	neutral := ComputeSizing(r, combat_agent.Signal{})
	strongIn := ComputeSizing(r, combat_agent.Signal{FlowRatio5D: 5})
	if strongIn.Scale <= neutral.Scale {
		t.Errorf("legacy path: strong inflow should raise scale: neutral=%v in=%v", neutral.Scale, strongIn.Scale)
	}
	strongOut := ComputeSizing(r, combat_agent.Signal{FlowRatio5D: -5})
	if strongOut.Scale >= neutral.Scale {
		t.Errorf("legacy path: strong outflow should lower scale: neutral=%v out=%v", neutral.Scale, strongOut.Scale)
	}
}

// TestFlowSellWeightTwoDimensionsWithBoost 净流出权重与 L2+ 减半两维叠加（窗长层面）。
// English: the flow weight and the L2+ halving stack on the window (two independent dimensions).
func TestFlowSellWeightTwoDimensionsWithBoost(t *testing.T) {
	cfg := config.DefaultSizingConfig().WithDefaults()
	// 仅权重：15 × 0.85 = 12
	if got := flowWindowMin(15, flowSellWeight(SellInput{FlowRatio5D: -5}, cfg)); got != 12 {
		t.Errorf("weight only: window = %d, want 12", got)
	}
	// 权重 + L2 减半：(15×0.85)/2 = 6
	if got := flowWindowMin(flowWindowMin(15, 0.85), 0.5); got != 6 {
		t.Errorf("weight + boost halving: window = %d, want 6", got)
	}
}

// TestFlowCfgOfBare 裸 cfg（未过 WithDefaults）按出厂档位行事——防调用方漏合并。
// English: a bare cfg (never merged) behaves with factory defaults.
func TestFlowCfgOfBare(t *testing.T) {
	bare := config.SizingConfig{} // FlowFactor 全零值
	sig := combat_agent.Signal{FlowRatio5D: 5}
	if got := flow5DFactor(sig, bare); got != 1.10 {
		t.Errorf("bare cfg flow5DFactor = %v, want factory 1.10", got)
	}
	in := SellInput{FlowRatio5D: -5}
	if got := flowSellWeight(in, bare); got != 0.85 {
		t.Errorf("bare cfg flowSellWeight = %v, want factory 0.85", got)
	}
}

// TestExplicitZeroAbstains 显式 0=弃权（进化器 C 组合层关因子的唯一手段）：
// scaleOr / tierFactor / flowScaleOr / breadthFactor 都必须放行 0 而不是兜底默认。
// English: an explicit 0 abstains the dimension (never falls back to a default).
func TestExplicitZeroAbstains(t *testing.T) {
	// scaleOr：显式 0 → 0；缺键/负值 → 默认。
	if got := scaleOr(map[string]float64{"-2": 0}, "-2", 0.70); got != 0 {
		t.Errorf("scaleOr explicit 0 = %v, want 0 (abstain)", got)
	}
	if got := scaleOr(map[string]float64{"-2": -1}, "-2", 0.70); got != 0.70 {
		t.Errorf("scaleOr negative = %v, want default 0.70", got)
	}
	if got := scaleOr(nil, "-2", 0.70); got != 0.70 {
		t.Errorf("scaleOr missing = %v, want default 0.70", got)
	}
	// tierFactor：Red 显式 0 → 弃权；缺键 → 0.30 默认。
	cfg := config.SizingConfig{TierScales: map[string]float64{"Red": 0}}
	if got := tierFactor(combat_agent.RiskTierRed, cfg); got != 0 {
		t.Errorf("tierFactor Red explicit 0 = %v, want 0 (abstain)", got)
	}
	if got := tierFactor(combat_agent.RiskTierRed, config.SizingConfig{}); got != 0.30 {
		t.Errorf("tierFactor Red missing = %v, want 0.30 default", got)
	}
	// breadthFactor：平面字段无三态，非正一律兜 0.90 降权（参与面恒开，见 sizing.go 注释）。
	if got := breadthFactor(math.NaN(), config.SizingConfig{BreadthMissingScale: 0}); got != 0.90 {
		t.Errorf("breadth explicit 0 = %v, want 0.90 (plain float has no tri-state)", got)
	}
	if got := breadthFactor(math.NaN(), config.SizingConfig{}); got != 0.90 {
		t.Errorf("breadth missing = %v, want 0.90 default", got)
	}
	// flowBuyFactor / flowSellWeight：档位显式 0 → 该档弃权。
	fc := config.FlowFactorConfig{
		Buy5DScales: map[string]float64{config.FlowBandStrongIn: 0},
		SellScales:  map[string]float64{config.FlowBandStrongOut: 0},
	}
	sig := combat_agent.Signal{FlowRatio5D: 5}
	if got := flow5DFactor(sig, config.SizingConfig{FlowFactor: fc}); got != 0 {
		t.Errorf("flow5DFactor with explicit 0 scale = %v, want 0 (abstain)", got)
	}
	// 卖出侧强出档写 0 → 该档不触发加权（权重保持 1.0），而非兜底 0.85。
	if got := flowSellWeight(SellInput{FlowRatio5D: -5}, config.SizingConfig{FlowFactor: fc}); got != 1.0 {
		t.Errorf("flowSellWeight with explicit 0 = %v, want 1.0 (no weight)", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── JudgeSellView 端到端：净流出真实缩短观察窗 ───────────────────────────────────

// flowRegime 构造"动态仓位开 + 非影子 + 无 boost 触发"的快照（只留净流出加权一个变量）。
// English: regime with sizing on, non-shadow, no boost trigger — the flow weight is the only variable.
func flowRegime() Regime {
	shadow := false
	return Regime{SizeCfg: config.SizingConfig{Enabled: boolPtr(true), Shadow: &shadow}.WithDefaults()}
}

// TestJudgeSellFlowWeightShortensWindow 5 日强流出 → 15 分钟窗 ×0.85 = 12 分钟，
// 结算点走 12 分钟栅格（10:00 首触 → 10:06）；理由带权重可归因。
// English: a strong 5-day outflow shortens the 15-minute window to 12 and lands on the 12-min grid.
func TestJudgeSellFlowWeightShortensWindow(t *testing.T) {
	pol := Policy{Regime: flowRegime()}
	c := New()
	in := sellIn("600201", 9.3) // -7% 触 −6 止损线
	in.FlowRatio5D = -5
	v := c.JudgeSellView(ChannelLive, "u1", in, pol, st(10, 0))
	if v.FlowWeight != 0.85 {
		t.Fatalf("FlowWeight = %v, want 0.85", v.FlowWeight)
	}
	s := stateOf(t, c, "600201")
	if s.WindowMin != 12 {
		t.Fatalf("窗长应 15×0.85=12，得 %d", s.WindowMin)
	}
	if !s.SettleStart.Equal(st(10, 6)) {
		t.Fatalf("12 分钟栅格（09:30 锚）10:00 首触应落 10:06，得 %s", s.SettleStart.Format("15:04:05"))
	}
	if !contains(v.HoldReason, "净流出加权窗长×0.85") {
		t.Fatalf("留痕理由须带权重，得 %q", v.HoldReason)
	}
	// 基准态反证：同一输入无流出读数 → 窗长/结算点保持 15 分钟口径。
	c2 := New()
	c2.JudgeSellView(ChannelLive, "u1", sellIn("600201", 9.3), Policy{Regime: flowRegime()}, st(10, 0))
	s2 := stateOf(t, c2, "600201")
	if s2.WindowMin != 15 || !s2.SettleStart.Equal(st(10, 15)) {
		t.Fatalf("无读数应保持 15 分钟窗/10:15，得 %d/%s", s2.WindowMin, s2.SettleStart.Format("15:04:05"))
	}
}

// TestJudgeSellFlowWeightDisposalReason 净流出加权态窗结算处置，理由前置 [净流出加权] 前缀。
// English: the disposal reason under a flow weight carries the [净流出加权] prefix.
func TestJudgeSellFlowWeightDisposalReason(t *testing.T) {
	pol := Policy{Regime: flowRegime()}
	c := New()
	in := sellIn("600202", 9.3)
	in.FlowRatio5D = -5 // 强出 → 12 分钟窗
	if d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 0)); d != nil {
		t.Fatalf("首触不应即卖，得 %+v", d)
	}
	// 12 分钟栅格结算点 10:06，无做多信号 → 处置（止损线首触未深破=半平）。
	d := c.JudgeSell(ChannelLive, "u1", in, pol, st(10, 6))
	if d == nil || d.Action != SellActionTrim {
		t.Fatalf("窗结算应半平，得 %+v", d)
	}
	if !contains(d.Reason, "[净流出加权]") {
		t.Fatalf("处置理由须带 [净流出加权] 前缀，得 %q", d.Reason)
	}
}

// TestJudgeSellFlowWeightShadow 影子模式只留痕不下发：窗长不动，但 FlowWeight 上报。
// English: shadow records only — the window is untouched but the weight is still reported.
func TestJudgeSellFlowWeightShadow(t *testing.T) {
	r := flowRegime()
	shadow := true
	r.SizeCfg.Shadow = &shadow
	pol := Policy{Regime: r}
	c := New()
	in := sellIn("600203", 9.3)
	in.FlowRatio5D = -5
	v := c.JudgeSellView(ChannelLive, "u1", in, pol, st(10, 0))
	if v.FlowWeight != 0.85 {
		t.Fatalf("影子态仍应上报权重 0.85，得 %v", v.FlowWeight)
	}
	if s := stateOf(t, c, "600203"); s.WindowMin != 15 {
		t.Fatalf("影子态不得改窗长，得 %d", s.WindowMin)
	}
}

// TestJudgeSellFlowWeightWithBoost 净流出权重与 L2 减半两维叠加：(15×0.85)/2 = 6 分钟。
// English: the flow weight and the L2 halving stack: (15×0.85)/2 = 6.
func TestJudgeSellFlowWeightWithBoost(t *testing.T) {
	r := boostRegime() // L2 档
	r.EmotionPhase = "退潮"
	pol := boostPol(r)
	c := New()
	in := sellIn("600204", 9.3)
	in.FlowRatio5D = -5
	c.JudgeSellView(ChannelLive, "u1", in, pol, st(10, 0))
	s := stateOf(t, c, "600204")
	if s.WindowMin != 6 {
		t.Fatalf("权重×档位应叠加为 6 分钟，得 %d", s.WindowMin)
	}
	// 6 分钟栅格：09:30/09:36/…/10:00/10:06 → 10:00 首触落 10:06
	if !s.SettleStart.Equal(st(10, 6)) {
		t.Fatalf("6 分钟栅格 10:00 首触应落 10:06，得 %s", s.SettleStart.Format("15:04:05"))
	}
}
