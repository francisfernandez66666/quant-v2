// Package main 量化交易系统入口：初始化所有模块（认证、行情、策略、板块、新闻、风控），
// 按市场时段循环执行盘前/盘中/盘后流程。
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
)

// main 系统入口：初始化数据目录、认证、行情 API、LLM、新闻代理、策略引擎等所有组件，
// 然后进入主循环，按  5 分钟间隔根据市场时段执行处理流程。
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	dataDir := getDataDir()
	os.MkdirAll(dataDir, 0755)

	authMgr := auth.NewManager(dataDir)
	if err := authMgr.Init(); err != nil {
		log.Fatalf("auth init: %v", err)
	}
	if !authMgr.IsInitialized() {
		u, err := authMgr.Register("admin", "admin123")
		if err != nil {
			log.Fatalf("create default admin: %v", err)
		}
		log.Printf("首次启动, 已创建默认管理员账号 admin / admin123 (token=%s)", u.Token[:16]+"...")
		authMgr.MarkInitialized()
	}

	marketAPI := data.NewMarketAPI()
	_ = data.NewTHSClient() // 同花顺用于 DataCoordinator 降级

	var matcher *data.EventMatcher
	eventsCfg, err := data.LoadEvents("config/events_leftside.yaml")
	if err == nil {
		matcher = data.NewEventMatcher(eventsCfg)
	}

	cfgMgr := config.NewManager(filepath.Join(dataDir, "config.json"))

	llmCfg := llm.Config{}
	llmCfg.APIKey = os.Getenv("LLM_API_KEY")
	llmCfg.APIURL = os.Getenv("LLM_API_URL")
	llmCfg.Model = os.Getenv("LLM_MODEL")
	if llmCfg.APIKey == "" {
		if v, ok := authMgr.GetConfig("", "llm_api_key"); ok {
			llmCfg.APIKey = v
		}
	}
	if llmCfg.APIURL == "" {
		if v, ok := authMgr.GetConfig("", "llm_api_url"); ok {
			llmCfg.APIURL = v
		}
	}
	if llmCfg.APIURL == "" {
		llmCfg.APIURL = cfgMgr.Rules.LLM.APIURL
	}
	if llmCfg.Model == "" {
		llmCfg.Model = cfgMgr.Rules.LLM.Model
	}

	var llmClient *llm.Client
	if llmCfg.APIKey != "" {
		llmClient = llm.New(llmCfg)
	} else {
		log.Println("[LLM] 未配置 API Key，LLM 功能不可用")
	}

	cleaner := data.NewStockCleaner(marketAPI)

	nAgent := newsagent.New(marketAPI, llmClient, cleaner, dataDir)
	nAgent.Start()
	defer nAgent.Stop()

	engine := strategy_engine.New(marketAPI)

	scanner := data.NewSectorScanner(marketAPI, matcher)
	engine.SetScanner(scanner)
	rpsMgr := data.NewRPSManager()
	sAgent := sector_agent.New(scanner, rpsMgr)
	stratCfg := cfgMgr.GetStrategyConfig()
	laodengCfg := &cfgMgr.Rules.Laodeng
	cAgent := combat_agent.New(stratCfg)
	cAgent.SetLaodengConfig(laodengCfg)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, matcher)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
	})
	cAgent.StartHotReload(filepath.Join(dataDir, "config.json"))

	rpt := report.New(filepath.Join(dataDir, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(dataDir)

	srv := server.New(authMgr, agg, cfgMgr, rpt, marketAPI, wlMgr)
	srv.SetNewsAgent(nAgent)
	srv.SetLLMRecreate(func(apiKey, apiURL, model string) {
		lc := llm.New(llm.Config{APIKey: apiKey, APIURL: apiURL, Model: model})
		nAgent.SetLLMClient(lc)
		log.Printf("[LLM] 客户端已热重建: model=%s url=%s", model, apiURL)
	})
	addr := ":8080"
	if v := os.Getenv("QUANT_ADDR"); v != "" {
		addr = v
	}
	go func() {
		log.Fatal(srv.Serve(addr))
	}()

	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	stockTracker := data.NewStockTracker(filepath.Join(dataDir, "tracked_stocks.json"))

	for {
		now := time.Now()
		session := data.CurrentSession(now)
		processSession(ctx, session, now, nAgent, engine, sAgent, cAgent, agg, rpt, srv, marketAPI, stockTracker, wlMgr)
		time.Sleep(5 * time.Minute)
	}
}

// processSession 根据当前市场时段执行处理流程：
// - 盘前：追回昨日收盘后的新闻
// - 午前：追回上午收盘后的新闻
// - 盘中：追回最近 30 分钟新闻
// - 依次执行：新闻处理 → 策略评估 → D1 评分 → 板块验证 → 战法扫描 → 信号聚合 → 广播通知
func processSession(ctx context.Context, session data.MarketSession, now time.Time,
	nAgent *newsagent.Agent,
	engine *strategy_engine.Engine,
	sAgent *sector_agent.Agent,
	cAgent *combat_agent.Agent,
	agg *display.Aggregator,
	rpt *report.Report,
	srv *server.Server,
	marketAPI *data.MarketAPI,
	stockTracker *data.StockTracker,
	wlMgr *data.WatchlistManager,
) {
	var since time.Time

	switch session {
	case data.SessionPreMarket:
		since = time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
		if now.Weekday() == time.Monday {
			since = since.Add(-72 * time.Hour)
		} else {
			since = since.Add(-24 * time.Hour)
		}
	case data.SessionPreAfternoon:
		since = time.Date(now.Year(), now.Month(), now.Day(), 11, 30, 0, 0, now.Location())
	case data.SessionMorningTrade, data.SessionAfternoonTrade:
		since = now.Add(-30 * time.Minute)
	default:
		return
	}

	log.Printf("[main] Session=%s 追回起始=%s", session, since.Format("01-02 15:04"))

	result, err := nAgent.Process(ctx, since)
	if err != nil {
		log.Printf("[main] NewsAgent失败: %v", err)
		return
	}

	// 收拢持仓+自选作为打分池
	positions := rpt.HeldPositionCodes()
	watchlist := wlMgr.List()

	sr := engine.Evaluate(ctx, result, positions, watchlist)

	// D1Scorer 批量评分（所有打分池个股）
	d1Scorer := combat_agent.NewD1Scorer(nil, "")
	d1Scores := d1Scorer.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)

	// 7a/7b 板块验证
	verifiedBull := sAgent.Verify(sr.HotSectors)
	var verifiedBear []sector_agent.VerifiedSector
	if srv.ShortEnabled() {
		verifiedBear = sAgent.Verify(sr.BearSectors)
	}

	// 8a 板块利好 → 战法
	bullInput := combat_agent.ScanInput{
		Sectors:    verifiedBull,
		L1Score:    sr.L1Score,
		L1Blocked:  sr.L1Blocked,
		MarketData: sr.MarketData,
		D1Scores:   d1Scores,
	}
	bullSignals := cAgent.ScanLong(bullInput)

	// 8b 板块利空 → 战法
	var bearSignals []combat_agent.Signal
	if srv.ShortEnabled() {
		bearInput := combat_agent.ScanInput{
			Sectors:    verifiedBear,
			L1Score:    sr.L1Score,
			L1Blocked:  sr.L1Blocked,
			MarketData: sr.MarketData,
			D1Scores:   d1Scores,
		}
		bearSignals = cAgent.ScanShort(bearInput)
	}

	// 8a/8b 个股直入（跳过 7a/7b）
	td := data.TradingDayDate(now)

	var newLong, newShort []string
	for _, st := range sr.LongStocks {
		newLong = append(newLong, st.Code)
	}
	for _, st := range sr.ShortStocks {
		newShort = append(newShort, st.Code)
	}

	trackedLong := stockTracker.GetActiveByDirection(td, "利好")
	trackedShort := stockTracker.GetActiveByDirection(td, "利空")

	var longCodes []string
	for _, s := range trackedLong {
		longCodes = append(longCodes, s.Code)
	}
	longCodes = append(longCodes, newLong...)

	var shortCodes []string
	for _, s := range trackedShort {
		shortCodes = append(shortCodes, s.Code)
	}
	shortCodes = append(shortCodes, newShort...)

	var individualSignals []combat_agent.Signal
	if len(longCodes) > 0 {
		in := combat_agent.ScanInput{
			IndividualStocks: longCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		sigs := cAgent.ScanLong(in)
		individualSignals = append(individualSignals, sigs...)
	}

	if len(shortCodes) > 0 && srv.ShortEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: shortCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		sigs := cAgent.ScanShort(in)
		individualSignals = append(individualSignals, sigs...)
	}

	expiry := data.AddTradingDays(td, 1)
	for _, sig := range individualSignals {
		dir := "利好"
		if sig.Direction == "做空" {
			dir = "利空"
		}
		stockTracker.Add(sig.Code, sig.Name, dir, sig.Reason, td, expiry)
	}

	allSigCodes := make([]string, len(individualSignals))
	for i, sig := range individualSignals {
		allSigCodes[i] = sig.Code
	}
	stockTracker.OnCycleDone(td, allSigCodes)

	bullSignals = append(bullSignals, individualSignals...)

	alertSignals := cAgent.CheckPositionAlerts(rpt, marketAPI)

	agg.Update(sr, verifiedBull, verifiedBear, bullSignals, bearSignals, alertSignals, rpt)
	if srv.GetSSE().Len() > 0 {
		srv.GetSSE().Broadcast(map[string]string{"type": "scan", "status": "done"})
	}
}

// getDataDir 返回数据存储目录，优先使用环境变量 QUANT_DATA_DIR，默认 ~/.quant-trading-v2。
func getDataDir() string {
	if v := os.Getenv("QUANT_DATA_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2")
}
