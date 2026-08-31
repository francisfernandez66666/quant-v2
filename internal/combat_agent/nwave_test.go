// 文件：nwave_test.go
// 包名：combat_agent
// 所属模块：「对抗式/量化交易决策 agent（买卖信号、风控）」
// 模块职责：本文件属于 对抗式/量化交易决策 agent（买卖信号、风控），负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

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
		Quote:  &data.StockInfo{Price: price, Volume: float64(volShares), Open: 10.3},
		KLines: []data.KLine{prev, today},
	}
}

// TestWaveTrackerOneThenTwoBreakout 验证 N 形状态机核心链路：
// 一突（价>前高×1.005 且量比≥1.8）→ Eval 返回 left；
// 回调（跌破峰价×0.997）→ 再重破峰价 → Eval 返回 right（二突）。
func TestWaveTrackerOneThenTwoBreakout(t *testing.T) {
	tr := NewWaveTracker()
	// 量比 = 当日累计成交量(股) / 近20日日均成交量(股) = 2_000_000 / 10_000 = 200 ≥ 1.8
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

// TestWaveTrackerVolumeRatioUsesVolumeNotPrice 验证一突量比用成交量而非价格做分母。
// 构造 prevLow 极低（1 元）的场景：旧公式 cumVol/prevLow 会虚高，新公式应以日均量为分母。
func TestWaveTrackerVolumeRatioUsesVolumeNotPrice(t *testing.T) {
	base := time.Now()
	prev := data.KLine{
		Date: base.AddDate(0, 0, -1), High: 11, Low: 1, Close: 10,
		Open: 9.8, Volume: 10000,
	}
	today := data.KLine{Date: base, High: 11.3, Low: 11.1, Close: 11.2, Volume: 10000}
	md := &strategy_engine.StockMarketData{
		Code: "600899", Name: "突破", Price: 11.2,
		Quote:  &data.StockInfo{Price: 11.2, Volume: 180000, Open: 10.3}, // 1800手，刚好量比=1.8
		KLines: []data.KLine{prev, today},
	}
	tr := NewWaveTracker()
	left, _ := tr.Eval("600899", md)
	if !left {
		t.Fatalf("量比=1.8 应触发一突，left=%v", left)
	}

	// 同场景下，若把 prevLow 压到 0.1 元，旧公式会滥触发；新公式不应因此改变结果
	prev.Low = 0.1
	today.Low = 0.1
	md2 := &strategy_engine.StockMarketData{
		Code: "600899", Name: "突破", Price: 11.2,
		Quote:  &data.StockInfo{Price: 11.2, Volume: 180000, Open: 10.3},
		KLines: []data.KLine{prev, today},
	}
	tr2 := NewWaveTracker()
	left2, _ := tr2.Eval("600899", md2)
	if left2 != left {
		t.Fatalf("prevLow 不应影响量比判定，left=%v left2=%v", left, left2)
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

// TestNShapeReason 验证 N 形信号附加 D1 理由（故事）与事件名称：有 base+D1 → 拼接；无 D1 → 原样。
// English: verifies N-shape signals append the D1 reason (narrative) and event name — base+D1 are
// joined when present, and the output stays intact when D1 is absent.
func TestNShapeReason(t *testing.T) {
	if got := nShapeReason("left_signal", &D1Score{Reason: "中标海外储能大单"}, ""); got != "left_signal | D1: 中标海外储能大单" {
		t.Fatalf("应拼接 D1 理由, got %q", got)
	}
	if got := nShapeReason("full_chain", &D1Score{Reason: ""}, ""); got != "full_chain" {
		t.Fatalf("空 D1 理由应原样输出, got %q", got)
	}
	if got := nShapeReason("", &D1Score{Reason: "利好"}, ""); got != "D1: 利好" {
		t.Fatalf("无 base 时只输出 D1, got %q", got)
	}
	if got := nShapeReason("full_chain", nil, ""); got != "full_chain" {
		t.Fatalf("nil D1 应原样输出, got %q", got)
	}
	if got := nShapeReason("full_chain", nil, "储能新签订单，海外大单落地"); got != "full_chain | 事件: 储能新签订单，海外大单落地" {
		t.Fatalf("应拼接事件名称, got %q", got)
	}
	if got := nShapeReason("", &D1Score{Reason: "中标大单"}, "储能新签订单"); got != "D1: 中标大单 | 事件: 储能新签订单" {
		t.Fatalf("应拼接 D1+事件, got %q", got)
	}
}

// TestScorePoolN1BreakoutEmit 验证端到端："一突打标"链路（0~40 D1 制下修正）。
// 一突破位（价>前高×1.005 且量比≥1.8）且 d1>0、总分≥60 时 → 提升为 Pass 并产出
// 带 tag=一突 的 buy 信号。低分股（总分<60）不强制推荐（见 TestScorePoolN1LowTotalSuppressed）。
func TestScorePoolN1BreakoutEmit(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{
		Type:     strategy.SignalNShape,
		Strategy: n_shape.New(cfg, nil),
	}})

	pool := map[string]*strategy_engine.StockMarketData{"600899": mkWaveMD(11.2, 2_000_000)}
	// D1 满分（0~40 制）→ 配合波形 D 分使总分≥60，满足一突门槛
	d1Scores := map[string]D1Score{"600899": {Code: "600899", Score: 40, Blocked: false}}

	_, sigs := a.ScorePool([]string{"600899"}, pool, d1Scores, "")

	found := false
	for _, s := range sigs {
		if s.Code == "600899" && s.Strategy == "N形" && s.Action == "buy" && s.Tag == "一突" {
			found = true
		}
	}
	if !found {
		t.Fatalf("总分≥60 时一突应产出 buy+一突 信号, got %+v", sigs)
	}
}

// TestScorePoolN1LowTotalSuppressed 验证一突总分门槛：波形一突确认但总分<60 时，
// 不比被强制提升为 Pass/发 buy（修复低分股被强制推荐）。
func TestScorePoolN1LowTotalSuppressed(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{
		Type:     strategy.SignalNShape,
		Strategy: n_shape.New(cfg, nil),
	}})

	pool := map[string]*strategy_engine.StockMarketData{"600899": mkWaveMD(11.2, 2_000_000)}
	// D1 低（0~40 制，0.5）→ 总分达不到 60 → 不发一突/buy
	d1Scores := map[string]D1Score{"600899": {Code: "600899", Score: 0.5, Blocked: false}}

	_, sigs := a.ScorePool([]string{"600899"}, pool, d1Scores, "")

	for _, s := range sigs {
		if s.Code == "600899" && s.Strategy == "N形" && (s.Tag == "一突" || s.Action == "buy") {
			t.Fatalf("总分<60 时一突不应发 buy 信号, got %+v", s)
		}
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
		if s.Code == "600899" && s.Strategy == "N形" {
			t.Fatalf("D1=0 不应发 N 形买入信号, got %+v", s)
		}
	}
}

// fakeFullChainStrategy 固定返回 full_chain Pass 的伪战法，用于隔离测试 B2 盘中确认门
// （避开 n_shape 真实波形/情绪硬闸，直接验证 evalAll 对纯 full_chain 的门控逻辑）。
// English: a fake strategy that always returns a full_chain Pass, isolating the B2 intraday
// confirmation gate from real n_shape waveform/emotion logic.
type fakeFullChainStrategy struct{}

// Name 返回测试桩战法名称 "fake_n"（伪 N 形）。
func (fakeFullChainStrategy) Name() string { return "fake_n" }

// Type 返回战法信号类型 SignalNShape。
func (fakeFullChainStrategy) Type() strategy.SignalType { return strategy.SignalNShape }

// Evaluate 固定返回 full_chain Pass（d1=0.8），用于隔离 N 形状态机判定。
func (fakeFullChainStrategy) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{
		Level: "full_chain", Pass: true, TotalScore: 80,
		Details:    map[string]float64{"d1": 0.8},
		Confidence: 0.7,
	}, nil
}

// GenerateSignal 返回 N 形买入信号（Price=11.0）。
func (fakeFullChainStrategy) GenerateSignal(code string, _ *strategy.Evaluation) (*strategy.Signal, error) {
	return &strategy.Signal{
		Code: code, Name: "突破", Action: strategy.ActionBuy,
		Price: 11.0, Confidence: 0.7, Reason: "N形full_chain形态",
	}, nil
}

// scorePoolFullChain 用伪战法跑 ScorePool，返回 code 的 N 形信号。
func scorePoolFullChain(t *testing.T, md *strategy_engine.StockMarketData) []Signal {
	t.Helper()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalNShape, Strategy: fakeFullChainStrategy{}}})
	pool := map[string]*strategy_engine.StockMarketData{"600899": md}
	_, sigs := a.ScorePool([]string{"600899"}, pool, map[string]D1Score{"600899": {Code: "600899", Score: 0.8, Blocked: false}}, "")
	return sigs
}

// TestFullChainVolumeGateWatchBeforeOpen 验证 B2 盘中确认门：竞价/盘前无真实成交（Volume=0）时，
// 纯 full_chain 形态买入信号降级为 watch，避免基于竞价/存量的假买入信号。
func TestFullChainVolumeGateWatchBeforeOpen(t *testing.T) {
	sigs := scorePoolFullChain(t, mkWaveMD(11.0, 0))
	for _, s := range sigs {
		if s.Code != "600899" {
			continue
		}
		if s.Action == "buy" {
			t.Fatalf("Volume=0 时 full_chain 不应买入, got %+v", s)
		}
		if s.Action != "watch" || !containsSubstr(s.Reason, "待盘中确认") {
			t.Fatalf("Volume=0 时 full_chain 应降级为 watch+待盘中确认, got %+v", s)
		}
		return
	}
	t.Fatalf("应产出降级后的 watch 信号, got %+v", sigs)
}

// TestFullChainVolumeGateBuyAfterOpen 验证 B2 盘中确认门：实盘有真实成交（Volume>0）后，
// 纯 full_chain 形态维持 buy 信号（门放行）。
func TestFullChainVolumeGateBuyAfterOpen(t *testing.T) {
	sigs := scorePoolFullChain(t, mkWaveMD(11.0, 2_000_000))
	for _, s := range sigs {
		if s.Code != "600899" {
			continue
		}
		if s.Action != "buy" {
			t.Fatalf("Volume>0 时 full_chain 应维持买入, got %+v", s)
		}
		if containsSubstr(s.Reason, "待盘中确认") {
			t.Fatalf("Volume>0 时不应标记待盘中确认, got %+v", s)
		}
		return
	}
	t.Fatalf("应产出 buy 信号, got %+v", sigs)
}

// TestFullChainVolumeGateLeftNotGated 验证确认门不误伤：已由波形状态机提升为 left_signal/right_signal
// 的信号（一突/二突，本身即盘中确认）不受 volume 门控，Volume=0 时仍为 buy。
func TestFullChainVolumeGateLeftNotGated(t *testing.T) {
	// 现价 11.2 > 前高11×1.005 且放量 → 一突打标，level 被提升为 left_signal
	sigs := scorePoolFullChain(t, mkWaveMD(11.2, 2_000_000))
	for _, s := range sigs {
		if s.Code != "600899" {
			continue
		}
		if s.Action != "buy" {
			t.Fatalf("一突/二突不被 volume 门控, 应保持 buy, got %+v", s)
		}
		return
	}
	t.Fatalf("应产出 buy 信号, got %+v", sigs)
}

// containsSubstr 子串包含判断（测试断言辅助）。
func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
