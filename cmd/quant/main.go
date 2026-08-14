// Package main 量化交易系统入口：初始化所有模块（认证、行情、策略、板块、新闻、风控），
// 按市场时段循环驱动顶层编排引擎。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
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

	// 时区加固：全部交易时段判断基于 time.Local（如 CurrentSession / 主循环 sinceForSession），
	// 服务器若在海外（如首尔 KST=UTC+9）或系统默认 UTC，会导致开盘/收盘/盘前窗口整体偏移。
	// 统一强制 Asia/Shanghai（北京时间，A 股交易时区）；仅当外部显式设置 TZ 环境变量时遵循外部值。
	// systemd 侧同时设置 TZ=Asia/Shanghai 双保险。
	// English: force Asia/Shanghai as process timezone so trading-session windows (which read time.Local)
	// align with A-share trading hours even on overseas hosts (e.g. Seoul KST) or UTC-default Ubuntu.
	// An explicit external TZ env var overrides this default.
	if os.Getenv("TZ") == "" {
		os.Setenv("TZ", "Asia/Shanghai")
		if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
			time.Local = loc
		}
		log.Printf("[main] 进程时区已固定为 Asia/Shanghai (北京时间), 当前 %s", time.Now().Format("2006-01-02 15:04:05 -07:00"))
	}

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
	llmCfg.Streaming = cfgMgr.Rules.LLM.StreamingEnabled()
	llmCfg.BatchConcurrency = cfgMgr.Rules.LLM.BatchConcurrency
	// 多 API 密钥：认证配置优先（逗号分隔），否则回退单 key
	if v, ok := authMgr.GetConfig("", "llm_api_keys"); ok && v != "" {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				llmCfg.APIKeys = append(llmCfg.APIKeys, k)
			}
		}
	}
	if len(llmCfg.APIKeys) == 0 && llmCfg.APIKey != "" {
		llmCfg.APIKeys = []string{llmCfg.APIKey}
	}

	// LLM 客户端：未配置 API Key 时降级为纯关键词分析（新闻归因不可用）
	var llmClient *llm.Client
	if len(llmCfg.APIKeys) > 0 {
		llmClient = llm.New(llmCfg)
	} else {
		log.Println("[LLM] 未配置 API Key，LLM 功能不可用")
	}

	// 启动前 LLM 通道预检：尽早暴露 key 失效/断网问题，避免盘前才被发现
	if llmClient != nil {
		if err := llmClient.Ping(); err != nil {
			log.Printf("[LLM] 启动预检失败(将降级运行): %v", err)
		} else {
			log.Printf("[LLM] 启动预检通过")
		}
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
	scanner.SetSectorSource(thsClient) // 板块成分股：同花顺优先，东财兜底
	strategyEngine.SetScanner(scanner)
	rpsMgr := data.NewRPSManager()
	sAgent := sector_agent.New(scanner, rpsMgr)

	// 组合作战代理：挂载四大战法 runner + Laodeng 评分修正，支持配置热更新
	stratCfg := cfgMgr.GetStrategyConfig()
	laodengCfg := &cfgMgr.Rules.Laodeng
	cAgent := combat_agent.New(stratCfg)
	cAgent.SetLaodengConfig(laodengCfg)
	cAgent.SetPositionDailyDropPct(cfgMgr.Rules.Position.DailyDropAlertPct)
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
	// 推送器：P1 清仓/止损强提醒走桌面 + Webhook（地址从 config.json notify.webhook_urls 读取，可热改）
	notifier := notify.New()
	notifier.SetWebhooks(cfgMgr.GetNotifyConfig().WebhookURLs)
	eng.SetNotifier(notifier)
	srv.SetEngineController(eng)
	// 前端修改 LLM 配置时热重建客户端，避免重启进程
srv.SetLLMRecreate(func(apiKeys []string, apiURL, model string, timeoutSec int, streaming bool, batchConcurrency int) {
		lc := llm.New(llm.Config{
			APIKeys:          apiKeys,
			APIURL:           apiURL,
			Model:            model,
			Timeout:          time.Duration(timeoutSec) * time.Second,
			Streaming:        streaming,
			BatchConcurrency: batchConcurrency,
		})
		eng.SetLLMClient(lc)
	})

	// 5秒实时行情采集器（激活 data.Fetcher：自选+持仓为监控池，供实时触发/快照使用）
	baseStocks := append(wlMgr.All(), rpt.HeldPositionCodes()...)
	dc := data.NewDataCoordinator(marketAPI, thsClient) // 统一行情源：新浪→同花顺→东财 三级降级链
	fetcher := data.NewFetcher(baseStocks, marketAPI, dc)
	go fetcher.Start()
	defer fetcher.Stop()
	srv.SetFetcher(fetcher) // 报价接口优先读 5s 快照，缺失再降级拉取
	srv.SetCoordinator(dc)  // HTTP 展示层统一走该降级链，保证跨页价格一致
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

	// 板块→个股成分股覆盖数（默认20）：扩大同板块强势股进打分池，避免只覆盖龙头前10漏选
	// English: per-sector constituent coverage (default 20) — widen same-sector leaders into the pool
	sectorTopN := cfgMgr.Rules.MainSector.SectorConstituentTopN
	if sectorTopN <= 0 {
		sectorTopN = 20
	}
	eng.SetSectorConstituentTopN(sectorTopN)
	sAgent.SetConstituentTopN(sectorTopN)

	// D1 评分 LLM 轮询重试次数（防重要信号随 LLM 偶发失败丢失）
	eng.SetD1MaxRetries(cfgMgr.Rules.LLM.MaxRetryTimes)

	// 启动 HTTP 服务：地址可用 QUANT_ADDR 覆盖。
	// 端口占用自动顺延：绑定失败时依次尝试下一个端口（最多 20 个），
	// 避免"bind: address already in use"直接把整个进程打崩（stale 进程占端口时的常见故障）。
	// English: start the HTTP server (address overridable via QUANT_ADDR). When the port is already
	// taken, roll over to the next port (up to 20) so a stale process holding the port cannot crash
	// the whole app with "bind: address already in use".
	addr := ":8080"
	if v := os.Getenv("QUANT_ADDR"); v != "" {
		addr = v
	}
	ln := pickListener(addr, 20)
	if ln == nil {
		log.Fatalf("HTTP 监听失败 %s: 连续 20 个端口均被占用", addr)
	}
	bound := ln.Addr().String()
	log.Printf("[main] HTTP 服务已绑定 %s (来源 %s)", bound, addr)

	// HTTP 服务加固：设置读写超时/头部超时/空闲超时，防慢速攻击与连接悬挂；
	// 不设 WriteTimeout 上限过大（SSE 长连接需长期保持），仅约束头部与空闲期。
	// English: hardened HTTP server with header/read/idle timeouts (slow-loris protection);
	// WriteTimeout is left generous because SSE keeps long-lived connections open.
	hs := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[main] HTTP 服务异常退出: %v", err)
		}
	}()

	// 优雅停机：收到 SIGTERM/SIGINT（systemd stop / Ctrl-C）时先关闭 HTTP、
	// 停掉所有后台循环，再做最终落盘，避免写一半的 JSON 损坏。
	// English: graceful shutdown on SIGTERM/SIGINT — close HTTP first, stop background
	// loops, then let deferred writers flush, avoiding half-written JSON files.
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		log.Println("[main] 收到退出信号，正在优雅停机…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
		trigCancel()
		scoreCancel()
		os.Exit(0)
	}()

	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	// 主循环：按市场时段驱动顶层引擎。
	// 盘前（8:30-9:15）"跑完即排下一轮"：等待异步引擎完成后立即触发下一轮，最大化新闻归因轮次，
	// 让昨夜晚间新闻在开盘前尽可能完成 LLM 归因（配合未归因队列失败重试）；
	// 其他时段按 5 分钟节奏推进，asyncBusy 忙锁防并发重入。
	for {
		now := time.Now()
		session := data.CurrentSession(now)
		since, ok := sinceForSession(session, now)
		if ok {
			log.Printf("[main] Session=%s 追回起始=%s", session, since.Format("01-02 15:04"))
			if session == data.SessionPreMarket {
				// 盘前：新闻流水线含 LLM，异步触发避免阻塞近实时打分；等待完成后立即排下一轮，
				// 用 AsyncIdle 轮询替代固定 5min 间隔，保证 9:15 前尽可能多轮归因。
				if eng.TryAsyncRun(ctx, since) {
					log.Printf("[main] 盘前异步引擎已触发, 等待完成后续轮")
				} else {
					log.Printf("[main] 盘前异步引擎仍在运行, 等待其完成")
				}
				// 等待本轮异步引擎完成（asyncBusy 清零）再立即排下一轮
				for !eng.AsyncIdle() {
					time.Sleep(500 * time.Millisecond)
				}
				continue // 立即下一轮，不 sleep 5min
			} else {
				// 午前/盘中：异步触发，避免 LLM 重试阻塞主循环/近实时打分，轮次仍按 5min 节奏推进；
				// asyncBusy 忙锁防止并发重入（上一轮未完成时本轮跳过）。
				// English: trade sessions also run asynchronously so LLM retries never block the main loop or
				// near-realtime scoring; the asyncBusy guard skips overlap, and rounds keep the 5-min cadence.
				if eng.TryAsyncRun(ctx, since) {
					log.Printf("[main] 盘中异步引擎已触发")
				} else {
					log.Printf("[main] 异步引擎仍在运行, 跳过本轮")
				}
			}
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

// pickListener 尝试监听 baseAddr；若端口被占用则自动顺延到下一个端口（最多 maxTries 次），
// 返回成功绑定的监听器；均失败返回 nil。
// English: tries to listen on baseAddr; when the port is taken it rolls over to the next port
// (up to maxTries times) and returns the first successfully bound listener, or nil if all fail.
func pickListener(baseAddr string, maxTries int) net.Listener {
	addr := baseAddr
	for i := 0; i < maxTries; i++ {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln
		}
		addr = bumpPort(addr)
	}
	return nil
}

// bumpPort 将 host:port 地址中的端口号 +1（如 :8080 -> :8081）；解析失败时原样返回。
// English: increments the port number of a host:port address (e.g. :8080 -> :8081);
// returns the address unchanged when it cannot be parsed.
func bumpPort(addr string) string {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort(host, strconv.Itoa(p+1))
}
