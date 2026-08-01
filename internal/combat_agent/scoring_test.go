package combat_agent

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy_engine"
)

// mkKLines 根据收盘价序列构造测试用日K线。
// 开/高/低价在收盘价基础上做固定偏移，成交量随索引递增模拟温和放量。
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
func mkBullMarketData() *strategy_engine.StockMarketData {
	closes := make([]float64, 40)
	for i := range closes {
		closes[i] = 10 + float64(i)*0.3 // 持续上行
	}
	kl := mkKLines(closes)
	// 分钟K线也持续上行 → 分钟MACD 多头
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
	flat := &strategy_engine.StockMarketData{Code: "000001", Price: 10}
	if MomentumScore(flat, config.MomentumConfig{}) != 0 {
		t.Fatalf("空数据动量分应为0")
	}
	// 权重全 0 → 回退默认 40/30/30，正常出分
	if MomentumScore(md, config.MomentumConfig{}) != MomentumScore(md, config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30}) {
		t.Fatalf("权重全0应回退默认")
	}
	// 分数应为整数
	if math.Round(s) != s {
		t.Fatalf("动量分应为整数, got %v", s)
	}
}

// TestNShapeScoreAlwaysReturns 验证 N 形输入构造的健壮性：
// 强多头行情下 WaveA/IntradayB/Ctx 均应非空且含关键字段，供打分不 panic。
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
	ctx := buildCtx(md, "")
	if ctx == nil {
		t.Fatalf("Ctx 不应为 nil")
	}
}

// TestEvalForNShape 验证 8a/8b 的 N 形打分路径：nil matcher 不 panic，且恒返回总分。
// 覆盖 adapter.go evalFor 中 N 形策略分支的完整数据适配链路。
func TestEvalForNShape(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	ns := n_shape.New(cfg, nil)
	eval, err := evalFor(StrategyRunner{Type: "n_shape", Strategy: ns}, "600000", mkBullMarketData(), nil, "")
	if err != nil {
		t.Fatalf("evalFor nshape err: %v", err)
	}
	if eval == nil {
		t.Fatalf("evalFor nshape 返回 nil")
	}
	_ = eval.TotalScore
}
