// risk_tier_test.go — §MARKET_RISK_GATE P3 风险档合成 + 信号收紧测试。
// 覆盖：Red/Yellow 矩阵三源、缺失维度弃权、总开关/情绪开关热回退、applyRiskTier 门槛/拦截/板块上浮。
package combat_agent

import (
	"math"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy"
)

func contractTodayEvent(now time.Time) data.MacroEvent {
	return data.MacroEvent{Date: now, Level: "contract", Title: "股指期货交割日", Impact: "high", Duration: 2}
}
func cpiWindowEvent(now time.Time) data.MacroEvent {
	return data.MacroEvent{Date: now.AddDate(0, 0, 1), Level: "cpi", Title: "9月CPI", Impact: "high", Duration: 2, DaysLeft: 1}
}

// TestSynthesizeRiskTierMatrix 逐条覆盖风险档矩阵与优先级（Red 压过 Yellow）。
func TestSynthesizeRiskTierMatrix(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)
	base := config.MacroGateConfig{} // 默认：RiskGateOn=true, EmotionOn=true, levels/阈值取默认
	cases := []struct {
		name      string
		emotion   string
		state     string
		upRatio   float64
		events    []data.MacroEvent
		want      string
		wantInRes string
	}{
		{"冰点→Red", "冰点", "range", 0.5, nil, "Red", "情绪冰点"},
		{"熊市→Red", "启动", "bear", 0.5, nil, "Red", "市场状态熊市"},
		{"交割日当日→Red", "发酵", "bull", 0.6, []data.MacroEvent{contractTodayEvent(now)}, "Red", "交割日"},
		{"高影响事件×退潮→Red", "退潮", "range", 0.5, []data.MacroEvent{cpiWindowEvent(now)}, "Red", "CPI×情绪转弱"},
		{"退潮单源→Yellow", "退潮", "range", 0.5, nil, "Yellow", "情绪退潮"},
		{"震荡弱势广度→Yellow", "启动", "range", 0.30, nil, "Yellow", "上涨占比"},
		{"高影响事件单源→Yellow", "发酵", "bull", 0.6, []data.MacroEvent{cpiWindowEvent(now)}, "Yellow", "影响期"},
		{"交割影响期→Yellow", "发酵", "bull", 0.6, []data.MacroEvent{contractTodayEvent(now.AddDate(0, 0, 1))}, "Yellow", "交割影响期"},
		{"全暖→空", "发酵", "bull", 0.6, nil, "", ""},
		{"广度NaN弃权", "启动", "range", math.NaN(), nil, "", ""},
	}
	for _, c := range cases {
		got, reasons := SynthesizeRiskTier(c.emotion, c.state, c.upRatio, c.events, now, base)
		if got != c.want {
			t.Errorf("%s: tier got=%q want=%q (reasons=%v)", c.name, got, c.want, reasons)
		}
		if c.wantInRes != "" && !strings.Contains(joinReasons(reasons), c.wantInRes) {
			t.Errorf("%s: reasons=%v 应含 %q", c.name, reasons, c.wantInRes)
		}
	}
}

// TestSynthesizeRiskTierRollback 两个热回退阀：总开关关=空档；情绪开关关=冰点不再触发 Red。
func TestSynthesizeRiskTierRollback(t *testing.T) {
	now := time.Now()
	f := false
	off := config.MacroGateConfig{RiskGateEnabled: &f}
	if tier, _ := SynthesizeRiskTier("冰点", "bear", 0.5, []data.MacroEvent{contractTodayEvent(now)}, now, off); tier != "" {
		t.Errorf("总开关关闭应返回空档, got=%q", tier)
	}
	emOff := config.MacroGateConfig{EmotionEnabled: &f}
	// 情绪关闭后，冰点/退潮不再参与；只剩宏观/状态维度。
	if tier, _ := SynthesizeRiskTier("冰点", "", 0.5, nil, now, emOff); tier != "" {
		t.Errorf("情绪关闭且无其他触发应空档, got=%q", tier)
	}
	// 情绪开+冰点 触发 Red（对照组）。
	emOn := config.MacroGateConfig{EmotionEnabled: boolPtr(true)}
	if tier, _ := SynthesizeRiskTier("冰点", "", 0.5, nil, now, emOn); tier != "Red" {
		t.Errorf("情绪开启冰点应 Red, got=%q", tier)
	}
}

// TestApplyRiskTierYellowBars Yellow：非 N 低置信降级、达门槛放行、动量 watch 不被 Yellow 剔除。
func TestApplyRiskTierYellowBars(t *testing.T) {
	cfg := config.MacroGateConfig{} // YellowMinConf 默认 0.90
	sigs := []Signal{
		testSignal(string(strategy.SignalDragon), "buy", 0.85), // <0.90 → watch
		testSignal(string(strategy.SignalDragon), "buy", 0.95), // ≥0.90 → 放行
		{Strategy: "动量", Action: "watch", Confidence: 0.5},     // Yellow 不剔除动量 watch
	}
	out := applyRiskTier(sigs, RiskTierYellow, cfg, []string{"情绪退潮"})
	if out[0].Action != "watch" || !strings.Contains(out[0].Reason, "风险档Yellow") {
		t.Errorf("Yellow 低置信买入应降级并标注, got=%s reason=%s", out[0].Action, out[0].Reason)
	}
	if out[1].Action != "buy" {
		t.Errorf("Yellow 达门槛买入应放行, got=%s", out[1].Action)
	}
	if len(out) != 3 {
		t.Fatalf("Yellow 不应剔除动量 watch, len=%d", len(out))
	}
}

// TestApplyRiskTierRedHardBlocks Red：门槛更高 + N 形降级 + 动量剔除。
func TestApplyRiskTierRedHardBlocks(t *testing.T) {
	cfg := config.MacroGateConfig{} // RedMinConf 默认 0.92，BlockN/Momentum 默认 true
	sigs := []Signal{
		testSignal(string(strategy.SignalNShape), "buy", 0.99), // N 形高置信也被拦截
		{Strategy: "动量", Action: "watch", Confidence: 0.99},    // 动量 watch 被剔除
		testSignal(string(strategy.SignalDragon), "buy", 0.91), // <0.92 → watch
		testSignal(string(strategy.SignalDragon), "buy", 0.93), // ≥0.92 → 放行
	}
	out := applyRiskTier(sigs, RiskTierRed, cfg, []string{"情绪冰点"})
	if out[0].Action != "watch" || !strings.Contains(out[0].Reason, "拦截N形") {
		t.Errorf("Red 应拦截 N 形, got=%s reason=%s", out[0].Action, out[0].Reason)
	}
	// 动量 watch 剔除后：N、0.91降级、0.93放行 = 3 条
	if len(out) != 3 {
		t.Fatalf("Red 应剔除动量 watch, len=%d (%+v)", len(out), out)
	}
	if out[1].Action != "watch" || out[2].Action != "buy" {
		t.Errorf("Red 门槛判定异常: %+v", out[1:])
	}
}

// TestApplyRiskTierSectorEscalation 板块映射命中：Yellow 档该信号按 Red 对待（N 形被拦、门槛 0.92）。
func TestApplyRiskTierSectorEscalation(t *testing.T) {
	cfg := config.MacroGateConfig{MacroSectorMap: map[string][]string{"cpi": {"科技"}}}
	sigs := []Signal{
		{Strategy: string(strategy.SignalDragon), Action: "buy", Confidence: 0.91, Sector: "半导体科技"}, // 命中→按 Red：0.91<0.92 降级
		{Strategy: string(strategy.SignalDragon), Action: "buy", Confidence: 0.91, Sector: "银行"},    // 未命中→Yellow：0.91<0.90? 否(≥0.90)放行
	}
	out := applyRiskTier(sigs, RiskTierYellow, cfg, []string{"CPI影响期"})
	if out[0].Action != "watch" {
		t.Errorf("命中映射的 Yellow 应按 Red 降级, got=%s", out[0].Action)
	}
	if out[1].Action != "buy" {
		t.Errorf("未命中映射的 Yellow 达 0.90 应放行, got=%s", out[1].Action)
	}
}

// TestComputeAndSetRiskTierStores 验证 ComputeAndSetRiskTier 落缓存且 RiskTier 读回一致。
func TestComputeAndSetRiskTierStores(t *testing.T) {
	a := New(nil) // 无策略配置 → macroGateConfig 零值 → 风险档走全部默认（总开关默认 ON）
	tier, reasons := a.ComputeAndSetRiskTier("冰点", "", 0.5)
	got, gotR := a.RiskTier()
	if got != tier || len(gotR) != len(reasons) {
		t.Fatalf("RiskTier 读回应与合成一致: %q/%q", got, tier)
	}
	if tier != "Red" { // 冰点默认必 Red（除非今日真实日历另有，冰点已足够）
		t.Errorf("冰点应合成 Red, got=%q", tier)
	}
}

// boolPtr 返回 b 的指针（测试构造 *bool 配置项）。
func boolPtr(b bool) *bool { return &b }

// TestRiskTierShortBoost §P7：风险日做空 sell/watch 置信度 +0.05（上限 1.0），无风险档不改。
func TestRiskTierShortBoost(t *testing.T) {
	sigs := []Signal{
		{Strategy: "高换手", Action: "sell", Confidence: 0.80},
		{Strategy: "高换手", Action: "watch", Confidence: 0.98},
		{Strategy: "高换手", Action: "buy", Confidence: 0.5}, // 非做空动作不加成
	}
	out := applyRiskTierShortBoost(sigs, RiskTierRed)
	if out[0].Confidence < 0.84 || !strings.HasPrefix(out[0].Reason, "风险档Red") {
		t.Errorf("Red 做空 sell 应+0.05并标注, conf=%.2f reason=%s", out[0].Confidence, out[0].Reason)
	}
	if out[1].Confidence > 1.0 {
		t.Errorf("置信度应封顶 1.0, got %.2f", out[1].Confidence)
	}
	if out[2].Confidence != 0.5 || out[2].Reason != "" {
		t.Errorf("buy 信号不应被做空加成改动, got %+v", out[2])
	}
	// 无风险档：原样
	none := []Signal{{Strategy: "高换手", Action: "sell", Confidence: 0.7}}
	if got := applyRiskTierShortBoost(none, RiskTierNone); got[0].Confidence != 0.7 {
		t.Errorf("空档不应加成, got %.2f", got[0].Confidence)
	}
}

// TestMarketRiskAlertsNotAuto §P5：系统性风险持仓提醒——只提醒、绝不被 SellAction 判为自动卖出动作。
func TestMarketRiskAlertsNotAuto(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("l1", "600276", "恒瑞", "做多", "dragon_return", 10, 20, 5)
	r.LogSignal("l2", "600519", "茅台", "做多", "手动", 100, 20, 5)
	r.LogSignal("s1", "000001", "平安", "做空", "手动", 8, 20, 5)
	quotes := qs(map[string]float64{"600276": 9, "600519": 100})

	alerts := a.MarketRiskAlerts(r, quotes, RiskTierRed, []string{"情绪冰点", "股指期货交割日(当日)"}, time.Now())
	if len(alerts) != 2 { // 仅做多持仓，做空跳过
		t.Fatalf("Red 应对 2 条做多持仓发减仓提醒, got %d", len(alerts))
	}
	for _, al := range alerts {
		if al.AlertType != "系统性风险" || al.Action != "减仓" || al.Direction != "提醒" {
			t.Errorf("字段异常: %+v", al)
		}
		// 关键：SellAction 必须返回 ""（不进 13e 自动卖出/减仓通道）
		if got := SellAction(al); got != "" {
			t.Errorf("系统性风险提醒不得被自动执行, SellAction=%q (%+v)", got, al)
		}
		if !containsStr(al.Reason, "冰点") || !containsStr(al.Reason, "不自动卖出") {
			t.Errorf("提醒应含触发源+不自动卖出说明, reason=%s", al.Reason)
		}
	}
	// 无风险档 → 无提醒
	if got := a.MarketRiskAlerts(r, quotes, RiskTierNone, nil, time.Now()); len(got) != 0 {
		t.Errorf("空档不应有提醒, got %d", len(got))
	}
}

// TestAutoCautionConfigDefaults §P4 配置默认：AutoRiskCaution nil=关，YellowScale nil=0.35。
func TestAutoCautionConfigDefaults(t *testing.T) {
	var q config.QMTConfig
	if q.AutoCautionOn() {
		t.Error("AutoRiskCaution 未配置应默认关闭（裁决④保守）")
	}
	if q.YellowScale() != 0.35 {
		t.Errorf("YellowPosScale 默认应 0.35, got %v", q.YellowScale())
	}
	on := true
	q.AutoRiskCaution = &on
	if !q.AutoCautionOn() {
		t.Error("显式 true 应开启谨慎层")
	}
	q.YellowPosScale = 2.5 // 非法 >1 → 回退默认
	if q.YellowScale() != 0.35 {
		t.Errorf("非法系数应回退默认 0.35, got %v", q.YellowScale())
	}
}

// TestForwardMacroWarningHit §P5：临近高影响事件（交割日 明日）应出一条"明日"前瞻提醒，键含日期。
func TestForwardMacroWarningHit(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)
	data.SetCalibratedEvents([]data.MacroEvent{
		{Date: now.AddDate(0, 0, 1), Level: "contract", Title: "股指期货交割日", Impact: "high", Duration: 1},
	})
	defer data.SetCalibratedEvents(nil)
	a := New(nil) // 默认总开关 ON
	msg, key, hit := a.ForwardMacroWarning(now)
	if !hit {
		t.Fatalf("临近高影响事件应命中前瞻预警")
	}
	if !strings.Contains(msg, "高影响事件") {
		t.Errorf("前瞻提醒文案异常: %q", msg)
	}
	if !strings.HasPrefix(key, "macro-warning@") {
		t.Errorf("去重键异常: %q", key)
	}
}

// TestForwardMacroWarningDisabled 总开关关闭 → 不前瞻（回退阀门）。
func TestForwardMacroWarningDisabled(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)
	data.SetCalibratedEvents([]data.MacroEvent{
		{Date: now.AddDate(0, 0, 1), Level: "contract", Title: "股指期货交割日", Impact: "high", Duration: 1},
	})
	defer data.SetCalibratedEvents(nil)
	off := false
	a := New(&config.StrategyConfig{MacroGate: config.MacroGateConfig{RiskGateEnabled: &off}})
	if _, _, hit := a.ForwardMacroWarning(now); hit {
		t.Fatalf("总开关关闭不应前瞻预警")
	}
}

// TestWarnWindowClamp WarnDays 默认 3、上限 7、<=0 回退 3。
func TestWarnWindowClamp(t *testing.T) {
	if got := (config.MacroGateConfig{}).WarnWindow(); got != 3 {
		t.Errorf("默认应 3, got %d", got)
	}
	if got := (config.MacroGateConfig{WarnDays: -1}).WarnWindow(); got != 3 {
		t.Errorf("<0 应回退 3, got %d", got)
	}
	if got := (config.MacroGateConfig{WarnDays: 99}).WarnWindow(); got != 7 {
		t.Errorf("上限应 7, got %d", got)
	}
	if got := (config.MacroGateConfig{WarnDays: 5}).WarnWindow(); got != 5 {
		t.Errorf("应 5, got %d", got)
	}
}
