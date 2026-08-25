package combat_agent

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mkKLines 根据收盘价序列构造测试用日K线。
// 开/高/低价在收盘价基础上做固定偏移，成交量随索引递增模拟温和放量。
// English: mkKLines builds test daily K-lines from a close-price series; open/high/low are fixed offsets from the close and volume grows with the index to simulate mild accumulation.
func mkKLines(closes []float64) []data.KLine {
	out := make([]data.KLine, len(closes))
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	for i, c := range closes {
		out[i] = data.KLine{
			Date:   base.AddDate(0, 0, i),
			Open:   c - 0.2,
			High:   c + 0.5,
			Low:    c - 0.5,
			Close:  c,
			Volume: 100000 + float64(i)*1000,
		}
	}
	return out
}

// mkBullMarketData 构造一段强多头行情：收盘价持续上行 + 放量 + MACD 水上金叉红柱。
// 用于动量分/N形打分等需要"强势"特征的测试用例。
// English: mkBullMarketData builds a strongly bullish market: steadily rising closes + volume + MACD golden cross above zero with red bars; used by tests needing "strength" traits such as momentum/N-shape scoring.
func mkBullMarketData() *strategy_engine.StockMarketData {
	closes := make([]float64, 40)
	for i := range closes {
		closes[i] = 10 + float64(i)*0.3 // 持续上行
	}
	kl := mkKLines(closes)
	// 分钟K线也持续上行 → 分钟MACD 多头
	// English: Minute K-lines also rise steadily → bullish minute MACD.
	minCloses := make([]float64, 48)
	for i := range minCloses {
		minCloses[i] = 10 + float64(i)*0.02
	}
	mkl := mkKLines(minCloses)
	return &strategy_engine.StockMarketData{
		Code:       "600000",
		Name:       "测试",
		Price:      10 + 39*0.3,
		ChangePct:  2.5,
		Quote:      &data.StockInfo{Price: 10 + 39*0.3, Volume: 3_000_000},
		KLines:     kl,
		MinuteMACD: data.CalcMACD(mkl),
	}
}

// TestMomentumScore 验证动量分核心行为：
// 强多头应得高分且不超过上限；空数据得 0 分；权重全 0 回退默认；结果取整。
// English: TestMomentumScore verifies core momentum-score behavior: a strong bull gets a high score within the cap; empty data scores 0; all-zero weights fall back to defaults; the result is rounded.
func TestMomentumScore(t *testing.T) {
	md := mkBullMarketData()
	s := MomentumScore(md, config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30})
	if s < 70 {
		t.Fatalf("强多头动量分应>=70, got %.0f", s)
	}
	if s > 100 {
		t.Fatalf("动量分超上限: %.0f", s)
	}
	// 无量无趋势 → 低分
	// English: No volume and no trend → low score.
	flat := &strategy_engine.StockMarketData{Code: "000001", Price: 10}
	if MomentumScore(flat, config.MomentumConfig{}) != 0 {
		t.Fatalf("空数据动量分应为0")
	}
	// 权重全 0 → 回退默认 40/30/30，正常出分
	// English: All-zero weights → fall back to defaults 40/30/30 and score normally.
	if MomentumScore(md, config.MomentumConfig{}) != MomentumScore(md, config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30}) {
		t.Fatalf("权重全0应回退默认")
	}
	// 分数应为整数
	// English: The score should be an integer.
	if math.Round(s) != s {
		t.Fatalf("动量分应为整数, got %v", s)
	}
}

// TestNShapeScoreAlwaysReturns 验证 N 形输入构造的健壮性：
// 强多头行情下 WaveA/IntradayB/Ctx 均应非空且含关键字段，供打分不 panic。
// English: TestNShapeScoreAlwaysReturns verifies robustness of N-shape input construction: under a strong bull, WaveA/IntradayB/Ctx must all be non-nil with key fields so scoring does not panic.
func TestNShapeScoreAlwaysReturns(t *testing.T) {
	md := mkBullMarketData()
	wa := buildWaveA(md, nil)
	if wa == nil || wa.AClose <= 0 {
		t.Fatalf("WaveA 应包含昨日收盘: %+v", wa)
	}
	ib := buildIntradayB(md)
	if ib == nil || ib.CurPrice <= 0 {
		t.Fatalf("IntradayB 应有当前价: %+v", ib)
	}
	ctx := buildCtx(md, "", nil, "", 0)
	if ctx == nil {
		t.Fatalf("Ctx 不应为 nil")
	}
}

// TestEvalForNShape 验证 8a/8b 的 N 形打分路径：nil matcher 不 panic，且恒返回总分。
// 覆盖 adapter.go evalFor 中 N 形策略分支的完整数据适配链路。
// English: TestEvalForNShape verifies the 8a/8b N-shape scoring path: a nil matcher does not panic and a total score is always returned; covers the full data-adaptation chain of the N-shape branch in adapter.go evalFor.
func TestEvalForNShape(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	ns := n_shape.New(cfg, nil)
	eval, err := evalFor(StrategyRunner{Type: "n_shape", Strategy: ns}, "600000", mkBullMarketData(), nil, "", nil, "", 0)
	if err != nil {
		t.Fatalf("evalFor nshape err: %v", err)
	}
	if eval == nil {
		t.Fatalf("evalFor nshape 返回 nil")
	}
	_ = eval.TotalScore
}

// TestBuildCtxD1Propagation 验证 D1 评分透传：buildCtx 收到非零 D1Score 时
// 应写入 ctx.LLMD1Score/LLMBlocked，calcD1 据此产出 d1>0（断链修复的核心断言）。
// English: TestBuildCtxD1Propagation verifies D1 score propagation: when buildCtx receives a non-zero D1Score it must write ctx.LLMD1Score/LLMBlocked, from which calcD1 yields d1>0 (the core assertion of the broken-chain fix).
func TestBuildCtxD1Propagation(t *testing.T) {
	md := mkBullMarketData()
	// 1) 无 D1 → LLMD1Score 保持 0（缺省路径）
	// English: 1) No D1 → LLMD1Score stays 0 (default path).
	ctx := buildCtx(md, "", nil, "", 0)
	if ctx.LLMD1Score != 0 || ctx.LLMBlocked {
		t.Fatalf("无D1时 LLMD1Score/Blocked 应为零, got %.2f/%v", ctx.LLMD1Score, ctx.LLMBlocked)
	}
	// 2) 有正向 D1 → LLMD1Score 透传（0~40 制）
	// English: 2) Positive D1 → LLMD1Score is passed through (0~40 scale).
	ctx = buildCtx(md, "", &D1Score{Code: "600000", Score: 20, Blocked: false}, "利好事件", 0)
	if ctx.LLMD1Score != 20 || ctx.LLMBlocked {
		t.Fatalf("D1=20 应透传, got %.2f/%v", ctx.LLMD1Score, ctx.LLMBlocked)
	}
	if ctx.EventDesc != "利好事件" {
		t.Fatalf("EventDesc 未透传: %q", ctx.EventDesc)
	}
	// 3) 负面阻断 D1 → LLMBlocked 透传
	// English: 3) Negative blocking D1 → LLMBlocked is passed through.
	ctx = buildCtx(md, "", &D1Score{Code: "600000", Score: 0, Blocked: true}, "", 0)
	if !ctx.LLMBlocked {
		t.Fatal("Blocked 应透传")
	}
	// 4) PE 透传（D3 超跌评分用）
	// English: 4) PE is passed through (used by D3 oversold scoring).
	ctx = buildCtx(md, "", nil, "", 15.5)
	if ctx.StockPE != 15.5 {
		t.Fatalf("StockPE 未透传: %.2f", ctx.StockPE)
	}
}

// failStrategy 恒不通过的战法桩（用于隔离测试动量信号的补发逻辑）。
// English: failStrategy is a strategy stub that always fails (used to isolate the momentum signal re-emission logic under test).
type failStrategy struct{}

// Name 返回战法名称（恒为"失败战法"）。
// English: Name returns the strategy name (always "失败战法"/failing strategy).
func (failStrategy) Name() string { return "失败战法" }

// Type 返回战法类型（恒为 N 形，便于走通用评分路径）。
// English: Type returns the strategy type (always N-shape, to take the generic scoring path).
func (failStrategy) Type() strategy.SignalType { return strategy.SignalNShape }

// Evaluate 恒返回未通过（Pass=false）且总分为 0 的评估，用于隔离测试动量信号的补发逻辑。
// English: Evaluate always returns a failing evaluation (Pass=false) with a total score of 0, used to isolate the momentum signal re-emission logic under test.
func (failStrategy) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, TotalScore: 0}, nil
}

// GenerateSignal 恒不产出信号，确保测试只关注动量补发信号。
// English: GenerateSignal always produces no signal, ensuring tests only focus on the re-emitted momentum signal.
func (failStrategy) GenerateSignal(string, *strategy.Evaluation) (*strategy.Signal, error) {
	return nil, nil
}

// TestScorePoolMomentumSignal 验证 Q2：四战法均不通过但动量分达阈值时，ScorePool 补发动量信号。
// §动量入模拟盘：≥ 买入阈值(默认75) → buy（带 StrategyType=momentum 归动量池）；
// 观察阈值 ≤ 分 < 买入阈值 → watch。
// English: verifies Q2 — buy at/above the buy threshold (StrategyType=momentum, routed to the paper
// momentum pool), watch between thresholds.
func TestScorePoolMomentumSignal(t *testing.T) {
	mc := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig()
	mc.Momentum.SignalThreshold = 60
	mc.Momentum.BuySignalThreshold = 75
	a := New(mc)
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: failStrategy{}}})

	md := mkBullMarketData()
	// 强多头动量分应>=70，超过阈值 60
	// English: The strong-bull momentum score should be >=70, above the threshold of 60.
	if MomentumScore(md, mc.Momentum) < 60 {
		t.Fatalf("测试数据动量分不足60, 无法触发信号")
	}
	scores, sigs := a.ScorePool([]string{"600000"}, map[string]*strategy_engine.StockMarketData{"600000": md}, nil, "")

	sc, ok := scores["600000"]
	if !ok {
		t.Fatalf("缺少打分结果")
	}
	if !sc.MomentumValid {
		t.Fatalf("强多头数据应判为动量数据完整")
	}
	// 动量分 ≥75 → buy 级信号，携带 StrategyType=momentum（模拟盘归动量池）
	// English: score >=75 → buy signal carrying StrategyType=momentum.
	if len(sigs) != 1 || sigs[0].Strategy != "动量" || sigs[0].Action != "buy" {
		t.Fatalf("动量%d≥买入阈值75应发buy信号, got %+v", int(sc.MomentumScore), sigs)
	}
	if sigs[0].StrategyType != "momentum" {
		t.Fatalf("动量buy信号应携带 StrategyType=momentum, got %q", sigs[0].StrategyType)
	}

	// 买入阈值抬到分数之上 → 回落为 watch 档
	// English: raising the buy threshold above the score downgrades to the watch tier.
	mc2 := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig()
	mc2.Momentum.SignalThreshold = 60
	mc2.Momentum.BuySignalThreshold = 99
	a2 := New(mc2)
	a2.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: failStrategy{}}})
	_, sigsWatch := a2.ScorePool([]string{"600000"}, map[string]*strategy_engine.StockMarketData{"600000": md}, nil, "")
	if len(sigsWatch) != 1 || sigsWatch[0].Action != "watch" {
		t.Fatalf("低于买入阈值应回落watch, got %+v", sigsWatch)
	}
}

// TestScorePoolMomentumNoTradePreOpen 验证竞价/盘前（今日成交量为 0）即使历史数据强势也不发动量 watch。
// 场景：强多头 MACD+走势（动量分仍会≥60）但 Quote.Volume=0，MomentumValid 应为 false，信号被抑制。
// English: TestScorePoolMomentumNoTradePreOpen verifies that during auction/pre-open (today's volume is 0) no momentum watch is emitted even with strong historical data. Scenario: strong-bull MACD+trend (momentum score still >=60) but Quote.Volume=0, so MomentumValid must be false and the signal is suppressed.
func TestScorePoolMomentumNoTradePreOpen(t *testing.T) {
	mc := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig()
	mc.Momentum.SignalThreshold = 60
	a := New(mc)
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: failStrategy{}}})

	md := mkBullMarketData()
	// 模拟竞价阶段：未成交，今日成交量=0（价格/均线/MACD 仍沿用存量数据）
	// English: Simulate the auction phase: no fills, today's volume=0 (price/MA/MACD still reuse stored data).
	md.Quote.Volume = 0
	if MomentumScore(md, mc.Momentum) < 60 {
		t.Fatalf("存量数据强势时动量分仍应>=60(验证场景前提)")
	}
	scores, sigs := a.ScorePool([]string{"600000"}, map[string]*strategy_engine.StockMarketData{"600000": md}, nil, "")

	sc, ok := scores["600000"]
	if !ok {
		t.Fatalf("缺少打分结果")
	}
	if sc.MomentumValid {
		t.Fatal("成交量为0应判为动量数据不完整")
	}
	for _, s := range sigs {
		if s.Strategy == "动量" {
			t.Fatalf("成交前不应发动量watch信号: %+v", s)
		}
	}
}

// TestScorePoolMomentumBelowThreshold 验证低于阈值的动量不产生信号。
// English: TestScorePoolMomentumBelowThreshold verifies that momentum below the threshold produces no signal.
func TestScorePoolMomentumBelowThreshold(t *testing.T) {
	m := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig()
	m.Momentum.SignalThreshold = 60
	a := New(m)
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: failStrategy{}}})

	// 非强势行情：平走 + 缩量 → 动量分必然 < 60
	// English: A non-strong market: flat movement + shrinking volume → momentum score is necessarily < 60.
	muted := &strategy_engine.StockMarketData{
		Code:  "600000",
		Name:  "测试",
		Price: 10,
		Quote: &data.StockInfo{Price: 10, Volume: 100},
	}
	_, sigs := a.ScorePool([]string{"600000"}, map[string]*strategy_engine.StockMarketData{"600000": muted}, nil, "")
	for _, s := range sigs {
		if s.Strategy == "动量" {
			t.Fatalf("低动量不应发信号: %+v", s)
		}
	}
}
