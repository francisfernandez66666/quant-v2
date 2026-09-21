// 逐股卖点评估：利空D1/破MA5·MA20/放量派发/动量衰竭 四因素命中与级别降序。
// （Per-stock sell-point assessment tests: the four factors and their severity ordering.）
package combat_agent

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// sellTestPM 锁定一个午后确定性交易时刻（北京 14:00，elapsed=180 → 派发阈值 1.5、
// 折算系数 240/180≈1.33），使纯因子判定测试不随真实运行时刻漂移。
// English: pinned afternoon clock (14:00 CST) so factor tests are time-of-day deterministic.
func sellTestPM() time.Time { return time.Date(2026, 9, 4, 14, 0, 0, 0, cntime.Loc) }

// sellTestMD 构造一个含行情/日K/分钟MACD的 StockMarketData。
// English: sellTestMD builds a StockMarketData with quote/daily K-lines/minute MACD.
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
// English: upKLines builds n rising K-lines (close increments by 1 each bar starting from 100).
func upKLines(n int) []data.KLine {
	kl := make([]data.KLine, n)
	for i := 0; i < n; i++ {
		kl[i] = data.KLine{Close: float64(100 + i), Volume: 1000}
	}
	return kl
}

// TestSellFactorBearishD1 利空D1（负面过滤拦截）→ 清仓级。
// English: TestSellFactorBearishD1 bearish D1 (negative filter veto) → liquidation tier.
func TestSellFactorBearishD1(t *testing.T) {
	md := sellTestMD(120, 2, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	fs := assessSellFactor("600001", md, D1Score{Score: 0, Blocked: true, Reason: "控股股东减持"}, 80, 60, sellTestPM())
	if len(fs) != 1 || fs[0].level != "清仓" || fs[0].action != "卖出" {
		t.Fatalf("利空D1 应清仓/卖出, got %+v", fs)
	}
}

// TestSellFactorBreakMA 现价跌破MA5与MA20 → 减仓级。
// English: TestSellFactorBreakMA price breaks below MA5 and MA20 → reduction tier.
func TestSellFactorBreakMA(t *testing.T) {
	// 前30根上行到129，现价跌到110（低于MA5≈124与MA20≈119）→ 破位
	// English: First 30 bars rise to 129, price drops to 110 (below MA5≈124 and MA20≈119) → breakdown.
	md := sellTestMD(110, -3, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60, sellTestPM())
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
// English: TestSellFactorVolumeDistribution high-volume decline → reduction tier.
func TestSellFactorVolumeDistribution(t *testing.T) {
	kl := upKLines(21) // 前20根为基准量，最后1根是当日
	md := sellTestMD(115, -2.5, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	// 当日量放大到均量的 2 倍（基准 1000，现 2000），且跌幅为负 → 派发
	// English: Today's volume expands to 2x the average (baseline 1000, now 2000) with a negative change → distribution.
	md.Quote.Volume = 2000
	md.ChangePct = -2.5
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60, sellTestPM())
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
// English: TestSellFactorMomentumExhaustion momentum score too low with a below-zero minute-MACD death cross → hint tier.
func TestSellFactorMomentumExhaustion(t *testing.T) {
	md := sellTestMD(105, -1, upKLines(30), data.MACD{DIF: -0.2, DEA: 0.1, Bar: -0.3})
	fs := assessSellFactor("600001", md, D1Score{}, 25, 60, sellTestPM())
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
	fs := assessSellFactor("600001", md, d1, 25, 60, sellTestPM())
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
	fs := assessSellFactor("600001", md, D1Score{}, 80, 60, sellTestPM())
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
	// §P4 缺陷1：派发因子首命中轮未过两轮确认被摘除 → 剩 利空D1+破位+衰竭 三项，置信度 0.75
	if sigs[0].Confidence != 0.75 {
		t.Fatalf("派发未确认轮三项因素置信度应为0.75, got %f", sigs[0].Confidence)
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
	// §P4 缺陷3：做空模式下原级别必须由结构化字段 SellLevel 携带（展示层据此定帽，不再猜子串）
	if sigs[0].SellLevel != "清仓" {
		t.Fatalf("做空模式应带结构化 SellLevel=清仓, got %q", sigs[0].SellLevel)
	}
}

// TestSellFactorDistributionGates §P4 缺陷1/2 回归锁（BUGFIX §三.4 前两把）：
//
//	① 缩量地板——原始累计量 < 前20日均量时任何时段都不得命中派发（10:55 折算 1.9 倍、
//	   实际累计 0.67 倍是缩量的历史误报案例）；
//	② 跌幅下限 −1.5%——-0.13% ≈ 平盘不得算"放量下跌、资金派发"；
//	③ 上午线性外推虚高抵消——10:00 温和放量 2 倍（折算后远超线性 1.5 但 <2.2 阈值）不得命中，
//	   午后同强度维持 1.5 阈值可命中。
func TestSellFactorDistributionGates(t *testing.T) {
	kl := upKLines(21) // 基准均量 1000

	// ① 缩量任何时段不得命中：累计 670（0.67×均量）、跌幅 -5%，早/午/尾盘全覆盖
	for _, now := range []time.Time{
		time.Date(2026, 9, 4, 9, 45, 0, 0, cntime.Loc),
		time.Date(2026, 9, 4, 10, 55, 0, 0, cntime.Loc),
		time.Date(2026, 9, 4, 12, 0, 0, 0, cntime.Loc),
		time.Date(2026, 9, 4, 14, 50, 0, 0, cntime.Loc),
	} {
		md := sellTestMD(115, -5, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
		md.Quote.Volume = 670
		for _, f := range assessSellFactor("600001", md, D1Score{}, 80, 60, now) {
			if f.kind == kindDistribution {
				t.Fatalf("缩量(0.67×均量)在 %v 不得命中派发", now.Format("15:04"))
			}
		}
	}

	// ② 跌幅下限：+30% 天量但只跌 0.13%（≈平盘）不得命中
	md2 := sellTestMD(115, -0.13, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	md2.Quote.Volume = 30000
	for _, f := range assessSellFactor("600001", md2, D1Score{}, 80, 60, sellTestPM()) {
		if f.kind == kindDistribution {
			t.Fatalf("跌幅-0.13%%≈平盘不得命中派发: %+v", f)
		}
	}

	// ③ 时段阈值双向夹逼：累计 1100（1.1×均量）在午休（elapsed=120，折算恰为 2.2，
	//    须严格大于才命中）不命中；累计 2200 在 14:00（elapsed=180，折算 2.93>1.5）正常命中。
	md3 := sellTestMD(115, -2.5, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	md3.Quote.Volume = 1100
	for _, f := range assessSellFactor("600001", md3, D1Score{}, 80, 60, time.Date(2026, 9, 4, 12, 0, 0, 0, cntime.Loc)) {
		if f.kind == kindDistribution {
			t.Fatalf("午休折算量比恰为2.2不得命中（阈值须严格大于）: %+v", f)
		}
	}
	md4 := sellTestMD(115, -2.5, kl, data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
	md4.Quote.Volume = 2200
	hit := false
	for _, f := range assessSellFactor("600001", md4, D1Score{}, 80, 60, sellTestPM()) {
		if f.kind == kindDistribution {
			hit = true
		}
	}
	if !hit {
		t.Fatal("14:00 累计2.2×均量(折算2.93>1.5) 跌2.5% 应命中派发")
	}
}

// TestAssessSellSideDistributionTwoRound §P4 缺陷1 两轮确认锁：仅放量派发命中的个股，
// 首命中轮不出信号（防单轮插针），连续第二轮才出减仓级；中途断一轮则计数清零。
func TestAssessSellSideDistributionTwoRound(t *testing.T) {
	a := New(&config.StrategyConfig{
		Momentum: config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60},
	})
	// 健康价（130 站上 MA5/MA20）、动量满分（不衰竭）、无 D1 拦截 → 只剩派发因子可命中
	distOnly := func(vol float64) map[string]*strategy_engine.StockMarketData {
		md := sellTestMD(130, -3, upKLines(30), data.MACD{DIF: 0.5, DEA: 0.4, Bar: 0.1})
		md.Quote.Volume = vol
		return map[string]*strategy_engine.StockMarketData{"600001": md}
	}
	d1 := map[string]D1Score{"600001": {Code: "600001"}}
	scores := map[string]StockScores{"600001": {MomentumScore: 80, MomentumValid: true}}
	mds := distOnly(10000) // 10× 均量，任何时段都过三重门

	if sigs := a.AssessSellSide([]string{"600001"}, mds, d1, scores, false); len(sigs) != 0 {
		t.Fatalf("派发首命中轮不得出信号, got %+v", sigs)
	}
	sigs := a.AssessSellSide([]string{"600001"}, mds, d1, scores, false)
	if len(sigs) != 1 || sigs[0].AlertType != "减仓" {
		t.Fatalf("派发连续第二轮应出减仓信号, got %+v", sigs)
	}
	// 断一轮（量回归缩量）→ 计数清零；再放量重新两轮计时
	if sigs := a.AssessSellSide([]string{"600001"}, distOnly(500), d1, scores, false); len(sigs) != 0 {
		t.Fatalf("断轮后不应有信号, got %+v", sigs)
	}
	if sigs := a.AssessSellSide([]string{"600001"}, mds, d1, scores, false); len(sigs) != 0 {
		t.Fatalf("清零后首命中轮应重新计时不出信号, got %+v", sigs)
	}
	if sigs := a.AssessSellSide([]string{"600001"}, mds, d1, scores, false); len(sigs) != 1 {
		t.Fatalf("重新两轮后应再出信号, got %+v", sigs)
	}
}
