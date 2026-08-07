package combat_agent

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mkWaveMD 构造带昨日波形的日内快照：昨日 high=11 / low=9 / close=10，今日现价与量由参数控制。
func mkWaveMD(price float64, volShares int64) *strategy_engine.StockMarketData {
	base := time.Now()
	prev := data.KLine{
		Date: base.AddDate(0, 0, -1), High: 11, Low: 9, Close: 10,
		Open: 9.8, Volume: 10000,
	}
	today := data.KLine{Date: base, High: price + 0.2, Low: price - 0.2, Close: price, Volume: 10000}
	return &strategy_engine.StockMarketData{
		Code:   "600899",
		Name:   "突破",
		Price:  price,
		Quote:  &data.StockInfo{Price: price, Volume: float64(volShares)},
		KLines: []data.KLine{prev, today},
	}
}

// TestWaveTrackerOneThenTwoBreakout 验证 N 形状态机核心链路：
// 一突（价>前高×1.005 且量比≥1.8）→ Eval 返回 left；
// 回调（跌破峰价×0.997）→ 再重破峰价 → Eval 返回 right（二突）。
func TestWaveTrackerOneThenTwoBreakout(t *testing.T) {
	tr := NewWaveTracker()
	// 量比=2000手 / max(prevLow=9,1)=9 ≈222 ≥1.8，恒满足一突量比条件
	const bigVol = int64(2_000_000) // 2000手

	// ① 一突：现价 11.2 > 11×1.005=11.055
	left, right := tr.Eval("600899", mkWaveMD(11.2, bigVol))
	if !left || right {
		t.Fatalf("第一步应触发一突(left=true,right=false), got left=%v right=%v", left, right)
	}

	// ② 回调：现价 11.0 < 峰价11.2×0.997=11.166，不触发、仅记录 dipped
	left, right = tr.Eval("600899", mkWaveMD(11.0, bigVol))
	if left || right {
		t.Fatalf("回调阶段不应触发任何信号, got left=%v right=%v", left, right)
	}

	// ③ 二突：现价 11.5 重破峰价11.2 且已回调 → right=true
	// 注：11.5 同样超过前高×1.005（即又是一次一突破位），此时 left 亦为 true，
	// 但上层按 right>left 优先级取 right，故只需保证 right=true 且优先被标记。
	left, right = tr.Eval("600899", mkWaveMD(11.5, bigVol))
	if !right {
		t.Fatalf("重破峰价应触发二突(right=true), got left=%v right=%v", left, right)
	}
}

// TestWaveTrackerNoOneNoTwo 验证：从未一突破位（现价未超前高×1.005）不触发任何信号。
func TestWaveTrackerNoOneNoTwo(t *testing.T) {
	tr := NewWaveTracker()
	// 10.9 < 11.055，不构成一突
	left, right := tr.Eval("600899", mkWaveMD(10.9, 2_000_000))
	if left || right {
		t.Fatalf("未破位不应触发信号, got left=%v right=%v", left, right)
	}
}

// TestGenerateSignalLeftRight 验证 N 形信号级别映射：
// left_signal → buy + P2（一突）、right_signal → buy + P1（二突）。
func TestGenerateSignalLeftRight(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	ns := n_shape.New(cfg, nil)

	one, _ := ns.GenerateSignal("600899", &strategy.Evaluation{
		Level: "left_signal", Details: map[string]float64{"left_signal": 1}, Confidence: 0.5,
	})
	if one == nil || one.Action != strategy.ActionBuy || one.Priority != strategy.P2 {
		t.Fatalf("一突应买入且 P2, got %+v", one)
	}

	two, _ := ns.GenerateSignal("600899", &strategy.Evaluation{
		Level: "right_signal", Details: map[string]float64{"right_signal": 1}, Confidence: 0.5,
	})
	if two == nil || two.Action != strategy.ActionBuy || two.Priority != strategy.P1 {
		t.Fatalf("二突应买入且 P1, got %+v", two)
	}
}

// TestNShapeTag 验证 nShapeTag 对级别→标记的映射，其余级别返回空。
func TestNShapeTag(t *testing.T) {
	if nShapeTag(&strategy.Evaluation{Level: "left_signal"}) != "一突" {
		t.Fatal("left_signal 应映射为一突")
	}
	if nShapeTag(&strategy.Evaluation{Level: "right_signal"}) != "二突" {
		t.Fatal("right_signal 应映射为二突")
	}
	if nShapeTag(&strategy.Evaluation{Level: "full_chain"}) != "" {
		t.Fatal("full_chain 不应有突标签")
	}
	if nShapeTag(nil) != "" {
		t.Fatal("nil 不应有标签")
	}
}

// TestNShapeReason 验证 N 形信号附加 D1 理由（故事）：有 base+D1 → 拼接；无 D1 → 原样。
func TestNShapeReason(t *testing.T) {
	if got := nShapeReason("left_signal", &D1Score{Reason: "中标海外储能大单"}); got != "left_signal | D1: 中标海外储能大单" {
		t.Fatalf("应拼接 D1 理由, got %q", got)
	}
	if got := nShapeReason("full_chain", &D1Score{Reason: ""}); got != "full_chain" {
		t.Fatalf("空 D1 理由应原样输出, got %q", got)
	}
	if got := nShapeReason("", &D1Score{Reason: "利好"}); got != "D1: 利好" {
		t.Fatalf("无 base 时只输出 D1, got %q", got)
	}
	if got := nShapeReason("full_chain", nil); got != "full_chain" {
		t.Fatalf("nil D1 应原样输出, got %q", got)
	}
}

// TestScorePoolNLeftBreakoutEmitUnmarked 验证端到端："一突打标"链路。
// 一突破位（价>前高×1.005 且量比≥1.8）且 D1>0 → 即使总分未达 full_chain，也提升为
// Pass 并产出带 tag=一突 的 buy 信号（P2）。避免旧逻辑在该股非 full_chain 时被过滤掉。
func TestScorePoolN1BreakoutEmit(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{
		Type:     strategy.SignalNShape,
		Strategy: n_shape.New(cfg, nil),
	}})

	pool := map[string]*strategy_engine.StockMarketData{"600899": mkWaveMD(11.2, 2_000_000)}
	d1Scores := map[string]D1Score{"600899": {Code: "600899", Score: 0.5, Blocked: false}}

	_, sigs := a.ScorePool([]string{"600899"}, pool, d1Scores, "")

	found := false
	for _, s := range sigs {
		if s.Code == "600899" && s.Strategy == "n_shape" && s.Action == "buy" && s.Tag == "一突" {
			found = true
		}
	}
	if !found {
		t.Fatalf("一突且D1>0应产出 buy+一突 信号, got %+v", sigs)
	}
}

// TestScorePoolN1NoD1Suppressed 验证硬闸：一突破位但 D1=0 → 不发 buy/一突 信号。
func TestScorePoolN1NoD1Suppressed(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: n_shape.New(cfg, nil)}})

	pool := map[string]*strategy_engine.StockMarketData{"600899": mkWaveMD(11.2, 2_000_000)}
	// 无 D1 评分（Score=0）→ d1=0，不满足一突硬闸
	d1Scores := map[string]D1Score{}

	_, sigs := a.ScorePool([]string{"600899"}, pool, d1Scores, "")
	for _, s := range sigs {
		if s.Code == "600899" && s.Strategy == "n_shape" {
			t.Fatalf("D1=0 不应发 N 形买入信号, got %+v", s)
		}
	}
}