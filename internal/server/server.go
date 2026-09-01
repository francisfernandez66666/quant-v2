// Package server HTTP 服务端：提供看板数据、策略配置、持仓管理、做空开关等 REST API。
package server

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"expvar"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	GetMessagesFor(userID string) []data.MessageItem // §GAP2-W2 按账号可见（公共∪本人私有）
	ClearMessages()
	DeleteMessage(id string)
	RefreshMessageName(code, name string)
	ConsultLLM(userID, userMsg string, proMode bool) (string, error)
	GetConsultHistoryFor(userID string) []data.ConsultMessage // §GAP2-W2 按账号隔离的咨询历史
	ClearConsultHistoryFor(userID string)                     // §GAP2-W2 只清本人的
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
	// liveDB 实盘账本隔离库（real_positions/orders/fills/real_account）。§OPT-3：与 researchDB（trading.db）
	// 拆分，避免夜间研究大批量写入与实时实盘对账/心跳同文件争锁，并便于独立备份。
	// 为空时 realDB() 回退 researchDB，保证旧部署（实盘账本仍在 trading.db）向后兼容。
	liveDB *store.DB

	cacheDir string // 看板快照落盘目录（休市/重启后前端仍可读取最近一次有效数据）

	llmMu      sync.Mutex // 保护 runtimeLLM/runtimeURL 的互斥锁
	runtimeLLM string     // 运行时实际使用的 model（与文件配置可能不同）
	runtimeURL string     // 运行时实际使用的 API 地址
	limiter    ipLimiter  // §A4 匿名端点 IP 频控（register/temp/login/setup）
	// §P1-5 初始化令牌：若环境变量 SETUP_TOKEN 非空，POST /setup 必须携带匹配令牌
	// （body.setup_token 或 X-Setup-Token 头），否则拒绝。防止未授权者抢跑初始化。
	// English: P1-5 setup token — when SETUP_TOKEN env is set, POST /setup requires the matching token.
	setupToken string

	calMu         sync.Mutex        // 保护日历缓存的互斥锁
	macroCache    []data.MacroEvent // 宏观日历事件缓存
	macroCacheDay string            // 宏观日历缓存对应的日期（用于按天失效）
	ipoCache      []data.IPOEvent   // IPO 事件缓存
	ipoCacheDay   string            // IPO 日历缓存对应的日期（用于按天失效）

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
	// SetPaperLabelResolver §C 注入规则池 ID→显示名 解析器（fac_1→"因子战法#1"），
	// 同步到全部已建账号并供懒加载引擎继承。English: injects the rule-pool label resolver.
	SetPaperLabelResolver(fn func(string) string)
}

// SetEngineRegistry 设置多账号引擎注册表（懒加载/按配置指纹共享）。
func (s *Server) SetEngineRegistry(r EngineRegistry) { s.registry = r }

// ctrlFor 返回指定账号的引擎控制面；账号首次访问时懒加载其引擎（登录后立即可用）。
// 未接入注册表时回退全局 ctrl（旧单引擎模式）。
// English: returns the engine controller for an account, lazily loading its engine on first access
// (available right after login). Falls back to the global ctrl in the legacy single-engine mode.
func (s *Server) ctrlFor(userID string) EngineController {
	if s.registry != nil {
		return s.registry.GetController(s.operatorID())
	}
	return s.ctrl
}

// dashFor 返回运营数据归属账号（管理员）的看板快照（运营数据系统级共享）。
// 未接入注册表时回退全局 agg（旧单引擎模式）。
// English: returns the operator's dashboard snapshot (operational data is system-scoped).
// Falls back to the global agg in legacy single-engine mode.
func (s *Server) dashFor(userID string) *display.DashboardData {
	var d *display.DashboardData
	if s.registry != nil {
		if c := s.registry.GetController(s.operatorID()); c != nil {
			d = c.DashboardData()
		}
	} else {
		d = s.agg.Current()
	}
	if d == nil {
		// 实时聚合器空闲（休市/夜间/重启）时，回退到最近一次落盘快照
		return s.loadCachedDash()
	}
	// 有实时数据时刷新落盘缓存，供空闲期回退
	s.cacheDash(d)
	return d
}

// SetCacheDir 设置看板快照落盘目录（缓存最近一次有效看板，解决派生数据不持久化导致休市期前端空白的问题）。
func (s *Server) SetCacheDir(dir string) { s.cacheDir = dir }

// cacheDashPath 返回看板快照落盘路径。
func (s *Server) cacheDashPath() string {
	if s.cacheDir == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, "dashboard_latest.json")
}

// cacheDash 将当前看板快照原子落盘；仅在有实质内容时写入，避免空闲期用空数据覆盖缓存。
func (s *Server) cacheDash(d *display.DashboardData) {
	if s.cacheDir == "" || d == nil {
		return
	}
	if len(d.NewsEvents) == 0 && len(d.HotSectors) == 0 && len(d.BearSectors) == 0 &&
		len(d.BullSignals) == 0 && len(d.FinalSignals) == 0 {
		return
	}
	p := s.cacheDashPath()
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// loadCachedDash 读取落盘的最近看板快照；若不存在则尝试用持久化新闻文件构造仅含新闻的最小快照，保证新闻组件始终有数据。
func (s *Server) loadCachedDash() *display.DashboardData {
	if p := s.cacheDashPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			var d display.DashboardData
			if json.Unmarshal(b, &d) == nil &&
				(len(d.NewsEvents) > 0 || len(d.HotSectors) > 0 || len(d.BearSectors) > 0 ||
					len(d.BullSignals) > 0 || len(d.FinalSignals) > 0) {
				return &d
			}
		}
	}
	if s.cacheDir != "" {
		np := filepath.Join(s.cacheDir, "news_events.json")
		if b, err := os.ReadFile(np); err == nil {
			var wrap struct {
				TradingDay string                `json:"trading_day"`
				Events     []newsagent.NewsEvent `json:"events"`
			}
			if json.Unmarshal(b, &wrap) == nil && len(wrap.Events) > 0 {
				return &display.DashboardData{NewsEvents: wrap.Events}
			}
		}
	}
	return nil
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

// macroEvents 返回当日宏观事件日历（按天缓存，每天首次调用时生成）。
// §R3-8 P1-J 接线：config.json 的 rules.calendar.events 作为补充事件并入
// （key=标题，value=日期|impact），此前 supplement 入参恒传 nil 被丢弃。
func (s *Server) macroEvents(now time.Time) []data.MacroEvent {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	day := now.Format("2006-01-02")
	if s.macroCacheDay == day && s.macroCache != nil {
		return s.macroCache
	}
	supplement := map[string]string{}
	if s.cfg != nil {
		for _, ev := range s.cfg.Get().Calendar.Events {
			if ev.Date == "" || ev.Title == "" {
				continue
			}
			impact := ev.Impact
			if impact == "" {
				impact = "medium"
			}
			supplement[ev.Title] = ev.Date + "|" + impact
		}
	}
	s.macroCache = data.GenMacroEvents(now.Year(), supplement)
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
		return s.cfg.GetLongShortConfigFor(s.operatorID()).LongEnabled
	}
	return true
}

// shortOnFor 返回运营数据归属账号（管理员）的做空开关（运营配置系统级共享）。
func (s *Server) shortOnFor(userID string) bool {
	if s.cfg != nil {
		return s.cfg.GetLongShortConfigFor(s.operatorID()).ShortEnabled
	}
	return false
}

// New 创建 HTTP 服务端实例。
func New(authMgr *auth.Manager, agg *display.Aggregator, cfg *config.Manager, rpt *report.Report, market *data.MarketAPI, wl *data.WatchlistManager, ths *data.THSClient) *Server {
	s := &Server{
		auth:       authMgr,
		agg:        agg,
		cfg:        cfg,
		rpt:        rpt,
		mux:        http.NewServeMux(),
		market:     market,
		ths:        ths,
		watchlist:  wl,
		sse:        NewSSEBroker(),
		startTime:  time.Now(),
		setupToken: os.Getenv("SETUP_TOKEN"),
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
// 回测任务自子系统统一改造（docs/RESEARCH_TASK_QUEUE_PLAN.md）起由 researchd 队列唯一执行，
// quant 不再 spawn 研究子进程，也不再负责启动恢复（researchd worker 打开队列时执行 running→preempted）。
// English: SetResearch wires the research-candidate store and app dir. Backtests are executed solely by
// the researchd queue worker now — quant spawns no research children and owns no startup recovery.
func (s *Server) SetResearch(db *store.DB, dataDir string) {
	s.researchDB = db
	s.researchDir = dataDir
}

// SetLiveDB 接入隔离的实盘账本库（live.db）。§OPT-3：实盘持仓/委托/成交与夜间研究库拆分，
// 降低同文件写竞争并便于独立备份。English: wires the isolated live-book DB (live.db).
func (s *Server) SetLiveDB(db *store.DB) {
	s.liveDB = db
}

// realDB 返回实盘账本库：优先 liveDB，未配置时回退 researchDB（旧部署兼容）。
// English: returns the live-book DB, falling back to researchDB when liveDB isn't wired.
func (s *Server) realDB() *store.DB {
	if s.liveDB != nil {
		return s.liveDB
	}
	return s.researchDB
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
	// §D7-B 注册已关闭，邀请码端点随之下线（auth 层能力保留以备将来重开）
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
	// §DAILY_OPSLOG 每日系统运行日志（管理员只读）：日期列表 + 按日内容（tail 截尾）
	s.mux.HandleFunc("GET /api/opslog/dates", s.adminMiddleware(s.handleOpslogDates))
	s.mux.HandleFunc("GET /api/opslog", s.adminMiddleware(s.handleOpslog))
	// §R4-9 指标面（鉴权后导出 expvar：下单/熔断/撤单/LLM 降级等关键事件计数）
	s.mux.HandleFunc("GET /api/metrics", s.authMiddleware(http.HandlerFunc(expvar.Handler().ServeHTTP)))
	s.mux.HandleFunc("GET /api/data_source_health", s.authMiddleware(s.handleDataSourceHealth))
	s.mux.HandleFunc("GET /api/news_source_health", s.authMiddleware(s.handleNewsSourceHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
	// 做多/做空开关：属运营配置，仅管理员可切换；状态对所有登录用户可读（看板展示用）。
	// （Long/short toggles are operator config: only admin may toggle; status is readable by all.）
	s.mux.HandleFunc("POST /api/long/toggle", s.adminMiddleware(s.handleLongToggle))
	s.mux.HandleFunc("GET /api/long/status", s.authMiddleware(s.handleLongStatus))
	s.mux.HandleFunc("POST /api/short/toggle", s.adminMiddleware(s.handleShortToggle))
	s.mux.HandleFunc("GET /api/short/status", s.authMiddleware(s.handleShortStatus))
	// 策略/D1/LLM 配置：运营配置系统级共享、仅管理员可读写（写会热替换全部账号引擎/新闻管线客户端）。
	// （Strategy/D1/LLM configs are operator-owned: admin-only read+write.）
	s.mux.HandleFunc("GET /api/config/strategy", s.adminMiddleware(s.handleGetStrategyConfig))
	// §GAP2-W2 权限收口：全局战法参数影响所有账号的实盘/模拟决策，写权限收敛到 admin
	// （此前任意 14 天临时账号可改全局止盈止损）。English: §GAP2-W2 — global strategy writes are admin-only.
	s.mux.HandleFunc("POST /api/config/strategy", s.adminMiddleware(s.handleSetStrategyConfig))
	s.mux.HandleFunc("GET /api/config/d1", s.adminMiddleware(s.handleGetD1Config))
	// §GAP2-W2 权限收口：D1 规则同为全局决策面，写权限收敛到 admin。
	s.mux.HandleFunc("POST /api/config/d1", s.adminMiddleware(s.handleSetD1Config))
	s.mux.HandleFunc("GET /api/config/llm", s.adminMiddleware(s.handleGetLLMConfig))
	// §GAP2-W2 权限收口（P1-3）：普通用户保存自己的 LLM 配置会经 llmRecreate 热替换【全部账号】
	// 引擎与新闻管线的客户端（归因上下文外送/计费劫持），故写权限收敛到 admin；普通用户 GET 只读。
	s.mux.HandleFunc("POST /api/config/llm", s.adminMiddleware(s.handleSetLLMConfig))

	// QMT 实盘配置：运营数据统一归属管理员（系统级共享），仅管理员可读写。
	// 子账号不拥有/不操作量化交易，后端据此鉴权，前端不自行判定权限。
	// （QMT config is operator-owned: admin-only read+write; sub-accounts are denied at the API layer.）
	s.mux.HandleFunc("GET /api/config/qmt", s.adminMiddleware(s.handleGetQMTConfig))
	s.mux.HandleFunc("POST /api/config/qmt", s.adminMiddleware(s.handleSetQMTConfig))

	// 模拟盘（纸面交易）：运营数据统一归属管理员（系统级共享），仅管理员可读写；
	// 子账号不操作模拟盘，后端鉴权，前端只负责展示与交互。
	// （Paper trading is operator-owned: admin-only read+write.）
	// 模拟盘为系统级共享的模拟数据（非真实资金），读接口对任一已登录账号开放；
	// 写接口（买入/卖出/清盘/分池配置）仍限管理员，避免越权改动。
	// English: paper is system-scoped simulation data — read endpoints are open to any
	// authenticated user; mutating endpoints stay admin-only.
	s.mux.HandleFunc("GET /api/paper/state", s.authMiddleware(s.handlePaperState))
	s.mux.HandleFunc("GET /api/paper/positions", s.authMiddleware(s.handlePaperPositions))
	s.mux.HandleFunc("GET /api/paper/trades", s.authMiddleware(s.handlePaperTrades))
	s.mux.HandleFunc("GET /api/paper/orders", s.authMiddleware(s.handlePaperOrders))
	s.mux.HandleFunc("GET /api/paper/equity", s.authMiddleware(s.handlePaperEquity))
	s.mux.HandleFunc("GET /api/paper/selfcheck", s.authMiddleware(s.handlePaperSelfCheck))
	s.mux.HandleFunc("POST /api/paper/sell", s.adminMiddleware(s.handlePaperSell))
	s.mux.HandleFunc("POST /api/paper/buy", s.adminMiddleware(s.handlePaperBuy))
	s.mux.HandleFunc("POST /api/paper/reset", s.adminMiddleware(s.handlePaperReset))
	s.mux.HandleFunc("POST /api/paper/pool/reset", s.adminMiddleware(s.handlePaperPoolReset))
	s.mux.HandleFunc("POST /api/paper/pool/config", s.adminMiddleware(s.handlePaperPoolConfig))
	// 持仓（运营账本）：读对所有登录用户开放（系统级共享的大盘持仓）；写仅管理员可操作。
	// （Positions: readable by all; writes are admin-only.）
	s.mux.HandleFunc("POST /api/positions", s.adminMiddleware(s.handleCreatePosition))
	s.mux.HandleFunc("PUT /api/positions/{id}", s.adminMiddleware(s.handleUpdatePosition))
	s.mux.HandleFunc("DELETE /api/positions/{id}", s.adminMiddleware(s.handleDeletePosition))
	s.mux.HandleFunc("POST /api/positions/{id}/exit", s.adminMiddleware(s.handleExitPosition))
	s.mux.HandleFunc("GET /api/positions", s.authMiddleware(s.handleListPositions))

	// fix 兼容端点
	s.mux.HandleFunc("GET /api/kline", s.authMiddleware(s.handleFixKLine))
	s.mux.HandleFunc("GET /api/minute", s.authMiddleware(s.handleFixMinute))
	s.mux.HandleFunc("GET /api/signals", s.authMiddleware(s.handleFixSignals))
	s.mux.HandleFunc("GET /api/status", s.authMiddleware(s.handleFixStatus))
	s.mux.HandleFunc("GET /api/engine_health", s.authMiddleware(s.handleFixEngineHealth))
	// 消息中心：列表对所有登录用户可读（运营数据系统级共享）；清空/删除为写操作，仅管理员可操作。
	s.mux.HandleFunc("GET /api/alerts", s.authMiddleware(s.handleFixAlerts))
	s.mux.HandleFunc("DELETE /api/alerts", s.adminMiddleware(s.handleClearAlerts))
	s.mux.HandleFunc("DELETE /api/alerts/{id}", s.adminMiddleware(s.handleDeleteAlert))
	// 自选股/持仓：读对所有登录用户开放（系统级共享的大盘自选）；写仅管理员可操作。
	s.mux.HandleFunc("GET /api/holdings", s.authMiddleware(s.handleFixGetHoldings))
	s.mux.HandleFunc("POST /api/holdings", s.adminMiddleware(s.handleFixSetHoldings))
	s.mux.HandleFunc("POST /api/holdings/{code}/add", s.adminMiddleware(s.handleFixAddHoldingLot))
	s.mux.HandleFunc("POST /api/holdings/{code}/cost", s.adminMiddleware(s.handleFixSetCost))
	s.mux.HandleFunc("POST /api/holdings/{code}/sell", s.adminMiddleware(s.handleFixSellHolding))
	s.mux.HandleFunc("POST /api/holdings/{code}/close", s.adminMiddleware(s.handleFixCloseHolding))
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
	// §GAP1.8/1.10 实盘端点收权：实盘账本/建议/手动执行仅 admin（单一实盘账户归属老板账号，
	// 堵住"任意登录用户看同一份实盘持仓/临时账号触发真实下单"的越权面）。
	// English: §GAP1.8/1.10 — real-book endpoints are admin-only (single live account owned by the
	// admin), closing the any-user-reads-real-positions / temp-account-fires-orders surface.
	s.mux.HandleFunc("GET /api/positions/real", s.adminMiddleware(s.handleRealPositions))
	s.mux.HandleFunc("GET /api/positions/advice", s.adminMiddleware(s.handleRealAdvice))
	s.mux.HandleFunc("POST /api/positions/execute", s.adminMiddleware(s.handleExecuteAction))
	s.mux.HandleFunc("POST /api/qmt/report", s.qmtReportMiddleware(s.handleQMTReport))
	// QMT 实盘状态/成交：运营数据系统级共享，仅管理员可读（子账号无量化交易权限）。
	s.mux.HandleFunc("GET /api/qmt/state", s.adminMiddleware(s.handleQMTState))
	s.mux.HandleFunc("GET /api/qmt/trades", s.adminMiddleware(s.handleQMTTrades))
	// §R4-1 kill-switch 与手动撤单（admin 权限：紧急停止/撤单属资损级操作）
	s.mux.HandleFunc("POST /api/qmt/halt", s.adminMiddleware(s.handleQMTHalt))
	s.mux.HandleFunc("POST /api/qmt/cancel/{order_id}", s.adminMiddleware(s.handleQMTCancel))
	s.mux.HandleFunc("GET /api/llm-debug", s.authMiddleware(s.handleLLMDebug))
	s.mux.HandleFunc("POST /api/consult", s.authMiddleware(s.handleConsult))
	s.mux.HandleFunc("GET /api/consult/history", s.authMiddleware(s.handleConsultHistory))
	s.mux.HandleFunc("DELETE /api/consult/history", s.authMiddleware(s.handleClearConsultHistory))
	s.mux.HandleFunc("GET /api/consult/pro-mode", s.authMiddleware(s.handleGetConsultProMode))
	s.mux.HandleFunc("PUT /api/consult/pro-mode", s.authMiddleware(s.handleSetConsultProMode))
	s.mux.HandleFunc("GET /api/stage-records", s.authMiddleware(s.handleStageRecords))
	s.mux.HandleFunc("GET /api/signal-logs", s.authMiddleware(s.handleSignalLogs))
	// B5 研究候选审批（仅拥有 research_approve 权限位或 admin 可操作；列表可见）
	s.mux.HandleFunc("GET /api/scheduler/status", s.authMiddleware(s.handleSchedulerStatus))
	s.mux.HandleFunc("GET /api/research/task/{id}/log", s.authMiddleware(s.handleResearchTaskLog))
	s.mux.HandleFunc("GET /api/research/progress", s.permMiddleware(auth.PermResearchApprove, s.handleResearchProgress))
	s.mux.HandleFunc("GET /api/research/factors", s.authMiddleware(s.handleResearchFactors))
	s.mux.HandleFunc("GET /api/research/candidates", s.permMiddleware(auth.PermResearchApprove, s.handleResearchCandidates))
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
	s.mux.HandleFunc("GET /api/research/backtest/running", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestRunning))
	s.mux.HandleFunc("GET /api/research/backtest/list", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestList))
	// 战法库（因子战法）：列出已应用 + 启用/禁用/删除 + 重命名 + 效果监测 + 全量回测全局开关
	s.mux.HandleFunc("GET /api/research/library", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibrary))
	// 阶段3.4 战法库回测入口：对战法库一条已应用规则跑历史回放回测（异步，结果进回测 tab）
	s.mux.HandleFunc("POST /api/research/library/{id}/backtest", s.permMiddleware(auth.PermResearchApprove, s.handleLibraryBacktest))
	// §P2-f 参数优化：入队扫参 / 列表 / 审批（写规则覆盖+热重载）/ 淘汰
	s.mux.HandleFunc("POST /api/backtest/optimize", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizeEnqueue))
	// §D1 各战法独立寻优参数池：列表 + 保存（审批权限，服务端组合数护栏校验）
	s.mux.HandleFunc("GET /api/research/sweep-pools", s.permMiddleware(auth.PermResearchApprove, s.handleSweepPoolList))
	s.mux.HandleFunc("PUT /api/research/sweep-pools", s.permMiddleware(auth.PermResearchApprove, s.handleSweepPoolUpsert))
	s.mux.HandleFunc("GET /api/research/optimizations", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationList))
	s.mux.HandleFunc("POST /api/research/optimizations/{id}/approve", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationApprove))
	s.mux.HandleFunc("POST /api/research/optimizations/{id}/reject", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationReject))
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
// §GAP-20260826 补 Cache-Control: no-store：行情/信号类接口数据每 5s 变化，禁止任何缓存——
// Android WebView 与运营商透明代理对无缓存头的 GET JSON 可能启发式缓存，导致 APK 端
// 「股价不自动刷新/显示陈旧价」。所有 JSON API 统一走本函数出口，一处设置全站生效。
// English: writeJSON emits JSON with Content-Type, status code and Cache-Control: no-store.
// English: Quote/signal payloads change every 5s; without explicit no-store, Android WebView and
// English: carrier transparent proxies may heuristically cache GET JSON, freezing prices on the APK.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 以标准错误结构 {"error": msg} 写入响应。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ── Auth handlers ──

// handleRegister 处理 POST /auth/register：创建用户并返回 token 与用户 ID。
// 用户名/密码缺失返回 400；用户名已存在返回 409。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	// §D7-B 自助注册已关闭（owner 2026-08-26 拍板）：公网部署下匿名开户是隔离体系的天窗，
	// 账号一律由管理员在后台"用户管理"页创建后线下交付账密。
	// English: self-registration disabled by decision D7-B — accounts are admin-created only.
	writeError(w, 403, "注册已关闭：账号由管理员在后台创建，请联系管理员")
}

// handleTemp 处理 POST /auth/temp：创建有效期 14 天的临时演示账号，返回 token/ID/过期时间。
// §GAP2-W2：临时号同样必须携带有效邀请码——匿名领 token 是隔离违例的头号放大器，此处关闸。
func (s *Server) handleTemp(w http.ResponseWriter, r *http.Request) {
	// §D7-B 匿名临时号与注册同批关闭——此前匿名领 14 天 token 是隔离违例的头号放大器。
	// English: anonymous temp accounts are closed together with registration (decision D7-B).
	writeError(w, 403, "注册已关闭：临时账号功能已停用，请联系管理员创建正式账号")
}

// loginReq 登录请求体：用户名 + 密码。
type loginReq struct {
	Username string `json:"username"` // 用户名
	Password string `json:"password"` // 密码
}

// handleLogin 处理 POST /auth/login（/api/auth/login 同路由）：校验凭据并返回 token/ID/账号名。
// 凭据错误返回 401。§A4 频控 10 次/分/IP（防撞库+bcrypt CPU 放大 DoS）；
// 错误文案统一 "invalid credentials"（此前区分"用户不存在/密码错"可枚举用户名）。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("login|"+clientIP(r), 10, time.Minute) {
		writeError(w, 429, "too many attempts")
		return
	}
	var req loginReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		// §A4 统一文案：不区分"用户不存在/密码错"，阻断用户名枚举
		writeError(w, 401, "invalid credentials")
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
	Username     string `json:"username"`      // 管理员用户名
	Password     string `json:"password"`      // 管理员密码
	LLMApiURL    string `json:"llm_api_url"`   // LLM 接口地址
	LLMApiKey    string `json:"llm_api_key"`   // LLM 接口密钥
	TushareToken string `json:"tushare_token"` // Tushare 数据平台 token
	SetupToken   string `json:"setup_token"`   // §P1-5 初始化令牌（SETUP_TOKEN 开启时必填）
}

// handleSetupSubmit 处理 POST /setup：完成首次初始化。
// 创建管理员账号，将非空的 LLM/Tushare 配置写入用户配置，并标记系统已初始化。
// §A6 改造：检查-创建-标记经 Manager.SetupInitialAdmin 原子完成（此前 IsInitialized 与
// CreateUser 之间存在 TOCTOU，并发双请求可产生两个 admin）；已初始化返回 400。
func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("setup|"+clientIP(r), 5, time.Minute) {
		writeError(w, 429, "too many attempts")
		return
	}

	var req setupReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	// §P1-5 SETUP_TOKEN 守卫：环境变量已配置时，POST /setup 必须携带匹配令牌，否则拒绝抢跑初始化。
	if s.setupToken != "" {
		provided := req.SetupToken
		if provided == "" {
			provided = r.Header.Get("X-Setup-Token")
		}
		if !hmac.Equal([]byte(provided), []byte(s.setupToken)) {
			writeError(w, 401, "setup token required")
			return
		}
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.SetupInitialAdmin(req.Username, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "already initialized") {
			writeError(w, 409, "already initialized")
			return
		}
		writeError(w, 500, err.Error())
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

	writeJSON(w, 200, map[string]interface{}{
		"token": user.Token,
		"id":    user.ID,
	})
}

// ── §A4 匿名端点 IP 频控 ────────────────────────────────────────────────
// register/temp/login/setup 完全开放且无频控：可被脚本灌号+撞库。进程内滑动窗口
// 计数器（单实例部署足够；多实例需外置网关）。

// ipLimiter 进程内滑动窗口频控器（单实例部署足够；多实例需外置网关）。
// 用于 register/temp/login/setup 等匿名端点的防刷与防撞库。
type ipLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time // key=IP，value=该 IP 近期的请求时间戳序列
}

// allow 滑动窗口判定：window 内该 IP 已达 max 次则拒绝。
func (l *ipLimiter) allow(ip string, max int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = make(map[string][]time.Time)
	}
	recent := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= max {
		l.hits[ip] = recent
		return false
	}
	l.hits[ip] = append(recent, now)
	return true
}

// ── §GAP2-W1 客户端 IP 提取（可信代理收口）─────────────────────────────────────
// 旧实现无条件信任 X-Forwarded-For 第一个值——公网直连 8080 的攻击者可随请求伪造该头，
// 每次都伪装成全新 IP，register/temp/login/setup 的匿名限流全部失效（撞库+灌号+内存放大）。
// 新语义：
//   1. TCP 对端（RemoteAddr）不在可信代理网段内 → 直接采用对端 IP，完全无视 XFF（不可伪造）；
//   2. 对端是可信代理（同机 Caddy 反代的 127.0.0.1 等）→ 从 XFF 自右向左跳过可信跳，
//      取第一个不可信地址作为真实客户端 IP（标准 XFF 解析方向）；
//   3. 全部条目均可信/解析失败 → 回退对端 IP。
// 可信网段默认覆盖环回 + 内网段（Caddy 与应用同机的部署形态），可用环境变量
// QUANT_TRUSTED_PROXIES 覆盖（逗号分隔 CIDR）。
// English: §GAP2-W1 client-IP extraction with trusted-proxy handling. The old code blindly trusted
// the first X-Forwarded-For value, so direct-to-8080 attackers could rotate fake IPs per request and
// defeat every anonymous rate limit. Now: untrusted peer → use peer address only; trusted proxy →
// walk XFF right-to-left skipping trusted hops; QUANT_TRUSTED_PROXIES overrides the default CIDR set.

var trustedProxyCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8", "::1/128", // 环回（Caddy 同机反代）
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", // RFC1918 内网
		"169.254.0.0/16", "fe80::/10", // 链路本地
	}
	if env := os.Getenv("QUANT_TRUSTED_PROXIES"); env != "" {
		cidrs = strings.Split(env, ",")
	}
	var out []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if _, ipnet, err := net.ParseCIDR(c); err == nil {
			out = append(out, ipnet)
		} else {
			log.Printf("[server] QUANT_TRUSTED_PROXIES 条目无效，已忽略: %q (%v)", c, err)
		}
	}
	return out
}()

// isTrustedProxy 判断给定 IP 是否属于可信代理网段。
// English: reports whether the IP falls within a trusted proxy CIDR.
func isTrustedProxy(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range trustedProxyCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientFromXFF 从 X-Forwarded-For 中自右向左取第一个不可信地址（真实客户端）。
// 全部可信或无有效条目时返回空串，由调用方回退 RemoteAddr。
// English: picks the right-most untrusted (real client) entry from XFF; "" when nothing usable.
func clientFromXFF(xff string) string {
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		cand := strings.TrimSpace(parts[i])
		if cand == "" {
			continue
		}
		if !isTrustedProxy(cand) {
			return cand
		}
	}
	return ""
}

// clientIP 提取请求来源 IP（§GAP2-W1 可信代理收口版，语义见上方块注释）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// 直连（非可信代理）：XFF 是攻击者可控的任意字符串，一律不采信。
	if !isTrustedProxy(host) {
		return host
	}
	// 经可信代理转发：解析 XFF 取真实客户端；拿不到则退回代理自身地址（限流粒度降级但安全）。
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if client := clientFromXFF(xf); client != "" {
			return client
		}
	}
	return host
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

// authMiddleware 认证中间件：校验 Authorization Bearer 令牌（兼容有无 "Bearer " 前缀），
// 有效时将用户注入请求上下文（ctxUserKey）后放行，否则 401。
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
			writeError(w, 403, "无权限：该接口仅管理员账号可用")
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
		"auction":       data.Auction, // §同花顺（新）竞价快照（9:15-9:26 窗口内非空）
		"short_enabled": s.shortOnFor(userID),
	}
	if data.Report != nil || s.rpt != nil {
		rpt := data.Report
		if rpt == nil {
			rpt = s.rpt // 落盘缓存不含 Report（json:"-"），回退到持久化报表库
		}
		total, holding, win, wr, avgW, avgL := rpt.Stats()
		resp["report_stats"] = map[string]interface{}{
			"total":        total,
			"holding":      holding,
			"win":          win,
			"win_rate":     wr,
			"avg_win_pct":  avgW,
			"avg_loss_pct": avgL,
			"by_strategy":  rpt.StatsByStrategy(""), // 按战法分组的胜率/盈亏比明细
		}
		resp["report_logs"] = rpt.List()
	}
	writeJSON(w, 200, resp)
}

// longToggleReq 做多开关请求体。
type longToggleReq struct {
	Enabled bool `json:"enabled"` // 是否启用做多
}

// handleLongToggle 处理 POST /api/long/toggle：切换做多开关（按账号持久化）并返回最新状态。
func (s *Server) handleLongToggle(w http.ResponseWriter, r *http.Request) {
	var req longToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	userID := s.operatorID()
	if s.cfg != nil {
		cur := s.cfg.GetLongShortConfigFor(userID)
		cur.LongEnabled = req.Enabled
		s.cfg.SetLongShortConfigFor(userID, cur)
	}
	if c := s.ctrlFor(userID); c != nil {
		c.SetLongEnabled(req.Enabled)
	}
	log.Printf("[server] 账号 %s 做多开关: %v", userID, req.Enabled)
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOnFor("")})
}

// handleLongStatus 处理 GET /api/long/status：返回运营数据归属账号当前做多开关状态。
func (s *Server) handleLongStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOnFor("")})
}

// shortToggleReq 做空开关请求体。
type shortToggleReq struct {
	Enabled bool `json:"enabled"` // 是否启用做空
}

// handleShortToggle 处理 POST /api/short/toggle：切换做空开关（按账号持久化）并返回最新状态。
func (s *Server) handleShortToggle(w http.ResponseWriter, r *http.Request) {
	var req shortToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	userID := s.operatorID()
	if s.cfg != nil {
		cur := s.cfg.GetLongShortConfigFor(userID)
		cur.ShortEnabled = req.Enabled
		s.cfg.SetLongShortConfigFor(userID, cur)
	}
	if c := s.ctrlFor(userID); c != nil {
		c.SetShortEnabled(req.Enabled)
	}
	log.Printf("[server] 账号 %s 做空开关: %v", userID, req.Enabled)
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOnFor("")})
}

// handleShortStatus 处理 GET /api/short/status：返回运营数据归属账号当前做空开关状态。
func (s *Server) handleShortStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOnFor("")})
}

// newsShowAllReq 资讯"显示全部"开关请求体。
type newsShowAllReq struct {
	Enabled bool `json:"enabled"` // 是否显示全部资讯
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
	Title  string `json:"title"`            // 资讯标题
	Digest string `json:"digest,omitempty"` // 正文摘要（可选）
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
	Code          string  `json:"code"`                      // 股票代码
	Name          string  `json:"name"`                      // 股票名称
	Direction     string  `json:"direction"`                 // 方向（long/short）
	Strategy      string  `json:"strategy"`                  // 触发策略
	EntryPrice    float64 `json:"entry_price"`               // 开仓价
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"` // 止盈百分比
	StopLossPct   float64 `json:"stop_loss_pct,omitempty"`   // 止损百分比
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
	TakeProfitPct *float64 `json:"take_profit_pct,omitempty"` // 新止盈百分比（nil=不修改）
	StopLossPct   *float64 `json:"stop_loss_pct,omitempty"`   // 新止损百分比（nil=不修改）
	Name          *string  `json:"name,omitempty"`            // 新名称（nil=不修改）
}

// ownsPosition §A2 归属校验：持仓记录属于当前登录用户（或 admin 全权）才可写。
// 此前三写接口（update/delete/exit）只认路径 id 不看归属——任意注册用户可改/删/
// 平仓他人持仓（IDOR）。空 UserID 的系统级记录仅 admin 可动。
// English: ownership gate for the position write APIs — closes an IDOR where any logged-in
// user could mutate another account's positions.
func (s *Server) ownsPosition(r *http.Request, id string) bool {
	u := userFromContext(r)
	if u == nil {
		return false
	}
	if u.Role == auth.RoleAdmin {
		return true
	}
	log := s.rpt.FindBySignalID(id)
	if log == nil {
		return true // 不存在：放行让下游自行处理，不提前泄露存在性
	}
	return log.UserID != "" && log.UserID == u.ID // 空 UserID=系统级记录，仅 admin 可动
}

// handleUpdatePosition 处理 PUT /api/positions/{id}：按 ID 更新持仓的止盈/止损/名称字段。
func (s *Server) handleUpdatePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.ownsPosition(r, id) {
		writeError(w, 403, "not your position")
		return
	}
	var req updatePositionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
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

// exitPositionReq 平仓请求体：平仓价格。
type exitPositionReq struct {
	ExitPrice float64 `json:"exit_price"` // 平仓价格
}

// handleDeletePosition 处理 DELETE /api/positions/{id}：软删除指定持仓记录。
func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.ownsPosition(r, id) { // §A2
		writeError(w, 403, "not your position")
		return
	}
	s.rpt.Delete(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleExitPosition 处理 POST /api/positions/{id}/exit：按平仓价计算盈亏并标记止盈/止损。
func (s *Server) handleExitPosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.ownsPosition(r, id) { // §A2
		writeError(w, 403, "not your position")
		return
	}
	var req exitPositionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
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
	// 持仓为运营数据，统一归属管理员（系统级共享），按 operatorID 读取统计与列表。
	uid := s.operatorID()
	logs := s.rpt.ListFor("")
	reportStats := map[string]interface{}{}
	total, holding, win, wr, avgW, avgL := s.rpt.StatsFor(uid)
	reportStats["total"] = total
	reportStats["holding"] = holding
	reportStats["win"] = win
	reportStats["win_rate"] = wr
	reportStats["avg_win_pct"] = avgW
	reportStats["avg_loss_pct"] = avgL
	reportStats["by_strategy"] = s.rpt.StatsByStrategy(uid) // 按运营账号、按战法分组的胜率明细
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
	APIKey     string   `json:"api_key,omitempty"`  // 单个 API 密钥（可空）
	APIKeys    []string `json:"api_keys,omitempty"` // 多 API 密钥（逗号分隔或数组，轮询分发；为空时回退 APIKey）
	APIURL     string   `json:"api_url"`            // LLM 接口地址
	Model      string   `json:"model"`              // 模型名
	TimeoutSec int      `json:"timeout_sec"`        // 单次请求超时（秒），缺省 0
	Stream     *bool    `json:"stream,omitempty"`   // 流式开关，缺省维持现状/默认开启
	// BatchConcurrency 新闻归因 LLM 批量并发批次，<=0 时维持现状/默认 4。
	// （BatchConcurrency is the news-attribution LLM batch concurrency; <=0 keeps current/default 4.）
	BatchConcurrency int `json:"batch_concurrency,omitempty"`
	// D1MaxTokens D1 评分 LLM 单次调用推理长度上限（§信号速度 S3），<=0 维持现状/默认 2048。
	// （D1MaxTokens is the D1-scoring max_tokens cap (§speed S3); <=0 keeps current/default 2048.）
	D1MaxTokens int `json:"d1_max_tokens,omitempty"`
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
		for _, k := range splitLLMKeys(v) {
			apiKeys = append(apiKeys, maskSecret(k)) // §GAP2-W2 脱敏回显
		}
	}
	if len(apiKeys) == 0 {
		// 兼容旧单 key 配置
		if v, ok := s.auth.GetConfig(uid, "llm_api_key"); ok && v != "" {
			apiKeys = []string{maskSecret(v)}
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
		"d1_max_tokens":     cfg.D1MaxTokens,
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

// requestUserIDSafe requestUserID 的 nil 安全版：测试可直传 nil request。
func requestUserIDSafe(r *http.Request) string {
	if r == nil {
		return ""
	}
	return requestUserID(r)
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

// operatorID 返回运营数据归属账号（管理员）ID。所有运营数据（量化/模拟盘/看板/告警/LLM）
// 统一归属该账号，后端按角色做访问控制，前端只负责展示与交互。
// （operatorID returns the operator (admin) account that owns all operational data.）
func (s *Server) operatorID() string {
	if s.auth == nil {
		return ""
	}
	return s.auth.AdminID()
}

// maskSecret §GAP2-W2 密钥脱敏（P2-4）：保留前 4 后 4，中段以 … 掩盖；短密钥全掩。
// English: masks a secret, keeping first/last 4 chars; short secrets are fully masked.
func maskSecret(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len(k) < 12 {
		return "***"
	}
	return k[:4] + "…" + k[len(k)-4:]
}

// isMaskedSecret 判断提交值是否为脱敏哨兵（GET 回显形态）——是则保持原值不变。
// English: reports whether the submitted value is a masked sentinel echoed from GET.
func isMaskedSecret(k string) bool {
	return k == "***" || strings.Contains(k, "…")
}

// validateGatewayURL 校验量化实盘网关地址（内部可信端点，与公网外呼不同）：
// 仅要求 http/https 协议，允许环回/私网地址（网关就部署在首尔机本机 127.0.0.1 或
// 广州执行机内网，属预期内网拓扑，不能按公网外呼的 fail-closed 标准拒绝）。
// 用于 POST /api/config/qmt 的 gateway_url 字段。
// English: validates the (internal, trusted) QMT gateway URL — http(s) only, and loopback/
// private addresses are permitted because the gateway intentionally lives on localhost/LAN.
func validateGatewayURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("URL 解析失败")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅允许 http/https")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("缺少主机名")
	}
	return nil
}

// validatePublicURL §GAP2-W2 外呼 URL 校验（scrm P1-f 同源思路）：
// 仅允许 http/https；域名解析后逐 IP 拒绝环回/私网/链路本地(含云元数据)/未指定/组播，
// 解析失败一律拒绝（fail-closed）。用于 LLM api_url 与通知 webhook 等服务器外呼地址。
// English: validates an outbound URL: http(s) only, and every resolved IP must not be
// loopback/private/link-local/unspecified/multicast; resolution failure rejects (fail-closed).
func validatePublicURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("URL 解析失败")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅允许 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("缺少主机名")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("主机解析失败: %v", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("禁止指向内网/保留地址: %s", ip)
		}
	}
	return nil
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
		D1MaxTokens:      req.D1MaxTokens,
	})

	// §GAP2-W2 出呼地址校验（SSRF 面收口）：非法直接拒绝，不落任何配置
	if req.APIURL != "" {
		if err := validatePublicURL(req.APIURL); err != nil {
			writeError(w, 400, "api_url "+err.Error())
			return
		}
	}

	// 保存 APIKey 到 auth config（按账号隔离）；§GAP2-W2 脱敏哨兵不覆盖原值
	if req.APIKey != "" && !isMaskedSecret(req.APIKey) {
		s.auth.SetConfig(uid, "llm_api_key", req.APIKey)
	}

	// 保存多 API 密钥到 auth config（逗号分隔）；为空时维持现状；
	// §GAP2-W2 逐位处理脱敏哨兵——提交 "sk-…abcd" 形态的槽位保留库中原值
	if len(req.APIKeys) > 0 {
		existing := ""
		if v, ok := s.auth.GetConfig(uid, "llm_api_keys"); ok {
			existing = v
		}
		old := splitLLMKeys(existing)
		out := make([]string, 0, len(req.APIKeys))
		for i, k := range req.APIKeys {
			switch {
			case isMaskedSecret(k) && i < len(old):
				out = append(out, old[i]) // 哨兵 → 原值
			case isMaskedSecret(k):
				// 哨兵但无对应原值（异常提交）：跳过，避免把掩码存成真钥
			default:
				out = append(out, k)
			}
		}
		if len(out) > 0 {
			s.auth.SetConfig(uid, "llm_api_keys", strings.Join(out, ","))
		}
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
// §FIX-0921c：内存 debugInfo 为空（重启窗口/懒建）时回落操作员账号当日落盘的最新一轮，
// 保证前端单轮兜底源（LLMDebug/LogModal 的回落链路）始终有 L1/L2 数据可取。
func (s *Server) handleLLMDebug(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	di := c.GetDebugInfo()
	if di == nil {
		if disk := s.operatorStageRecordsFromDisk(); len(disk) > 0 {
			di = &disk[len(disk)-1] // 落盘顺序即时间正序，取最后一轮（最新）
		}
	}
	if di == nil {
		writeJSON(w, 200, map[string]string{"status": "no_data"})
		return
	}
	writeJSON(w, 200, di)
}

// consultReq 股票咨询请求体：用户消息。
type consultReq struct {
	Message string `json:"message"` // 用户咨询消息
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
	Enabled bool `json:"enabled"` // 是否启用专业模式
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
	uid := requestUserID(r)
	c := s.ctrlFor(uid)
	if c == nil {
		writeJSON(w, 200, []data.ConsultMessage{})
		return
	}
	// §GAP2-W2 只返回本人账号目录下的咨询历史
	h := c.GetConsultHistoryFor(uid)
	if h == nil {
		h = []data.ConsultMessage{}
	}
	writeJSON(w, 200, h)
}

// handleClearConsultHistory 处理 DELETE /api/consult/history：清空当日咨询对话。
func (s *Server) handleClearConsultHistory(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	if c := s.ctrlFor(uid); c != nil {
		c.ClearConsultHistoryFor(uid) // §GAP2-W2 只清本人账号的历史
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleStageRecords 返回当日全量 Stage 流水线轮次记录（用于复盘/策略引擎实时调取）。
// §FIX-0921c（2026-09-01 用户反馈实录「LLM 页白板」）：内存为空（引擎重启后当日轮次尚未
// 产出/懒建窗口/跨实例差异）时回落读**操作员账号目录的当日落盘文件**（引擎每次捕获都会
// persistStageRecords，磁盘上始终有当日最新 20 轮），保证前端即使无价值轮次也能如实展示
// L1/L2；同时打诊断日志（uid/来源/条数）便于远程定位用户侧真实返回。
func (s *Server) handleStageRecords(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	c := s.ctrlFor(uid)
	if c == nil {
		log.Printf("[llm-diag] uid=%s stage-records: ctrl=nil → no_engine", uid)
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	recs := c.GetStageRecords()
	src := "mem"
	if len(recs) == 0 {
		if disk := s.operatorStageRecordsFromDisk(); len(disk) > 0 {
			recs = disk
			src = "disk"
		}
	}
	log.Printf("[llm-diag] uid=%s stage-records: src=%s recs=%d", uid, src, len(recs))
	if recs == nil {
		recs = []newsagent.DebugInfo{}
	}
	// §FIX-0921d 响应瘦身（2026-09-01 实录）：单轮全量 Stage2 事件含长文本理由，20 轮合计
	// ~700KB——弱网下前端取数超时 → 「LLM 页白板」。仅最近 5 轮保留完整 L2 事件明细（页面
	// 默认展示最新轮），更早轮次保留 L1（原始标题/计数，raw_count/selected_count 真实值不动），
	// 体积降至 ~1/3。recs 为时间正序（引擎原始序），最旧的在头部。
	const fullDetailKeep = 5
	if len(recs) > fullDetailKeep {
		for i := 0; i < len(recs)-fullDetailKeep; i++ {
			recs[i].Stage2Events = nil
		}
	}
	// 就地倒序，最新轮次的记录排在最前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}

// operatorStageRecordsFromDisk 读操作员账号目录的当日 stage_records.json（内存为空时的兜底源）。
// 仅当日交易日的记录有效（与引擎 loadStageRecords 同口径，跨日自动视为空）。
// English: reads the operator account's on-disk stage_records.json as a fallback when the in-memory
// records are empty; only same-trading-day records are served (same rule as the engine loader).
func (s *Server) operatorStageRecordsFromDisk() []newsagent.DebugInfo {
	dir := s.cacheDir
	if dir == "" {
		return nil
	}
	op := s.operatorID()
	if op == "" {
		return nil
	}
	p := filepath.Join(dir, "accounts", op, "stage_records.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var f struct {
		TradingDay string                `json:"trading_day"`
		Records    []newsagent.DebugInfo `json:"records"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// operatorSignalLogsFromDisk 读操作员账号目录的当日 signal_records.json（内存为空时的兜底源）。
// English: reads the operator account's on-disk signal_records.json as a fallback when the in-memory
// signal logs are empty; same-trading-day records only.
func (s *Server) operatorSignalLogsFromDisk() []combat_agent.SignalLog {
	dir := s.cacheDir
	if dir == "" {
		return nil
	}
	op := s.operatorID()
	if op == "" {
		return nil
	}
	p := filepath.Join(dir, "accounts", op, "signal_records.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var f struct {
		TradingDay string                   `json:"trading_day"`
		Records    []combat_agent.SignalLog `json:"records"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// handleSignalLogs 返回当日全量信号批次记录（用于"信号日志"弹窗按批次复盘）。
// §FIX-0921c：内存为空时回落操作员账号当日落盘（同 stage-records 口径），并打诊断日志。
func (s *Server) handleSignalLogs(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	c := s.ctrlFor(uid)
	if c == nil {
		log.Printf("[llm-diag] uid=%s signal-logs: ctrl=nil → no_engine", uid)
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	recs := c.GetSignalLogs()
	src := "mem"
	if len(recs) == 0 {
		if disk := s.operatorSignalLogsFromDisk(); len(disk) > 0 {
			recs = disk
			src = "disk"
		}
	}
	log.Printf("[llm-diag] uid=%s signal-logs: src=%s recs=%d", uid, src, len(recs))
	if recs == nil {
		recs = []combat_agent.SignalLog{}
	}
	// §FIX-0921d 响应瘦身：仅最近 5 批保留完整信号明细，更早批次截断到前 20 条（批次时间/
	// 原始条数保留，复盘仍可辨认）。
	const fullDetailKeep = 5
	if len(recs) > fullDetailKeep {
		for i := 0; i < len(recs)-fullDetailKeep; i++ {
			if len(recs[i].Signals) > 20 {
				recs[i].Signals = recs[i].Signals[:20]
			}
		}
	}
	// 就地倒序，最新批次的记录排在最前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}
