// 逐股卖点评估：利空D1/破MA5·MA20/放量派发/动量衰竭 四因素命中与级别降序。
// （Per-stock sell-point assessment tests: the four factors and their severity ordering.）
package combat_agent

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// sellTestMD 构造一个含行情/日K/分钟MACD的 StockMarketData。
func sellTestMD(price float64, chgPct float64, kl []data.KLine, macd data.MACD) *strategy_engine.StockMarketData {
	return &strategy_engine.StockMarketData{
		Code:       "600001",
		Name:       "测试",
		Price:      price,
		ChangePct:  chgPct,
		KLines:     kl,
		Quote:      &data.StockInfo{Code: "600001", Name: "测试", Price: price, ChangePct: chgPct, Volume: 100000},
		MinuteMACD: macd,
	}
}

// upKLines 构造 n 根上行K线（close 逐根+1 从 100 起）。
func upKLines(n int) []data.KLine {
	kl := make([]data.KLine, n)
	for i := 0; i < n; i++ {
		kl[i] = data.KLine{Close: float64(100 + i), Volume: 1000}
	}
	return kl
}

// TestSellFactorBearishD1 利空D1（负面过滤拦截）→ 清仓级。
func TestSellFactorBearishD1(t *testing.T) {
	md := sellTestMD(120, 2, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	fs := assessSellFactor("600001", md, D1Score{Score: 0, Blocked: true, Reason: "控股股东减持"}, 80, 60)
	if len(fs) != 1 || fs[0].level != "清仓" || fs[0].action != "卖出" {
		t.Fatalf("利空D1 应清仓/卖出, got %+v", fs)
	}
}

// TestSellFactorBreakMA 现价跌破MA5与MA20 → 减仓级。
func TestSellFactorBreakMA(t *testing.T) {
	// 前30根上行到129，现价跌到110（低于MA5≈124与MA20≈119）→ 破位
	md := sellTestMD(110, -3, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60)
	found := false
	for _, f := range fs {
		if f.level == "减仓" && f.action == "卖出" {
			found = true
		}
	}
	if !found {
		t.Fatalf("破MA5/MA20 应减仓/卖出, got %+v", fs)
	}
}

// TestSellFactorVolumeDistribution 放量下跌 → 减仓级。
func TestSellFactorVolumeDistribution(t *testing.T) {
	kl := upKLines(21) // 前20根为基准量，最后1根是当日
	md := sellTestMD(115, -2.5, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	// 当日量放大到均量的 2 倍（基准 1000，现 2000），且跌幅为负 → 派发
	md.Quote.Volume = 2000
	md.ChangePct = -2.5
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60)
	found := false
	for _, f := range fs {
		if f.level == "减仓" && f.action == "卖出" {
			found = true
		}
	}
	if !found {
		t.Fatalf("放量下跌应减仓/卖出, got %+v", fs)
	}
}

// TestSellFactorMomentumExhaustion 动量分过低且分钟MACD零下死叉 → 提示级。
func TestSellFactorMomentumExhaustion(t *testing.T) {
	md := sellTestMD(105, -1, upKLines(30), data.MACD{DIF: -0.2, DEA: 0.1, Bar: -0.3})
	fs := assessSellFactor("600001", md, D1Score{}, 25, 60)
	found := false
	for _, f := range fs {
		if f.level == "提示" && f.action == "卖出" {
			found = true
		}
	}
	if !found {
		t.Fatalf("动量衰竭应提示/卖出, got %+v", fs)
	}
}

// TestSellFactorSeverityOrdering 多因素同时命中时取最严重级别（利空D1>破位>派发>衰竭）。
func TestSellFactorSeverityOrdering(t *testing.T) {
	// 利空D1 + 破位 + 动量衰竭同时命中 → 最终应为清仓（最高级）
	kl := upKLines(30)
	md := sellTestMD(110, -3, kl, data.MACD{DIF: -0.2, DEA: 0.1, Bar: -0.3})
	d1 := D1Score{Score: 0, Blocked: true, Reason: "立案调查"}
	fs := assessSellFactor("600001", md, d1, 25, 60)
	if len(fs) < 2 {
		t.Fatalf("应至少命中2项因素, got %+v", fs)
	}
	if fs[0].level != "清仓" {
		t.Fatalf("多因素时应取清仓级, got %+v", fs)
	}
}

// TestSellFactorNoHit 健康上行股（站上均线、量价正常、MACD多头）不产生任何卖点信号。
func TestSellFactorNoHit(t *testing.T) {
	md := sellTestMD(128, 2, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60)
	if len(fs) != 0 {
		t.Fatalf("健康上行股不应命中卖点, got %+v", fs)
	}
}

// TestAssessSellSide 集成：AssessSellSide 对命中个股产出售点信号，未命中个股跳过。
func TestAssessSellSide(t *testing.T) {
	a := New(&config.StrategyConfig{
		Momentum: config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60},
	})
	good := sellTestMD(128, 2, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	bad := sellTestMD(110, -3, upKLines(30), data.MACD{DIF: -0.2, DEA: 0.1, Bar: -0.3})
	bad.Quote.Volume = 2000
	md := map[string]*strategy_engine.StockMarketData{"600001": good, "600002": bad}
	d1 := map[string]D1Score{"600001": {Code: "600001"}, "600002": {Code: "600002", Blocked: true, Reason: "减持"}}
	scores := map[string]StockScores{"600001": {MomentumScore: 80, MomentumValid: true}, "600002": {MomentumScore: 25, MomentumValid: true}}

	sigs := a.AssessSellSide([]string{"600001", "600002"}, md, d1, scores, false)
	if len(sigs) != 1 {
		t.Fatalf("应只有 600002 产出售点信号, got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].Code != "600002" || sigs[0].AlertType != "清仓" {
		t.Fatalf("600002 应清仓/卖出, got %+v", sigs[0])
	}
	if sigs[0].Confidence != 1.0 {
		t.Fatalf("两项因素置信度应为1.0, got %f", sigs[0].Confidence)
	}
}

// TestAssessSellSideShortEnabled 做多+做空模式：卖点评估级别徽标改为方向词"做空"，Reason 保留原等级。
func TestAssessSellSideShortEnabled(t *testing.T) {
	a := New(&config.StrategyConfig{
		Momentum: config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60},
	})
	bad := sellTestMD(110, -3, upKLines(30), data.MACD{DIF: -0.2, DEA: 0.1, Bar: -0.3})
	bad.Quote.Volume = 2000
	md := map[string]*strategy_engine.StockMarketData{"600002": bad}
	d1 := map[string]D1Score{"600002": {Code: "600002", Blocked: true, Reason: "减持"}}
	scores := map[string]StockScores{"600002": {MomentumScore: 25, MomentumValid: true}}

	sigs := a.AssessSellSide([]string{"600002"}, md, d1, scores, true)
	if len(sigs) != 1 {
		t.Fatalf("应产出一条卖点信号, got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].AlertType != "做空" || sigs[0].Direction != "做空" || sigs[0].Action != "卖出" {
		t.Fatalf("短仓模式下级别应为方向词做空/卖出, got %+v", sigs[0])
	}
	if !strings.Contains(sigs[0].Reason, "卖点等级:清仓") {
		t.Fatalf("Reason 应保留原清仓等级, got %q", sigs[0].Reason)
	}
}
