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
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/store"
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
		u, err := authMgr.CreateUser("admin", "admin123", auth.RoleAdmin, nil, 0)
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
	// 可选分类专用模型：新闻归因 Stage0/1 等快速分类/初筛用它，主模型留给 D1/Stage2 深度分析。
	llmCfg.ClassifierModel = cfgMgr.Rules.LLM.ClassifierModel
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

	// 模拟盘（纸面交易）：独立于真实持仓的虚拟撮合/净值/信号质量统计。
	// 开启后引擎按实时快照价自动撮合 buy 信号；config.json 的 rules.paper 控制开关与参数。
	// English: paper trading — virtual fills/net-value/signal-quality stats isolated from the real book.
	// When enabled, the engine auto-fills buy signals at the live snapshot price; rules.paper in
	// config.json controls the switch and parameters.
	paperCfg := cfgMgr.Rules.Paper
	paperEngine := paper.New(paper.Config{
		Enabled:        paperCfg.Enabled,
		FixedAmount:    paperCfg.FixedAmount,
		MaxPositions:   paperCfg.MaxPositions,
		InitialCapital: paperCfg.InitialCapital,
	}, filepath.Join(dataDir, "paper.json"))
	srv.SetPaper(paperEngine)
	if paperEngine.Enabled() {
		log.Printf("[paper] 模拟盘已启用: 每票%.0f元 上限%d仓 初始%.0f元",
			paperEngine.Cfg().FixedAmount, paperEngine.Cfg().MaxPositions, paperEngine.Cfg().InitialCapital)
	} else {
		log.Printf("[paper] 模拟盘未启用（rules.paper.enabled=false）")
	}

	// B5 研究闭环：研究库与实时库同目录，web 审批端点读写候选、应用权重。
	// 同时把研究库作为实盘财务因子数据源（SetFinaLookup：把最新财务指标注入 StockMarketData，
	// 供实盘因子战法对 ROE/净利同比等财务类因子打分）。
	// English: the research DB (same dir as live) backs the B5 approval endpoints and also serves as the
	// live financial source (SetFinaLookup injects latest financials into StockMarketData so the live
	// factor strategy can score financial factors like ROE/YoyNetProfit).
	if researchDB, err := store.Open(filepath.Join(dataDir, "trading.db")); err != nil {
		log.Printf("[research] 研究库接入失败: %v", err)
	} else {
		srv.SetResearch(researchDB, dataDir)
		// 实盘财务因子查询：取研究库 fina_indicator 最新报告期（点对时）作为该股财务指标。
		// 带进程内 TTL 缓存，避免 5s 打分循环反复查库；缓存缺失/过期时读库。
		finaCache := newFinaCache(researchDB)
		strategyEngine.SetFinaLookup(finaCache.Lookup)
		log.Printf("[research] 研究库已接入（含实盘财务因子）: %s", filepath.Join(dataDir, "trading.db"))
	}

	// 推送器：P1 清仓/止损强提醒走桌面 + Webhook（地址从 config.json notify.webhook_urls 读取，可热改）
	notifier := notify.New()
	notifier.SetWebhooks(cfgMgr.GetNotifyConfig().WebhookURLs)
	// 外部推送网关：config.json notify.push 启用时，把关键提醒转发到推送服务，
	// 实现 APK 后台/离线的系统通知触达。provider=jpush 走极光 REST API（AppKey+Secret+Alias），
	// 否则走通用 webhook 网关（URL 指向接收 JSON 的推送地址）。
	if pushCfg := cfgMgr.GetNotifyConfig().Push; pushCfg.Enabled {
		if pushCfg.Provider == "jpush" {
			gw := notify.NewJPushGateway(pushCfg.AppKey, pushCfg.Secret, pushCfg.Alias)
			notifier.SetGateway(gw)
			log.Printf("[main] 外部推送网关已启用: 极光(alias=%s)", gw.Alias)
		} else if pushCfg.URL != "" {
			gw := notify.NewWebhookGateway(pushCfg.URL)
			notifier.SetGateway(gw)
			log.Printf("[main] 外部推送网关已启用: webhook(%s)", pushCfg.URL)
		}
	}

	// 5秒实时行情采集器（激活 data.Fetcher：自选+持仓为监控池，供实时触发/快照使用）
	baseStocks := append(wlMgr.All(), rpt.HeldPositionCodes()...)
	dc := data.NewDataCoordinator(marketAPI, thsClient) // 统一行情源：新浪→同花顺→东财 三级降级链
	fetcher := data.NewFetcher(baseStocks, marketAPI, dc)
	go fetcher.Start()
	defer fetcher.Stop()
	srv.SetFetcher(fetcher) // 报价接口优先读 5s 快照，缺失再降级拉取
	srv.SetCoordinator(dc)  // HTTP 展示层统一走该降级链，保证跨页价格一致
	log.Printf("[main] 实时行情采集已启动: 监控 %d 只(自选+持仓), 5s 轮询", len(baseStocks))

	// 板块→个股成分股覆盖数（默认20）：扩大同板块强势股进打分池，避免只覆盖龙头前10漏选
	// English: per-sector constituent coverage (default 20) — widen same-sector leaders into the pool
	sectorTopN := cfgMgr.Rules.MainSector.SectorConstituentTopN
	if sectorTopN <= 0 {
		sectorTopN = 20
	}
	sAgent.SetConstituentTopN(sectorTopN)

	// 多账号独立引擎注册表：数据源全局共享一份，每个账号登录时懒加载自己的引擎实例。
	// 同一账号任何设备读取同一份后端计算结果（信号/评分/做多做空开关/战法参数均按账号隔离）。
	// English: multi-account engine registry — data sources are shared; each account lazily gets its
	// own engine on login. The same account reads the same backend-computed results on any device
	// (signals/scores/long-short toggles/strategy params are all isolated per account).
	registry := engine.NewRegistry(engine.EngineOptions{
		MarketAPI:    marketAPI,
		NewsAgent:    nAgent,
		StrategyEng:  strategyEngine,
		SectorAgent:  sAgent,
		Scanner:      scanner,
		Matcher:      matcher,
		Rpt:          rpt,
		StockTracker: stockTracker,
		WlMgr:        wlMgr,
		SSE:          srv.GetSSE(),
		LLMClient:    llmClient,
		THS:          thsClient,
		Fetcher:      fetcher,
		CfgMgr:       cfgMgr,
		DataDir:      dataDir,
		Notifier:     notifier,
		SectorTopN:   sectorTopN,
		Paper:        paperEngine,
		D1MaxRetries: cfgMgr.Rules.LLM.MaxRetryTimes,
	})
	srv.SetEngineRegistry(registry)

	// 前端修改 LLM 配置时热重建客户端，避免重启进程（对所有已创建账号引擎生效）
	srv.SetLLMRecreate(func(apiKeys []string, apiURL, model string, timeoutSec int, streaming bool, batchConcurrency int, classifierModel string) {
		lc := llm.New(llm.Config{
			APIKeys:          apiKeys,
			APIURL:           apiURL,
			Model:            model,
			Timeout:          time.Duration(timeoutSec) * time.Second,
			Streaming:        streaming,
			BatchConcurrency: batchConcurrency,
			ClassifierModel:  classifierModel,
		})
		for _, e := range registry.All() {
			e.SetLLMClient(lc)
		}
	})

	// 实时触发引擎（daban式放量急拉检测，SSE 推送）
	trigCtx, trigCancel := context.WithCancel(context.Background())
	defer trigCancel()
	triggerEngine := trigger.New(fetcher, srv.GetSSE(), trigger.DefaultConfig())
	go triggerEngine.Run(trigCtx)

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
		os.Exit(0)
	}()

	ctx := context.Background()
	log.Println("quant-trading-v2 已启动")

	// 近实时 8a/8b 打分循环：5s 节奏，驱动所有已创建的账号引擎（共享引擎去重）。
	// 各账号引擎内部按各自配置打分，持仓+自选持续打分 + 状态翻转信号。
	// English: near-realtime 8a/8b scoring loop at a 5s cadence, driving every created account
	// engine (shared engines deduplicated). Each engine scores by its own config over its pool.
	scoreLoopCtx, scoreLoopCancel := context.WithCancel(ctx)
	defer scoreLoopCancel()
	go func() {
		<-scoreLoopCtx.Done()
	}()
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-scoreLoopCtx.Done():
				return
			case <-tick.C:
				for _, e := range registry.All() {
					e.RunScoringLoopOnce(scoreLoopCtx)
				}
				// 盘后内存释放：非活跃时段按节流间隔把常驻堆归还 OS，
				// 让物理内存让给盘后 research 夜间作业（避免叠加 OOM）。
				// English: after-hours memory trim — release the resident heap back to the OS on a
				// throttled cadence so the nightly research job has the RAM it needs (no stacking OOM).
				for _, e := range registry.All() {
					e.TrimAfterHoursIfDue(time.Now())
				}
			}
		}
	}()

	// 主循环：按市场时段驱动所有账号引擎。
	// 盘前（8:30-9:15）"跑完即排下一轮"：等待异步引擎完成后立即触发下一轮，最大化新闻归因轮次，
	// 让昨夜晚间新闻在开盘前尽可能完成 LLM 归因（配合未归因队列失败重试）；
	// 其他时段按 5 分钟节奏推进，asyncBusy 忙锁防并发重入。
	for {
		now := time.Now()
		session := data.CurrentSession(now)
		since, ok := sinceForSession(session, now)
		if ok {
			log.Printf("[main] Session=%s 追回起始=%s", session, since.Format("01-02 15:04"))
			engines := registry.All()
			if len(engines) == 0 {
				// 尚无账号登录/懒加载引擎，跳过本轮（等服务有账号时再驱动）
				time.Sleep(5 * time.Minute)
				continue
			}
			if session == data.SessionPreMarket {
				// 盘前：新闻流水线含 LLM，异步触发避免阻塞近实时打分；等待完成后立即排下一轮，
				// 用 AsyncIdle 轮询替代固定 5min 间隔，保证 9:15 前尽可能多轮归因。
				for _, e := range engines {
					if e.TryAsyncRun(ctx, since) {
						log.Printf("[main] 盘前异步引擎已触发 (账号 %s)", e.UserID())
					} else {
						log.Printf("[main] 盘前异步引擎仍在运行 (账号 %s)", e.UserID())
					}
				}
				// 等待所有账号引擎异步完成（asyncBusy 清零）再立即排下一轮
				for {
					allIdle := true
					for _, e := range engines {
						if !e.AsyncIdle() {
							allIdle = false
							break
						}
					}
					if allIdle {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
				continue // 立即下一轮，不 sleep 5min
			} else {
				// 午前/盘中：异步触发，避免 LLM 重试阻塞主循环/近实时打分；
				// asyncBusy 忙锁防止并发重入（上一轮未完成时本轮跳过）。
				// 触发后用"新新闻到达或超时兜底"的自适应等待替代原固定 5min 心跳：
				// 一旦探测到新新闻（或距上次触发满 maxIdleWait 兜底）且引擎空闲，立即排下一轮，
				// 把"新闻出现→开始扫描"的延迟从分钟级压到探测周期内（默认 30s）。
				// English: trade sessions run asynchronously so LLM retries never block the main loop or
				// near-realtime scoring; the asyncBusy guard skips overlap. After triggering, an adaptive wait
				// replaces the old fixed 5-min heartbeat: as soon as new news is probed (or the maxIdleWait
				// backstop elapses) with engines idle, the next round starts at once, cutting news→scan
				// latency down to the probe period (default 30s).
				for _, e := range engines {
					if e.TryAsyncRun(ctx, since) {
						log.Printf("[main] 盘中异步引擎已触发 (账号 %s)", e.UserID())
					} else {
						log.Printf("[main] 异步引擎仍在运行, 跳过本轮 (账号 %s)", e.UserID())
					}
				}
				adaptiveIntradayWait(ctx, engines)
				continue // 由自适应等待决定何时排下一轮（不再固定 sleep 5min）
			}
		} else {
			log.Printf("[main] Session=%s 非处理时段, 跳过本轮", session)
		}
		time.Sleep(5 * time.Minute)
	}
}

// adaptiveIntradayWait 盘中自适应等待：以短周期探测各引擎是否空闲 + 是否有新新闻到达，
// 满足"全部空闲 且（有新新闻 或 距上次触发已超最大空闲间隔）"即返回让主循环立即排下一轮。
// 相比原固定 5min 心跳，"新闻到达→触发扫描"的延迟压缩到探测周期内（默认 30s）；
// 无新闻时靠 maxIdleWait 兜底周期刷新（行情/自选持仓打分仍需周期性更新），避免盘中长时间静默。
// （adaptiveIntradayWait waits adaptively during trade sessions: on a short cadence it checks whether all
// engines are idle and whether new news has arrived, and returns once all idle AND either new news arrived
// or the max idle interval elapsed, so the main loop starts the next round at once. vs the old fixed 5-min
// heartbeat, news→scan latency drops to ~the probe period (default 30s); quiet periods are covered by the
// maxIdleWait backstop so quote/watchlist-only refreshes still happen periodically.）
func adaptiveIntradayWait(ctx context.Context, engines []*engine.Engine) {
	const (
		probeInterval = 30 * time.Second // 新新闻探测周期：决定"新闻到达→触发"的最大感知延迟
		maxIdleWait   = 3 * time.Minute  // 无新新闻时的兜底触发间隔
	)
	lastTrigger := time.Now()
	probe := time.NewTicker(probeInterval)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-probe.C:
			// 上一轮异步尚未结束时不排下一轮（asyncBusy 忙锁防并发重入）
			if !allEnginesIdle(engines) {
				continue
			}
			// 有新新闻到达 → 立即触发下一轮扫描
			if anyNewNews(engines) {
				log.Printf("[main] 盘中探测到新新闻, 立即排下一轮")
				return
			}
			// 兜底：距上次触发已超最大空闲间隔，即使无新新闻也刷新一轮
			// （行情/自选持仓打分需要周期性更新，防止盘中长时间静默）
			if time.Since(lastTrigger) >= maxIdleWait {
				log.Printf("[main] 盘中无新新闻已达 %v, 超时兜底排下一轮", maxIdleWait)
				return
			}
		}
	}
}

// allEnginesIdle 报告全部引擎异步是否空闲（上轮异步 run 均已完成）。
func allEnginesIdle(engines []*engine.Engine) bool {
	for _, e := range engines {
		if !e.AsyncIdle() {
			return false
		}
	}
	return true
}

// anyNewNews 探测任一引擎的新闻源是否有新新闻到达（命中即触发下一轮扫描）。
func anyNewNews(engines []*engine.Engine) bool {
	for _, e := range engines {
		if e.HasNewNews() {
			return true
		}
	}
	return false
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
