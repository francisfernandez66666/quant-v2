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
	"quant-trading-v2/internal/trigger"
)

// main 系统入口：初始化数据目录、认证、行情 API、LLM、新闻代理、策略引擎等所有组件，
// 然后进入主循环，每 5 分钟驱动一次顶层编排引擎（engine.Engine）。
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 数据目录：存放认证、配置、报告、自选等持久化文件
	dataDir := getDataDir()
	os.MkdirAll(dataDir, 0755)

	// 认证管理：初始化用户库，首次启动时创建默认管理员账号 admin / admin123
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

	// 行情客户端：东财行情 API + 同花顺板块出口（板块列表/涨跌幅/主力净流入）
	marketAPI := data.NewMarketAPI()
	thsClient := data.NewTHSClient() // 同花顺出口（板块列表/涨跌幅/主力净流入）

	// 事件匹配器：加载左侧事件规则（config/events_leftside.yaml），失败时禁用事件匹配
	var matcher *data.EventMatcher
	eventsCfg, err := data.LoadEvents("config/events_leftside.yaml")
	if err == nil {
		matcher = data.NewEventMatcher(eventsCfg)
	}

	// 配置管理器：读取数据目录下的 config.json（策略/风控/情绪/LLM 等）
	cfgMgr := config.NewManager(filepath.Join(dataDir, "config.json"))

	// LLM 配置优先级：环境变量 → 认证配置项 → 配置文件 → 默认值
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
	llmCfg.Timeout = time.Duration(cfgMgr.Rules.LLM.TimeoutSec) * time.Second

	// LLM 客户端：未配置 API Key 时降级为纯关键词分析（新闻归因不可用）
	var llmClient *llm.Client
	if llmCfg.APIKey != "" {
		llmClient = llm.New(llmCfg)
	} else {
		log.Println("[LLM] 未配置 API Key，LLM 功能不可用")
	}

	// 股票清洗器：负责股票代码/名称归一化，供新闻归因与板块扫描使用
	cleaner := data.NewStockCleaner(marketAPI)

	// 新闻代理：聚合新闻 + LLM 归因分析，后台常驻运行
	nAgent := newsagent.New(marketAPI, llmClient, cleaner, dataDir)
	nAgent.Start()
	defer nAgent.Stop()

	// 策略引擎：注册四大战法策略（龙头/双响炮/N形/龙回头）
	strategyEngine := strategy_engine.New(marketAPI)
	strategyEngine.SetTHS(thsClient)

	// 板块扫描器 + RPS 强度管理器：板块→个股传播与验证
	scanner := data.NewSectorScanner(marketAPI, matcher)
	strategyEngine.SetScanner(scanner)
	rpsMgr := data.NewRPSManager()
	sAgent := sector_agent.New(scanner, rpsMgr)

	// 组合作战代理：挂载四大战法 runner + Laodeng 评分修正，支持配置热更新
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

	// 报告 / 前端聚合 / 自选股 / 持仓追踪等数据服务
	rpt := report.New(filepath.Join(dataDir, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(dataDir)
	stockTracker := data.NewStockTracker(filepath.Join(dataDir, "tracked_stocks.json"))

	// HTTP 服务：认证/前端/报告/自选股 + SSE 实时推送
	srv := server.New(authMgr, agg, cfgMgr, rpt, marketAPI, wlMgr, thsClient)
	effModel := llmCfg.Model
	if effModel == "" {
		effModel = llm.DefaultModel
	}
	srv.SetRuntimeLLM(llmCfg.APIURL, effModel)
	log.Printf("[LLM] 运行模型: %s @ %s", effModel, llmCfg.APIURL)

	// 顶层编排引擎：绑定新闻/策略/板块/作战/报告等全部模块
	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt, stockTracker, wlMgr, srv.GetSSE(), llmClient, thsClient, dataDir)
	eng.SetScanner(scanner)
	srv.SetEngineController(eng)
	// 前端修改 LLM 配置时热重建客户端，避免重启进程
	srv.SetLLMRecreate(func(apiKey, apiURL, model string, timeoutSec int) {
		lc := llm.New(llm.Config{APIKey: apiKey, APIURL: apiURL, Model: model, Timeout: time.Duration(timeoutSec) * time.Second})
		eng.SetLLMClient(lc)
		log.Printf("[LLM] 客户端已热重建: model=%s url=%s timeout=%ds", model, apiURL, timeoutSec)
	})

	// 5秒实时行情采集器（激活 data.Fetcher：自选+持仓为监控池，供实时触发/快照使用）
	baseStocks := append(wlMgr.List(), rpt.HeldPositionCodes()...)
	dc := data.NewDataCoordinator(marketAPI, thsClient) // 统一行情源：新浪→同花顺→东财 三级降级链
	fetcher := data.NewFetcher(baseStocks, marketAPI, dc)
	go fetcher.Start()
	defer fetcher.Stop()
	srv.SetFetcher(fetcher)   // 报价接口优先读 5s 快照，缺失再降级拉取
	srv.SetCoordinator(dc)    // HTTP 展示层统一走该降级链，保证跨页价格一致
	log.Printf("[main] 实时行情采集已启动: 监控 %d 只(自选+持仓), 5s 轮询", len(baseStocks))

	// 实时触发引擎（daban式放量急拉检测，SSE 推送）
	trigCtx, trigCancel := context.WithCancel(context.Background())
	defer trigCancel()
	triggerEngine := trigger.New(fetcher, srv.GetSSE(), trigger.DefaultConfig())
	go triggerEngine.Run(trigCtx)

	// 近实时 8a/8b 打分循环（5s 节奏，持仓+自选持续打分 + 状态翻转信号）
	eng.SetFetcher(fetcher)
	scoreCtx, scoreCancel := context.WithCancel(context.Background())
	defer scoreCancel()
	go eng.RunScoringLoop(scoreCtx)

	// 情绪周期阈值注入（SSE 广播情绪阶段）
	eng.SetEmotionConfig(&cfgMgr.Rules.Emotion)

	// 启动 HTTP 服务，监听地址可用 QUANT_ADDR 覆盖
	addr := ":8080"
	if v := os.Getenv("QUANT_ADDR"); v != "" {
		addr = v
	}
	// 后台运行 HTTP 服务：阻塞监听 addr，Serve 返回错误时由 log.Fatal 终止进程
	go func() {
		log.Fatal(srv.Serve(addr))
	}()

	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	// 主循环：每 5 分钟按市场时段驱动一次顶层引擎
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
