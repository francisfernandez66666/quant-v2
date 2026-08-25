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

// fakeDragonStrategy 固定返回 full_chain Pass 的伪龙头战法，用于隔离测试动量"提升才提醒"门槛
// （龙头战法不套用双响炮第二波确认门，可干净地验证动量门槛）。
// English: fakeDragonStrategy is a fake dragon strategy that always returns full_chain Pass, used to isolate the momentum "alert only on improvement" gate (the dragon strategy does not apply the double-bump second-wave confirmation gate, so the momentum gate can be tested cleanly).
type fakeDragonStrategy struct{}

func (fakeDragonStrategy) Name() string              { return "fake_dragon" }
func (fakeDragonStrategy) Type() strategy.SignalType { return strategy.SignalDragon }
func (fakeDragonStrategy) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Level: "full_chain", Pass: true, TotalScore: 80, Confidence: 0.7}, nil
}
func (fakeDragonStrategy) GenerateSignal(code string, _ *strategy.Evaluation) (*strategy.Signal, error) {
	return &strategy.Signal{Code: code, Name: "龙头", Action: strategy.ActionBuy, Price: 12.0, Confidence: 0.7, Reason: "full_chain"}, nil
}

// mkMomentumMD 构造带有效动量数据的行情：Volume>0、≥5 根日K、非零 MinuteMACD。
// changePct 控制动量分里的价格维度，volume 控制量能维度。
// English: mkMomentumMD builds a quote with valid momentum data: Volume>0, >=5 daily K-lines, non-zero MinuteMACD. changePct controls the price dimension of the momentum score and volume controls the volume dimension.
func mkMomentumMD(chgPct float64, volume float64) *strategy_engine.StockMarketData {
	base := time.Now()
	kl := make([]data.KLine, 20)
	close := 10.0
	for i := 0; i < len(kl); i++ {
		close = 10 + float64(i)*0.1
		kl[i] = data.KLine{Date: base.AddDate(0, 0, i-19), Open: close, High: close + 0.2, Low: close - 0.2, Close: close, Volume: 10000}
	}
	return &strategy_engine.StockMarketData{
		Code:       "600901",
		Name:       "动量",
		Price:      kl[len(kl)-1].Close,
		ChangePct:  chgPct,
		Quote:      &data.StockInfo{Price: kl[len(kl)-1].Close, Volume: volume, ChangePct: chgPct},
		KLines:     kl,
		MinuteMACD: data.MACD{DIF: 1, DEA: 0.5, Bar: 0.5}, // 金叉+水上+红柱 → 动量 MACD 子项有效
	}
}

// runMomentumPool 用一个复用 Agent 按给定行情序列逐轮跑 ScorePool（龙头战法），返回每轮该股是否有信号。
// 关键：Agent 跨轮复用，动量历史 momentumPrev 与双响炮状态机才会跨 5s 周期保留。
// English: runMomentumPool runs ScorePool round by round (dragon strategy) over the given quote sequence with a reused Agent, returning whether the stock had a signal each round. Key point: the Agent is reused across rounds so the momentum history momentumPrev and the double-bump state machine persist across 5s cycles.
func runMomentumPool(t *testing.T, cfg *config.StrategyConfig, mds ...*strategy_engine.StockMarketData) []bool {
	t.Helper()
	a := New(cfg)
	a.SetRunners([]StrategyRunner{{Type: strategy.SignalDragon, Strategy: fakeDragonStrategy{}}})
	var outs []bool
	for _, md := range mds {
		pool := map[string]*strategy_engine.StockMarketData{"600901": md}
		_, sigs := a.ScorePool([]string{"600901"}, pool, map[string]D1Score{}, "")
		hit := false
		for _, s := range sigs {
			if s.Code == "600901" && s.Strategy == "龙头" && s.Action == "buy" {
				hit = true
			}
		}
		outs = append(outs, hit)
	}
	return outs
}

// activeVCfg 返回动量门槛开启且数据有效所需的动量配置：Volume>0 且 MACD 有效。
// English: activeMomentumCfg returns the momentum config needed for the gate enabled and valid data: Volume>0 and valid MACD.
func activeMomentumCfg(t *testing.T) *config.StrategyConfig {
	t.Helper()
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json")).GetStrategyConfig()
	cfg.Momentum.MomentumGateEnabled = true
	cfg.Momentum.MomentumDeltaTol = 5
	return cfg
}

// TestMomentumGateImprovedPasses 验证：动量分提升时放行信号。
// English: TestMomentumGateImprovedPasses verifies: signals pass when the momentum score improves.
func TestMomentumGateImprovedPasses(t *testing.T) {
	// 第一轮 momentum=60（起点，无上一轮 → 放行），第二轮 changePct 更高 → 动量提升 → 放行
	// English: Round one momentum=60 (starting point, no previous round → passes); round two has a higher changePct → momentum improves → passes.
	low := mkMomentumMD(1.0, 20000)  // 较低动量
	high := mkMomentumMD(6.0, 20000) // 涨幅更大 → 动量提升
	outs := runMomentumPool(t, activeMomentumCfg(t), low, high)
	if !outs[0] || !outs[1] {
		t.Fatalf("动量提升应两轮都放行, got %v", outs)
	}
}

// TestMomentumGateFallWithinTolPasses 验证：动量分虽略回落但 ≤ 容忍差(5) 仍放行。
// English: TestMomentumGateFallWithinTolPasses verifies: a slight momentum drop within the tolerance (5) still passes.
func TestMomentumGateFallWithinTolPasses(t *testing.T) {
	// 第一轮涨幅 6% 高动量，第二轮涨幅 3% 略回落（差 3 ≤ 5）→ 仍放行
	// English: Round one +6% high momentum, round two +3% slight drop (difference 3 <= 5) → still passes.
	high := mkMomentumMD(6.0, 20000)
	medium := mkMomentumMD(3.0, 20000)
	outs := runMomentumPool(t, activeMomentumCfg(t), high, medium)
	if !outs[0] || !outs[1] {
		t.Fatalf("回落≤容忍差应仍放行, got %v", outs)
	}
}

// TestMomentumGateFallBeyondTolSilent 验证：动量分明显回落（>容忍差）时静默拦截信号。
// English: TestMomentumGateFallBeyondTolSilent verifies: signals are silently blocked when momentum drops markedly (>tolerance).
func TestMomentumGateFallBeyondTolSilent(t *testing.T) {
	// 第一轮涨幅 6% 高动量，第二轮涨幅 0%（平盘）→ 动量明显回落 → 拦截
	// English: Round one +6% high momentum, round two 0% (flat) → momentum drops markedly → blocked.
	high := mkMomentumMD(6.0, 20000)
	flat := mkMomentumMD(0.0, 20000)
	outs := runMomentumPool(t, activeMomentumCfg(t), high, flat)
	if !outs[0] {
		t.Fatalf("首轮应放行(起点), got %v", outs)
	}
	if outs[1] {
		t.Fatalf("动量明显回落应被拦截, got %v", outs)
	}
}

// TestMomentumGateInvalidDataPasses 验证：动量数据无效（竞价 Volume=0）时跳过门槛放行。
// English: TestMomentumGateInvalidDataPasses verifies: when momentum data is invalid (auction Volume=0) the gate is skipped and the signal passes.
func TestMomentumGateInvalidDataPasses(t *testing.T) {
	// Volume=0 → momentumDataValid=false → 门槛放行（等实盘有量后再正常判定）
	// English: Volume=0 → momentumDataValid=false → the gate passes (judged normally once live volume appears).
	high := mkMomentumMD(6.0, 20000)
	beforeOpen := mkMomentumMD(3.0, 0) // 竞价无成交
	outs := runMomentumPool(t, activeMomentumCfg(t), high, beforeOpen)
	if !outs[0] || !outs[1] {
		t.Fatalf("动量数据无效应放行, got %v", outs)
	}
}

// TestMomentumGateDisabledAlwaysPasses 验证：动量门槛开关关闭时，即使动量回落也始终放行。
// English: TestMomentumGateDisabledAlwaysPasses verifies: when the momentum gate toggle is off, signals always pass even on a momentum drop.
func TestMomentumGateDisabledAlwaysPasses(t *testing.T) {
	cfg := activeMomentumCfg(t)
	cfg.Momentum.MomentumGateEnabled = false
	high := mkMomentumMD(6.0, 20000)
	flat := mkMomentumMD(0.0, 20000)
	outs := runMomentumPool(t, cfg, high, flat)
	if !outs[0] || !outs[1] {
		t.Fatalf("门槛关闭应始终放行, got %v", outs)
	}
}
