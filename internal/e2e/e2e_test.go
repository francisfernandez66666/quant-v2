// e2e 全流程测试：用 fixtures.json 实盘快照 mock 全部外部数据源 + mock LLM，
// 按 cmd/quant/main.go 的装配方式构建 engine，驱动 engine.Run 全链路，
// 逐场景断言 新闻→Stage0/2→事件→归因→板块验证→信号 各层确定性输出。
package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// newTestEngine 按 cmd/quant/main.go 装配顶层引擎，注入 mock 网络与 mock LLM。
// 返回 (engine, aggregator, llm调用记录, 清理函数)。
func newTestEngine(t *testing.T, fix *Fixture) (*engine.Engine, *display.Aggregator, *llmCalls) {
	t.Helper()

	rt := &fixtureTransport{fix: fix}

	marketAPI := data.NewMarketAPI()
	marketAPI.SetTransport(rt)

	thsClient := data.NewTHSClient()
	thsClient.SetTransport(rt)

	var matcher *data.EventMatcher
	if cfg, err := data.LoadEvents(filepath.Join("..", "..", "events_leftside.yaml")); err == nil {
		matcher = data.NewEventMatcher(cfg)
	}

	srv, calls := newMockLLMServer()
	llmClient := llm.New(llm.Config{APIKey: "e2e-mock", APIURL: srv.URL, Model: "mock"})

	tmp := t.TempDir()

	cleaner := data.NewStockCleaner(marketAPI)
	nAgent := newsagent.New(marketAPI, llmClient, cleaner, tmp)

	strategyEngine := strategy_engine.New(marketAPI)
	scanner := data.NewSectorScanner(marketAPI, matcher)
	strategyEngine.SetScanner(scanner)

	cfgMgr := config.NewManager(filepath.Join(tmp, "config.json"))
	sAgent := sector_agent.New(scanner, data.NewRPSManager())
	cAgent := combat_agent.New(cfgMgr.GetStrategyConfig())
	cAgent.SetLaodengConfig(&cfgMgr.Rules.Laodeng)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, matcher)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
	})

	rpt := report.New(filepath.Join(tmp, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(tmp)
	stockTracker := data.NewStockTracker(filepath.Join(tmp, "tracked_stocks.json"))

	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt,
		stockTracker, wlMgr, nil, llmClient, thsClient, tmp)
	eng.SetScanner(scanner)

	t.Cleanup(func() { srv.Close() })
	return eng, agg, calls
}

// TestEndToEndFullPipeline 全流水线端到端：驱动 Run 后逐场景断言。
func TestEndToEndFullPipeline(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载 fixture: %v", err)
	}

	eng, agg, calls := newTestEngine(t, fix)
	eng.SetShortEnabled(true)

	since := time.Date(2026, 7, 31, 8, 30, 0, 0, time.Local)
	sr := eng.Run(context.Background(), since)

	dash := agg.Current()
	if dash == nil {
		t.Fatal("agg.Current() 为空，流水线未产出看板")
	}

	t.Run("LLM调用覆盖", func(t *testing.T) {
		if len(calls.stage0) == 0 {
			t.Error("Stage0/1合并(质检) LLM 未被调用")
		}
		if len(calls.stage2) < 2 {
			t.Errorf("Stage2 深度分析 LLM 调用次数 <2, got %d (个股批次+板块批次)", len(calls.stage2))
		}
		if len(calls.d1) == 0 {
			t.Error("D1 批量评分 LLM 未被调用")
		}
	})

	t.Run("事件归因", func(t *testing.T) {
		events := dash.NewsEvents
		if len(events) == 0 {
			t.Fatal("无事件产出")
		}

		// 宁德时代：个股利好
		nd := findEvent(events, "宁德时代中标")
		if nd == nil {
			t.Error("缺少宁德时代事件")
		} else {
			assertEvent(t, nd, "个股", "利好", 0.75)
			assertHasCode(t, nd.RelatedStocks, "300750")
		}

		// 贵州茅台：个股利空
		mt := findEvent(events, "贵州茅台三季度")
		if mt == nil {
			t.Error("缺少贵州茅台事件")
		} else {
			assertEvent(t, mt, "个股", "利空", -0.75)
			assertHasCode(t, mt.RelatedStocks, "600519")
		}

		// 恒瑞医药：个股利好
		hr := findEvent(events, "恒瑞医药创新药获批")
		if hr == nil {
			t.Error("缺少恒瑞医药事件")
		} else {
			assertEvent(t, hr, "个股", "利好", 0.75)
			assertHasCode(t, hr.RelatedStocks, "600276")
		}

		// 药品集采：板块利空 → 创新药
		jc := findEvent(events, "药品集采")
		if jc == nil {
			t.Error("缺少药品集采事件")
		} else {
			assertEvent(t, jc, "板块", "利空", -0.75)
			assertHasSector(t, jc.Sectors, "创新药")
		}

		// AI算力：板块利好（工信部政策 + 突发大消息 聚簇）
		ai := findEvent(events, "人工智能算力基础设施")
		if ai == nil {
			t.Error("缺少AI算力板块事件")
		} else {
			assertEvent(t, ai, "板块", "利好", 0.75)
			assertHasSector(t, ai.Sectors, "人工智能")
			assertHasCode(t, ai.RelatedStocks, "300308")
			assertHasCode(t, ai.RelatedStocks, "000938")
		}
	})

	t.Run("板块归因", func(t *testing.T) {
		if len(sr.HotSectors) == 0 {
			t.Error("无利好板块归因")
		} else {
			ai := findSector(sr.HotSectors, "人工智能")
			if ai == nil {
				t.Error("利好板块缺少 人工智能")
			} else if ai.Score <= 0 {
				t.Errorf("人工智能板块评分应>0, got %.2f", ai.Score)
			}
		}
		if len(sr.BearSectors) == 0 {
			t.Error("无利空板块归因")
		} else {
			xy := findSector(sr.BearSectors, "创新药")
			if xy == nil {
				t.Error("利空板块缺少 创新药")
			} else if xy.Score >= 0 {
				t.Errorf("创新药板块评分应<0, got %.2f", xy.Score)
			}
		}
	})

	t.Run("个股分流", func(t *testing.T) {
		if !containsCode(sr.LongStocks, "300750") {
			t.Error("LongStocks 缺少 300750(宁德时代)")
		}
		if !containsCode(sr.LongStocks, "600276") {
			t.Error("LongStocks 缺少 600276(恒瑞医药)")
		}
		if !containsCode(sr.ShortStocks, "600519") {
			t.Error("ShortStocks 缺少 600519(贵州茅台)")
		}
		for _, want := range []string{"300750", "600276", "600519"} {
			if !containsStr(sr.ScoringPool, want) {
				t.Errorf("ScoringPool 缺少 %s", want)
			}
		}
	})

	t.Run("行情数据", func(t *testing.T) {
		md := sr.MarketData["300750"]
		if md == nil || md.Price <= 0 {
			t.Fatalf("300750 行情缺失: %+v", md)
		}
		if diff := md.ChangePct - 2.90; diff > 0.05 || diff < -0.05 {
			t.Errorf("300750 涨跌幅应≈+2.90%%, got %.2f", md.ChangePct)
		}
		if md.Name != "宁德时代" {
			t.Errorf("300750 名称应为宁德时代, got %q", md.Name)
		}
		if md.KLines == nil || len(md.KLines) < 60 {
			t.Errorf("300750 K线不足60根, got %d", len(md.KLines))
		}
		if md.MoneyFlow == nil {
			t.Error("300750 资金流缺失")
		}

		mt := sr.MarketData["600519"]
		if mt == nil || mt.ChangePct > -3 || mt.ChangePct < -4 {
			t.Errorf("600519 涨跌幅应≈-3.51%%, got %v", mt)
		}
	})

	t.Run("板块验证", func(t *testing.T) {
		if !containsVerified(dash.VerifiedBull, "人工智能") {
			t.Error("VerifiedBull 缺少 人工智能")
		}
		if !containsVerified(dash.VerifiedBear, "创新药") {
			t.Error("VerifiedBear 缺少 创新药")
		}
	})

	t.Run("预期差信号", func(t *testing.T) {
		// 恒瑞医药：有利好新闻但股价 -1.49% → 利好不涨(GapBullishNoRise) 触发
		sig := findSignal(dash.FinalSignals, "600276")
		if sig == nil {
			t.Fatal("FinalSignals 缺少 600276")
		}
		if sig.Strategy != "预期差" {
			t.Errorf("600276 应为预期差信号, got %s", sig.Strategy)
		}
		// 宁德时代 +2.9% 属利好正常反应，不应触发预期差
		for _, s := range dash.FinalSignals {
			if s.Code == "300750" && s.Strategy == "预期差" {
				t.Errorf("300750 不应触发预期差信号: %s", s.Reason)
			}
		}
	})

	t.Run("板块事件传播", func(t *testing.T) {
		// AI 板块事件经 propagateSectorToStocks 后 RelatedStocks 含成分股代码
		ai := findEvent(dash.NewsEvents, "人工智能算力基础设施")
		if ai == nil {
			t.Fatal("缺少AI板块事件")
		}
		if len(ai.CleanedStocks) == 0 {
			t.Error("AI板块事件 CleanedStocks 为空")
		}
	})
}

// ── 断言辅助 ──

func findEvent(events []newsagent.NewsEvent, kw string) *newsagent.NewsEvent {
	for i := range events {
		if strings.Contains(events[i].Title, kw) {
			return &events[i]
		}
	}
	return nil
}

func assertEvent(t *testing.T, ev *newsagent.NewsEvent, level, direction string, score float64) {
	t.Helper()
	if ev.Level != level {
		t.Errorf("%s: Level=%s, want %s", ev.Title, ev.Level, level)
	}
	if ev.Direction != direction {
		t.Errorf("%s: Direction=%s, want %s", ev.Title, ev.Direction, direction)
	}
	if ev.Score != score {
		t.Errorf("%s: Score=%.2f, want %.2f", ev.Title, ev.Score, score)
	}
}

func assertHasSector(t *testing.T, sectors []string, want string) {
	t.Helper()
	if !containsStr(sectors, want) {
		t.Errorf("Sectors=%v 缺少 %s", sectors, want)
	}
}

func assertHasCode(t *testing.T, stocks []string, want string) {
	t.Helper()
	for _, s := range stocks {
		if strings.Contains(s, want) {
			return
		}
	}
	t.Errorf("RelatedStocks=%v 缺少代码 %s", stocks, want)
}

func findSector(sectors []strategy_engine.SectorHot, name string) *strategy_engine.SectorHot {
	for i := range sectors {
		if sectors[i].Name == name {
			return &sectors[i]
		}
	}
	return nil
}

func containsCode(stocks []strategy_engine.IndividualStock, code string) bool {
	for _, s := range stocks {
		if s.Code == code {
			return true
		}
	}
	return false
}

func containsVerified(vs []sector_agent.VerifiedSector, name string) bool {
	for _, v := range vs {
		if v.Name == name {
			return true
		}
	}
	return false
}

func findSignal(signals []combat_agent.Signal, code string) *combat_agent.Signal {
	for i := range signals {
		if signals[i].Code == code {
			return &signals[i]
		}
	}
	return nil
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
