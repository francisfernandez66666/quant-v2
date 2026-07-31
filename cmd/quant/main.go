// Package main 量化交易系统入口：初始化所有模块（认证、行情、策略、板块、新闻、风控），
// 按市场时段循环驱动顶层编排引擎。
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

// main 系统入口：初始化数据目录、认证、行情 API、LLM、新闻代理、策略引擎等所有组件，
// 然后进入主循环，每 5 分钟驱动一次顶层编排引擎（engine.Engine）。
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
	thsClient := data.NewTHSClient() // 同花顺出口（板块列表/涨跌幅/主力净流入）

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

	strategyEngine := strategy_engine.New(marketAPI)

	scanner := data.NewSectorScanner(marketAPI, matcher)
	strategyEngine.SetScanner(scanner)
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
	stockTracker := data.NewStockTracker(filepath.Join(dataDir, "tracked_stocks.json"))

	srv := server.New(authMgr, agg, cfgMgr, rpt, marketAPI, wlMgr, thsClient)
	effModel := llmCfg.Model
	if effModel == "" {
		effModel = llm.DefaultModel
	}
	srv.SetRuntimeLLM(llmCfg.APIURL, effModel)
	log.Printf("[LLM] 运行模型: %s @ %s", effModel, llmCfg.APIURL)

	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt, stockTracker, wlMgr, srv.GetSSE(), llmClient, thsClient, dataDir)
	eng.SetScanner(scanner)
	srv.SetEngineController(eng)
	srv.SetLLMRecreate(func(apiKey, apiURL, model string) {
		lc := llm.New(llm.Config{APIKey: apiKey, APIURL: apiURL, Model: model})
		eng.SetLLMClient(lc)
		log.Printf("[LLM] 客户端已热重建: model=%s url=%s", model, apiURL)
	})

	// 5秒实时行情采集器（激活 data.Fetcher：自选+持仓为监控池，供实时触发/快照使用）
	baseStocks := append(wlMgr.List(), rpt.HeldPositionCodes()...)
	fetcher := data.NewFetcher(baseStocks, data.NewDataCoordinator(marketAPI, thsClient))
	go fetcher.Start()
	defer fetcher.Stop()
	log.Printf("[main] 实时行情采集已启动: 监控 %d 只(自选+持仓), 5s 轮询", len(baseStocks))

	addr := ":8080"
	if v := os.Getenv("QUANT_ADDR"); v != "" {
		addr = v
	}
	go func() {
		log.Fatal(srv.Serve(addr))
	}()

	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	for {
		now := time.Now()
		session := data.CurrentSession(now)
		since, ok := sinceForSession(session, now)
		if ok {
			log.Printf("[main] Session=%s 追回起始=%s", session, since.Format("01-02 15:04"))
			eng.Run(ctx, since)
		} else {
			log.Printf("[main] Session=%s 非处理时段, 跳过本轮", session)
		}
		time.Sleep(5 * time.Minute)
	}
}

// sinceForSession 根据市场时段计算新闻追回起始时间：
// - 盘前：追回昨日收盘后的新闻（周一追回周五收盘后）
// - 午前：追回上午收盘后的新闻
// - 盘中：追回最近 30 分钟新闻
// ok=false 表示当前时段不处理（收盘后/夜间）。
func sinceForSession(session data.MarketSession, now time.Time) (time.Time, bool) {
	switch session {
	case data.SessionPreMarket:
		since := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
		if now.Weekday() == time.Monday {
			since = since.Add(-72 * time.Hour)
		} else {
			since = since.Add(-24 * time.Hour)
		}
		return since, true
	case data.SessionPreAfternoon:
		return time.Date(now.Year(), now.Month(), now.Day(), 11, 30, 0, 0, now.Location()), true
	case data.SessionMorningTrade, data.SessionAfternoonTrade:
		return now.Add(-30 * time.Minute), true
	default:
		return time.Time{}, false
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
