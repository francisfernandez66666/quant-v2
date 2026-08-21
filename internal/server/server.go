// Package server HTTP 服务端：提供看板数据、策略配置、持仓管理、做空开关等 REST API。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/trading"
)

// EngineController 引擎对外暴露的控制面：利好/利空开关 + 流水线调试数据 + 消息中心 + 热点记录。
// 由顶层编排引擎实现，server 不直接依赖 engine 包（避免导入环）。
type EngineController interface {
	LongEnabled() bool
	SetLongEnabled(v bool)
	ShortEnabled() bool
	SetShortEnabled(v bool)
	GetDebugInfo() *newsagent.DebugInfo
	GetStageRecords() []newsagent.DebugInfo
	GetSignalLogs() []combat_agent.SignalLog
	GetHotRecords() []data.HotRecord
	GetAllNewsEvents() []newsagent.NewsEvent
	SetNewsShowAll(v bool)
	NewsShowAll() bool
	ReanalyzeNews() (map[string]int, error)
	TestAttribution(title, digest string) ([]newsagent.NewsEvent, error)
	GetMessages() []data.MessageItem
	ClearMessages()
	DeleteMessage(id string)
	RefreshMessageName(code, name string)
	ConsultLLM(userID, userMsg string, proMode bool) (string, error)
	GetConsultHistory() []data.ConsultMessage
	ClearConsultHistory()
	// DashboardData 返回该账号/引擎的当前看板快照（信号/评分/新闻事件/开关状态等）。
	// English: returns the current dashboard snapshot for this account/engine (signals/scores/news/toggles).
	DashboardData() *display.DashboardData
	// 战法库（因子战法）：热重载 / 运行统计 / 前向收益记录（效果监测）。
	// English: factor-strategy library: hot-reload / run stats / forward-return recording (monitoring).
	ReloadFactorRules(dataDir string)
	FactorStats() []factorstrat.ActiveRule
	RecordFactorForwardReturn(ruleID string, ret float64)
	// 战法库（形态战法）：热重载 / 运行统计 / 前向收益记录（效果监测）。
	// English: pattern-strategy library: hot-reload / run stats / forward-return recording (monitoring).
	ReloadPatternRules(dataDir string)
	PatternStats() []patternstrat.ActivePattern
	RecordPatternForwardReturn(ruleID string, ret float64)
	// QMTController 返回实盘交易执行控制器（AUTO_TRADING_PLAN M1；可为 nil = 未接入）。
	// 供 HTTP 层读取熔断/配置、触发下单与回报落库。
	// English: returns the live-trading controller (AUTO_TRADING_PLAN M1; may be nil when not wired).
	// Lets the HTTP layer read the breaker/config, place orders and persist gateway reports.
	QMTController() *trading.Controller
}

// Server HTTP 服务端，聚合所有依赖组件并注册 REST/SSE 路由。
type Server struct {
	auth        *auth.Manager                                                                                                              // 认证管理器：注册/登录/临时账号/token 校验
	agg         *display.Aggregator                                                                                                        // 看板数据聚合器（读取实时看板快照）
	cfg         *config.Manager                                                                                                            // 配置管理器（策略/D1/LLM 参数）
	rpt         *report.Report                                                                                                             // 交易持仓报告（开仓/平仓/统计）
	mux         *http.ServeMux                                                                                                             // 路由注册表
	market      *data.MarketAPI                                                                                                            // 行情数据 API（实时报价/板块/IPO 等）
	ths         *data.THSClient                                                                                                            // 同花顺客户端（板块行情表）
	fetcher     *data.Fetcher                                                                                                              // 5s 实时行情采集器（报价优先读其快照，缺失再降级拉取）
	dc          *data.DataCoordinator                                                                                                      // 行情统一数据源（新浪→同花顺→东财 三级降级链）
	paper       *paper.Engine                                                                                                              // 模拟盘引擎（独立纸面交易，nil=未启用）
	watchlist   *data.WatchlistManager                                                                                                     // 自选股管理器
	sse         *SSEBroker                                                                                                                 // SSE 事件广播器（向前端实时推送）
	startTime   time.Time                                                                                                                  // 服务启动时间（用于 uptime 统计）
	llmRecreate func(apiKeys []string, apiURL, model string, timeoutSec int, streaming bool, batchConcurrency int, classifierModel string) // 热重建 LLM 客户端
	ctrl        EngineController                                                                                                           // 引擎控制面（做多/做空开关、流水线调试数据等）

	researchDB  *store.DB // B5 研究候选库（optimize 产出入库；web 审批读写）
	researchDir string    // B5 应用目录（applied_rules.json 落盘处）

	llmMu      sync.Mutex // 保护 runtimeLLM/runtimeURL 的互斥锁
	runtimeLLM string     // 运行时实际使用的 model（与文件配置可能不同）
	runtimeURL string     // 运行时实际使用的 API 地址

	calMu         sync.Mutex // 保护日历缓存的互斥锁
	macroCache    []data.MacroEvent
	macroCacheDay string // 宏观日历缓存对应的日期（用于按天失效）
	ipoCache      []data.IPOEvent
	ipoCacheDay   string // IPO 日历缓存对应的日期（用于按天失效）

	thsMu       sync.Mutex        // 保护同花顺板块兜底缓存的互斥锁
	thsBoards   []data.SectorInfo // 同花顺 top 板块兜底缓存（LLM 无归因时使用）
	thsBoardsAt time.Time         // 兜底缓存最近刷新时间（每分钟轮动一次）

	newsMu      sync.Mutex        // 保护 news 响应缓存的互斥锁
	newsCache   map[string][]byte // news 接口 TTL 缓存（key: "all"/""，value: JSON 响应）
	newsCacheAt time.Time         // news 缓存最近刷新时间（TTL 30s）

	registry EngineRegistry // 多账号引擎注册表（懒加载/按配置指纹共享计算引擎）
}

// EngineRegistry 引擎注册表的 HTTP 可见接口（由 engine.Registry 实现，避免 server→engine 依赖环）。
// 提供按账号获取引擎控制面、查询初始化进度、懒加载引擎的能力。
// English: the registry interface visible to the HTTP layer (implemented by engine.Registry;
// avoids a server→engine import cycle). Provides per-account engine control, init-status probing
// and lazy load.
type EngineRegistry interface {
	GetController(userID string) EngineController
	InitStatusJSON(userID string) map[string]interface{}
	AllControllers() []EngineController
	PaperForUser(userID string) *paper.Engine
	Len() int
	// SetPaperPools 更新全局战法资金池类型模板并同步到所有账号模拟盘（分仓，热加载用）。
	// English: updates the global strategy pool-type template and syncs every account's paper book
	// (allocation; used on hot reload).
	SetPaperPools(types []string)
}

// SetEngineRegistry 设置多账号引擎注册表（懒加载/按配置指纹共享）。
func (s *Server) SetEngineRegistry(r EngineRegistry) { s.registry = r }

// ctrlFor 返回指定账号的引擎控制面；账号首次访问时懒加载其引擎（登录后立即可用）。
// 未接入注册表时回退全局 ctrl（旧单引擎模式）。
// English: returns the engine controller for an account, lazily loading its engine on first access
// (available right after login). Falls back to the global ctrl in the legacy single-engine mode.
func (s *Server) ctrlFor(userID string) EngineController {
	if s.registry != nil {
		return s.registry.GetController(userID)
	}
	return s.ctrl
}

// dashFor 返回指定账号的看板快照（通过注册表懒加载引擎）。
// 未接入注册表时回退全局 agg（旧单引擎模式）。
// English: returns the dashboard snapshot for an account (via registry lazy load).
// Falls back to the global agg in legacy single-engine mode.
func (s *Server) dashFor(userID string) *display.DashboardData {
	if s.registry != nil {
		if c := s.registry.GetController(userID); c != nil {
			return c.DashboardData()
		}
		return nil
	}
	return s.agg.Current()
}

// SetLLMRecreate 设置 LLM 客户端热重建回调。
func (s *Server) SetLLMRecreate(fn func(apiKeys []string, apiURL, model string, timeoutSec int, streaming bool, batchConcurrency int, classifierModel string)) {
	s.llmRecreate = fn
}

// SetFetcher 注入 5s 实时行情采集器（报价接口优先读快照，缺失再降级拉取）。
func (s *Server) SetFetcher(f *data.Fetcher) { s.fetcher = f }

// SetCoordinator 注入行情统一数据源（新浪→同花顺→东财 三级降级链）。
func (s *Server) SetCoordinator(dc *data.DataCoordinator) { s.dc = dc }

// SetPaper 注入模拟盘引擎（nil 表示未启用）。
// English: injects the paper-trading engine (nil = disabled).
func (s *Server) SetPaper(p *paper.Engine) { s.paper = p }

// SetEngineController 设置引擎控制器。
func (s *Server) SetEngineController(c EngineController) { s.ctrl = c }

// SetRuntimeLLM 记录启动时实际生效的 LLM 模型与地址（供 /api/config/llm 返回真实值）。
func (s *Server) SetRuntimeLLM(url, model string) {
	s.llmMu.Lock()
	s.runtimeURL = url
	s.runtimeLLM = model
	s.llmMu.Unlock()
}

// runtimeModel 返回运行时 LLM 模型；未记录时回退配置文件中的 model。
func (s *Server) runtimeModel() string {
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	if s.runtimeLLM != "" {
		return s.runtimeLLM
	}
	return s.cfg.GetLLMConfig().Model
}

// macroEvents 返回当日宏观日历事件（按天缓存，每天首次调用时生成）。
func (s *Server) macroEvents(now time.Time) []data.MacroEvent {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	day := now.Format("2006-01-02")
	if s.macroCacheDay == day && s.macroCache != nil {
		return s.macroCache
	}
	s.macroCache = data.GenMacroEvents(now.Year(), nil)
	s.macroCacheDay = day
	return s.macroCache
}

// ipoCalendar 返回当日 IPO 日历（按天缓存，每天首次调用时远程拉取）。
func (s *Server) ipoCalendar(now time.Time) ([]data.IPOEvent, error) {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	day := now.Format("2006-01-02")
	if s.ipoCacheDay == day && s.ipoCache != nil {
		return s.ipoCache, nil
	}
	list, err := s.market.GetEastMoneyIPOCalendar()
	if err != nil {
		return nil, err
	}
	s.ipoCache = list
	s.ipoCacheDay = day
	return list, nil
}

// longOnFor / shortOnFor 读取指定账号的开关：账号级配置优先，未配置回退全局默认
// （做多开 / 做空关）。多账号各自独立保存，跨设备同一账号读到的状态一致。
// English: per-account long/short toggles; account override wins, else the global default
// (long on / short off). Each account persists its own state; the same account sees the
// same value on any device.
func (s *Server) longOnFor(userID string) bool {
	if s.cfg != nil {
		return s.cfg.GetLongShortConfigFor(userID).LongEnabled
	}
	return true
}

func (s *Server) shortOnFor(userID string) bool {
	if s.cfg != nil {
		return s.cfg.GetLongShortConfigFor(userID).ShortEnabled
	}
	return false
}

// New 创建 HTTP 服务端实例。
func New(authMgr *auth.Manager, agg *display.Aggregator, cfg *config.Manager, rpt *report.Report, market *data.MarketAPI, wl *data.WatchlistManager, ths *data.THSClient) *Server {
	s := &Server{
		auth:      authMgr,
		agg:       agg,
		cfg:       cfg,
		rpt:       rpt,
		mux:       http.NewServeMux(),
		market:    market,
		ths:       ths,
		watchlist: wl,
		sse:       NewSSEBroker(),
		startTime: time.Now(),
	}
	// 多账号多配置：把 auth.Manager 作为 per-user 配置存储注入 config.Manager，
	// 使策略/D1/LLM 配置可按账号隔离保存。
	// English: inject the auth.Manager as the per-user config store so that strategy/D1/LLM
	// settings can be isolated per account.
	cfg.SetStore(authMgr)
	s.registerRoutes()
	return s
}

// SetResearch 注入 B5 研究候选库与应用目录（approve 时把权重写入 applied_rules.json）。
// （SetResearch wires the research-candidate store and app dir used by the approval endpoints.）
func (s *Server) SetResearch(db *store.DB, dataDir string) {
	s.researchDB = db
	s.researchDir = dataDir
	// 启动恢复：上次进程崩溃遗留的 running 回测任务标记为 interrupted（前端可重新发起续跑，
	// 断点缓存仍有效）。只在研究库接入时才执行。
	// English: startup recovery — any leftover running backtest jobs from a crashed process are marked
	// interrupted (the frontend can re-trigger a resume; checkpoints remain valid). Runs only when the
	// research DB is wired in.
	if db != nil {
		if n, err := db.MarkRunningInterrupted(); err != nil {
			log.Printf("[research] 标记残留回测任务失败: %v", err)
		} else if n > 0 {
			log.Printf("[research] 已把 %d 个残留 running 回测任务标记为 interrupted", n)
		}
	}
}

// GetSSE 返回 SSE 事件推送器。
func (s *Server) GetSSE() *SSEBroker { return s.sse }

// registerRoutes 注册全部 HTTP 路由：
// 认证/初始化（register/temp/login/setup）无需鉴权；业务 API 统一包一层 authMiddleware。
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/temp", s.handleTemp)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /setup", s.handleSetupStatus)
	s.mux.HandleFunc("POST /setup", s.handleSetupSubmit)

	// 当前登录用户信息（前端据此渲染权限相关的菜单/按钮）
	s.mux.HandleFunc("GET /api/auth/me", s.authMiddleware(s.handleAuthMe))

	// 用户/账号管理（仅 admin）
	s.mux.HandleFunc("GET /api/admin/users", s.adminMiddleware(s.handleListUsers))
	s.mux.HandleFunc("POST /api/admin/users", s.adminMiddleware(s.handleCreateUser))
	s.mux.HandleFunc("POST /api/admin/users/{id}/role", s.adminMiddleware(s.handleSetUserRole))
	s.mux.HandleFunc("POST /api/admin/users/{id}/perms", s.adminMiddleware(s.handleSetUserPerms))
	s.mux.HandleFunc("POST /api/admin/users/{id}/password", s.adminMiddleware(s.handleSetUserPassword))
	s.mux.HandleFunc("POST /api/admin/users/{id}/enabled", s.adminMiddleware(s.handleSetUserEnabled))
	s.mux.HandleFunc("POST /api/admin/users/{id}/expiry", s.adminMiddleware(s.handleSetUserExpiry))
	s.mux.HandleFunc("DELETE /api/admin/users/{id}", s.adminMiddleware(s.handleDeleteUser))
	// 管理员代配他人账号配置（strategy / d1 / longshort / llm）
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/strategy", s.adminMiddleware(s.handleAdminGetStrategyConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/strategy", s.adminMiddleware(s.handleAdminSetStrategyConfig))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/d1", s.adminMiddleware(s.handleAdminGetD1Config))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/d1", s.adminMiddleware(s.handleAdminSetD1Config))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/longshort", s.adminMiddleware(s.handleAdminGetLongShortConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/longshort", s.adminMiddleware(s.handleAdminSetLongShortConfig))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/llm", s.adminMiddleware(s.handleAdminGetLLMConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/llm", s.adminMiddleware(s.handleAdminSetLLMConfig))

	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))
	s.mux.HandleFunc("GET /api/data_source_health", s.authMiddleware(s.handleDataSourceHealth))
	s.mux.HandleFunc("GET /api/news_source_health", s.authMiddleware(s.handleNewsSourceHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
	s.mux.HandleFunc("POST /api/long/toggle", s.authMiddleware(s.handleLongToggle))
	s.mux.HandleFunc("GET /api/long/status", s.authMiddleware(s.handleLongStatus))
	s.mux.HandleFunc("POST /api/short/toggle", s.authMiddleware(s.handleShortToggle))
	s.mux.HandleFunc("GET /api/short/status", s.authMiddleware(s.handleShortStatus))
	s.mux.HandleFunc("GET /api/config/strategy", s.authMiddleware(s.handleGetStrategyConfig))
	s.mux.HandleFunc("POST /api/config/strategy", s.authMiddleware(s.handleSetStrategyConfig))
	s.mux.HandleFunc("GET /api/config/d1", s.authMiddleware(s.handleGetD1Config))
	s.mux.HandleFunc("POST /api/config/d1", s.authMiddleware(s.handleSetD1Config))
	s.mux.HandleFunc("GET /api/config/llm", s.authMiddleware(s.handleGetLLMConfig))
	s.mux.HandleFunc("POST /api/config/llm", s.authMiddleware(s.handleSetLLMConfig))

	// 模拟盘（纸面交易）：独立于真实持仓，实时价撮合 + 净值曲线 + 信号质量统计
	s.mux.HandleFunc("GET /api/paper/state", s.authMiddleware(s.handlePaperState))
	s.mux.HandleFunc("GET /api/paper/positions", s.authMiddleware(s.handlePaperPositions))
	s.mux.HandleFunc("GET /api/paper/trades", s.authMiddleware(s.handlePaperTrades))
	s.mux.HandleFunc("GET /api/paper/orders", s.authMiddleware(s.handlePaperOrders))
	s.mux.HandleFunc("GET /api/paper/equity", s.authMiddleware(s.handlePaperEquity))
	s.mux.HandleFunc("POST /api/paper/sell", s.authMiddleware(s.handlePaperSell))
	s.mux.HandleFunc("POST /api/paper/buy", s.authMiddleware(s.handlePaperBuy))
	s.mux.HandleFunc("POST /api/paper/reset", s.authMiddleware(s.handlePaperReset))
	s.mux.HandleFunc("POST /api/paper/pool/reset", s.authMiddleware(s.handlePaperPoolReset))
	s.mux.HandleFunc("POST /api/paper/pool/config", s.authMiddleware(s.handlePaperPoolConfig))
	s.mux.HandleFunc("POST /api/positions", s.authMiddleware(s.handleCreatePosition))
	s.mux.HandleFunc("PUT /api/positions/{id}", s.authMiddleware(s.handleUpdatePosition))
	s.mux.HandleFunc("DELETE /api/positions/{id}", s.authMiddleware(s.handleDeletePosition))
	s.mux.HandleFunc("POST /api/positions/{id}/exit", s.authMiddleware(s.handleExitPosition))
	s.mux.HandleFunc("GET /api/positions", s.authMiddleware(s.handleListPositions))

	// fix 兼容端点
	s.mux.HandleFunc("GET /api/kline", s.authMiddleware(s.handleFixKLine))
	s.mux.HandleFunc("GET /api/minute", s.authMiddleware(s.handleFixMinute))
	s.mux.HandleFunc("GET /api/signals", s.authMiddleware(s.handleFixSignals))
	s.mux.HandleFunc("GET /api/status", s.authMiddleware(s.handleFixStatus))
	s.mux.HandleFunc("GET /api/engine_health", s.authMiddleware(s.handleFixEngineHealth))
	s.mux.HandleFunc("GET /api/alerts", s.authMiddleware(s.handleFixAlerts))
	s.mux.HandleFunc("DELETE /api/alerts", s.authMiddleware(s.handleClearAlerts))
	s.mux.HandleFunc("DELETE /api/alerts/{id}", s.authMiddleware(s.handleDeleteAlert))
	s.mux.HandleFunc("GET /api/holdings", s.authMiddleware(s.handleFixGetHoldings))
	s.mux.HandleFunc("POST /api/holdings", s.authMiddleware(s.handleFixSetHoldings))
	s.mux.HandleFunc("POST /api/holdings/{code}/add", s.authMiddleware(s.handleFixAddHoldingLot))
	s.mux.HandleFunc("POST /api/holdings/{code}/cost", s.authMiddleware(s.handleFixSetCost))
	s.mux.HandleFunc("POST /api/holdings/{code}/sell", s.authMiddleware(s.handleFixSellHolding))
	s.mux.HandleFunc("POST /api/holdings/{code}/close", s.authMiddleware(s.handleFixCloseHolding))
	s.mux.HandleFunc("GET /api/sector/hot", s.authMiddleware(s.handleFixSectorHot))
	s.mux.HandleFunc("GET /api/sector/hot/records", s.authMiddleware(s.handleSectorHotRecords))
	s.mux.HandleFunc("GET /api/snapshot", s.authMiddleware(s.handleFixSnapshot))
	s.mux.HandleFunc("GET /api/snapshot/hot", s.authMiddleware(s.handleFixHotSnapshot))
	s.mux.HandleFunc("GET /api/evaluations", s.authMiddleware(s.handleFixEvaluations))
	s.mux.HandleFunc("GET /api/ipo/calendar", s.authMiddleware(s.handleFixIPOCalendar))
	s.mux.HandleFunc("GET /api/stock/lookup", s.authMiddleware(s.handleFixStockLookup))
	s.mux.HandleFunc("GET /api/depth/{code}", s.authMiddleware(s.handleFixDepth))
	s.mux.HandleFunc("GET /api/news", s.authMiddleware(s.handleFixNews))
	s.mux.HandleFunc("POST /api/news/showall", s.authMiddleware(s.handleNewsShowAllToggle))
	s.mux.HandleFunc("GET /api/news/showall", s.authMiddleware(s.handleNewsShowAllStatus))
	s.mux.HandleFunc("POST /api/news/reanalyze", s.authMiddleware(s.handleNewsReanalyze))
	s.mux.HandleFunc("POST /api/news/test-attribution", s.authMiddleware(s.handleNewsTestAttribution))
	s.mux.HandleFunc("GET /api/engine/init-status", s.authMiddleware(s.handleEngineInitStatus))
	s.mux.HandleFunc("GET /api/watchlist", s.authMiddleware(s.handleFixGetWatchlist))
	s.mux.HandleFunc("POST /api/watchlist", s.authMiddleware(s.handleFixAddWatchlist))
	s.mux.HandleFunc("DELETE /api/watchlist", s.authMiddleware(s.handleFixRemoveWatchlist))
	s.mux.HandleFunc("POST /api/action", s.authMiddleware(s.handleFixAction))
	s.mux.HandleFunc("POST /api/notify-test", s.authMiddleware(s.handleFixNotifyTest))
	// 实盘交易（AUTO_TRADING_PLAN M1）：持仓页实盘 tab 拉真实持仓/建议/执行 + 网关回报/状态。
	// English: live trading (AUTO_TRADING_PLAN M1) — live tab real positions/advice/execute + gateway report/state.
	s.mux.HandleFunc("GET /api/positions/real", s.authMiddleware(s.handleRealPositions))
	s.mux.HandleFunc("GET /api/positions/advice", s.authMiddleware(s.handleRealAdvice))
	s.mux.HandleFunc("POST /api/positions/execute", s.authMiddleware(s.handleExecuteAction))
	s.mux.HandleFunc("POST /api/qmt/report", s.qmtReportMiddleware(s.handleQMTReport))
	s.mux.HandleFunc("GET /api/qmt/state", s.authMiddleware(s.handleQMTState))
	s.mux.HandleFunc("GET /api/llm-debug", s.authMiddleware(s.handleLLMDebug))
	s.mux.HandleFunc("POST /api/consult", s.authMiddleware(s.handleConsult))
	s.mux.HandleFunc("GET /api/consult/history", s.authMiddleware(s.handleConsultHistory))
	s.mux.HandleFunc("DELETE /api/consult/history", s.authMiddleware(s.handleClearConsultHistory))
	s.mux.HandleFunc("GET /api/consult/pro-mode", s.authMiddleware(s.handleGetConsultProMode))
	s.mux.HandleFunc("PUT /api/consult/pro-mode", s.authMiddleware(s.handleSetConsultProMode))
	s.mux.HandleFunc("GET /api/stage-records", s.authMiddleware(s.handleStageRecords))
	s.mux.HandleFunc("GET /api/signal-logs", s.authMiddleware(s.handleSignalLogs))
	// B5 研究候选审批（仅拥有 research_approve 权限位或 admin 可操作；列表可见）
	s.mux.HandleFunc("GET /api/research/progress", s.authMiddleware(s.handleResearchProgress))
	s.mux.HandleFunc("GET /api/research/factors", s.authMiddleware(s.handleResearchFactors))
	s.mux.HandleFunc("GET /api/research/candidates", s.authMiddleware(s.handleResearchCandidates))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/approve", s.permMiddleware(auth.PermResearchApprove, s.handleResearchApprove))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/reject", s.permMiddleware(auth.PermResearchApprove, s.handleResearchReject))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/backtest", s.permMiddleware(auth.PermResearchApprove, s.handleCandidateBacktest))
	s.mux.HandleFunc("GET /api/research/backtest/{id}", s.authMiddleware(s.handleBacktestStatus))
	// 阶段3.2 回测运行控制：取消（kill+interrupted，断点缓存可续跑）/ 暂停（SIGSTOP）/ 继续（SIGCONT）
	s.mux.HandleFunc("POST /api/research/backtest/{id}/cancel", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestCancel))
	s.mux.HandleFunc("POST /api/research/backtest/{id}/pause", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestPause))
	s.mux.HandleFunc("POST /api/research/backtest/{id}/resume", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestResume))
	// 回测任务中心：运行中任务列表（前端刷新后恢复轮询）+ 全部任务列表（回测 tab 进度查看，含夜间全量）
	// English: backtest task center — running-job list (for frontend polling recovery after a refresh)
	// and the full job list (backtest tab progress view, including nightly runs).
	s.mux.HandleFunc("GET /api/research/backtest/running", s.authMiddleware(s.handleBacktestRunning))
	s.mux.HandleFunc("GET /api/research/backtest/list", s.authMiddleware(s.handleBacktestList))
	// 战法库（因子战法）：列出已应用 + 启用/禁用/删除 + 重命名 + 效果监测 + 全量回测全局开关
	s.mux.HandleFunc("GET /api/research/library", s.authMiddleware(s.handleResearchLibrary))
	s.mux.HandleFunc("POST /api/research/library/{id}/enable", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryToggle("enable")))
	s.mux.HandleFunc("POST /api/research/library/{id}/disable", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryToggle("disable")))
	s.mux.HandleFunc("POST /api/research/library/{id}/delete", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryDelete))
	s.mux.HandleFunc("POST /api/research/library/{id}/rename", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryRename))
	s.mux.HandleFunc("GET /api/research/backtest-toggle", s.authMiddleware(s.handleResearchBacktestToggle))
	s.mux.HandleFunc("POST /api/research/backtest-toggle", s.permMiddleware(auth.PermResearchApprove, s.handleResearchBacktestToggle))
	s.mux.HandleFunc("GET /api/events", s.handleFixSSE)
}

// maxBodyBytes 请求体大小上限：64KB（防超大 body 打爆内存，正常业务请求远小于此）。
// （maxBodyBytes caps request bodies at 64KB to prevent memory exhaustion.）
const maxBodyBytes = 64 << 10

// Serve 启动 HTTP 服务监听指定地址。
func (s *Server) Serve(addr string) error {
	log.Printf("HTTP server starting on %s", addr)
	return http.ListenAndServe(addr, s.chain(s.mux))
}

// ServeListener 使用已创建的监听器启动 HTTP 服务。
// 与 Serve 的区别：监听器由调用方预先绑定（端口占用自动顺延后拿到的 listener），
// 复用同一对象服务请求，避免"先探测端口再 ListenAndServe"的 bind 竞争。
// English: serves HTTP on a pre-bound listener. Unlike Serve, the listener is created by the caller
// (e.g. after auto port-switching) and reused for serving, avoiding the bind race of
// "probe the port, then ListenAndServe".
func (s *Server) ServeListener(ln net.Listener) error {
	log.Printf("HTTP server serving on %s", ln.Addr().String())
	return http.Serve(ln, s.chain(s.mux))
}

// ServeHTTP 实现 http.Handler 接口，供 httptest / 内嵌路由直接驱动（测试与复用场景）。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.chain(s.mux).ServeHTTP(w, r)
}

// recoverMiddleware 恢复中间件：捕获请求处理链中的 panic，记录日志并返回 500，
// 避免单个 handler 崩溃把整个 HTTP 服务进程打挂（配合 systemd Restart=always 双保险）。
// 已开始写入的响应无法再改状态码，此时仅记录日志并中止该连接。
// （recoverMiddleware catches panics in the request chain, logs them and returns 500
// so a single handler crash cannot take down the whole HTTP process.）
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				log.Printf("[server] PANIC recovered: %v\n%s", rec, stack[:n])
				// 响应尚未写入时返回 500；已写入则放弃改写状态码
				if rw, ok := w.(http.Hijacker); ok && rw != nil {
					_ = rw
				}
				// 尽力尝试写入错误响应（若头已发送则 WriteHeader 无效，不报错）
				defer func() {
					if rec2 := recover(); rec2 != nil {
						log.Printf("[server] panic-response write also panicked: %v", rec2)
					}
				}()
				w.Header().Set("Content-Type", "application/json")
				writeError(w, 500, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware 跨域中间件：为所有响应添加 CORS 头，并直接终结 OPTIONS 预检请求。
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chain 将多个中间件按顺序包装 next（外层 → 内层）。
// recoverMiddleware 在最外层兜底 panic。
func (s *Server) chain(next http.Handler) http.Handler {
	return s.recoverMiddleware(s.corsMiddleware(next))
}

// GetServeMux 返回路由注册表。
func (s *Server) GetServeMux() *http.ServeMux { return s.mux }

// GetAuthManager 返回认证管理器。
func (s *Server) GetAuthManager() *auth.Manager { return s.auth }

// writeJSON 以 JSON 格式写入响应：设置 Content-Type、状态码并编码序列化 v。
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 以标准错误结构 {"error": msg} 写入响应。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ── Auth handlers ──

// registerReq 注册请求体：用户名 + 密码。
type registerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleRegister 处理 POST /auth/register：创建用户并返回 token 与用户 ID。
// 用户名/密码缺失返回 400；用户名已存在返回 409。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Register(req.Username, req.Password)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"token": user.Token,
		"id":    user.ID,
	})
}

// handleTemp 处理 POST /auth/temp：创建有效期 14 天的临时演示账号，返回 token/ID/过期时间。
func (s *Server) handleTemp(w http.ResponseWriter, r *http.Request) {
	user, err := s.auth.CreateTemp(14 * 24 * time.Hour)
	if err != nil {
		writeError(w, 500, "create temp account failed")
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"token":      user.Token,
		"id":         user.ID,
		"expires_at": user.TokenExp,
	})
}

// loginReq 登录请求体：用户名 + 密码。
type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 处理 POST /auth/login（/api/auth/login 同路由）：校验凭据并返回 token/ID/账号名。
// 凭据错误返回 401。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"token":   user.Token,
		"id":      user.ID,
		"account": user.Username,
		"role":    user.Role,
		"perms":   user.Perms,
	})
}

// ── Setup handlers ──

// handleSetupStatus 处理 GET /setup：返回系统是否已完成初始化（用于前端引导首次配置）。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	initialized := s.auth.IsInitialized()
	writeJSON(w, 200, map[string]bool{"initialized": initialized})
}

// setupReq 首次初始化配置请求体：管理员账号 + LLM 参数 + Tushare token。
type setupReq struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	LLMApiURL    string `json:"llm_api_url"`
	LLMApiKey    string `json:"llm_api_key"`
	TushareToken string `json:"tushare_token"`
}

// handleSetupSubmit 处理 POST /setup：完成首次初始化。
// 创建管理员账号，将非空的 LLM/Tushare 配置写入用户配置，并标记系统已初始化。
// 已初始化时返回 400 拒绝重复配置。
func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if s.auth.IsInitialized() {
		writeError(w, 400, "already initialized")
		return
	}

	var req setupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.CreateUser(req.Username, req.Password, auth.RoleAdmin, nil, 0)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}

	if req.LLMApiURL != "" {
		s.auth.SetConfig(user.ID, "llm_api_url", req.LLMApiURL)
	}
	if req.LLMApiKey != "" {
		s.auth.SetConfig(user.ID, "llm_api_key", req.LLMApiKey)
	}
	if req.TushareToken != "" {
		s.auth.SetConfig(user.ID, "tushare_token", req.TushareToken)
	}
	s.auth.MarkInitialized()

	writeJSON(w, 200, map[string]interface{}{
		"token": user.Token,
		"id":    user.ID,
	})
}

// ── API middleware ──

// authMiddleware 认证中间件：从 Authorization 头提取 token（兼容 Bearer 前缀），
// 校验通过后放行请求，否则返回 401。
// ctxUserKey 认证用户上下文键，authMiddleware 将校验通过的用户写入 request context。
type ctxUserKey struct{}

// userFromContext 从请求上下文取出认证用户（由 authMiddleware 注入），未注入返回 nil。
func userFromContext(r *http.Request) *auth.User {
	u, _ := r.Context().Value(ctxUserKey{}).(*auth.User)
	return u
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			writeError(w, 401, "missing authorization token")
			return
		}
		if strings.HasPrefix(token, "Bearer ") {
			token = strings.TrimPrefix(token, "Bearer ")
		}
		user := s.auth.ValidateToken(token)
		if user == nil {
			writeError(w, 401, "invalid or expired token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, user)))
	}
}

// adminMiddleware 管理员中间件：在认证基础上要求当前用户为管理员角色，否则返回 403。
// 仅包裹用户/账号管理与全局配置等管理类接口。
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r)
		if user == nil || !user.IsAdmin() {
			writeError(w, 403, "admin required")
			return
		}
		next(w, r)
	})
}

// permMiddleware 权限位中间件：要求当前用户拥有指定权限位（管理员隐式拥有全部）。
func (s *Server) permMiddleware(perm string, next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r)
		if user == nil || !user.HasPerm(perm) {
			writeError(w, 403, "no permission: "+perm)
			return
		}
		next(w, r)
	})
}

// ── API handlers ──

// handleHealth 处理 GET /api/health：健康检查，恒返回 {"status":"ok"}。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDataSourceHealth 处理 GET /api/data_source_health：返回各数据源健康探测结果。
// （handleDataSourceHealth returns the probing results of each data source.）
func (s *Server) handleDataSourceHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.dc.HealthCheck())
}

// handleNewsSourceHealth 处理 GET /api/news_source_health：返回新闻资讯源健康探测结果。
// （handleNewsSourceHealth returns the probing results of each news source.）
func (s *Server) handleNewsSourceHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.dc.NewsSourceHealth())
}

// handleDashboard 处理 GET /api/dashboard：返回看板聚合快照。
// 包括新闻事件、热门/利空板块、多空信号、最终信号、L1 评分与做多/做空开关状态；
// 若报表存在则附带统计指标与持仓日志。无数据时返回 waiting_for_data。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	userID := requestUserID(r)
	data := s.dashFor(userID)
	if data == nil {
		writeJSON(w, 200, map[string]string{"status": "waiting_for_data"})
		return
	}
	resp := map[string]interface{}{
		"news_events":   data.NewsEvents,
		"hot_sectors":   data.HotSectors,
		"bear_sectors":  data.BearSectors,
		"bear_stocks":   data.BearStocks,
		"verified_bull": data.VerifiedBull,
		"verified_bear": data.VerifiedBear,
		"bull_signals":  data.BullSignals,
		"bear_signals":  data.BearSignals,
		"final_signals": data.FinalSignals,
		"l1_score":      data.L1Score,
		"l1_blocked":    data.L1Blocked,
		"long_enabled":  s.longOnFor(userID),
		"short_enabled": s.shortOnFor(userID),
	}
	if data.Report != nil {
		total, holding, win, wr, avgW, avgL := data.Report.Stats()
		resp["report_stats"] = map[string]interface{}{
			"total":        total,
			"holding":      holding,
			"win":          win,
			"win_rate":     wr,
			"avg_win_pct":  avgW,
			"avg_loss_pct": avgL,
			"by_strategy":  data.Report.StatsByStrategy(""), // 按战法分组的胜率/盈亏比明细
		}
		resp["report_logs"] = data.Report.List()
	}
	writeJSON(w, 200, resp)
}

// longToggleReq 做多开关请求体。
type longToggleReq struct {
	Enabled bool `json:"enabled"`
}

// handleLongToggle 处理 POST /api/long/toggle：切换做多开关（按账号持久化）并返回最新状态。
func (s *Server) handleLongToggle(w http.ResponseWriter, r *http.Request) {
	var req longToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	userID := requestUserID(r)
	if s.cfg != nil {
		cur := s.cfg.GetLongShortConfigFor(userID)
		cur.LongEnabled = req.Enabled
		s.cfg.SetLongShortConfigFor(userID, cur)
	}
	if c := s.ctrlFor(userID); c != nil {
		c.SetLongEnabled(req.Enabled)
	}
	log.Printf("[server] 账号 %s 做多开关: %v", userID, req.Enabled)
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOnFor(userID)})
}

// handleLongStatus 处理 GET /api/long/status：返回指定账号当前做多开关状态。
func (s *Server) handleLongStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOnFor(requestUserID(r))})
}

// shortToggleReq 做空开关请求体。
type shortToggleReq struct {
	Enabled bool `json:"enabled"`
}

// handleShortToggle 处理 POST /api/short/toggle：切换做空开关（按账号持久化）并返回最新状态。
func (s *Server) handleShortToggle(w http.ResponseWriter, r *http.Request) {
	var req shortToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	userID := requestUserID(r)
	if s.cfg != nil {
		cur := s.cfg.GetLongShortConfigFor(userID)
		cur.ShortEnabled = req.Enabled
		s.cfg.SetLongShortConfigFor(userID, cur)
	}
	if c := s.ctrlFor(userID); c != nil {
		c.SetShortEnabled(req.Enabled)
	}
	log.Printf("[server] 账号 %s 做空开关: %v", userID, req.Enabled)
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOnFor(userID)})
}

// handleShortStatus 处理 GET /api/short/status：返回指定账号当前做空开关状态。
func (s *Server) handleShortStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOnFor(requestUserID(r))})
}

// newsShowAllReq 资讯"显示全部"开关请求体。
type newsShowAllReq struct {
	Enabled bool `json:"enabled"`
}

// newsShowAllOn 读取引擎"资讯显示全部"开关；未接入引擎时回退默认（关闭）。
func (s *Server) newsShowAllOn(userID string) bool {
	if c := s.ctrlFor(userID); c != nil {
		return c.NewsShowAll()
	}
	return false
}

// handleNewsShowAllToggle 处理 POST /api/news/showall：切换"资讯显示全部"开关。
func (s *Server) handleNewsShowAllToggle(w http.ResponseWriter, r *http.Request) {
	var req newsShowAllReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	userID := requestUserID(r)
	if c := s.ctrlFor(userID); c != nil {
		c.SetNewsShowAll(req.Enabled)
	}
	log.Printf("[server] 账号 %s 资讯显示全部开关: %v", userID, req.Enabled)
	writeJSON(w, 200, map[string]bool{"news_show_all": s.newsShowAllOn(userID)})
}

// handleEngineInitStatus 处理 GET /api/engine/init-status：返回指定账号的引擎初始化进度。
// 前端登录后轮询该接口显示进度条 + 预计时间。
// English: handles GET /api/engine/init-status — returns the account's engine init progress
// for the frontend login progress bar + ETA.
func (s *Server) handleEngineInitStatus(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, 200, map[string]interface{}{
			"initialized": true,
			"stage":       "ready",
			"percent":     100,
			"eta_seconds": 0,
		})
		return
	}
	resp := s.registry.InitStatusJSON(requestUserID(r))
	if resp == nil {
		resp = map[string]interface{}{
			"initialized": false,
			"percent":     0,
			"eta_seconds": 0,
			"stage":       "",
		}
	}
	writeJSON(w, 200, resp)
}

// handleNewsShowAllStatus 处理 GET /api/news/showall：返回"资讯显示全部"开关状态。
func (s *Server) handleNewsShowAllStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"news_show_all": s.newsShowAllOn(requestUserID(r))})
}

// handleNewsReanalyze 处理 POST /api/news/reanalyze：手动 LLM 补推。
// 异步执行（拉取+LLM耗时），立即返回 202 表示已触发；结果打印到日志。
func (s *Server) handleNewsReanalyze(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeError(w, 503, "engine not ready")
		return
	}
	log.Printf("[server] 触发手动LLM补推")
	writeJSON(w, 202, map[string]bool{"accepted": true})
	go func() {
		stat, err := c.ReanalyzeNews()
		if err != nil {
			log.Printf("[server] 补推失败: %v", err)
			return
		}
		log.Printf("[server] 补推完成: 原始%d 个股%d 板块%d IPO%d 一般%d 事件%d",
			stat["raw"], stat["stock"], stat["sector"], stat["ipo"], stat["general"], stat["events"])
	}()
}

// testAttributionReq 单条归因测试请求体：标题 + 可选正文摘要（供 LLM 价值链背景）。
type testAttributionReq struct {
	Title  string `json:"title"`
	Digest string `json:"digest,omitempty"`
}

// handleNewsTestAttribution 处理 POST /api/news/test-attribution：单条新闻走 Stage2
// 归因测试（含产业链价值传导推导 + 差分事件拆分），返回拆分后的 NewsEvent。
// 用于快速验证"海外自产关键材料→利好国内上游"等归因逻辑是否正确产出个股。
func (s *Server) handleNewsTestAttribution(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeError(w, 503, "engine not ready")
		return
	}
	var req testAttributionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, 400, "title required")
		return
	}
	events, err := c.TestAttribution(req.Title, req.Digest)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(events) == 0 {
		writeJSON(w, 200, map[string]interface{}{
			"events": []interface{}{},
			"note":   "无事件产出：可能 LLM 判定中性/无 A 股归因，或 LLM 调用失败",
		})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"events": events})
}

// streamingEnabled 解析流式开关：nil（请求未携带）时保持默认开启。
func streamingEnabled(s *bool) bool {
	if s == nil {
		return true
	}
	return *s
}

// ── 持仓管理 ──

// createPositionReq 新建持仓请求体：股票代码/名称、方向、策略、开仓价及止盈止损百分比。
type createPositionReq struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Direction     string  `json:"direction"`
	Strategy      string  `json:"strategy"`
	EntryPrice    float64 `json:"entry_price"`
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   float64 `json:"stop_loss_pct,omitempty"`
}

// handleCreatePosition 处理 POST /api/positions：记录一条开仓信号到报表，返回生成的信号 ID。
func (s *Server) handleCreatePosition(w http.ResponseWriter, r *http.Request) {
	var req createPositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Code == "" || req.EntryPrice <= 0 {
		writeError(w, 400, "code and entry_price required")
		return
	}
	uid := requestUserID(r)
	id := fmt.Sprintf("POS%d", time.Now().UnixNano())
	s.rpt.LogSignal(id, req.Code, req.Name, req.Direction, req.Strategy, req.EntryPrice, req.TakeProfitPct, req.StopLossPct)
	s.rpt.Update(id, func(log *report.ExecLog) { log.UserID = uid })
	writeJSON(w, 201, map[string]string{"id": id})
}

// updatePositionReq 更新持仓请求体：止盈/止损百分比与名称均可选，指针为 nil 表示不修改。
type updatePositionReq struct {
	TakeProfitPct *float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   *float64 `json:"stop_loss_pct,omitempty"`
	Name          *string  `json:"name,omitempty"`
}

// handleUpdatePosition 处理 PUT /api/positions/{id}：按 ID 更新持仓的止盈/止损/名称字段。
func (s *Server) handleUpdatePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updatePositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.rpt.Update(id, func(log *report.ExecLog) {
		if req.TakeProfitPct != nil {
			log.TakeProfitPct = *req.TakeProfitPct
		}
		if req.StopLossPct != nil {
			log.StopLossPct = *req.StopLossPct
		}
		if req.Name != nil {
			log.Name = *req.Name
		}
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeletePosition 处理 DELETE /api/positions/{id}：软删除指定持仓记录。
func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.rpt.Delete(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// exitPositionReq 平仓请求体：平仓价格。
type exitPositionReq struct {
	ExitPrice float64 `json:"exit_price"`
}

// handleExitPosition 处理 POST /api/positions/{id}/exit：按平仓价计算盈亏并标记止盈/止损。
func (s *Server) handleExitPosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req exitPositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.ExitPrice <= 0 {
		writeError(w, 400, "exit_price required")
		return
	}
	s.rpt.LogExit(id, req.ExitPrice, "手动平仓")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleListPositions 处理 GET /api/positions：返回当前账号的持仓记录与交易统计指标。
func (s *Server) handleListPositions(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	logs := s.rpt.ListFor(uid)
	reportStats := map[string]interface{}{}
	total, holding, win, wr, avgW, avgL := s.rpt.StatsFor(uid)
	reportStats["total"] = total
	reportStats["holding"] = holding
	reportStats["win"] = win
	reportStats["win_rate"] = wr
	reportStats["avg_win_pct"] = avgW
	reportStats["avg_loss_pct"] = avgL
	reportStats["by_strategy"] = s.rpt.StatsByStrategy(uid) // 按账号、按战法分组的胜率明细
	writeJSON(w, 200, map[string]interface{}{
		"positions": logs,
		"stats":     reportStats,
	})
}

// ── 策略参数配置 ──

// handleGetStrategyConfig 处理 GET /api/config/strategy：返回全局策略参数配置。
// 战法参数全局共享（多账号一致），不按账号隔离。
func (s *Server) handleGetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetStrategyConfig())
}

// handleSetStrategyConfig 处理 POST /api/config/strategy：保存全局策略参数配置。
func (s *Server) handleSetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.StrategyConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetStrategyConfig(&cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── D1 规则配置 ──

// handleGetD1Config 处理 GET /api/config/d1：返回全局 D1 规则配置（战法一致）。
func (s *Server) handleGetD1Config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetD1Config())
}

// handleSetD1Config 处理 POST /api/config/d1：保存全局 D1 规则配置。
func (s *Server) handleSetD1Config(w http.ResponseWriter, r *http.Request) {
	var cfg config.D1Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetD1Config(&cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── LLM 配置 ──

// setLLMConfigReq LLM 配置请求体：APIKey 可选（不修改时留空），APIURL 与 Model 必填。
type setLLMConfigReq struct {
	APIKey     string   `json:"api_key,omitempty"`
	APIKeys    []string `json:"api_keys,omitempty"` // 多 API 密钥（逗号分隔或数组，轮询分发；为空时回退 APIKey）
	APIURL     string   `json:"api_url"`
	Model      string   `json:"model"`
	TimeoutSec int      `json:"timeout_sec"`      // 单次请求超时（秒），缺省 0
	Stream     *bool    `json:"stream,omitempty"` // 流式开关，缺省维持现状/默认开启
	// BatchConcurrency 新闻归因 LLM 批量并发批次，<=0 时维持现状/默认 4。
	// （BatchConcurrency is the news-attribution LLM batch concurrency; <=0 keeps current/default 4.）
	BatchConcurrency int `json:"batch_concurrency,omitempty"`
	// ClassifierModel 可选分类专用模型（Stage0/1 等快速分类/初筛），留空用主模型。
	// （ClassifierModel is an optional dedicated model for cheap classification/screening; empty = main model.）
	ClassifierModel string `json:"classifier_model,omitempty"`
}

// handleGetLLMConfig 处理 GET /api/config/llm：返回当前账号的 API 地址、运行时生效模型与流式开关。
func (s *Server) handleGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetLLMConfigFor(requestUserID(r))
	// 多 key 从 auth config 读取（逗号分隔）
	var apiKeys []string
	uid := requestUserID(r)
	if v, ok := s.auth.GetConfig(uid, "llm_api_keys"); ok && v != "" {
		apiKeys = splitLLMKeys(v)
	}
	if len(apiKeys) == 0 {
		// 兼容旧单 key 配置
		if v, ok := s.auth.GetConfig(uid, "llm_api_key"); ok && v != "" {
			apiKeys = []string{v}
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"api_url":           cfg.APIURL,
		"api_keys":          apiKeys,
		"model":             s.runtimeModel(),
		"stream":            cfg.StreamingEnabled(),
		"timeout_sec":       cfg.TimeoutSec,
		"batch_concurrency": cfg.BatchConcurrency,
		"max_retry_times":   cfg.MaxRetryTimes,
		"classifier_model":  cfg.ClassifierModel,
	})
}

// splitLLMKeys 解析逗号分隔（含空白）的 API 密钥列表为去空去重数组。
// （splitLLMKeys splits a comma-separated key string into a trimmed, deduplicated list.）
func splitLLMKeys(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// requestUserID 从请求上下文取出当前登录用户 ID；未认证返回空串（走全局配置）。
// （requestUserID returns the authenticated user ID, or "" for unauthenticated requests.）
func requestUserID(r *http.Request) string {
	u := userFromContext(r)
	if u == nil {
		return ""
	}
	return u.ID
}

// handleSetLLMConfig 处理 POST /api/config/llm：保存当前账号的 LLM 配置并热重建客户端。
// 依次执行：APIURL+Model 写入当前账号配置 → APIKey 写入 auth 配置 → 触发 llmRecreate
// 回调重建客户端 → 记录运行时实际生效的 model（空值兜底为默认模型）。
func (s *Server) handleSetLLMConfig(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	var req setLLMConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	// 保存 APIURL + Model 到当前账号配置（多账号多配置隔离）
	s.cfg.SetLLMConfigFor(uid, &config.LLMConfig{
		APIURL:           req.APIURL,
		Model:            req.Model,
		TimeoutSec:       req.TimeoutSec,
		Stream:           req.Stream,
		BatchConcurrency: req.BatchConcurrency,
		ClassifierModel:  req.ClassifierModel,
	})

	// 保存 APIKey 到 auth config（按账号隔离）
	if req.APIKey != "" {
		s.auth.SetConfig(uid, "llm_api_key", req.APIKey)
	}

	// 保存多 API 密钥到 auth config（逗号分隔）；为空时维持现状
	if len(req.APIKeys) > 0 {
		s.auth.SetConfig(uid, "llm_api_keys", strings.Join(req.APIKeys, ","))
	}

	// 热重建 LLM 客户端（如果提供了回调）
	if s.llmRecreate != nil {
		// 优先用请求里的多 key；未提供则读已保存的多 key；再无则回退单 key
		keys := req.APIKeys
		if len(keys) == 0 {
			if v, ok := s.auth.GetConfig(uid, "llm_api_keys"); ok && v != "" {
				keys = splitLLMKeys(v)
			}
		}
		if len(keys) == 0 {
			key := req.APIKey
			if key == "" {
				if v, ok := s.auth.GetConfig(uid, "llm_api_key"); ok {
					key = v
				}
			}
			if key != "" {
				keys = []string{key}
			}
		}
		s.llmRecreate(keys, req.APIURL, req.Model, req.TimeoutSec, streamingEnabled(req.Stream), req.BatchConcurrency, req.ClassifierModel)
	}

	// 记录运行时实际生效的 model（空值会被 llm 客户端按默认模型兜底）
	model := req.Model
	if model == "" {
		model = llm.DefaultModel
	}
	s.SetRuntimeLLM(req.APIURL, model)

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleLLMDebug 处理 GET /api/llm-debug：返回引擎的 LLM 流水线调试信息。
// 未接入引擎或无数据时分别返回 no_engine / no_data 状态。
func (s *Server) handleLLMDebug(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	di := c.GetDebugInfo()
	if di == nil {
		writeJSON(w, 200, map[string]string{"status": "no_data"})
		return
	}
	writeJSON(w, 200, di)
}

// consultReq 股票咨询请求体：用户消息。
type consultReq struct {
	Message string `json:"message"`
}

// 专业模式相关配置键（per-user，落盘 auth.json，跨重启保留）。
const (
	consultProModeKey      = "consult_pro_mode"      // "1"/"0"，默认关
	consultProModeLastUsed = "consult_pro_mode_last" // 最近一次专业咨询 Unix 秒
	consultProModeInterval = 15 * time.Minute        // 盘中专业模式调用间隔上限
)

// consultProModeEnabled 读取当前用户专业模式开关状态（默认关）。
func (s *Server) consultProModeEnabled(userID string) bool {
	v, _ := s.auth.GetConfig(userID, consultProModeKey)
	return v == "1"
}

// consultProModeRateLimited 判定专业模式是否命中盘中 15 分钟限流。
// 仅交易时段（周一至周五 9:15-15:00）受限；盘前/盘后/周末不限。
// 命中限流返回剩余等待时长；未命中返回 0。
func (s *Server) consultProModeRateLimited(userID string, now time.Time) time.Duration {
	if !data.IsTradeTime(now) {
		return 0
	}
	v, ok := s.auth.GetConfig(userID, consultProModeLastUsed)
	if !ok || v == "" {
		return 0
	}
	last, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	elapsed := now.Sub(time.Unix(last, 0))
	if elapsed >= consultProModeInterval {
		return 0
	}
	return consultProModeInterval - elapsed
}

// handleConsult 处理 POST /api/consult：多轮 LLM 咨询。
// 专业模式（开关打开）时注入该股全部实时行情，且盘中 15 分钟限流一次；
// 普通模式不注入数据、不限流。未接入引擎或 LLM 未配置时返回对应错误提示。
func (s *Server) handleConsult(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeError(w, 503, "引擎未启动")
		return
	}
	var req consultReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Message == "" {
		writeError(w, 400, "message required")
		return
	}
	user := userFromContext(r)
	userID := ""
	if user != nil {
		userID = user.ID
	}
	proMode := s.consultProModeEnabled(userID)

	// 盘中限流：仅专业模式且交易时段生效（按用户，落盘跨重启保留）。
	if proMode {
		if wait := s.consultProModeRateLimited(userID, time.Now()); wait > 0 {
			writeError(w, 429, fmt.Sprintf("盘中专业模式每 15 分钟可用一次，请 %s 后再试", wait.Round(time.Second)))
			return
		}
	}

	reply, err := c.ConsultLLM(userID, req.Message, proMode)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 专业咨询成功后记录调用时间（供下次盘中限流判定）。
	if proMode {
		_ = s.auth.SetConfig(userID, consultProModeLastUsed, strconv.FormatInt(time.Now().Unix(), 10))
	}
	writeJSON(w, 200, map[string]string{"reply": reply})
}

// handleGetConsultProMode 处理 GET /api/consult/pro-mode：返回当前用户专业模式开关状态。
func (s *Server) handleGetConsultProMode(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	userID := ""
	if user != nil {
		userID = user.ID
	}
	writeJSON(w, 200, map[string]bool{"enabled": s.consultProModeEnabled(userID)})
}

// proModeReq 专业模式开关请求体。
type proModeReq struct {
	Enabled bool `json:"enabled"`
}

// handleSetConsultProMode 处理 PUT /api/consult/pro-mode：切换当前用户专业模式开关。
func (s *Server) handleSetConsultProMode(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	userID := ""
	if user != nil {
		userID = user.ID
	}
	var req proModeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	v := "0"
	if req.Enabled {
		v = "1"
	}
	if err := s.auth.SetConfig(userID, consultProModeKey, v); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": req.Enabled})
}

// handleConsultHistory 处理 GET /api/consult/history：返回当日咨询对话历史。
func (s *Server) handleConsultHistory(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeJSON(w, 200, []data.ConsultMessage{})
		return
	}
	h := c.GetConsultHistory()
	if h == nil {
		h = []data.ConsultMessage{}
	}
	writeJSON(w, 200, h)
}

// handleClearConsultHistory 处理 DELETE /api/consult/history：清空当日咨询对话。
func (s *Server) handleClearConsultHistory(w http.ResponseWriter, r *http.Request) {
	if c := s.ctrlFor(requestUserID(r)); c != nil {
		c.ClearConsultHistory()
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleStageRecords 返回当日全量 Stage 流水线轮次记录（用于复盘/策略引擎实时调取）。
func (s *Server) handleStageRecords(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	recs := c.GetStageRecords()
	if recs == nil {
		recs = []newsagent.DebugInfo{}
	}
	// 就地倒序，最新轮次的记录排在最前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}

// handleSignalLogs 返回当日全量信号批次记录（用于"信号日志"弹窗按批次复盘）。
func (s *Server) handleSignalLogs(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	recs := c.GetSignalLogs()
	if recs == nil {
		recs = []combat_agent.SignalLog{}
	}
	// 就地倒序，最新批次的记录排在最前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}
