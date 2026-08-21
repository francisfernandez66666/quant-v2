// C1 D1 软加成与负面硬 veto 测试。
// English: C1 D1 soft boost and negative hard-veto tests.
package combat_agent

import (
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
		Code: "300000", Name: "强势股",
		Price: 11, ChangePct: 9.9,
		Quote:  &data.StockInfo{Price: 11, ChangePct: 9.9, Volume: 1e7, Amount: 1e8},
		KLines: ks,
	}
}

// TestD1BoostNearMissCrossesBuyGate 软加成单元：龙头 63 分 + D1=30（0~40 制）
// → 63×1.1125≈70.1 ≥60 → 提升为 full_chain/Pass，跨过买入门槛，且加成量被记录。
// English: TestD1BoostNearMissCrossesBuyGate soft-boost unit: dragon 63 + D1=30 (0~40 scale) → 63×1.1125≈70.1 >=60 → raised to full_chain/Pass, crossing the buy gate, and the boost amount is recorded.
func TestD1BoostNearMissCrossesBuyGate(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 63, Pass: false, Level: "brief", Confidence: 0.6, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragon, eval, &D1Score{Score: 30, Blocked: false})
	if !eval.Pass || eval.Level != "full_chain" {
		t.Fatalf("63分+D1=30 应提升为 full_chain/pass, got level=%s pass=%v total=%.2f", eval.Level, eval.Pass, eval.TotalScore)
	}
	if eval.Details["d1_raw"] != 63 {
		t.Fatalf("应记录原始总分 d1_raw=63, got %v", eval.Details["d1_raw"])
	}
	if eval.Details["d1_boost"] <= 0 {
		t.Fatalf("应记录加成量 d1_boost>0, got %v", eval.Details["d1_boost"])
	}
}

// TestD1BoostDragonReturnFirst 龙回头 56 分 + D1=20 → 56×1.075≈60.2 ≥60 → 提升为 first/Pass。
// English: TestD1BoostDragonReturnFirst dragon-return 56 + D1=20 → 56×1.075≈60.2 >=60 → raised to first/Pass.
func TestD1BoostDragonReturnFirst(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 56, Pass: false, Level: "none", Confidence: 0.5, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDragonReturn, eval, &D1Score{Score: 20, Blocked: false})
	if !eval.Pass || eval.Level != "first" {
		t.Fatalf("56分+D1=20 应提升为 first/pass, got level=%s pass=%v total=%.2f", eval.Level, eval.Pass, eval.TotalScore)
	}
}

// TestD1BoostCapAt100 加成封顶 100：90 分 + D1=40 → 90×1.15=103.5 → 截断到 100 且仍为买入档。
// English: TestD1BoostCapAt100 boost caps at 100: 90 + D1=40 → 90×1.15=103.5 → truncated to 100 and still the buy tier.
func TestD1BoostCapAt100(t *testing.T) {
	a := New(config.NewManager("").GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	eval := &strategy.Evaluation{TotalScore: 90, Pass: true, Level: "full_chain", Confidence: 0.9, Details: map[string]float64{}}
	a.applyD1Boost(strategy.SignalDoubleBump, eval, &D1Score{Score: 40, Blocked: false})
	if eval.TotalScore != 100 {
		t.Fatalf("加成应封顶 100, got %.2f", eval.TotalScore)
	}
	if eval.Level != "full_chain" || !eval.Pass {
		t.Fatalf("封顶后应保持 full_chain/pass, got %s/%v", eval.Level, eval.Pass)
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

	pool := map[string]*strategy_engine.StockMarketData{"300000": d1BoostDragonMD()}

	// 对照组：无 D1（Score=0）→ dragon 62 ≥60（放宽后买入层级）→ 直接 buy
	// English: Control group: no D1 (Score=0) → dragon 62 ≥60 (relaxed buy gate) → buy directly.
	_, sigsNo := a.ScorePool([]string{"300000"}, pool, map[string]D1Score{}, "")
	if !hasDragonAction(sigsNo, "300000", "buy") {
		t.Fatalf("放宽到60后 dragon 62 无加成也应发 buy, got %+v", sigsNo)
	}

	// D1=40 → 62.3×1.15≈71.6（软加成路径仍在）→ full_chain → buy
	// English: D1=40 → 62.3×1.15≈71.6 (soft-boost path still live) → full_chain → buy.
	a2 := New(cfg.GetStrategyConfig())
	a2.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a2.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: dragon.New(cfg)}})
	_, sigsBoost := a2.ScorePool([]string{"300000"}, pool, map[string]D1Score{"300000": {Code: "300000", Score: 40, Blocked: false}}, "")
	if !hasDragonAction(sigsBoost, "300000", "buy") {
		t.Fatalf("D1=40 软加成后应升级为 dragon buy, got %+v", sigsBoost)
	}
}

// fakeAlwaysPass 恒通过的假战法，用于隔离 D1 负面硬 veto 的拦截行为。
// English: fakeAlwaysPass is a fake strategy that always passes, used to isolate the blocking behavior of the D1 negative hard veto.
type fakeAlwaysPass struct{}

func (f *fakeAlwaysPass) Name() string              { return "恒通过" }
func (f *fakeAlwaysPass) Type() strategy.SignalType { return strategy.SignalDragon }
func (f *fakeAlwaysPass) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{TotalScore: 80, Pass: true, Level: "full_chain", Confidence: 0.8}, nil
}
func (f *fakeAlwaysPass) GenerateSignal(code string, _ *strategy.Evaluation) (*strategy.Signal, error) {
	return &strategy.Signal{Action: "buy", Name: code, Confidence: 0.8}, nil
}

// TestD1BlockedVetoesAllSignals 负面硬 veto：D1.Blocked=true 时该股任何战法都不产信号；
// 对照组（未 blocked）正常产出。
// English: TestD1BlockedVetoesAllSignals negative hard veto: when D1.Blocked=true no strategy produces signals for that stock; the control group (not blocked) produces them normally.
func TestD1BlockedVetoesAllSignals(t *testing.T) {
	cfg := config.NewManager("")
	a := New(cfg.GetStrategyConfig())
	a.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})

	pool := map[string]*strategy_engine.StockMarketData{"300000": d1BoostDragonMD()}

	_, sigs := a.ScorePool([]string{"300000"}, pool,
		map[string]D1Score{"300000": {Code: "300000", Score: 30, Blocked: true, Reason: "立案调查"}}, "")
	for _, s := range sigs {
		if s.Code == "300000" {
			t.Fatalf("D1 负面 blocked 应拦截所有战法信号, got %+v", s)
		}
	}

	// 对照组：未 blocked 时正常产出 dragon buy
	// English: Control group: when not blocked, a dragon buy is produced normally.
	a2 := New(cfg.GetStrategyConfig())
	a2.SetD1Config(&config.D1Config{BoostWeight: 0.15, BoostThreshold: 8})
	a2.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: &fakeAlwaysPass{}}})
	_, sigs2 := a2.ScorePool([]string{"300000"}, pool,
		map[string]D1Score{"300000": {Code: "300000", Score: 30, Blocked: false}}, "")
	if !hasDragonAction(sigs2, "300000", "buy") {
		t.Fatalf("对照组应正常产出 dragon buy, got %+v", sigs2)
	}
}

// hasDragonAction 判断信号列表中是否存在某股指定 action 的 dragon 信号。
// English: hasDragonAction reports whether the signal list contains a dragon signal for a given stock with the given action.
func hasDragonAction(sigs []Signal, code, action string) bool {
	for _, s := range sigs {
		if s.Code == code && s.Strategy == "dragon" && s.Action == action {
			return true
		}
	}
	return false
}
