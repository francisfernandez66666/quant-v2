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
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategy_engine"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	dataDir := getDataDir()
	os.MkdirAll(dataDir, 0755)

	// ── 认证 ──
	authMgr := auth.NewManager(dataDir)
	if err := authMgr.Init(); err != nil {
		log.Fatalf("auth init: %v", err)
	}
	if !authMgr.IsInitialized() {
		log.Println("首次启动, 请访问 /setup 完成配置")
	}

	// ── 数据层 ──
	marketAPI := data.NewMarketAPI()
	_ = data.NewTHSClient()
	tushareToken := data.TushareToken()
	if tushareToken != "" {
		data.NewTushareClient(tushareToken)
	}

	// ── 事件匹配 ──
	var matcher *data.EventMatcher
	eventsCfg, err := data.LoadEvents("config/events_leftside.yaml")
	if err == nil {
		matcher = data.NewEventMatcher(eventsCfg)
	}

	// ── LLM ──
	llmKey := os.Getenv("LLM_API_KEY")
	var llmClient *llm.Client
	if llmKey != "" {
		llmClient = llm.New(llmKey, "")
	} else {
		log.Println("[LLM] 未配置 API Key，LLM 功能不可用")
	}

	// ── NewsAgent ──
	nAgent := newsagent.New(marketAPI, llmClient, dataDir)
	nAgent.Start()
	defer nAgent.Stop()

	// ── StrategyEngine ──
	engine := strategy_engine.New(marketAPI, llmClient)

	// ── SectorScanner ──
	scanner := data.NewSectorScanner(marketAPI, matcher)
	engine.SetScanner(scanner)
	rpsMgr := data.NewRPSManager()
	sAgent := sector_agent.New(scanner, rpsMgr)

	// ── CombatAgent ──
	cfg := &config.StrategyConfig{} // TODO: load from file
	cAgent := combat_agent.New(cfg)
	cAgent.SetRunners([]combat_agent.StrategyRunner{})
	cAgent.StartHotReload(filepath.Join("config", "strategies.yaml"))

	// ── Display ──
	agg := display.New()

	// ── HTTP Server ──
	srv := server.New(authMgr, agg)
	addr := ":8080"
	if v := os.Getenv("QUANT_ADDR"); v != "" {
		addr = v
	}
	go func() {
		log.Fatal(srv.Serve(addr))
	}()

	// ── 主循环 ──
	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	for {
		now := time.Now()
		session := data.CurrentSession(now)
		processSession(ctx, session, now, nAgent, engine, sAgent, cAgent, agg)
		time.Sleep(5 * time.Minute)
	}
}

func processSession(ctx context.Context, session data.MarketSession, now time.Time,
	nAgent *newsagent.Agent,
	engine *strategy_engine.Engine,
	sAgent *sector_agent.Agent,
	cAgent *combat_agent.Agent,
	agg *display.Aggregator,
) {
	var since time.Time

	switch session {
	case data.SessionPreMarket:
		// 盘前: 追回到上个交易日15:00
		since = time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
		if now.Weekday() == time.Monday {
			since = since.Add(-72 * time.Hour) // 回溯到周五
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

	// 1. NewsAgent: 新闻追回+LLM
	result, err := nAgent.Process(ctx, since)
	if err != nil {
		log.Printf("[main] NewsAgent失败: %v", err)
		return
	}

	// 2. StrategyEngine: 归因+产业链+评分
	sr := engine.Evaluate(ctx, result)

	// 3. SectorAgent: 板块验证
	verified := sAgent.Verify(nil) // TODO: 传入 HotSectors

	// 4. CombatAgent: 战法扫描
	input := combat_agent.ScanInput{
		Sectors:   verified,
		L1Score:   sr.L1Score,
		L1Blocked: sr.L1Blocked,
	}
	signals := cAgent.Scan(input)

	// 5. Display: 聚合展示
	agg.Update(sr, verified, signals)
}

func getDataDir() string {
	if v := os.Getenv("QUANT_DATA_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2")
}
