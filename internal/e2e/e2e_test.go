// e2e 全流程测试：用 fixtures.json 实盘快照 mock 全部外部数据源 + mock LLM，
// 按 cmd/quant/main.go 的装配方式构建 engine，驱动 engine.Run 全链路，
// 逐场景断言 新闻→Stage0/2→事件→归因→板块验证→信号 各层确定性输出。
package e2e

import (
	"context"
	"encoding/json"
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
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// testRig 端到端装配产物：除引擎外暴露 SSE/报表/行情客户端等供后续子测试直接断言。
type testRig struct {
	eng    *engine.Engine
	agg    *display.Aggregator
	calls  *llmCalls
	sse    *server.SSEBroker
	rpt    *report.Report
	market *data.MarketAPI
	cfgMgr *config.Manager
	wl     *data.WatchlistManager
}

// applyScenarioOverrides 为战法触发/空路径验证场景对实盘快照做确定性增量覆盖：
//   - 300308 中际旭创：涨幅 +10.02%(封板级) + 近5日K线 +12.2%，配合 Dragon 权重激活后触发战法信号
//   - EMBoardList：东财 m:90 行业板块行情（diff 结构）真实数据，供 GetSectorList 解析验证
//   - News["sina"]：新浪滚动新闻真实格式，供 GetSinaNews 解析验证（主场景仍由 THS+CLS 提供 20 条）
//   - THSConcepts：追加"人工智能"首屏行情行，供 captureHotRecord 命中热点板块
func applyScenarioOverrides(fix *Fixture) {
	if csv, ok := fix.Quotes["300308"]; ok {
		parts := strings.Split(csv, ",")
		if len(parts) > 5 {
			parts[1] = "95.00"  // open
			parts[2] = "86.54"  // prev_close：相对现价 95.21 涨 +10.02%
			parts[3] = "95.21"  // price
			parts[4] = "95.21"  // high
			parts[5] = "94.00"  // low
			fix.Quotes["300308"] = strings.Join(parts, ",")
		}
	}
	if kls, ok := fix.Klines["300308"]; ok && len(kls) >= 5 {
		n := len(kls)
		closes := []float64{84.00, 88.00, 92.00, 94.00, 95.21}
		for i := 0; i < 5; i++ {
			k := &kls[n-5+i]
			c := closes[i]
			k.Open, k.Close = c-0.6, c
			k.High, k.Low = c+1.2, c-1.4
			k.Volume = 40000000 + float64(i)*5000000
		}
		fix.Klines["300308"] = kls
	}

	fix.EMBoardList = []data.SectorInfo{
		{Code: "BK0475", Name: "银行", ChangePct: 0.85, Amount: 1.2e11, NetInflow: 3e9, VolumeRank: 5, LimitupCnt: 0},
		{Code: "BK0477", Name: "证券", ChangePct: 1.26, Amount: 2.0e11, NetInflow: 5e9, VolumeRank: 2, LimitupCnt: 1},
		{Code: "BK0447", Name: "通信设备", ChangePct: 2.34, Amount: 8e10, NetInflow: 1e9, VolumeRank: 8, LimitupCnt: 2},
	}

	fix.News["sina"] = []data.NewsItem{
		{Title: "沪指半日涨0.62% 算力概念持续走强", Content: "7月31日午间收盘，沪指半日涨0.62%，AI算力概念持续走强。", Datetime: "2026-07-31 11:32:00", URL: "https://finance.sina.com.cn/stock/2026-07-31/doc-xyz.shtml", Source: "新浪财经"},
		{Title: "央行开展3000亿元7天期逆回购操作", Content: "央行公告，为维护银行体系流动性合理充裕，开展3000亿元逆回购操作。", Datetime: "2026-07-31 09:45:00", URL: "https://finance.sina.com.cn/roll/2026-07-31/doc-abc.shtml", Source: "新浪财经"},
	}

	fix.THSConcepts += `<tbody><tr><td>1</td><td><a href="http://q.10jqka.com.cn/gn/detail/code/302035/" target="_blank">人工智能</a></td><td>3.50</td><td>120.5</td><td>80</td><td>18.2</td></tr></tbody>`
}

// newTestEngine 按 cmd/quant/main.go 装配顶层引擎，注入 mock 网络与 mock LLM。
// 返回 rig，其中含 SSE/报表/行情客户端等供各子测试断言。
func newTestEngine(t *testing.T, fix *Fixture) *testRig {
	t.Helper()

	applyScenarioOverrides(fix)

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
	// 战法权重：仅激活 Dragon（F1~F4），其余策略权重保持 0，
	// 保证确定性只产出 dragon 信号，避免多策略同标的触发后的信号去重歧义。
	cfgMgr.Rules.Strategy.Dragon.F1SealWeight = 0.30
	cfgMgr.Rules.Strategy.Dragon.F2ResonanceWeight = 0.25
	cfgMgr.Rules.Strategy.Dragon.F3PremiumWeight = 0.20
	cfgMgr.Rules.Strategy.Dragon.F4RsWeight = 0.25
	// 情绪周期阈值：涨停池 99 家、最高连板 9 → 高潮。
	cfgMgr.Rules.Emotion.EmoClimaxLimitupMin = 90
	cfgMgr.Rules.Emotion.EmoClimaxBoardMin = 5

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

	sse := server.NewSSEBroker()
	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt,
		stockTracker, wlMgr, sse, llmClient, thsClient, tmp)
	eng.SetScanner(scanner)
	eng.SetEmotionConfig(&cfgMgr.Rules.Emotion)

	t.Cleanup(func() { srv.Close() })
	return &testRig{
		eng: eng, agg: agg, calls: calls, sse: sse, rpt: rpt,
		market: marketAPI, cfgMgr: cfgMgr, wl: wlMgr,
	}
}

// TestEndToEndFullPipeline 全流水线端到端：驱动 Run 后逐场景断言。
func TestEndToEndFullPipeline(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载 fixture: %v", err)
	}

	rig := newTestEngine(t, fix)
	rig.eng.SetShortEnabled(true)

	// SSE 订阅：引擎完成时广播 scan-done 摘要(含情绪相位)，用于断言广播链路与情绪周期。
	sseCh := rig.sse.Subscribe()
	defer rig.sse.Unsubscribe(sseCh)
	var sseRaw []map[string]string
	drain := func() {
		for {
			select {
			case raw := <-sseCh:
				var m map[string]string
				if err := json.Unmarshal(raw, &m); err == nil {
					sseRaw = append(sseRaw, m)
				}
			default:
				return
			}
		}
	}

	// 自选股注入：300750 + 300308 进入打分池（D1 权重=4；300308 供 Dragon 战法评估）。
	if !rig.wl.Add("300750") {
		t.Fatal("自选添加 300750 失败")
	}
	if !rig.wl.Add("300308") {
		t.Fatal("自选添加 300308 失败")
	}
	// 持仓注入：600276 恒瑞医药 开仓价 20 元（现价≈44.5，盈利 >122% 触及止盈50%）
	// → CheckPositionAlerts 产出一条止盈提醒，并同步进消息中心。
	rig.rpt.LogSignal("e2e-position-600276", "600276", "恒瑞医药", "做多",
		"dragon", 20.0, 50.0, 10.0)

	since := time.Date(2026, 7, 31, 8, 30, 0, 0, time.Local)
	sr := rig.eng.Run(context.Background(), since)
	drain()

	dash := rig.agg.Current()
	if dash == nil {
		t.Fatal("agg.Current() 为空，流水线未产出看板")
	}

	t.Run("LLM调用覆盖", func(t *testing.T) {
		if len(rig.calls.stage0) == 0 {
			t.Error("Stage0/1合并(质检) LLM 未被调用")
		}
		if len(rig.calls.stage2) < 2 {
			t.Errorf("Stage2 深度分析 LLM 调用次数 <2, got %d (个股批次+板块批次)", len(rig.calls.stage2))
		}
		if len(rig.calls.d1) == 0 {
			t.Error("D1 批量评分 LLM 未被调用")
		}
	})

	t.Run("事件归因", func(t *testing.T) {
		events := dash.NewsEvents
		if len(events) == 0 {
			t.Fatal("无事件产出")
		}

		// 事件聚簇：8 条 → 7 条（同板块 AI 利好两条合并）
		if len(events) != 7 {
			t.Errorf("有效事件数应为7(8→聚簇→7), got %d", len(events))
		}
		aiCnt := 0
		for _, ev := range events {
			if containsStr(ev.Sectors, "人工智能") {
				aiCnt++
			}
		}
		if aiCnt != 1 {
			t.Errorf("人工智能事件应聚簇为1条, got %d", aiCnt)
		}

		// IPO 新闻流注入（聚仁新材，Score 0.5 直构）
		ipoFeed := findEvent(events, "聚仁新材")
		if ipoFeed == nil {
			t.Error("缺少IPO新闻流事件(聚仁新材)")
		} else if ipoFeed.Score < 0.5 {
			t.Errorf("IPO新闻流事件 score=%.2f, want ≥0.5", ipoFeed.Score)
		}

		// IPO 日历事件注入（Source=IPO日历）
		ipoCal := false
		for i := range events {
			if events[i].Source == "IPO日历" {
				ipoCal = true
			}
		}
		if !ipoCal {
			t.Error("缺少IPO日历事件(Source=IPO日历)")
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

	t.Run("SSE广播与情绪周期", func(t *testing.T) {
		if len(sseRaw) == 0 {
			t.Fatal("未收到 SSE 广播")
		}
		scanned := false
		for _, m := range sseRaw {
			if m["type"] != "scan" || m["status"] != "done" {
				continue
			}
			scanned = true
			if m["zt_pool"] != "99" {
				t.Errorf("SSE zt_pool=%s, want 99", m["zt_pool"])
			}
			if m["bull"] == "" {
				t.Error("SSE bull 信号数缺失")
			}
			if m["emotion"] == "" {
				t.Error("SSE emotion 相位缺失")
			}
			if !strings.Contains(m["emotion"], "高潮") {
				t.Errorf("涨停99家+最高连板9 应判为高潮, got %q", m["emotion"])
			}
		}
		if !scanned {
			t.Error("SSE 无 scan-done 摘要广播")
		}
		if rig.sse.Len() < 1 {
			t.Error("SSE 订阅连接未保持")
		}
	})

	t.Run("做多信号与持仓提醒", func(t *testing.T) {
		if len(dash.FinalSignals) == 0 {
			t.Fatal("无最终信号")
		}
		var longN, alertN int
		for _, s := range dash.FinalSignals {
			switch {
			case s.Direction == "做多":
				longN++
			case s.AlertType != "":
				alertN++
			}
		}
		if longN == 0 {
			t.Error("无做多信号（300308 Dragon 战法应触发）")
		}
		// 600276 已被策略信号占用（冲突裁决跳过提醒），止盈提醒落在 AlertSignals 通道。
		hold := findAlert(dash.AlertSignals, "600276", "止盈")
		if hold == nil {
			t.Error("600276 持仓止盈提醒缺失(AlertSignals)")
		}
	})

	t.Run("东财板块列表", func(t *testing.T) {
		boards, err := rig.market.GetSectorList()
		if err != nil {
			t.Fatalf("GetSectorList: %v", err)
		}
		if len(boards) != 3 {
			t.Fatalf("板块列表数量=3, got %d", len(boards))
		}
		byCode := map[string]data.SectorInfo{}
		for _, b := range boards {
			byCode[b.Code] = b
		}
		comm := byCode["BK0447"]
		if comm.Name != "通信设备" {
			t.Errorf("BK0447 名称=%s, want 通信设备", comm.Name)
		}
		if diff := comm.ChangePct - 2.34; diff > 0.005 || diff < -0.005 {
			t.Errorf("BK0447 涨跌幅=%.2f%%, want 2.34%% (f3 千分位÷100 解析)", comm.ChangePct)
		}
		if comm.LimitupCnt != 2 || comm.VolumeRank != 8 {
			t.Errorf("BK0447 涨停家数/量比排名=%d/%d, want 2/8", comm.LimitupCnt, comm.VolumeRank)
		}
		if comm.Amount != 8e10 || comm.NetInflow != 1e9 {
			t.Errorf("BK0447 成交额/主力净流入=%.0f/%.0f, want 8e10/1e9", comm.Amount, comm.NetInflow)
		}
	})

	t.Run("新浪滚动新闻", func(t *testing.T) {
		items, err := rig.market.GetSinaNews(50)
		if err != nil {
			t.Fatalf("GetSinaNews: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("新浪滚动新闻条数=2, got %d", len(items))
		}
		first := items[0]
		if !strings.Contains(first.Title, "算力概念") {
			t.Errorf("首条标题=%q, 应含算力概念", first.Title)
		}
		if !strings.Contains(first.URL, "finance.sina.com.cn") {
			t.Errorf("URL=%q, 应含 finance.sina.com.cn", first.URL)
		}
	})

	t.Run("热点记录", func(t *testing.T) {
		hot := rig.eng.GetHotRecords()
		if len(hot) == 0 {
			t.Fatal("无热点记录")
		}
		found := false
		for _, h := range hot {
			for _, s := range h.Sectors {
				if s.Name == "人工智能" {
					found = true
					if s.Code != "302035" {
						t.Errorf("人工智能热点板块 code=%s, want 302035", s.Code)
					}
				}
			}
		}
		if !found {
			t.Errorf("热点记录缺少 人工智能，got %+v", hot)
		}
	})

	t.Run("消息中心", func(t *testing.T) {
		msgs := rig.eng.GetMessages()
		if len(msgs) == 0 {
			t.Fatal("消息中心为空")
		}
		var holdMsg, alertMsg bool
		for _, m := range msgs {
			if m.Level == "持仓提示" && m.Code == "600276" {
				holdMsg = true
			}
			if m.Level == "止盈" && m.Code == "600276" {
				alertMsg = true
			}
		}
		if !holdMsg {
			t.Errorf("消息中心缺少持仓提示(600276)，got %+v", msgs)
		}
		if !alertMsg {
			t.Errorf("消息中心缺少止盈告警(600276)，got %+v", msgs)
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

// findAlert 在最终信号中查找指定代码+提醒类型的持仓告警信号。
func findAlert(signals []combat_agent.Signal, code, alertType string) *combat_agent.Signal {
	for i := range signals {
		if signals[i].Code == code && signals[i].AlertType == alertType {
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
