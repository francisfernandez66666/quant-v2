// §W5-v3 LLM 解耦测试：非 N 战法的 D1 只做参考标注（不改级别/不拦截信号），
// 负面过滤降级为 Reason 提示；N 形自身硬闸不受影响（见 n_shape 包测试）。
// English: §W5-v3 LLM decoupling tests — D1 is annotation-only for non-N strategies (no level rewrite,
// no veto); the negative filter degrades to a Reason caution. N-shape's own hard gate is unaffected.
package combat_agent

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// d1BoostDragonMD 构造龙头"准满分级"行情：封板 + 溢价 + 5日强趋势，无板块上下文时
// 总分 ≈62（已 ≥60 放宽后买入门槛）。Volume/Amount 取自最后一根日K。
// English: d1BoostDragonMD builds a dragon "near-full-score" market: sealed limit-up + premium + 5-day strong trend; without sector context the total is ≈62 (already above the relaxed 60 buy gate). Volume/Amount come from the last daily K-line.
func d1BoostDragonMD() *strategy_engine.StockMarketData {
	base := time.Now()
	ks := make([]data.KLine, 10)
	for i := 0; i < 10; i++ {
		c := 10 + 3*float64(i)/9
		ks[i] = data.KLine{
			Date: base.AddDate(0, 0, i-9), Open: c, High: c + 0.2, Low: c - 0.2, Close: c,
			Volume: 1e7, Amount: 1e8,
		}
	}
	return &strategy_engine.StockMarketData{
		Code: "000001", Name: "强势股",
		Price: 11, ChangePct: 9.9,
		Quote:  &data.StockInfo{Price: 11, ChangePct: 9.9, Volume: 1e7, Amount: 1e8},
		KLines: ks,
	}
}

// TestD1BoostAnnotationOnly §W5-v3：龙头 63 分 + D1=30 —— 级别完全由原始分决定（保持 brief/false），
// 仅写入参考分 d1_ref 与理论加成对照值 d1_ref_boost。
// English: §W5-v3 — level stays untouched; only reference annotations are recorded.
func TestD1BoostAnnotationOnly(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 63, Pass: false, Level: "brief", Confidence: 0.6, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragon, eval, &D1Score{Score: 30, Blocked: false})
	if eval.Pass || eval.Level != "brief" || eval.TotalScore != 63 {
		t.Fatalf("§W5-v3 级别不得被 D1 改写, got level=%s pass=%v total=%.2f", eval.Level, eval.Pass, eval.TotalScore)
	}
	if eval.Details["d1_ref"] != 30 {
		t.Fatalf("应记录参考分 d1_ref=30, got %v", eval.Details["d1_ref"])
	}
}

// TestD1BoostNoLevelRewriteAnyTier §W5-v3：任意战法层级都不因 D1 改写（龙回头样例）。
// English: §W5-v3 no tier rewrite for any strategy (dragon-return sample).
func TestD1BoostNoLevelRewriteAnyTier(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 56, Pass: false, Level: "none", Confidence: 0.5, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragonReturn, eval, &D1Score{Score: 20, Blocked: false})
	if eval.Pass || eval.Level != "none" || eval.TotalScore != 56 {
		t.Fatalf("§W5-v3 龙回头级别不应被改写, got level=%s pass=%v total=%.2f", eval.Level, eval.Pass, eval.TotalScore)
	}
}

// TestD1BoostNeverMutatesScoreEvenHighD1 §W5-v3：高分+满分 D1 也不改总分（旧封顶语义废除）。
// English: §W5-v3 the score is never mutated even with a maxed D1 (legacy cap semantics removed).
func TestD1BoostNeverMutatesScoreEvenHighD1(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 90, Pass: true, Level: "full_chain", Confidence: 0.9, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDoubleBump, eval, &D1Score{Score: 40, Blocked: false})
	if eval.TotalScore != 90 || !eval.Pass || eval.Level != "full_chain" {
		t.Fatalf("总分应保持 90 不变, got %.2f/%v/%s", eval.TotalScore, eval.Pass, eval.Level)
	}
}

// TestD1BoostBelowThresholdNoOp D1 分低于 BoostThreshold（8）时不加成。
// English: TestD1BoostBelowThresholdNoOp no boost when the D1 score is below BoostThreshold (8).
func TestD1BoostBelowThresholdNoOp(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 63, Pass: false, Level: "brief", Confidence: 0.6, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragon, eval, &D1Score{Score: 5, Blocked: false})
	if eval.TotalScore != 63 || eval.Pass || eval.Level != "brief" {
		t.Fatalf("D1=5 低于门槛不应加成, got total=%.2f pass=%v level=%s", eval.TotalScore, eval.Pass, eval.Level)
	}
}

// TestD1BoostBlockedNoOp D1 负面 blocked 时软加成不生效（该股由 evalAll 前置硬 veto 整体拦截）。
// English: TestD1BoostBlockedNoOp the soft boost does not apply when D1 is negatively blocked (the stock is wholly blocked by evalAll's upfront hard veto).
func TestD1BoostBlockedNoOp(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 63, Pass: false, Level: "brief", Confidence: 0.6, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragon, eval, &D1Score{Score: 30, Blocked: true})
	if eval.TotalScore != 63 || eval.Pass {
		t.Fatalf("blocked 时不应加成, got total=%.2f pass=%v", eval.TotalScore, eval.Pass)
	}
}

// TestD1BoostDisabledNoOp BoostWeight≤0（未启用）时保持原分。
// English: TestD1BoostDisabledNoOp when BoostWeight<=0 (disabled) the original score is kept.
func TestD1BoostDisabledNoOp(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 63, Pass: false, Level: "brief", Confidence: 0.6, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragon, eval, &D1Score{Score: 30, Blocked: false})
	if eval.TotalScore != 63 || eval.Pass {
		t.Fatalf("BoostWeight=0 时不应加成, got total=%.2f pass=%v", eval.TotalScore, eval.Pass)
	}
}

// TestD1BoostDragonEndToEnd 端到端：龙头真实评分 ≈62（brief/watch）时，
// 不加成时：dragon ≈62 已 ≥60（买入层级放宽到 60）→ 直接发 buy；D1=40 软加成进一步放大（仅验证加成路径仍通）。
// English: without the boost, a dragon ≈62 already clears the relaxed buy gate (≥60) → buy emitted
// directly; with D1=40 the soft boost still scales it further (verifies the boost path remains live).
func TestD1BoostDragonEndToEnd(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: dragon.New(cfg)}})

	pool := map[string]*strategy_engine.StockMarketData{"000001": d1BoostDragonMD()}

	// 对照组：无 D1（Score=0）→ dragon 62 ≥60（放宽后买入层级）→ 直接 buy
	// English: Control group: no D1 (Score=0) → dragon 62 ≥60 (relaxed buy gate) → buy directly.
	_, sigsNo := a.ScorePool([]string{"000001"}, pool, map[string]D1Score{}, "")
	if !hasDragonAction(sigsNo, "000001", "buy") {
		t.Fatalf("放宽到60后 dragon 62 无加成也应发 buy, got %+v", sigsNo)
	}

	// D1=40 → 62.3×1.15≈71.6（软加成路径仍在）→ full_chain → buy
	// English: D1=40 → 62.3×1.15≈71.6 (soft-boost path still live) → full_chain → buy.
	a2 := New(cfg.GetStrategyConfig())
	a2.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a2.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: dragon.New(cfg)}})
	_, sigsBoost := a2.ScorePool([]string{"000001"}, pool, map[string]D1Score{"000001": {Code: "000001", Score: 40, Blocked: false}}, "")
	if !hasDragonAction(sigsBoost, "000001", "buy") {
		t.Fatalf("D1=40 软加成后应升级为 dragon buy, got %+v", sigsBoost)
	}
}

// fakeAlwaysPass 恒通过的假战法，用于隔离 D1 负面硬 veto 的拦截行为。
// English: fakeAlwaysPass is a fake strategy that always passes, used to isolate the blocking behavior of the D1 negative hard veto.
type fakeAlwaysPass struct{}

// Name 返回测试桩战法名称 "恒通过"。
func (f *fakeAlwaysPass) Name() string { return "恒通过" }

// Type 返回战法信号类型 SignalDragon。
func (f *fakeAlwaysPass) Type() strategy.SignalType { return strategy.SignalDragon }

// Evaluate 固定返回满分级 Pass（TotalScore=80），用于隔离 D1 负面硬 veto 的拦截行为。
func (f *fakeAlwaysPass) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{TotalScore: 80, Pass: true, Level: "full_chain", Confidence: 0.8}, nil
}

// GenerateSignal 返回买入信号，用于隔离 D1 负面硬 veto 的拦截行为。
func (f *fakeAlwaysPass) GenerateSignal(code string, _ *strategy.Evaluation) (*strategy.Signal, error) {
	return &strategy.Signal{Action: "buy", Name: code, Confidence: 0.8}, nil
}

// TestD1BlockedAnnotatesButNotVetoes §W5-v3：Blocked 个股的信号照常产出，
// Reason 带 "⚠️LLM利空提示" 后缀；对照组（未 blocked）无该后缀。
// English: §W5-v3 blocked stocks still emit signals annotated with an LLM-caution suffix;
// the control group has none.
func TestD1BlockedAnnotatesButNotVetoes(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})

	pool := map[string]*strategy_engine.StockMarketData{"000001": d1BoostDragonMD()}

	_, sigs := a.ScorePool([]string{"000001"}, pool,
		map[string]D1Score{"000001": {Code: "000001", Score: 30, Blocked: true, Reason: "立案调查"}}, "")
	found := 0
	for _, sg := range sigs {
		if sg.Code == "000001" && strings.Contains(sg.Reason, "LLM利空提示") {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("blocked 个股应产出带利空提示后缀的信号, got %+v", sigs)
	}

	// 对照组：未 blocked 无后缀且正常产出 dragon buy
	a2 := New(cfg.GetStrategyConfig())
	a2.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a2.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})
	_, sigs2 := a2.ScorePool([]string{"000001"}, pool,
		map[string]D1Score{"000001": {Code: "000001", Score: 30, Blocked: false}}, "")
	if !hasDragonAction(sigs2, "000001", "buy") {
		t.Fatalf("对照组应正常产出 dragon buy, got %+v", sigs2)
	}
	for _, sg := range sigs2 {
		if strings.Contains(sg.Reason, "LLM利空提示") {
			t.Fatalf("未 blocked 不应有利空提示后缀: %+v", sg)
		}
	}
}

// hasDragonAction 判断信号列表中是否存在某股指定 action 的 dragon 信号。
// English: hasDragonAction reports whether the signal list contains a dragon signal for a given stock with the given action.
func hasDragonAction(sigs []Signal, code, action string) bool {
	for _, s := range sigs {
		if s.Code == code && s.Strategy == "龙头" && s.Action == action {
			return true
		}
	}
	return false
}
