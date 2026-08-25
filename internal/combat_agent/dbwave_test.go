package combat_agent

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mkDBWaveMD 构造双响炮第二波确认状态机的日内快照：昨日 high=11，今日现价与量由参数控制。
// 一突破位条件为 现价 > 前高11×1.005 且累计量>0（当日有真实成交）。
func mkDBWaveMD(price float64, volShares int64) *strategy_engine.StockMarketData {
	base := time.Now()
	prev := data.KLine{Date: base.AddDate(0, 0, -1), High: 11, Low: 9, Close: 10, Open: 9.8, Volume: 10000}
	today := data.KLine{Date: base, High: price + 0.2, Low: price - 0.2, Close: price, Volume: 10000}
	return &strategy_engine.StockMarketData{
		Code:   "600900",
		Name:   "双凸",
		Price:  price,
		Quote:  &data.StockInfo{Price: price, Volume: float64(volShares)},
		KLines: []data.KLine{prev, today},
	}
}

// TestDoubleBumpWatcherFirstAdjustSecondConfirm 验证双响炮第二波确认状态机核心链路：
// 一突破位 → 回调(Adjust) → 重破峰价 → PhaseSecond 确认；未到达第二波前不确认。
func TestDoubleBumpWatcherFirstAdjustSecondConfirm(t *testing.T) {
	w := NewDoubleBumpWatcher()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig().DoubleBump

	// ① 一突：现价 11.2 > 11×1.005=11.055 且有量 → 到达 PhaseFirst，尚未第二波 → 不确认
	if w.Confirm("600900", mkDBWaveMD(11.2, 2_000_000), cfg) {
		t.Fatal("一突阶段不应确认双凸")
	}

	// ② 回调：现价 11.0 < 峰价11.2×0.997 → 记为 Adjust，仍不确认
	if w.Confirm("600900", mkDBWaveMD(11.0, 2_000_000), cfg) {
		t.Fatal("缩量回调阶段不应确认双凸")
	}

	// ③ 二突：现价 11.5 重破峰价 11.2 → PhaseSecond 确认
	if !w.Confirm("600900", mkDBWaveMD(11.5, 2_000_000), cfg) {
		t.Fatal("重破峰价应确认双凸(PhaseSecond)")
	}
}

// TestDoubleBumpWatcherNeverFirstNoConfirm 验证：从未一突破位（现价未超前高×1.005）不确认。
func TestDoubleBumpWatcherNeverFirstNoConfirm(t *testing.T) {
	w := NewDoubleBumpWatcher()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig().DoubleBump

	// 10.9 < 11.055，不构成一突 → 永不确认
	if w.Confirm("600900", mkDBWaveMD(10.9, 2_000_000), cfg) {
		t.Fatal("未一突破位不应确认双凸")
	}
}

// TestDoubleBumpWatcherNoVolumeBeforeOpen 验证：竞价/盘前无真实成交(Volume=0)时，一突条件
// (累计量>0) 不满足，状态机不推进 → 不确认。避免把竞价"假放量"当成双凸确认。
func TestDoubleBumpWatcherNoVolumeBeforeOpen(t *testing.T) {
	w := NewDoubleBumpWatcher()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig().DoubleBump

	// Volume=0：即使价格超前高，也没有真实成交 → 不确认
	if w.Confirm("600900", mkDBWaveMD(11.5, 0), cfg) {
		t.Fatal("竞价无成交时不应确认双凸")
	}
}

// fakeDoubleBumpStrategy 固定返回 full_chain Pass（volScore=40 隐含达标），用于隔离测试
// 双响炮第二波确认门与动量门槛（绕过真实 double_bump 的日K volScore 判断）。
type fakeDoubleBumpStrategy struct{}

func (fakeDoubleBumpStrategy) Name() string              { return "fake_db" }
func (fakeDoubleBumpStrategy) Type() strategy.SignalType { return strategy.SignalDoubleBump }
func (fakeDoubleBumpStrategy) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{
		Level: "full_chain", Pass: true, TotalScore: 80,
		Details:    map[string]float64{"vol_score": 40, "adjust_score": 20, "ma_score": 30},
		Confidence: 0.7,
	}, nil
}
func (fakeDoubleBumpStrategy) GenerateSignal(code string, _ *strategy.Evaluation) (*strategy.Signal, error) {
	// 故意不填 Price（=0），验证 evalAll 用 md.Price 兜底触发价（任务2）
	return &strategy.Signal{Code: code, Name: "双凸", Action: strategy.ActionBuy, Confidence: 0.7, Reason: "full_chain"}, nil
}

// dbScorePool 用伪双响炮战法跑 ScorePool，返回 code 的信号列表。
func dbScorePool(t *testing.T, md *strategy_engine.StockMarketData) []Signal {
	t.Helper()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDoubleBump, Strategy: fakeDoubleBumpStrategy{}}})
	pool := map[string]*strategy_engine.StockMarketData{"600900": md}
	_, sigs := a.ScorePool([]string{"600900"}, pool, map[string]D1Score{}, "")
	return sigs
}

// TestDoubleBumpWaitForSecondWave 验证：双响炮虽日K满 volScore(full_chain Pass)，
// 但日内状态机未推进到第二波时，不发买入信号（被 "双:待二波" 拦截）。
func TestDoubleBumpWaitForSecondWave(t *testing.T) {
	sigs := dbScorePool(t, mkDBWaveMD(11.2, 2_000_000)) // 仅一突
	for _, s := range sigs {
		if s.Code == "600900" && s.Strategy == "双响炮" && s.Action == "buy" {
			t.Fatalf("未到第二波不应发双响炮买入信号, got %+v", s)
		}
	}
}

// TestDoubleBumpConfirmSecondWave 验证：推进一突→回调→二突后，双响炮 full_chain 买入信号放行，
// 且触发价由 md.Price 兜底（sig.Price 为 0 时不失效为 0）。
func TestDoubleBumpConfirmSecondWave(t *testing.T) {
	// 同一 Agent 需分三次调用来推进状态机；此处用单个 watcher 串行推进后断言一例
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	a := New(cfg.GetStrategyConfig())
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDoubleBump, Strategy: fakeDoubleBumpStrategy{}}})
	pool := map[string]*strategy_engine.StockMarketData{"600900": mkDBWaveMD(11.2, 2_000_000)}
	_, _ = a.ScorePool([]string{"600900"}, pool, map[string]D1Score{}, "")
	pool["600900"] = mkDBWaveMD(11.0, 2_000_000)
	_, _ = a.ScorePool([]string{"600900"}, pool, map[string]D1Score{}, "")
	pool["600900"] = mkDBWaveMD(11.5, 2_000_000)
	_, sigs := a.ScorePool([]string{"600900"}, pool, map[string]D1Score{}, "")

	found := false
	for _, s := range sigs {
		if s.Code == "600900" && s.Strategy == "双响炮" {
			if s.Action != "buy" {
				t.Fatalf("到达第二波应发买入信号, got %+v", s)
			}
			if s.Price <= 0 {
				t.Fatalf("触发价应由 md.Price 兜底, got %+v", s)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("到达第二波应产出双响炮买入信号, got %+v", sigs)
	}
}
