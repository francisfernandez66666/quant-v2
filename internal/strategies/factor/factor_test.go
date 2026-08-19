// E6 因子战法测试：从日K价量因子打分、GenerateSignal 出信号、SetRule 启停。
package factor

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mkKLines 构造 n 根日K，close 严格递增（动量因子高分位）。
func mkKLines(n int) []data.KLine {
	kl := make([]data.KLine, n)
	for i := range kl {
		kl[i] = data.KLine{
			Date: time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC),
			Open: 100 + float64(i), High: 101 + float64(i),
			Low: 99 + float64(i), Close: 100 + float64(i),
			Volume: 1000, Amount: 100000,
		}
	}
	return kl
}

// TestFactorEvaluateEnabled 启用规则后能打分：涨势序列 → 动量分位高 → 高分。
func TestFactorEvaluateEnabled(t *testing.T) {
	fs := New()
	fs.SetRule(Rule{
		Factors: []string{"Mom20"}, Weights: map[string]float64{"Mom20": 1.0},
		Directions: map[string]int{"Mom20": 1}, BuyThreshold: 50,
	})
	md := &strategy_engine.StockMarketData{KLines: mkKLines(60)}
	eval, err := fs.Evaluate("600000", md)
	if err != nil {
		t.Fatalf("Evaluate 失败: %v", err)
	}
	if eval == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	// 持续上涨序列，Mom20 当前分位应较高 → 总分 > 50
	if eval.TotalScore < 50 {
		t.Fatalf("涨势序列复合分=%.2f 期望 >50", eval.TotalScore)
	}
	if !eval.Pass {
		t.Fatalf("涨势序列应 Pass")
	}
}

// TestFactorEvaluateDisabled 未启用（空规则）→ 0 分不 Pass。
func TestFactorEvaluateDisabled(t *testing.T) {
	fs := New() // 未 SetRule
	md := &strategy_engine.StockMarketData{KLines: mkKLines(60)}
	eval, _ := fs.Evaluate("600000", md)
	if eval.TotalScore != 0 || eval.Pass {
		t.Fatalf("未启用应 0 分不 Pass, got %.2f pass=%v", eval.TotalScore, eval.Pass)
	}
}

// TestFactorEvaluateShort 看空因子：下跌序列分位低 → 看空贡献高 → 高分。
func TestFactorEvaluateShort(t *testing.T) {
	fs := New()
	fs.SetRule(Rule{
		Factors: []string{"Mom20"}, Weights: map[string]float64{"Mom20": 1.0},
		Directions: map[string]int{"Mom20": -1}, BuyThreshold: 50,
	})
	// 下跌序列：close 递减 → Mom20 分位低 → 看空因子贡献 (1-pct) 高
	kl := mkKLines(60)
	for i := range kl {
		kl[i].Close = 200 - float64(i)
	}
	md := &strategy_engine.StockMarketData{KLines: kl}
	eval, _ := fs.Evaluate("600000", md)
	if eval.TotalScore < 50 {
		t.Fatalf("下跌序列配看空因子 复合分=%.2f 期望 >50", eval.TotalScore)
	}
}

// TestFactorGenerateSignal 高置信 → P1 buy；未 Pass → nil。
func TestFactorGenerateSignal(t *testing.T) {
	fs := New()
	sig, err := fs.GenerateSignal("600000", &strategy.Evaluation{Pass: true, Confidence: 0.85, TotalScore: 85})
	if err != nil || sig == nil {
		t.Fatalf("Pass 高分应出信号, sig=%v err=%v", sig, err)
	}
	if sig.Action != strategy.ActionBuy || sig.Priority != strategy.P1 {
		t.Fatalf("高置信应为 P1 buy, got action=%v prio=%v", sig.Action, sig.Priority)
	}
	sig2, _ := fs.GenerateSignal("600000", &strategy.Evaluation{Pass: false, Confidence: 0.3})
	if sig2 != nil {
		t.Fatalf("未 Pass 不应出信号")
	}
}

// TestPercentile 分位函数：末值高于历史 → 高分位。
func TestPercentile(t *testing.T) {
	// [1,2,3,4, 末值=5] → 4/4 = 1.0
	if p := percentile([]float64{1, 2, 3, 4, 5}); p != 1.0 {
		t.Fatalf("末值最大应 1.0, got %.2f", p)
	}
	// 末值=历史最小值 → 0
	if p := percentile([]float64{5, 4, 3, 2, 1}); p != 0.0 {
		t.Fatalf("末值最小应 0, got %.2f", p)
	}
	// 序列过短 → NaN
	if p := percentile([]float64{1, 2}); p == p {
		t.Fatalf("过短序列应 NaN, got %.2f", p)
	}
}

// TestFactorFinancial 财务因子打分：注入最新财务指标后，ROE/净利同比等财务因子应参与复合分。
func TestFactorFinancial(t *testing.T) {
	fs := New()
	fs.SetRules([]*ActiveRule{{
		ID: "fac_fin", Name: "财务因子", CandID: 9,
		Rule: Rule{
			Factors:      []string{"Mom20", "ROE", "YoyNetProfit"},
			Weights:      map[string]float64{"Mom20": 0.4, "ROE": 0.3, "YoyNetProfit": 0.3},
			Directions:   map[string]int{"Mom20": 1, "ROE": 1, "YoyNetProfit": 1},
			BuyThreshold: 50,
		},
	}})
	md := &strategy_engine.StockMarketData{KLines: mkKLines(60), Fina: &strategy_engine.FinancialData{
		Roe: 15, YoyNetProfit: 30, NetMargin: 10, GrossMargin: 25,
	}}
	eval, err := fs.Evaluate("600000", md)
	if err != nil || eval == nil {
		t.Fatalf("Evaluate 失败: %v", err)
	}
	// ROE=15 → 15/20=0.75；YoyNetProfit=30 → 30/30=1.0；Mom20 线性涨势分位中高
	// 财务因子明显抬升复合分（高于纯价量基线）
	if eval.TotalScore < 70 {
		t.Fatalf("财务强股复合分应较高, got %.2f", eval.TotalScore)
	}
	// 财务数据缺失时应回退价量因子（不 panic，仍可打分）
	md2 := &strategy_engine.StockMarketData{KLines: mkKLines(60)}
	eval2, err := fs.Evaluate("600001", md2)
	if err != nil || eval2 == nil {
		t.Fatalf("无财务数据 Evaluate 失败: %v", err)
	}
	_ = eval2
}
func TestFactorMultiRule(t *testing.T) {
	fs := New()
	rules := []*ActiveRule{
		{ID: "fac_1", Name: "因子战法#1", CandID: 1, Rule: Rule{
			Factors: []string{"Mom20"}, Weights: map[string]float64{"Mom20": 1},
			Directions: map[string]int{"Mom20": 1}, BuyThreshold: 50}},
		{ID: "fac_2", Name: "因子战法#2", CandID: 2, Rule: Rule{
			Factors: []string{"Mom20"}, Weights: map[string]float64{"Mom20": 1},
			Directions: map[string]int{"Mom20": 1}, BuyThreshold: 50}},
	}
	fs.SetRules(rules)
	if !fs.Enabled() || fs.RuleCount() != 2 {
		t.Fatalf("多规则应启用 2 条, enabled=%v count=%d", fs.Enabled(), fs.RuleCount())
	}
	md := &strategy_engine.StockMarketData{KLines: mkKLines(60)}
	eval, err := fs.Evaluate("600000", md)
	if err != nil || eval == nil || !eval.Pass {
		t.Fatalf("涨势序列应 Pass, eval=%+v err=%v", eval, err)
	}
	sig, err := fs.GenerateSignal("600000", eval)
	if err != nil || sig == nil {
		t.Fatalf("应出信号: %v", err)
	}
	// 信号应归属到某一条已启用规则（StrategyName 非空、strategy_id 有效）
	if sig.StrategyName == "" {
		t.Fatalf("信号应归属具体规则, got 空名")
	}
	wonID := int64(sig.Meta["strategy_id"])
	if wonID <= 0 {
		t.Fatalf("signal strategy_id 应>0, got %v", sig.Meta["strategy_id"])
	}
	// 获胜规则信号计数 +1
	st := fs.Stats()
	found := false
	for _, r := range st {
		if r.CandID == wonID {
			found = true
			if r.SignalCount != 1 {
				t.Fatalf("规则 %s 信号计数应=1, got %d", r.Name, r.SignalCount)
			}
		}
	}
	if !found {
		t.Fatalf("未找到获胜规则 candID=%d, stats=%+v", wonID, st)
	}
	// 记录前向收益 → Win/Loss/CumReturn 更新（对 fac_1）
	fs.RecordForwardReturn("fac_1", 0.05)
	fs.RecordForwardReturn("fac_1", -0.02)
	st = fs.Stats()
	for _, r := range st {
		if r.ID == "fac_1" {
			if r.Win != 1 || r.Loss != 1 || r.CumReturn < 0.029 || r.CumReturn > 0.031 {
				t.Fatalf("效果统计更新异常: %+v", r)
			}
		}
	}
}
