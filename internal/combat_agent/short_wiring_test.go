// short_wiring_test.go §SHORT-1 做空管道接线测试：两层门控、sell/watch 持仓决策、
// ST 屏蔽、旧骨架回退、buildShortData 派生正确性。评分逻辑本身在各战法包单测覆盖。
package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategies/shortbase"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// alwaysPassStub 恒过闸的桩战法：验证 evalShort 的 sell/watch/字段装配逻辑。
type alwaysPassStub struct{}

func (alwaysPassStub) Name() string              { return "stub" }
func (alwaysPassStub) Type() strategy.SignalType { return strategy.SignalHighChurn }
func (alwaysPassStub) Evaluate(string, interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{TotalScore: 80, Pass: true, Level: "full_chain", Confidence: 0.8}, nil
}
func (alwaysPassStub) GenerateSignal(string, *strategy.Evaluation) (*strategy.Signal, error) {
	return &strategy.Signal{Type: strategy.SignalHighChurn, Action: strategy.ActionSell, Confidence: 0.8, Reason: "stub"}, nil
}

// stubMD 构造最小可评行情（K线数量满足各战法硬闸）。
func stubMD(name string) *strategy_engine.StockMarketData {
	kl := make([]data.KLine, 60)
	for i := range kl {
		c := 10.0 + float64(i)*0.1
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-60), Open: c, High: c, Low: c, Close: c, Volume: 1e6}
	}
	return &strategy_engine.StockMarketData{Code: "600001.SH", Name: name, Price: kl[59].Close, KLines: kl}
}

func newShortAgent(t *testing.T) *Agent {
	t.Helper()
	a := New(&config.StrategyConfig{})
	a.SetShortRunners([]StrategyRunner{{Type: strategy.SignalHighChurn, Strategy: alwaysPassStub{}}})
	a.SetShortEnabled(true)
	return a
}

// TestScanShortGateOff 全局做空开关关闭 → 零信号（两层门第一层）。
func TestScanShortGateOff(t *testing.T) {
	a := newShortAgent(t)
	a.SetShortEnabled(false)
	in := ScanInput{IndividualStocks: []string{"600001.SH"}, MarketData: map[string]*strategy_engine.StockMarketData{"600001.SH": stubMD("测试股")}}
	if sigs := a.ScanShort(in); sigs != nil {
		t.Fatalf("全局关闭应零信号, got %d", len(sigs))
	}
}

// TestScanShortTacticLayerOff 战法层开关 rules.strategy.short.enabled=false → 静默（两层门第二层）。
func TestScanShortTacticLayerOff(t *testing.T) {
	off := false
	a := New(&config.StrategyConfig{Short: config.ShortStrategiesConfig{Enabled: &off}})
	a.SetShortRunners([]StrategyRunner{{Type: strategy.SignalHighChurn, Strategy: alwaysPassStub{}}})
	a.SetShortEnabled(true)
	in := ScanInput{IndividualStocks: []string{"600001.SH"}, MarketData: map[string]*strategy_engine.StockMarketData{"600001.SH": stubMD("测试股")}}
	if sigs := a.ScanShort(in); sigs != nil {
		t.Fatalf("战法层关闭应零信号, got %d", len(sigs))
	}
}

// TestScanShortSellVsWatch 持仓 → sell；非持仓 → watch（决策②安全侧）。
func TestScanShortSellVsWatch(t *testing.T) {
	a := newShortAgent(t)
	md := stubMD("测试股")
	in := ScanInput{
		IndividualStocks: []string{"600001.SH", "600002.SH"},
		MarketData: map[string]*strategy_engine.StockMarketData{
			"600001.SH": md,
			"600002.SH": func() *strategy_engine.StockMarketData { m := stubMD("测试股2"); m.Code = "600002.SH"; return m }(),
		},
		HeldCodes: map[string]bool{"600001.SH": true},
		Scores:    map[string]StockScores{},
	}
	sigs := a.ScanShort(in)
	byCode := map[string]Signal{}
	for _, s := range sigs {
		byCode[s.Code] = s
	}
	if s, ok := byCode["600001.SH"]; !ok || s.Action != "sell" || s.Direction != "做空" {
		t.Fatalf("持仓股应为做空 sell: %+v", s)
	}
	if s, ok := byCode["600002.SH"]; !ok || s.Action != "watch" {
		t.Fatalf("非持仓股应降级 watch: %+v", s)
	}
	if sc := in.Scores["600001.SH"]; sc.ShortScore < 80 || !sc.SignalActive {
		t.Fatalf("做空打分应归档 ShortScore 并置 SignalActive: %+v", sc)
	}
}

// TestScanShortSTBlocked ST 个股不产生做空信号。
func TestScanShortSTBlocked(t *testing.T) {
	a := newShortAgent(t)
	in := ScanInput{
		IndividualStocks: []string{"600001.SH"},
		MarketData:       map[string]*strategy_engine.StockMarketData{"600001.SH": stubMD("ST测试")},
		HeldCodes:        map[string]bool{"600001.SH": true},
	}
	if sigs := a.ScanShort(in); len(sigs) != 0 {
		t.Fatalf("ST 个股不应出做空信号, got %d", len(sigs))
	}
}

// TestScanShortSectorFilter 仅利空板块进 8b；利好板块个股不走做空扫描。
func TestScanShortSectorFilter(t *testing.T) {
	a := newShortAgent(t)
	md := stubMD("测试股")
	in := ScanInput{
		Sectors: []sector_agent.VerifiedSector{
			{Name: "板块A", Direction: "利好", Stocks: []string{"600001.SH"}},
		},
		MarketData: map[string]*strategy_engine.StockMarketData{"600001.SH": md},
		HeldCodes:  map[string]bool{"600001.SH": true},
	}
	if sigs := a.ScanShort(in); len(sigs) != 0 {
		t.Fatalf("利好板块不应走做空路径, got %d", len(sigs))
	}
	in.Sectors[0].Direction = "利空"
	if sigs := a.ScanShort(in); len(sigs) == 0 {
		t.Fatalf("利空板块应产出做空信号")
	}
}

// TestBuildShortDataDerives 派生正确性：均线/位置/破位/连板/事件窗。
func TestBuildShortDataDerives(t *testing.T) {
	n := 60
	kl := make([]data.KLine, n)
	for i := range kl {
		c := float64(10 + i/10) // 台阶式上行
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n), Open: c, High: c, Low: c * 0.99, Close: c, Volume: 1e6}
	}
	// 末 4 根改为连续涨停（主板 10%）
	for i := n - 4; i < n; i++ {
		nc := kl[i-1].Close * 1.1
		kl[i].Open, kl[i].High, kl[i].Close = nc*0.99, nc, nc
	}
	md := &strategy_engine.StockMarketData{Code: "600001.SH", Name: "测试股", Price: kl[n-1].Close, KLines: kl}
	in := &ScanInput{
		EmotionPhase: "高潮",
		News: map[string][]NewsBrief{"600001.SH": {
			{Title: "重大利好", Positive: true, Time: time.Now().AddDate(0, 0, -2).Format("2006-01-02 15:04:05"), Score: 0.75, Level: "个股"},
		}},
	}
	sd := buildShortData("600001.SH", md, nil, in, "板块X", true, 0)
	if sd.MA5 <= 0 || sd.MA20 <= 0 {
		t.Fatalf("均线应派生: %+v", sd)
	}
	if sd.PosHigh < 0.99 {
		t.Fatalf("连板末端现价≈60日最高: %.3f", sd.PosHigh)
	}
	if sd.ConsecBoards < 3 {
		t.Fatalf("连板数应≥3, got %d", sd.ConsecBoards)
	}
	if !sd.SealedToday {
		t.Fatalf("末日收盘=板价应判封板")
	}
	if !sd.EventInWindow || sd.EventScore != 0.75 || sd.EventPropagation {
		t.Fatalf("事件窗派生错误: %+v", sd)
	}
	if !sd.Held || sd.EmotionPhase != "高潮" {
		t.Fatalf("持仓/情绪透传错误")
	}
}

// TestStrategyDisplayNameShortTactics 四做空战法中文名映射。
func TestStrategyDisplayNameShortTactics(t *testing.T) {
	want := map[string]string{"high_churn": "高位滞涨", "break_down": "放量破位", "leader_decay": "龙头断板", "good_news_fade": "利好兑现砸盘"}
	for k, v := range want {
		if got := StrategyDisplayName(k); got != v {
			t.Fatalf("StrategyDisplayName(%s)=%s want %s", k, got, v)
		}
		if got := NormalizeStrategyName(k); got != v {
			t.Fatalf("NormalizeStrategyName(%s)=%s want %s", k, got, v)
		}
	}
}

// shortbase 使用占位（避免未使用 import 在重构时漂移）。
var _ = shortbase.Data{}
