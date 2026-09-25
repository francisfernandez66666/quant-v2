// ─────────────────────────────────────────────────────────────────────────────
// 文件概述（internal/server/server.go）
//
// 本文件实现量化交易系统的 HTTP 服务端（quant-trading-v2 的 Web/ API 入口），
// 主要职责：
//
//  1. 路由注册（registerRoutes）：在一个 http.ServeMux 上集中注册全部 REST 端点，
//     覆盖认证/初始化、看板数据、策略/D1/LLM/QMT 配置、模拟盘、实盘交易、
//     持仓台账、自选股、消息中心、新闻归因、研究候选/回测、运维指标等。
//
//  2. 鉴权与访问控制：基于 Bearer Token 的会话认证（authMiddleware）、
//     管理员角色门槛（adminMiddleware）、细粒度权限位门槛（permMiddleware）；
//     配套登录/初始化端点的 IP 滑动窗口频控（ipLimiter）与
//     高成本业务端点的按用户频控（userRateLimit）。
//
//  3. 多租户 / 多账号：租户级 API 频控（tenantLimiter）、按账号隔离的引擎
//     控制面路由（ctrlFor/liveCtrlFor/dashFor，经 EngineRegistry 懒加载），
//     运营数据统一归属管理员账号（operatorID）。
//
//  4. SSE 实时推送：SSEBroker 事件广播 + 短时效建链票据（sseTickets，§M5 起 TTL 内可复用）鉴权，
//     供浏览器 EventSource 建立只读事件流。
//
//  5. 通用中间件链（chain）：panic 恢复（recoverMiddleware）在最外层兜底，
//     CORS（corsMiddleware）按同源/白名单收紧放行，二者包裹全部请求。
//
//  6. 安全加固：请求体大小上限（maxBodyBytes）、可信代理下的真实客户端 IP
//     提取（clientIP/trustedProxyCIDRs）、外呼 URL 的 SSRF 校验
//     （validatePublicURL）、密钥脱敏回显（maskSecret）等。
//
//  7. 缓存与兜底：看板快照原子落盘/回读（cacheDash/loadCachedDash）、
//     宏观日历/IPO/新闻/同花顺板块等多级 TTL 缓存，以及内存为空时
//     从磁盘回读当日 stage/signal 记录的兜底链路。
//
// 处理器实现按业务域拆分在同目录的其他文件（admin.go/paper.go/qmt.go/
// research.go/handlers_fix.go/sse.go 等），本文件承载服务骨架与核心中间件。
// ─────────────────────────────────────────────────────────────────────────────

// Package server HTTP 服务端：提供看板数据、策略配置、持仓管理、做空开关等 REST API。
package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/signalctl"
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
	// ConsultLLM §FIX-4(20260919)：首参为请求 ctx（r.Context()），用户断开即中止出呼链。
	ConsultLLM(ctx context.Context, userID, userMsg string, proMode bool) (string, error)
	GetConsultHistoryFor(userID string) []data.ConsultMessage // §GAP2-W2 按账号隔离的咨询历史
	ClearConsultHistoryFor(userID string)                     // §GAP2-W2 只清本人的
	// DashboardData 返回该账号/引擎的当前看板快照（信号/评分/新闻事件/开关状态等）。
	// English: returns the current dashboard snapshot for this account/engine (signals/scores/news/toggles).
	DashboardData() *display.DashboardData
	// SignalVerdicts §SIGNAL_CONTROLLER 返回该引擎信号控制器最近的裁定留痕（最新在前）——
	// 实盘/模拟盘两通道 pass/hold/block+原因，供审计端点回答"提醒了为何没成交"。
	// English: recent signal-controller verdict tail (newest first) for the audit endpoint.
	SignalVerdicts(limit int) []signalctl.Decision
	// IgnoreSignal §F-1（20260917）：手动忽略信号——对 code@strategy 打失效墓碑并移除消息，
	// 返回墓碑条数；strategy 为空忽略该 code 当日全部。
	IgnoreSignal(code, strategy string) int
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
	// §A7（20260918 审计批）buildCommit：构建期 -ldflags 注入的 git 短指纹，经 SetBuildCommit
	// 传入，随 GET /api/status 的 build_commit 字段下发。前端（尤其 APK 内嵌 assets）在启动与
	// 60s 轮询中比对本地构建指纹与服务端报告值，不一致即顶栏横幅告警"内嵌前端已过期"。
	// English: §A7 — git short SHA stamped at build time, exposed via /api/status so the
	// (APK-bundled) frontend can detect its embedded assets drifting behind the deployed backend.
	buildCommit string

	llmMu      sync.Mutex // 保护 runtimeLLM/runtimeURL 与 LLM 快照的互斥锁
	runtimeLLM string     // 运行时实际使用的 model（与文件配置可能不同）
	runtimeURL string     // 运行时实际使用的 API 地址
	// llmApplyMu 串行化「探测 → 热切换 → 落库」这一整套动作（见 llm_apply.go）。
	// 盘中连点保存、或保存与回滚并发时，两个流程交错会让"谁最后生效"不可预测——
	// 而这里预测错就是线上 LLM 客户端被换错。只保护写路径，不参与请求路径。
	llmApplyMu sync.Mutex
	// llmLastApplied 最后一次**生效**的运行时配置快照（含明文密钥，仅内存）。
	llmLastApplied *llmSnapshot
	// llmLastGood 最后一次**经验证可用**的运行时配置快照：一键回滚的目标。
	// 与 lastApplied 分开维护——正是"force 强行应用了未验证配置"这条路径才需要回滚。
	llmLastGood *llmSnapshot
	// consultInflight §FIX-4(20260919) 每用户咨询并发闸门（uid → struct{}）：一次咨询出呼
	// 可长达分钟级，同一用户连点发送会让多条出呼并行（费用×N、且共用会话历史互相污染）。
	// busy 时第二个请求直接 429，回复落定（defer）即释放。
	consultInflight sync.Map
	limiter         ipLimiter // §A4 匿名端点 IP 频控（register/temp/login/setup）
	// tenantLimiter §MT 租户级业务 API 频控：key=租户 ID，窗口 1 分钟，
	// 上限取租户配额 Quota.APIRatePerMin（0=默认 600/min）。
	tenantLimiter ipLimiter
	// §P1-5 初始化令牌：若环境变量 SETUP_TOKEN 非空，POST /setup 必须携带匹配令牌
	// （body.setup_token 或 X-Setup-Token 头），否则拒绝。防止未授权者抢跑初始化。
	// English: P1-5 setup token — when SETUP_TOKEN env is set, POST /setup requires the matching token.
	setupToken string

	// §WS-F C4a SSE 建链票据：SSE 用 Authorization 头（浏览器 EventSource 不支持），
	// 改为「短时 ticket」——POST /api/events/ticket 取 60s 有效随机票，
	// SSE URL /api/events?ticket=xxx 校验。§M5 起票据 TTL 内可复用（保原生重连），
	// 泄漏可利用窗口 ≤60s 且只授予本账号只读事件流。
	// English: WS-F C4a — EventSource cannot set Authorization headers, so SSE uses a short-lived
	// ticket minted at POST /api/events/ticket (60s TTL, bound to the issuing user; reusable within
	// the TTL since §M5 so the browser's native reconnect keeps working).
	sseTicketsMu sync.Mutex
	sseTickets   map[string]sseTicket

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

	// §C9-UX（2026-09-22 PM 批清扫）通知器注入：/api/notify-test 从空 stub 升级为
	// 真实连通性探测（webhook/网关/ntfy 逐通道试发）。nil=独立 server 模式，接口回显 noop。
	notifier *notify.Notifier
	// §N-2（2026-09-22 傍晚批）notify-test 进程内频控：该端点打的是全局推送通道（server 级
	// 单例 notifier），按账号限流挡不住多管理员合流刷 owner 手机，故取**全进程最小间隔 60s**
	// 这一最严口径——命中回 429 + Retry-After。仅内存态（重启即清零）：探测本身无副作用
	// 持久化诉求，没必要为它落库。
	// English: §N-2 — process-wide minimum interval (60s) for /api/notify-test; the endpoint
	// blasts the GLOBAL channels, so the throttle is per-process rather than per-account.
	notifyTestMu     sync.Mutex
	notifyTestLastAt time.Time // 最近一次被受理的探测时刻（零值=从未）
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
	// SetPaperConfig §F-4（20260917）账户级模拟盘撮合配置热同步：更新注册表模板与全部
	// 已建账号引擎（POST /api/paper/config 消费，自本批起转正）。
	SetPaperConfig(cfg paper.Config)
	// SetPaperLabelResolver §C 注入规则池 ID→显示名 解析器（fac_1→"因子战法#1"），
	// 同步到全部已建账号并供懒加载引擎继承。English: injects the rule-pool label resolver.
	SetPaperLabelResolver(fn func(string) string)
	// TriggerPositionReview §DAILY_REVIEW 手动触发指定账号的盘后持仓 LLM 复盘，返回成功复盘的股票数。
	// English: force-runs the §DAILY_REVIEW after-hours LLM position review for one account; returns count.
	TriggerPositionReview(userID string) (int, error)
	// OpenPositionStrategyCounts §EXIT-RETAIN 当前开放持仓按策略键（持仓 Strategy 原文，=规则 ID
	// 或显示名）计数：实盘账本 ∪ 全部账号模拟盘账本，一笔持仓只记一个键。战法库端点用它给每条
	// 战法标 open_positions，让操作员在点「停用」前就看到"这条还有几笔仓"。
	// English: open positions counted by strategy key (live book ∪ every account's paper book), one key
	// per position; the library payload surfaces it as open_positions.
	OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys
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

// liveCtrlFor §2026-09-07 多账号实盘：返回实盘读取路径归属的引擎控制面——
// 按调用方账号自身路由（其引擎带独立 QMT 控制器/gateway），运营账号行为与 ctrlFor 一致。
// 仅用于实盘链路（持仓/资金/网关状态/熔断），看板等运营数据仍走 ctrlFor（系统级共享）。
// English: engine controller for live-trading read paths — routed to the CALLER's own account so each
// account sees its own QMT controller/gateway; identical to ctrlFor for the operator account.
func (s *Server) liveCtrlFor(userID string) EngineController {
	if s.registry != nil {
		return s.registry.GetController(userID)
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

// SetBuildCommit 注入构建期 git 指纹（main.buildCommit），供 /api/status 下发给前端做版本比对（§A7）。
// English: §A7 — injects the build-time git commit so /api/status can report it to the frontend.
func (s *Server) SetBuildCommit(c string) { s.buildCommit = c }

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

// SetNotifier §C9-UX 注入全局通知器（/api/notify-test 真实探测用；nil 时接口回显 noop）。
// English: §C9-UX — installs the global notifier so /api/notify-test can probe live channels.
func (s *Server) SetNotifier(n *notify.Notifier) { s.notifier = n }

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
// §F3（2026-09-22 修复批）404/405 统一 JSON 信封的接线在 muxWithJSONErrors（同文件）：
// 本工具链（go1.26 路由重构版）的 http.ServeMux 已无 NotFound/MethodNotAllowed 挂载字段，
// 改为在 mux 出口前用 mux.Handler(r) 预判「未命中任何注册 pattern」并走 writeError。
// 若上游 Go 恢复该字段，可迁移为 s.mux.NotFound/s.mux.MethodNotAllowed 直挂同一出口。
func (s *Server) registerRoutes() {
	// ── 认证/初始化端点：完全匿名可达（注册/临时号实际已关闭，返回 403 提示文案）──
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/temp", s.handleTemp)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	// §APPVER 2026-09-22 C批：APK 强制更新的版本检查端点。**故意不套 authMiddleware**——
	// 原生壳在登录前就要拉取更新单（旧版本可能因鉴权链路缺陷根本登不进来，更新通道若依赖
	// 登录即死锁），只读发布单无副作用，泄露面仅为「公开可知的自家 APK 版本号+下载地址」。
	// English: §APPVER public by design — the native shell checks for updates *before* login,
	// so this read-only endpoint must stay auth-free (a login-gated update channel would deadlock).
	s.mux.HandleFunc("GET /api/app/version", s.handleAppVersion)
	// §D7 自助退出：吊销当前 Bearer 对应的服务端会话（旧行为只清浏览器 localStorage，
	// 令牌在服务端 Sessions 里长期有效，换设备/清缓存后仍可被截获回放）。
	// English: self-service logout revokes the presenting token's server-side session.
	s.mux.HandleFunc("POST /api/auth/logout", s.authMiddleware(s.handleLogout))
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
	// §U-5 脏账号批量清理（temp_/过期）。static 段 "cleanup" 优先于 {id}，二者路径段数不同不冲突。
	s.mux.HandleFunc("POST /api/admin/users/cleanup", s.adminMiddleware(s.handleCleanupUsers))
	// §MT 多租户：租户管理（平台运营者）+ 本租户用量 + 跨租户迁移
	s.mux.HandleFunc("GET /api/tenants", s.adminMiddleware(s.handleListTenants))
	s.mux.HandleFunc("POST /api/tenants", s.adminMiddleware(s.handleCreateTenant))
	s.mux.HandleFunc("PUT /api/tenants/{id}", s.adminMiddleware(s.handleUpdateTenant))
	s.mux.HandleFunc("GET /api/tenant/usage", s.adminMiddleware(s.handleTenantUsage))
	s.mux.HandleFunc("PUT /api/admin/users/{id}/tenant", s.adminMiddleware(s.handleMoveUserTenant))
	// 管理员代配他人账号配置（strategy / d1 / longshort / llm）
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/strategy", s.adminMiddleware(s.handleAdminGetStrategyConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/strategy", s.adminMiddleware(s.handleAdminSetStrategyConfig))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/d1", s.adminMiddleware(s.handleAdminGetD1Config))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/d1", s.adminMiddleware(s.handleAdminSetD1Config))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/longshort", s.adminMiddleware(s.handleAdminGetLongShortConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/longshort", s.adminMiddleware(s.handleAdminSetLongShortConfig))
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/llm", s.adminMiddleware(s.handleAdminGetLLMConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/llm", s.adminMiddleware(s.handleAdminSetLLMConfig))
	// §2026-09-07 多账号实盘：管理员逐账号配置 QMT 实盘（每账号独立 gateway/token/资金）。
	s.mux.HandleFunc("GET /api/admin/users/{id}/config/qmt", s.adminMiddleware(s.handleAdminGetQMTConfig))
	s.mux.HandleFunc("POST /api/admin/users/{id}/config/qmt", s.adminMiddleware(s.handleAdminSetQMTConfig))

	// §E-1（20260917 全量对账）运维/机器端点无 UI 消费方属**有意设计**（curl/监控脚本/网关回调），
	// 不算前端断链；新增前端调用前先确认页面归属。
	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))
	// §DAILY_OPSLOG 每日系统运行日志（管理员只读）：日期列表 + 按日内容（tail 截尾）
	s.mux.HandleFunc("GET /api/opslog/dates", s.adminMiddleware(s.handleOpslogDates))
	s.mux.HandleFunc("GET /api/opslog", s.adminMiddleware(s.handleOpslog))
	// §R4-9 指标面（鉴权后导出 expvar：下单/熔断/撤单/LLM 降级等关键事件计数）
	// §EXPVAR（2026-09-22 LOW 族收口）：expvar.Handler() 即 /debug/vars 的整套默认注册表
	// （含 cmdline/memstats/全部业务计数器），属运维面数据，此前仅 authMiddleware——
	// 任意登录成员可枚举。现升 adminMiddleware，与同文件 :565 opslog 及 prometheus/alerts
	// 等运维端点鉴权口径一致。另锤实：本服务只听自有 mux（muxWithJSONErrors），
	// expvar 包 init 注册的 http.DefaultServeMux /debug/vars 从未被挂载/监听，无第二暴露面；
	// 路由守卫测试见 expvar_admin_guard_test.go（含 /debug/vars 404 现状锁）。
	s.mux.HandleFunc("GET /api/metrics", s.adminMiddleware(http.HandlerFunc(expvar.Handler().ServeHTTP)))
	// §WS-L 维5 Prometheus text 导出（admin 鉴权，promtool 可校验）+ 阈值告警状态
	s.mux.HandleFunc("GET /api/metrics/prometheus", s.adminMiddleware(s.handlePrometheusMetrics))
	s.mux.HandleFunc("GET /api/metrics/alerts", s.adminMiddleware(s.handleAlertState))
	s.mux.HandleFunc("GET /api/data_source_health", s.authMiddleware(s.handleDataSourceHealth))
	s.mux.HandleFunc("GET /api/news_source_health", s.authMiddleware(s.handleNewsSourceHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
	// §Dashboard 情绪面板 A：日级情绪/风险档历史（近 30 交易日色带 + 关键指标）
	// English: sentiment card backend — daily emotion/market-state series for the Dashboard.
	s.mux.HandleFunc("GET /api/market/emotion/history", s.authMiddleware(s.handleMarketEmotionHistory))
	// §Dashboard 情绪面板 B：情绪×战法回测矩阵（候选逐事件断点缓存分相聚合）
	// English: emotion × strategy matrix (B4 event cache bucketed by daily sentiment phase).
	s.mux.HandleFunc("GET /api/research/emotion-strategy-matrix", s.authMiddleware(s.handleEmotionStrategyMatrix))
	// ── 做多/做空开关：属运营配置，仅管理员可切换；状态对所有登录用户可读（看板展示用）。──
	// （Long/short toggles are operator config: only admin may toggle; status is readable by all.）
	s.mux.HandleFunc("POST /api/long/toggle", s.adminMiddleware(s.handleLongToggle))
	s.mux.HandleFunc("GET /api/long/status", s.authMiddleware(s.handleLongStatus))
	s.mux.HandleFunc("POST /api/short/toggle", s.adminMiddleware(s.handleShortToggle))
	s.mux.HandleFunc("GET /api/short/status", s.authMiddleware(s.handleShortStatus))
	// ── 策略/D1/LLM 配置：运营配置系统级共享、仅管理员可读写（写会热替换全部账号引擎/新闻管线客户端）。──
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
	// LLM 通道自检：盘中"现在到底能不能用"的唯一快速手段（此前只能读日志或重启进程）。
	// 只读、不改任何状态，支持带候选配置探测（与 POST /api/config/llm 同形，含脱敏哨兵回填）。
	s.mux.HandleFunc("POST /api/config/llm/probe", s.adminMiddleware(s.handleProbeLLMConfig))
	// 一键回滚：热更新翻车（例如强制应用了一个其实不能用的配置）时回到上一个已验证可用的配置，
	// 不必重启、不必回忆上次填了什么。与写接口同权限（会改运行时与落库）。
	s.mux.HandleFunc("POST /api/config/llm/rollback", s.adminMiddleware(s.handleRollbackLLMConfig))

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
	// §SIGNAL_CONTROLLER 模拟盘战法开关（白名单语义与实盘 /api/config/qmt.strategies 同构）：
	// 读对已登录账号开放，写限管理员（模拟盘为运营数据）。
	// English: paper strategy whitelist endpoints (read for authed users, write admin-only).
	s.mux.HandleFunc("GET /api/paper/strategies", s.authMiddleware(s.handleGetPaperStrategies))
	s.mux.HandleFunc("POST /api/paper/strategies", s.adminMiddleware(s.handleSetPaperStrategies))
	// §F-4（20260917 缺陷修复批）模拟盘撮合配置读写：总开关/自动卖出/单笔资金/初始资金/做空预算
	// 改后即热同步（main.go 与 Registry.SetPaperConfig 共用 paper.ConfigFromRules，重启不再是
	// 唯一生效路径）。GET 登录可见，POST 限管理员。
	s.mux.HandleFunc("GET /api/paper/config", s.authMiddleware(s.handleGetPaperConfig))
	s.mux.HandleFunc("POST /api/paper/config", s.adminMiddleware(s.handleSetPaperConfig))
	// 信号控制器裁定留痕审计（"提醒了为何没成交"一屏定位）。
	// English: signal-controller verdict audit endpoint.
	s.mux.HandleFunc("GET /api/signalctl/verdicts", s.authMiddleware(s.handleSignalVerdicts))
	s.mux.HandleFunc("POST /api/paper/sell", s.adminMiddleware(s.handlePaperSell))
	s.mux.HandleFunc("POST /api/paper/buy", s.adminMiddleware(s.handlePaperBuy))
	// §SHORT-4 融券做空手动端点（开仓/买回）
	s.mux.HandleFunc("POST /api/paper/short_open", s.adminMiddleware(s.handlePaperShortOpen))
	s.mux.HandleFunc("POST /api/paper/short_cover", s.adminMiddleware(s.handlePaperShortCover))
	s.mux.HandleFunc("POST /api/paper/reset", s.adminMiddleware(s.handlePaperReset))
	s.mux.HandleFunc("POST /api/paper/pool/reset", s.adminMiddleware(s.handlePaperPoolReset))
	s.mux.HandleFunc("POST /api/paper/pool/config", s.adminMiddleware(s.handlePaperPoolConfig))
	// 持仓（运营账本）：读对所有登录用户开放（系统级共享的大盘持仓）；写仅管理员可操作。
	// （Positions: readable by all; writes are admin-only.）
	// §E-1 对账：/api/positions 台账 CRUD/exit 系运营账本 API（前端持仓页实际走 /api/holdings 系），
	// 当前无 UI 调用方；保留供脚本/后续台账页使用（非断链）。
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
	s.mux.HandleFunc("POST /api/holdings/balance", s.adminMiddleware(s.handleFixSetBalance)) // §P1-11 窄口径改资金
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
	// §H3（2026-09-22 修复批）成员越权写全局收口：showall 开关与 reanalyze 补推都落在
	// **运营账号引擎**的进程内全局状态（ctrlFor 恒取 operatorID，见 server.go ctrlFor 注释），
	// 旧路由只挂 authMiddleware——任何成员可改写 admin 的资讯过滤开关、随时发起全量 LLM
	// 补推（成本/状态污染）。与 /api/action 一起升 adminMiddleware（GET showall 只读不动）。
	// English: §H3 — global-state write endpoints (news showall toggle, reanalyze, manual action)
	// now require admin: they mutate the operator account's in-process engine regardless of caller.
	s.mux.HandleFunc("POST /api/news/showall", s.adminMiddleware(s.handleNewsShowAllToggle))
	s.mux.HandleFunc("GET /api/news/showall", s.authMiddleware(s.handleNewsShowAllStatus))
	s.mux.HandleFunc("POST /api/news/reanalyze", s.adminMiddleware(s.handleNewsReanalyze))
	// §M-14（2026-09-22 PM 批清扫）：test-attribution 是运营引擎 Stage2 的手动试跑口，
	// 每次调用消耗老板 LLM key 配额且前端零调用——旧路由只挂 authMiddleware，普通成员
	// 可无限把任意新闻推进 Stage2。与 §H3 同族收权 adminMiddleware（保留端点供 admin 排障，
	// 不删——能力尺寸在，权限收口）。
	// English: §M-14 — Stage2 attribution test endpoint burns LLM quota and has zero frontend
	// callers; raised from authMiddleware to adminMiddleware (kept for admin debugging, gated).
	s.mux.HandleFunc("POST /api/news/test-attribution", s.adminMiddleware(s.handleNewsTestAttribution))
	s.mux.HandleFunc("GET /api/engine/init-status", s.authMiddleware(s.handleEngineInitStatus))
	s.mux.HandleFunc("GET /api/watchlist", s.authMiddleware(s.handleFixGetWatchlist))
	s.mux.HandleFunc("POST /api/watchlist", s.authMiddleware(s.handleFixAddWatchlist))
	s.mux.HandleFunc("DELETE /api/watchlist", s.authMiddleware(s.handleFixRemoveWatchlist))
	// §H3：/api/action 的 ignore 分支写运营账号引擎信号簿（墓碑全局生效）、buy/sell 分支
	// 触实盘下单（内部本已 admin 闸）——路由整体升 adminMiddleware，成员点「忽略」不再能
	// 静默改写他人信号簿；前端信号页忽略按钮对成员隐藏（见 web 侧 isForbidden 兜底）。
	s.mux.HandleFunc("POST /api/action", s.adminMiddleware(s.handleFixAction))
	// §N-2（2026-09-22 傍晚批 §NOTIFYADMIN）档位抬升：/api/notify-test 打的是 server 级单例
	// 通知器（全局 Webhook/推送网关/ntfy 通道，非本用户配置），消息级 LevelHigh——此前只挂
	// authMiddleware，任何登录成员一次 POST 就能向 owner 全部推送通道发实弹，可用噪声淹没真
	// 告警（§C9 把空 stub 升级成真探测时漏抬的档位）。web/src 对该端点零调用，抬 admin 不破坏
	// 现网流程；配套进程内 60s 最小间隔频控 + opslog 审计（见 handleFixNotifyTest）。
	// English: §N-2 — raised to adminMiddleware: the probe fires on the GLOBAL notifier channels
	// with LevelHigh; any logged-in member could previously flood the owner's push channels.
	s.mux.HandleFunc("POST /api/notify-test", s.adminMiddleware(s.handleFixNotifyTest))
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
	// §U-2 当日委托列表（撤单 UI 的 order_id 来源，admin 权限）
	s.mux.HandleFunc("GET /api/qmt/orders", s.adminMiddleware(s.handleQMTOrders))
	// §R4-1 kill-switch 与手动撤单（admin 权限：紧急停止/撤单属资损级操作）
	s.mux.HandleFunc("POST /api/qmt/halt", s.adminMiddleware(s.handleQMTHalt))
	s.mux.HandleFunc("POST /api/qmt/cancel/{order_id}", s.adminMiddleware(s.handleQMTCancel))
	// §QMT-DUAL：网关 active 通道（miniqmt=xt / qmt=queued）读取与切换（仅 admin）
	s.mux.HandleFunc("GET /api/qmt/broker", s.adminMiddleware(s.handleQMTBroker))
	s.mux.HandleFunc("POST /api/qmt/broker", s.adminMiddleware(s.handleQMTBrokerSwitch))
	// §0925EVE-W3-G（FIX_PLAN ⑫ C3）第三态「待核对」人工收敛出口（仅 admin：人工改判
	// 委托终态是特权动作，每次尝试落 opslog.Audit）：清单透传 + 确认转发网关。
	// English: admin-only pending-review list and manual order-confirm passthrough.
	s.mux.HandleFunc("GET /api/qmt/pending-review", s.adminMiddleware(s.handleQMTPendingReview))
	s.mux.HandleFunc("POST /api/qmt/order-confirm", s.adminMiddleware(s.handleQMTOrderConfirm))
	// §WS-B：券商交割单三方对账（触发 + 历史查询，仅 admin）
	s.mux.HandleFunc("POST /api/qmt/settle", s.adminMiddleware(s.handleQMTSettle))
	s.mux.HandleFunc("GET /api/qmt/settle/history", s.adminMiddleware(s.handleQMTSettleHistory))
	// §FILL-AMEND（2026-09-23）历史错账的人工逐笔勘误 + 守恒自检（仅 admin：实盘账本族端点
	// 本就 admin-only，且勘误是"人工改判账目口径"的资损级动作）。
	// 提交→批准两步分离，批准前是影子态（不影响任何数字）；apply/revoke 各一条审计留痕。
	// English: §FILL-AMEND — admin-only human correction channel (submit → approve → optional
	// revoke) plus the read-only conservation self-check.
	s.mux.HandleFunc("GET /api/qmt/fill-amendments", s.adminMiddleware(s.handleListFillAmendments))
	s.mux.HandleFunc("POST /api/qmt/fill-amendments", s.adminMiddleware(s.handleCreateFillAmendment))
	s.mux.HandleFunc("POST /api/qmt/fill-amendments/{id}/apply", s.adminMiddleware(s.handleApplyFillAmendment))
	s.mux.HandleFunc("POST /api/qmt/fill-amendments/{id}/revoke", s.adminMiddleware(s.handleRevokeFillAmendment))
	s.mux.HandleFunc("GET /api/qmt/fills/conservation", s.adminMiddleware(s.handleFillConservation))
	// §WS-C：风控闸口状态（命中明细 + 开关状态，仅 admin）
	s.mux.HandleFunc("GET /api/risk/gates", s.adminMiddleware(s.handleRiskGates))
	// §WS-E 敏感管线隔离：llm-debug / stage-records 含运营账号 LLM 密钥池与全链路日志，
	// 仅管理员可见（子账号 403）。English: §WS-E sensitive-pipeline isolation — these endpoints expose
	// the operator's LLM key pool and full pipeline logs, so they are admin-only.
	s.mux.HandleFunc("GET /api/llm-debug", s.adminMiddleware(s.handleLLMDebug))
	s.mux.HandleFunc("POST /api/consult", s.authMiddleware(s.handleConsult))
	s.mux.HandleFunc("GET /api/consult/history", s.authMiddleware(s.handleConsultHistory))
	s.mux.HandleFunc("DELETE /api/consult/history", s.authMiddleware(s.handleClearConsultHistory))
	// §DAILY_REVIEW 手动触发当前账号的盘后持仓 LLM 复盘（同步执行，返回复盘股票数；正常每日自动跑亦存在）。
	s.mux.HandleFunc("POST /api/review/positions", s.authMiddleware(s.handleTriggerPositionReview))
	s.mux.HandleFunc("GET /api/consult/pro-mode", s.authMiddleware(s.handleGetConsultProMode))
	s.mux.HandleFunc("PUT /api/consult/pro-mode", s.authMiddleware(s.handleSetConsultProMode))
	// §WS-E 敏感管线隔离：stage-records 暴露运营账号全链路 stage 记录，仅管理员可见（子账号 403）。
	s.mux.HandleFunc("GET /api/stage-records", s.adminMiddleware(s.handleStageRecords))
	s.mux.HandleFunc("GET /api/signal-logs", s.authMiddleware(s.handleSignalLogs))
	// B5 研究候选审批（仅拥有 research_approve 权限位或 admin 可操作；列表可见）
	s.mux.HandleFunc("GET /api/scheduler/status", s.authMiddleware(s.handleSchedulerStatus))
	s.mux.HandleFunc("GET /api/research/task/{id}/log", s.authMiddleware(s.handleResearchTaskLog))
	s.mux.HandleFunc("GET /api/research/event-factor", s.authMiddleware(s.handleResearchEventFactor))
	s.mux.HandleFunc("GET /api/research/progress", s.permMiddleware(auth.PermResearchApprove, s.handleResearchProgress))
	s.mux.HandleFunc("GET /api/research/factors", s.authMiddleware(s.handleResearchFactors))
	s.mux.HandleFunc("GET /api/research/candidates", s.permMiddleware(auth.PermResearchApprove, s.handleResearchCandidates))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/approve", s.permMiddleware(auth.PermResearchApprove, s.handleResearchApprove))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/reject", s.permMiddleware(auth.PermResearchApprove, s.handleResearchReject))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/grayscale", s.permMiddleware(auth.PermResearchApprove, s.handleResearchGrayscale))
	s.mux.HandleFunc("POST /api/research/candidates/{id}/backtest", s.permMiddleware(auth.PermResearchApprove, s.handleCandidateBacktest))
	s.mux.HandleFunc("GET /api/research/backtest/{id}", s.authMiddleware(s.handleBacktestStatus))
	// §D-1 夜间信号质量研究报告历史（正文跨账号聚合，admin-only）
	s.mux.HandleFunc("GET /api/research/paper-reports", s.adminMiddleware(s.handlePaperResearchReports))
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
	// §回测自动增强 A0：回测引擎增强配置（研究库 backtest_settings 单行 JSON，入队注入 payload）
	// English: backtest enhancement settings endpoints (single-row JSON store, payload injection on enqueue).
	s.mux.HandleFunc("GET /api/research/backtest-config", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestConfigGet))
	s.mux.HandleFunc("PUT /api/research/backtest-config", s.permMiddleware(auth.PermResearchApprove, s.handleBacktestConfigPut))
	s.mux.HandleFunc("GET /api/research/optimizations", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationList))
	s.mux.HandleFunc("POST /api/research/optimizations/{id}/approve", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationApprove))
	s.mux.HandleFunc("POST /api/research/optimizations/{id}/reject", s.permMiddleware(auth.PermResearchApprove, s.handleOptimizationReject))
	s.mux.HandleFunc("POST /api/research/library/{id}/enable", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryToggle("enable")))
	s.mux.HandleFunc("POST /api/research/library/{id}/disable", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryToggle("disable")))
	s.mux.HandleFunc("POST /api/research/library/{id}/delete", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryDelete))
	s.mux.HandleFunc("POST /api/research/library/{id}/rename", s.permMiddleware(auth.PermResearchApprove, s.handleResearchLibraryRename))
	s.mux.HandleFunc("GET /api/research/backtest-toggle", s.authMiddleware(s.handleResearchBacktestToggle))
	s.mux.HandleFunc("POST /api/research/backtest-toggle", s.permMiddleware(auth.PermResearchApprove, s.handleResearchBacktestToggle))
	// §WS-H C2 参数版本化：历史快照列表（auth 可见）+ 回滚（admin 专属，原子恢复+审计）
	// §E-1 对账：配置快照/回滚 API 面已就绪但 UI 未接（docs/DEFECT_FIX_PLAN_20260917 E-1 台账），按需接。
	s.mux.HandleFunc("GET /api/research/strategies/snapshots", s.authMiddleware(s.handleStrategySnapshots))
	s.mux.HandleFunc("POST /api/research/strategies/rollback", s.adminMiddleware(s.handleStrategyRollback))
	// §WS-K 维4 配置历史/回滚：快照+diff 列表（admin）、回滚恢复（admin）
	s.mux.HandleFunc("GET /api/config/history", s.adminMiddleware(s.handleConfigHistory))
	s.mux.HandleFunc("POST /api/config/rollback", s.adminMiddleware(s.handleConfigRollback))
	// §WS-F C4a SSE 建链票据签发端点（需认证）；SSE 建链用 /api/events?ticket=xxx（60s 有效，§M5 起 TTL 内可复用）。
	// ── SSE 事件流：建链端点自带票据校验（不走 authMiddleware，因 EventSource 无法带 Authorization 头）──
	s.mux.HandleFunc("POST /api/events/ticket", s.authMiddleware(s.handleSSETicket))
	s.mux.HandleFunc("GET /api/events", s.handleFixSSE)
}

// maxBodyBytes 请求体大小上限：64KB（防超大 body 打爆内存，正常业务请求远小于此）。
// （maxBodyBytes caps request bodies at 64KB to prevent memory exhaustion.）
const maxBodyBytes = 64 << 10

// Serve 启动 HTTP 服务监听指定地址。
// Serve 启动 HTTP 服务。§WS-F B4：若设置 QUANT_TLS_CERT 与 QUANT_TLS_KEY 则启用 HTTPS
// （可选本地直连加密；生产公网仍以 Caddy TLS 终结为准，见 deploy/caddy/guangzhou.conf）。
// 文档强制约定：公网部署必须经 TLS（Caddy）暴露，禁止明文 8080 直连公网。
// English: Serve starts the HTTP server. If QUANT_TLS_CERT/QUANT_TLS_KEY are set, it serves HTTPS.
// Production TLS termination stays with Caddy; plaintext 8080 must never be exposed to the public net.
func (s *Server) Serve(addr string) error {
	// 读取 TLS 证书/私钥环境变量：两者齐备则走 HTTPS 直连（本地加密）；否则明文 HTTP
	cert, key := os.Getenv("QUANT_TLS_CERT"), os.Getenv("QUANT_TLS_KEY")
	if cert != "" && key != "" {
		log.Printf("HTTPS server starting on %s (TLS: %s)", addr, cert)
		return http.ListenAndServeTLS(addr, cert, key, s.chain(s.muxWithJSONErrors()))
	}
	log.Printf("HTTP server starting on %s", addr)
	return http.ListenAndServe(addr, s.chain(s.muxWithJSONErrors()))
}

// ServeListener 使用已创建的监听器启动 HTTP 服务。
// 与 Serve 的区别：监听器由调用方预先绑定（端口占用自动顺延后拿到的 listener），
// 复用同一对象服务请求，避免"先探测端口再 ListenAndServe"的 bind 竞争。
// English: serves HTTP on a pre-bound listener. Unlike Serve, the listener is created by the caller
// (e.g. after auto port-switching) and reused for serving, avoiding the bind race of
// "probe the port, then ListenAndServe".
func (s *Server) ServeListener(ln net.Listener) error {
	log.Printf("HTTP server serving on %s", ln.Addr().String())
	return http.Serve(ln, s.chain(s.muxWithJSONErrors()))
}

// ServeHTTP 实现 http.Handler 接口，供 httptest / 内嵌路由直接驱动（测试与复用场景）。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.chain(s.muxWithJSONErrors()).ServeHTTP(w, r)
}

// muxWithJSONErrors §F3（2026-09-22 修复批）：给路由表套一层 404/405 JSON 信封出口。
// 背景：全仓 API 错误统一走 writeError（{"error":…}，application/json），但 Go ServeMux
// 对「未注册路径/方法不匹配」兜底回 text/plain（"404 page not found"）——前端 request()
// JSON 解析抛语法错、E2E Content-Type 断言在此分裂。FIX_PLAN §6.3 的修法本应直挂
// s.mux.NotFound/MethodNotAllowed，但本工具链（go1.26 路由重构版）已移除该字段，
// 改为用 mux.Handler(r) 预判：命中注册 pattern 的请求原样直通透传（SSE 流式响应零扰动）；
// 未命中（pattern==""）时先在缓冲里跑兜底 handler——3xx（路径规范化重定向）原样放行，
// 其余（plain-text 404/405）替换为同状态码的 JSON 信封。
// English: §F3 — wraps the mux so unmatched routes (404) and wrong methods (405) answer with
// the standard {"error":…} JSON envelope; matched requests pass through untouched (streaming
// included), and canonicalization redirects still flow as-is.
func (s *Server) muxWithJSONErrors() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "*" 请求与非法 URI 交由 mux 自身的 400 早退分支，不做兜底预判。
		if r.RequestURI == "*" {
			s.mux.ServeHTTP(w, r)
			return
		}
		h, pattern := s.mux.Handler(r)
		if pattern != "" {
			s.mux.ServeHTTP(w, r) // 命中注册路由：原样直通（不缓冲、不影响 SSE 流式写出）
			return
		}
		if h == nil {
			writeError(w, http.StatusNotFound, "接口不存在: "+r.Method+" "+r.URL.Path)
			return
		}
		// 未命中：兜底 handler 无副作用（只写状态码/文本），先在缓冲里跑一遍拿状态码，
		// 以区分「规范化重定向(3xx，放行)」与「404/405（改走 JSON 信封）」。
		buf := &statusCapture{header: w.Header()}
		h.ServeHTTP(buf, r)
		switch {
		case buf.status >= 300 && buf.status < 400:
			w.WriteHeader(buf.status)
			_, _ = w.Write(buf.body.Bytes())
		case buf.status == http.StatusMethodNotAllowed:
			writeError(w, http.StatusMethodNotAllowed, "方法不允许: "+r.Method+" "+r.URL.Path)
		default:
			writeError(w, http.StatusNotFound, "接口不存在: "+r.Method+" "+r.URL.Path)
		}
	})
}

// statusCapture §F3 辅助：捕获式 ResponseWriter（Header 与真实 writer 共享，状态码/体先落缓冲）。
type statusCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

// Header 复用真实 writer 的 header map（不缓冲）：中间件在 WriteHeader 前塞进去的头必须原样透传。
// English: shares the real writer's header map so headers set before WriteHeader still land.
func (c *statusCapture) Header() http.Header { return c.header }

// WriteHeader 只认第一次调用的状态码（http.ResponseWriter 契约：重复调用应被忽略）。
// English: keeps only the first status code, per the ResponseWriter contract.
func (c *statusCapture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

// Write 把响应体先落缓冲：兜底 handler 要跑完才知道状态码——3xx 原样回写这条体，
// 404/405 丢弃它改走 JSON 信封（分支见 :914）。未显式 WriteHeader 时按 200 记，
// 与 net/http 的隐式行为一致（否则成功请求会被误判成异常）。
// English: buffers the body because the fallback handler reveals its status only after running;
// 3xx replays the buffered body, 404/405 replaces it with the JSON envelope, absent header => 200.
func (c *statusCapture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

// genTraceID 生成 16 字节十六进制 trace-id（crypto/rand，不可预测），用于把一次请求
// 的 panic/500 与排障日志关联。crypto/rand 失败（极罕见）时退化为时间戳串，不阻断。
// English: genTraceID returns a 16-byte hex trace id for correlating a request's 500/panic with logs.
func genTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t-fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// recoverMiddleware 恢复中间件：捕获请求处理链中的 panic，记录日志并返回 500，
// 避免单个 handler 崩溃把整个 HTTP 服务进程打挂（配合 systemd Restart=always 双保险）。
// 已开始写入的响应无法再改状态码，此时仅记录日志并中止该连接。§FIX-4：panic 时生成 trace-id
// 并回写响应头 X-Trace-Id 与 body，便于把 500 与排障日志关联。
// （recoverMiddleware catches panics in the request chain, logs them and returns 500
// so a single handler crash cannot take down the whole HTTP process.）
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				traceID := genTraceID()
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				// §FIX-4：结构化日志带 trace-id，便于把 500 与排障日志关联（此前只能看到裸 stack）。
				log.Printf("[server] PANIC recovered trace_id=%s path=%s method=%s: %v\n%s",
					traceID, r.URL.Path, r.Method, rec, stack[:n])
				// 响应尚未写入时返回 500；已写入则放弃改写状态码
				if rw, ok := w.(http.Hijacker); ok && rw != nil {
					_ = rw
				}
				// 尽力尝试写入错误响应（若头已发送则 WriteHeader 无效，不报错）
				defer func() {
					if rec2 := recover(); rec2 != nil {
						log.Printf("[server] panic-response write also panicked trace_id=%s: %v", traceID, rec2)
					}
				}()
				w.Header().Set("Content-Type", "application/json")
				// 把 trace-id 同时写响应头与 body，前端 500 页可展示供用户/排障回传。
				w.Header().Set("X-Trace-Id", traceID)
				w.WriteHeader(500)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":    "internal server error",
					"trace_id": traceID,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware 跨域中间件：为所有响应添加 CORS 头，并直接终结 OPTIONS 预检请求。
// §WS-F B4 收紧：不再恒发 `Allow-Origin:*`——仅放行「同源」（Origin 与 Host 匹配）或
// `ALLOWED_ORIGINS` 白名单（逗号分隔 host，可带 scheme，如
// https://trade.example.com,http://127.0.0.1:8080）中的源；无 Origin 头的非浏览器请求回退 `*`。
// 跨域源不写 Allow-Origin 头 → 浏览器直接拦截（fetch 被拒）。用 token header 鉴权，无 cookie，
// 故不开 Allow-Credentials。
// English: §WS-F B4 CORS hardening — echo the origin only when it is same-origin (Origin host matches
// request Host) or on the ALLOWED_ORIGINS whitelist; non-browser requests without an Origin header
// fall back to "*". Cross-origin requests get no Allow-Origin header, so browsers block them.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	// 启动时一次性解析 ALLOWED_ORIGINS 环境变量，构建放行源白名单（精确匹配 origin 或其 host）
	allowed := map[string]bool{}
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 逐请求判定放行策略：无 Origin（非浏览器）回退 *；
		// 命中白名单（完整源 / host / 与 Host 同源）则回显该源；跨域一律不写 Allow-Origin。
		origin := r.Header.Get("Origin")
		allow := ""
		switch {
		case origin == "":
			allow = "*" // 非浏览器（curl/探针）无 Origin 头，回退全放行
		case allowed[origin], allowed[originHost(origin)], originMatchesHost(r, origin):
			allow = origin
		}
		if allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
		}
		// 预检响应通用头：允许的方法与请求头（token 走 Authorization，无 cookie，不开 Credentials）
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		// OPTIONS 预检直接终结，不进入业务 handler
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originHost 提取 Origin 的 host 段（https://a.com:8443 → a.com）。
// English: originHost extracts the hostname from an origin URL.
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// originMatchesHost Origin 与请求 Host 是否同源（忽略端口，适配反代场景）。
// English: reports whether the Origin and the request Host share a hostname (port-insensitive, reverse-proxy friendly).
func originMatchesHost(r *http.Request, origin string) bool {
	oh := originHost(origin)
	if oh == "" {
		return false
	}
	rh := r.Host
	if h, _, e := net.SplitHostPort(r.Host); e == nil {
		rh = h
	}
	return oh == rh
}

// chain 将多个中间件按顺序包装 next（外层 → 内层）。
// recoverMiddleware 在最外层兜底 panic。
func (s *Server) chain(next http.Handler) http.Handler {
	// 中间件链由外到内：recover（兜底 panic）→ CORS → bodyLimit（§FIX-6 全局请求体闸）→ 业务路由
	return s.recoverMiddleware(s.corsMiddleware(bodyLimit(next)))
}

// bodyLimit §FIX-6(20260919 批四)：全局请求体上限 64KB。此前 170 条路由仅 4 处各自包了
// MaxBytesReader，其余端点（含 /api/consult）裸读 r.Body——超大 body 直打内存与解码器。
// 全仓端点均为 JSON 请求体（无文件上传类大 body 端点，已逐处核对），统一封顶安全；
// 已知 ContentLength 超限直接 413 拒掉（不读 body），chunked 传输由 MaxBytesReader
// 在读取越限时令解码失败兜底。处理器内自包的 MaxBytesReader 不受影响（内层更严者生效）。
// English: global 64KB request-body cap; all endpoints are JSON-only, so no upload exemption exists.
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "请求体超过 64KB 上限")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
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
		// §FIX-3：429 一律走统一出口（带 Retry-After 重试窗口），不再裸吐无头 429。
		rejectRateLimit(w, time.Minute)
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
		// §WS-F C1 审计：登录失败留痕（actor 用 clientIP 而非用户名，防枚举用户名单）
		opslog.Audit("login", clientIP(r), req.Username, "fail")
		writeError(w, 401, "invalid credentials")
		return
	}
	opslog.Audit("login", user.ID, req.Username, "ok")
	writeJSON(w, 200, map[string]interface{}{
		"token":   user.Token,
		"id":      user.ID,
		"account": user.Username,
		"role":    user.Role,
		"perms":   user.Perms,
	})
}

// handleLogout 处理 POST /api/auth/logout：吊销当前 Bearer 对应的服务端会话。
// §D7 修复：退出必须回收 Sessions 中的哈希条目——只清客户端 localStorage 会让令牌
// 在服务端一直有效到 TTL，任何拿到旧请求头的路径（浏览器历史/代理日志）都能续命。
// 幂等：已吊销也返回 200（前端 logout 常因网络重试重复调用）。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	uid := requestUserID(r)
	ok := s.auth.RevokeSession(tok)
	opslog.Audit("logout", uid, uid, map[bool]string{true: "ok", false: "noop"}[ok])
	writeJSON(w, 200, map[string]bool{"revoked": ok})
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
		// §FIX-3：429 一律走统一出口（带 Retry-After 重试窗口），不再裸吐无头 429。
		rejectRateLimit(w, time.Minute)
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

	// 只有空库未初始化时才允许建首个管理员；已初始化一律 409，防止覆盖既有账号口令。
	user, err := s.auth.SetupInitialAdmin(req.Username, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "already initialized") {
			writeError(w, 409, "already initialized")
			return
		}
		writeError(w, 500, err.Error())
		return
	}

	// 初始化时顺带提交的三方凭据按用户维度落 auth 配置；空值跳过，绝不覆盖成空串。
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
// §WS-F P3 加固：map 增 TTL 清理（超过 maxIPs 时淘汰过期条目，防 IP 放大内存）。
// English: in-process sliding-window rate limiter for anonymous endpoints; hardened with a TTL sweep so
// a rotating-IP flood cannot grow the map unboundedly.
type ipLimiter struct {
	mu       sync.Mutex
	hits     map[string][]time.Time // key=IP，value=该 IP 近期的请求时间戳序列
	maxIPs   int                    // map 上限（0=默认 10000）
	sweepMod int                    // 每 N 次 allow 清扫一次（0=默认 256）
	ops      int                    // allow 调用计数（清扫节拍）
}

// allow 滑动窗口判定：window 内该 IP 已达 max 次则拒绝。
// 首次调用惰性初始化 map；每 sweepMod 次调用触发一次过期清扫，防止长期运行内存膨胀。
func (l *ipLimiter) allow(ip string, max int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = make(map[string][]time.Time)
	}
	l.ops++
	if l.sweepMod <= 0 {
		l.sweepMod = 256
	}
	// §WS-F TTL 清理：每 sweepMod 次调用清扫一次（淘汰超过 MaxWindow 的整段旧数据 + 超上限截断）。
	if l.ops%l.sweepMod == 0 {
		l.sweep(now, 24*time.Hour)
	}
	recent := l.hits[ip][:0]
	// 原地过滤：仅保留窗口内的请求时间戳（复用底层数组，避免每请求分配）
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

// sweep 淘汰超过 maxAge 的全部条目；仍超 maxIPs 则保留最活跃的 maxIPs 个 IP。
// English: sweep drops entries older than maxAge, then keeps only the most-recent maxIPs IPs.
func (l *ipLimiter) sweep(now time.Time, maxAge time.Duration) {
	for ip, ts := range l.hits {
		cut := 0
		for _, t := range ts {
			if now.Sub(t) < maxAge {
				ts[cut] = t
				cut++
			}
		}
		if cut == 0 {
			delete(l.hits, ip)
		} else {
			l.hits[ip] = ts[:cut]
		}
	}
	if l.maxIPs <= 0 {
		l.maxIPs = 10000
	}
	if len(l.hits) <= l.maxIPs {
		return
	}
	// 按最近时间戳排序，保留最新的 maxIPs 个 IP
	type kv struct {
		ip string
		at time.Time
	}
	var all []kv
	for ip, ts := range l.hits {
		var last time.Time
		for _, t := range ts {
			if t.After(last) {
				last = t
			}
		}
		all = append(all, kv{ip, last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	for _, e := range all[l.maxIPs:] {
		delete(l.hits, e.ip)
	}
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

// trustedProxyCIDRs 可信代理网段列表（§GAP2-W1）：
// 默认覆盖环回 + RFC1918 内网 + 链路本地（适配 Caddy 与应用同机的反代部署），
// 可用环境变量 QUANT_TRUSTED_PROXIES（逗号分隔 CIDR）整体覆盖；
// 仅当 TCP 对端落在这此网段内时，才会采信其 X-Forwarded-For 头。
// English: trusted proxy CIDR set (loopback/RFC1918/link-local by default, overridable via
// QUANT_TRUSTED_PROXIES); XFF is honored only when the TCP peer is inside this set.
var trustedProxyCIDRs = func() []*net.IPNet {
	// 默认网段：环回（同机 Caddy 反代）+ RFC1918 内网 + 链路本地
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

// userRateLimit §UAT-D6（2026-09-16）：高成本认证端点的按用户频控。
// 旧 ipLimiter 只挂 login/setup 两个匿名口——登录用户可以单账号无限打 LLM 咨询/参数寻优/
// 新闻补推/持仓复盘（每请求背后是数十秒 LLM/全参回测），成本放大且饿死他人。复用同一
// 滑动窗实现，键优先用户 ID（未登录回落 IP）。超限返回 false，调用侧回 429。
// English: §UAT-D6 — per-user sliding-window throttle for expensive authenticated endpoints
// (LLM consult / backtest optimize / news re-analyze / position review), reusing ipLimiter.
func (s *Server) userRateLimit(r *http.Request, bucket string, max int, window time.Duration) bool {
	uid := requestUserID(r)
	key := bucket + "|ip:" + clientIP(r)
	if uid != "" {
		key = bucket + "|u:" + uid
	}
	return s.limiter.allow(key, max, window)
}

// rejectRateLimit 统一 429 出口（带 Retry-After 提示窗口长度）。
func rejectRateLimit(w http.ResponseWriter, window time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
	writeError(w, 429, fmt.Sprintf("请求过于频繁，请 %s 后再试", window))
}

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
		// §MT 租户级频控：同一租户成员业务 API 合计超过配额 → 429（滑动窗口 1 分钟）。
		if tid := s.auth.TenantOf(user.ID); tid != "" {
			if !s.tenantLimiter.allow(tid, s.auth.TenantAPIRate(tid), time.Minute) {
				// §FIX-3：与匿名滑动窗限流一致，统一走 rejectRateLimit 带 Retry-After，
				// 避免租户级 429 缺头导致前端无法判定重试窗口。
				rejectRateLimit(w, time.Minute)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, user)))
	}
}

// bearerToken 从 Authorization 头取原始令牌（去 "Bearer " 前缀，与 authMiddleware 同口径）。
// English: bearerToken extracts the raw bearer credential from the Authorization header, same
// normalization as authMiddleware.
func bearerToken(r *http.Request) string {
	t := r.Header.Get("Authorization")
	if t == "" {
		return ""
	}
	return strings.TrimPrefix(t, "Bearer ")
}

// adminMiddleware 管理员中间件：在认证基础上要求当前用户为管理员角色，否则返回 403。
// 仅包裹用户/账号管理与全局配置等管理类接口。
// §WS-E 审计：越权访问（子账号请求 admin 端点）记 opslog，配合 WS-F 审计日志。
// English: admin middleware — requires the admin role after auth, else 403; §WS-E audits unauthorized
// (non-admin) attempts to opslog for the WS-F audit trail.
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r)
		if user == nil || !user.IsAdmin() {
			opslog.Logf("quant", "越权访问拒绝 uid=%s %s %s（仅管理员可用）", userIDFor(r), r.Method, r.URL.Path)
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
	if s.dc == nil {
		writeError(w, http.StatusServiceUnavailable, "data coordinator not wired")
		return
	}
	writeJSON(w, 200, s.dc.HealthCheck())
}

// handleNewsSourceHealth 处理 GET /api/news_source_health：返回新闻资讯源健康探测结果。
// （handleNewsSourceHealth returns the probing results of each news source.）
func (s *Server) handleNewsSourceHealth(w http.ResponseWriter, r *http.Request) {
	if s.dc == nil {
		writeError(w, http.StatusServiceUnavailable, "data coordinator not wired")
		return
	}
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
	// §UAT-D6 每次补推 = 全市场新闻拉取 + LLM 批量调用，按用户 6 次/分钟封顶。
	if !s.userRateLimit(r, "news-reanalyze", 6, time.Minute) {
		rejectRateLimit(w, time.Minute)
		return
	}
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
		// 越权保护放在最前面：持仓不属于当前账号时直接 403，不再触碰后面的平仓落库。
		writeError(w, 403, "not your position")
		return
	}
	var req exitPositionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	// 平仓价必须显式给出且为正数：缺价平仓会让盈亏按 0 计，统计口径直接失真。
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
// §N-4：响应体含 updated_at（§中-6 版本戳），前端保存时原样回传做写前比对。
func (s *Server) handleGetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetStrategyConfig())
}

// handleSetStrategyConfig 处理 POST /api/config/strategy：稀疏 merge 保存全局策略参数配置。
//
// §N-4（2026-09-22 傍晚批 §CFGSMASH）**对外语义变更**：旧实现把 body 反序列化成完整
// StrategyConfig 后全量替换（json 缺键=零值），前端一次加载失败 + 一次整份保存即把五套战法
// 阈值落 0、已落库、重启救不回。现改为「**没传=保留旧值**」的稀疏 merge（逐字段递归，详见
// config.Manager.MergeStrategyConfig）；显式传某个键仍会更新该键（要清 0 请明写 0）。
// §中-6 乐观锁：body 可携带 GET 读到的 updated_at——与服务端当前版本不一致回 409
// （附 current_updated_at，前端提示"配置已被他人更新，请重载"）；不带 = 不比对（兼容脚本直 POST）。
// 端点注释即对外契约：响应回传本次写入后的新 updated_at。
// English: §N-4 — the write is now a SPARSE MERGE ("absent key keeps the stored value", the old
// decode-then-replace-all turned a failed page load + save into zeroed tactics); optional
// optimistic locking via updated_at, mismatch => 409 with current_updated_at.
func (s *Server) handleSetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	// 先解成「顶层键→原始 JSON」而不是 typed struct：typed 解码会把缺失键折叠成零值，
	// 正是本缺陷的成因；RawMessage 保留了"键到底出现过没有"这一稀疏 merge 的唯一判据。
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	// §中-6：取出客户端基线版本（允许缺省=不比对），非法类型宽容忽略而非 400——
	// 版本只是比对输入，不值得为它拒绝一次合法参数写入。
	baseVersion := ""
	if raw, ok := body["updated_at"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err == nil {
			baseVersion = v
		}
	}
	merged, err := s.cfg.MergeStrategyConfig(body, baseVersion)
	if errors.Is(err, config.ErrStrategyVersionConflict) {
		// 后写不覆盖前写：回 409 让管理员重载后再改（静默覆盖=两个人互相"改没了"）。
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":              "战法参数已被其他操作更新（版本不一致），请重新加载后再保存",
			"current_updated_at": merged.UpdatedAt,
		})
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// 操作留痕：merge 写改了哪些顶层键（不含数值正文，配合配置历史快照可追溯）。
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	opslog.Audit("config_strategy_merge", userIDFor(r), "strategy", fmt.Sprintf("keys=%v version=%s", keys, merged.UpdatedAt))
	writeJSON(w, 200, map[string]string{"status": "ok", "updated_at": merged.UpdatedAt})
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
	// Force 强制应用：跳过「探测未通过则拒绝」的保护，无条件切换并落库。
	//
	// 为什么需要这个开关：探测可能因为**与配置无关**的原因失败（供应商正在抖动、本机代理不通、
	// 网关不认我们探测请求体里的某个字段）。这类情况不该永久堵死用户——盘中他必须能改。
	// 默认 false（拒绝并给出确凿原因），只有用户显式勾选才走强制路径，且强制路径会：
	// ① 在响应里如实回告"未经验证"；② 保留回滚点，随时可一键退回上一个可用配置。
	// English: force-apply, bypassing the "probe must pass" guard. Only an explicit opt-in, and it
	// still reports "unverified" and keeps the rollback point intact.
	Force bool `json:"force,omitempty"`
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
		// runtime 回报**运行时实际生效**的那一份（进程内存里的客户端用的就是它）。
		// 为什么必须单独回报：库里的配置与内存里的客户端可能不一致（历史事故形态），
		// 而"页面上看到的值"此前无法区分是"已保存"还是"代码默认值"（内置默认供应商恰好
		// 也是 SiliconFlow），用户只能靠猜。这里给出确切事实 + 有无回滚点。
		"runtime": s.runtimeLLMView(),
	})
}

// runtimeLLMView 运行时 LLM 状态视图（供 GET /api/config/llm 回报）。
//
// 只暴露"是什么状态"，绝不带密钥本身：key 数、生效地址/模型、是否经验证、有无回滚点。
func (s *Server) runtimeLLMView() map[string]interface{} {
	s.llmMu.Lock()
	applied := s.llmLastApplied
	good := s.llmLastGood
	s.llmMu.Unlock()

	// 组装视图：运行时快照缺省时 available=false（前端据此提示"尚未热生效过"），
	// 有 lastGood 才允许回滚按钮亮起——两个字段都来自锁内拷贝，组装放锁外避免长持锁。
	view := map[string]interface{}{
		"available":    applied != nil,
		"url":          s.runtimeURLOf(),
		"model":        s.runtimeModel(),
		"can_rollback": good != nil,
	}
	if applied != nil {
		view["keys"] = len(applied.Keys)
		view["verified"] = applied.Verified
		view["applied_at"] = applied.At.Format(time.RFC3339)
	}
	if good != nil {
		view["last_good_at"] = good.At.Format(time.RFC3339)
		view["last_good_url"] = good.APIURL
		view["last_good_keys"] = len(good.Keys)
	}
	return view
}

// runtimeURLOf 返回运行时实际使用的 API 地址（无记录时为空串）。
func (s *Server) runtimeURLOf() string {
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	return s.runtimeURL
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
// 依次执行：APIURL+Model 写入当前账号配置（URL 留空=保持原值）→ 密钥槽位一次性解析为真钥
// （脱敏哨兵映射回库中原值）→ 落 auth 配置 → 用**同一份真钥**触发 llmRecreate 重建客户端
// → 记录运行时实际生效的 model（空值兜底为默认模型）。
//
// §P0 2026-09-18：落库与热重建必须共用同一份解析结果——历史上热重建直接吃 req.APIKeys，
// 把 GET 回显的脱敏掩码当成了真钥（详见下方密钥解析处注释）。
// English: persists the account's LLM config and hot-rebuilds the client from the SAME resolved
// key list that was persisted (masked sentinels map back to the stored real keys).
// handleSetLLMConfig 处理 POST /api/config/llm：保存当前账号的 LLM 配置并热生效。
//
// 实现已收口到 llm_apply.go 的 runSetLLMConfig —— 设置页 / 管理端 / 咨询页三个入口共用同一份，
// 完整语义（探测 → 切换 → 落库，"拒绝必须有确凿证据"，回滚点维护）见该文件头部说明。
// 口径分叉正是历次"改了不生效"的根源：同一件事两处各写一遍，迟早有一处漏。
//
// 这里只留两条历史事故的索引（细节见 llm_apply.go 的对应注释）：
//   - 脱敏哨兵必须解析回库中真钥，且**落库与热切换共用同一份解析结果**：否则线上客户端的密钥
//     会变成字面量掩码 → 全线 401，而库里那把好钥还在、页面回读还是老样子（"改了没生效"）。
//   - api_url / model 留空 = 保持原值：咨询页只提交 Key 时，空串曾把已配好的供应商静默清空。
//
// English: the implementation now lives in llm_apply.go and is shared by every write path.
func (s *Server) handleSetLLMConfig(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	var req setLLMConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	res, status, msg := s.runSetLLMConfig(uid, req)
	if msg != "" {
		writeError(w, status, msg)
		return
	}
	writeLLMApplyResult(w, status, res, res.Reason)
}

// resolveSubmittedLLMKeys 把设置页提交的密钥槽位解析为**可实际使用**的真钥列表，
// 返回 (真钥列表, 提交中被识别为脱敏哨兵的槽位数)。
//
// 槽位对齐规则（与 GET 的回显顺序一一对应）：第 i 个哨兵 → 库中原值的第 i 把；
// 哨兵但库中无对应槽位（异常提交）→ 跳过，绝不把掩码当真钥；明文槽位原样保留。
// 库中原值优先多 key（llm_api_keys），为空时回退旧版单 key（llm_api_key）——
// 旧账号只存了单 key，哨兵此前会因对不上而静默丢弃。
//
// English: maps each submitted masked sentinel back to the stored real key at the same index
// (multi-key store first, legacy single-key as fallback), passes plaintext through, and drops
// sentinels that have no stored original — a mask must never survive as a key.
func (s *Server) resolveSubmittedLLMKeys(uid string, submitted []string) ([]string, int) {
	if len(submitted) == 0 {
		return nil, 0
	}
	old := []string{}
	if v, ok := s.auth.GetConfig(uid, "llm_api_keys"); ok && v != "" {
		old = splitLLMKeys(v)
	}
	if len(old) == 0 {
		if v, ok := s.auth.GetConfig(uid, "llm_api_key"); ok && v != "" {
			old = []string{v}
		}
	}
	out := make([]string, 0, len(submitted))
	masked := 0
	for i, k := range submitted {
		switch {
		case !isMaskedSecret(k):
			out = append(out, k)
		case i < len(old):
			out = append(out, old[i]) // 哨兵 → 库中原值
			masked++
		default:
			masked++ // 哨兵且无对应原值：跳过（不得把掩码存成/当成真钥）
		}
	}
	return out, masked
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

// consultMessageMaxRunes §FIX-6(20260919 批四)：咨询消息长度上限（rune 计）。
// 正常提问几十字，2000 已极宽松；超限=灌 prompt/灌历史/放大外呼的恶意或误操作载荷。
const consultMessageMaxRunes = 2000

// 专业模式相关配置键（per-user，落盘 auth.json，跨重启保留）。
const (
	consultProModeKey = "consult_pro_mode" // "1"/"0"，默认开（§生产 20260916：咨询必须带近期+今日数据，关到"0"才关）
)

// consultProModeEnabled 读取当前用户专业模式开关状态（默认开：显式设 "0" 才关）。
// 2026-09-16 翻转：AI 顾问空口谈逻辑是产品缺陷（用户实录"我手头没数据"式回答），
// 数据上下文必须是默认行为，"不带数据"反而是需要主动选择的降级。
func (s *Server) consultProModeEnabled(userID string) bool {
	v, _ := s.auth.GetConfig(userID, consultProModeKey)
	return v != "0"
}

// handleConsult 处理 POST /api/consult：多轮 LLM 咨询。
// 专业模式（开关打开）时注入该股全部实时行情；普通模式仅追加深度分析风格要求。
// 未接入引擎或 LLM 未配置时返回对应错误提示（§FIX-9d：错误经机读 code 分流）。
func (s *Server) handleConsult(w http.ResponseWriter, r *http.Request) {
	// §UAT-D6 按用户频控（12 次/分钟）兜住刷调用。§FIX-9a(20260919)：旧"盘中 15 分钟注入限流"
	// 已在 §生产 20260916 移除，此处频控是请求路径唯一的频次闸。
	if !s.userRateLimit(r, "consult", 12, time.Minute) {
		rejectRateLimit(w, time.Minute)
		return
	}
	c := s.ctrlFor(requestUserID(r))
	if c == nil {
		writeError(w, 503, "引擎未启动")
		return
	}
	// 解析咨询请求体：空消息直接拒绝。
	var req consultReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	// §FIX-6(20260919 批四)：消息长度闸——纯空白按空处理；上限 2000 字符（rune 计，
	// 中文一字一符）。不限长=数百 KB 文本进付费 prompt 与历史文件，且代码扫描类
	// 载荷会把外呼量放大到行情配额极限（引擎侧另有前 5 只截断兜底）。
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeError(w, 400, "message required")
		return
	}
	if utf8.RuneCountInString(req.Message) > consultMessageMaxRunes {
		writeError(w, 400, fmt.Sprintf("咨询内容过长（上限 %d 字），请精简后分次提问", consultMessageMaxRunes))
		return
	}
	// 提取用户 ID（未登录时为空串），用于专业模式与私有历史寻址。
	user := userFromContext(r)
	userID := ""
	if user != nil {
		userID = user.ID
	}
	proMode := s.consultProModeEnabled(userID)

	// §生产 20260916：盘中 429 限流拒绝已移除——带数据咨询改为默认能力后，拒绝回答
	// 反而是产品缺陷；外部接口消耗由引擎侧按代码 60s 块缓存兜底（engine.buildStockBlock）。
	// §FIX-9a/9c(20260919)：旧的 consultProModeRateLimited/lastUsed 留档链路已整体删除——
	// 限流不复存在后仍每轮咨询重写 auth.json（含全部口令哈希的认证库）纯属无谓 IO 与风险面。

	// §FIX-4(20260919) 每用户 in-flight=1：一次出呼分钟级，连点发送=并行多条计费 + 共用历史
	// 互相污染。与"刷调用"的 12/min 频控正交——这条兜的是"同时"，那条兜的是"频次"。
	if _, busy := s.consultInflight.LoadOrStore(userID, struct{}{}); busy {
		writeErrCode(w, 429, "consult_inflight", "上一条咨询仍在处理中，请等待回复完成后再发送")
		return
	}
	defer s.consultInflight.Delete(userID)

	// §FIX-4(20260919)：传 r.Context()——用户断开/页面刷新即中止出呼链（退避/流式读/HTTP）。
	reply, err := c.ConsultLLM(r.Context(), userID, req.Message, proMode)
	if err != nil {
		if r.Context().Err() != nil {
			// 客户端已断开，写响应只是徒劳，如实留痕即可。
			log.Printf("[consult] 请求被取消（用户断开）: uid=%s err=%v", userID, err)
			return
		}
		// §FIX-9d(20260919)：出呼错误统一分流（含 §FIX-7 预算 429、§FIX-10 隔离 503）——
		// 状态码按语义归类，外发文案脱敏截断，响应附机读 code 供前端免关键字猜测。
		status, code, msg := consultErrorResponse(err)
		if status == 500 {
			// 500=真未知故障，留全量错误日志；上游/超时类已归类，不再灌 error 日志。
			log.Printf("[consult] 引擎错误: uid=%s err=%v", userID, err)
		}
		writeErrCode(w, status, code, msg)
		return
	}
	// §FIX-9h(20260919)：免责尾注在 HTTP 出口统一追加（不再只靠前端组件）——
	// 脚本/APK 等非浏览器客户端直连时同样覆盖。历史存储保持干净（引擎侧已落盘原文）。
	writeJSON(w, 200, map[string]string{"reply": reply + consultDisclaimer})
}

// consultDisclaimer §FIX-9h：咨询回复出口的固定免责句（与前端 Disclaimer 文案同口径）。
const consultDisclaimer = "\n\n（以上内容由 AI 生成，仅供参考，不构成投资建议。）"

// consultUpstreamStatusRe 兜底匹配旧式裸错误串"LLM API 返回 %d"（llm.UpstreamError 之外的历史形态）。
var consultUpstreamStatusRe = regexp.MustCompile(`LLM API 返回 (\d{3})`)

// consultErrorResponse §FIX-9d(20260919)：把咨询出呼错误分流为「HTTP 状态 + 机读 code +
// 脱敏文案」三元组。此前一律 500 直出 err.Error()——上游供应商响应体（可能含 URL/密钥回显）
// 原样进客户端，且前端只能拿 message 做"配置"关键字猜测。
// 分类优先级：预算熔断(429) > 隔离存储未就绪(503) > LLM 未配置(503) > 模型超时(504) >
// 上游故障(429/5xx→503 可重试，其余 4xx→502) > 未知(500，文案截断 200 字符)。
// English: maps consult pipeline errors to (status, machine-readable code, sanitized message).
func consultErrorResponse(err error) (int, string, string) {
	// §FIX-7：预算熔断是配额语义（次日自动恢复），文案本就面向用户，原样透传。
	if errors.Is(err, llm.ErrBudgetExceeded) {
		return 429, "consult_budget_exceeded", err.Error()
	}
	// §FIX-10：账号隔离存储未就绪=依赖缺失（可重试），拒绝发生在付费调用之前。
	if errors.Is(err, data.ErrStoreUnavailable) {
		return 503, "consult_store_unavailable", err.Error()
	}
	// LLM 未配置：引导配置文案面向管理员，不含任何 key 材料。
	if errors.Is(err, llm.ErrNoAPIKey) || strings.Contains(err.Error(), "未配置 LLM_API_KEY") {
		return 503, "llm_not_configured", "AI 顾问暂不可用：LLM 未配置 API Key，请管理员在咨询页完成配置"
	}
	// §P0 2026-09-20：上游返回的是网页（HTML）而不是模型接口 —— 这是**配置问题**，不是上游故障，
	// 文案必须把用户直接引到"地址填错了"上（旧行为：混进 500 并把 160 字 HTML 摘录当线索，
	// 用户完全看不出是地址问题）。详见 docs/BUGFIX_LLM_CONSOLE_URL_20260920.md。
	if errors.Is(err, llm.ErrUpstreamNotAPI) {
		return 502, "llm_upstream_not_api",
			"AI 顾问暂不可用：LLM 返回的是网页而不是模型接口响应 —— 地址很可能填成了供应商的" +
				"网页控制台/登录页地址。请在设置页把 LLM 地址改为 API 端点" +
				"（形如 https://api.<供应商域名>/v1/chat/completions）后重存"
	}
	// §FIX-3 超时速记：空闲超时/总时长超限都是"模型卡死/回包过慢"，504 语义可重试。
	msg := err.Error()
	if strings.Contains(msg, "空闲超时") || strings.Contains(msg, "总时长超限") {
		return 504, "llm_timeout", "模型响应超时，已中止本轮出呼，请稍后重试或精简提问"
	}
	// 上游 HTTP 故障：优先机读类型，兜底旧式消息串。
	status := 0
	var ue *llm.UpstreamError
	if errors.As(err, &ue) {
		status = ue.Status
	} else if m := consultUpstreamStatusRe.FindStringSubmatch(msg); m != nil {
		status, _ = strconv.Atoi(m[1])
	}
	if status > 0 {
		if status == http.StatusTooManyRequests || status >= 500 {
			return 503, "llm_upstream_unavailable", fmt.Sprintf("上游模型服务暂不可用（HTTP %d），请稍后重试", status)
		}
		return 502, "llm_upstream_error", fmt.Sprintf("上游模型服务拒绝了请求（HTTP %d），请联系管理员核查配置", status)
	}
	// 未知错误：截断脱敏后透传（可能含用户可见的诊断信息），全量已进日志。
	runes := []rune(msg)
	if len(runes) > 200 {
		msg = string(runes[:200]) + "…（详情见服务端日志）"
	}
	return 500, "consult_failed", msg
}

// writeErrCode 标准错误结构扩展：{"error": msg, "code": 机读错误码}（§FIX-9d）。
func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
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
// §FIX-9g(20260919)：引擎缺失时不再回"ok"假成功——前端 await 后清屏会造成
// "界面已清空、服务端历史仍在、刷新复现"的分裂态，如实 503 让调用方保留原界面。
func (s *Server) handleClearConsultHistory(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	if c := s.ctrlFor(uid); c != nil {
		c.ClearConsultHistoryFor(uid) // §GAP2-W2 只清本人账号的历史
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	writeErrCode(w, 503, "engine_unavailable", "引擎未启动，历史未清空，请稍后重试")
}

// handleTriggerPositionReview 处理 POST /api/review/positions：§DAILY_REVIEW 手动触发当前账号的盘后
// 持仓 LLM 综合复盘（同步执行）。复盘依赖 LLM 调用，耗时数秒~数十秒；成功返回 {reviewed:N}；
// 依赖未就绪/LLM 失败返回 502。与每日自动复盘同源（消息按 pos-review@uid@code@日 键每日覆盖更新）。
// English: force-runs the §DAILY_REVIEW position review for the calling account (synchronous LLM pass).
// Returns {reviewed:N}; 502 when deps/LLM fail. Same message keys as the daily auto run (per-day upsert).
func (s *Server) handleTriggerPositionReview(w http.ResponseWriter, r *http.Request) {
	// §UAT-D6 同步 LLM 全持仓复盘，单次数十秒，按用户 4 次/5 分钟封顶。
	if !s.userRateLimit(r, "pos-review", 4, 5*time.Minute) {
		rejectRateLimit(w, 5*time.Minute)
		return
	}
	uid := requestUserID(r)
	if s.registry == nil {
		writeError(w, 503, "引擎注册表未就绪")
		return
	}
	n, err := s.registry.TriggerPositionReview(uid)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"reviewed": n})
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
