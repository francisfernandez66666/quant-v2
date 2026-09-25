// Package engine 顶层编排引擎。
// registry.go 提供多账号独立引擎注册表（EngineRegistry）：
// 共享数据源（行情/新闻/板块/策略引擎等）被所有账号引擎复用；
// 每个账号拥有独立的 Engine 实例（独立看板聚合、持久化、做多/做空开关、战法参数），
// 真正实现"后端分账号计算，前端只拿结果"——同一账号任何设备结果一致。
// English: registry.go implements the multi-account engine registry. Shared data sources
// (quotes/news/sectors/strategy engine) are reused by all account engines; each account owns an
// independent Engine instance (aggregator, persistence, long/short toggles, strategy params), so
// the backend computes per account and the frontend only fetches results — the same account sees
// identical results on any device.
package engine

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/store"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy_engine"
	"quant-trading-v2/internal/trading"
)

// EngineOptions 注册表的共享依赖（数据源全局一份，所有账号引擎复用）。
// English: shared dependencies for the registry — data sources are global and reused by every account engine.
type EngineOptions struct {
	MarketAPI    *data.MarketAPI         // 行情 API（全局共享）
	NewsAgent    *newsagent.Agent        // 新闻归因代理（全局共享）
	StrategyEng  *strategy_engine.Engine // 策略引擎（全局共享）
	SectorAgent  *sector_agent.Agent     // 板块代理（全局共享）
	Scanner      *data.SectorScanner     // 板块扫描器（全局共享）
	Matcher      *data.EventMatcher      // 事件匹配器（全局共享）
	Rpt          *report.Report          // 报告/镜像持仓账本（全局共享）
	StockTracker *data.StockTracker      // 个股跟踪器（全局共享）
	WlMgr        *data.WatchlistManager  // 自选股管理器（全局共享）
	SSE          *server.SSEBroker       // SSE 推送 broker（全局共享）
	LLMClient    *llm.Client             // LLM 客户端（全局共享）
	THS          *data.THSClient         // 同花顺数据客户端（全局共享）
	Fetcher      *data.Fetcher           // 数据获取器（全局共享）
	CfgMgr       *config.Manager         // 配置管理器（全局共享）
	// Coordinator §MARKET_RISK_GATE P0：统一行情源协调器（hithink 主源+东财兜底），全局共享一份。
	// 非空且 RiskHithinkPrimary 时，引擎风险盘口（涨停池等）走该协调器；否则回落旧的东财直连。
	// English: the shared multi-source coordinator (hithink primary + EastMoney fallback) used by the
	// engine's risk boards when present and RiskHithinkPrimary; otherwise the old EastMoney-direct path.
	Coordinator        *data.DataCoordinator
	RiskHithinkPrimary bool             // 风险盘口是否以 hithink 为主源（默认 true，false=应急回退东财直连）
	DataDir            string           // 数据目录根路径
	Notifier           *notify.Notifier // 通知推送器（全局共享）
	SectorTopN         int              // 主线板块纳入成分股数量
	D1MaxRetries       int              // D1 评分 LLM 调用最大重试次数
	D1MaxTokens        int              // D1 评分 LLM 单次调用推理长度上限（§S3）
	Paper              *paper.Engine    // 模拟盘引擎模板（配置来源；每账号独立实例+独立 paper.json）
	// 实盘账本（AUTO_TRADING_PLAN M1）：QMT 控制器存取 real_positions/orders/fills 的库句柄。
	// §OPT-3 已隔离至独立 live.db。与纸面账本完全独立。nil = 未接入实盘（QMT 链路整体禁用）。
	// English: real book (AUTO_TRADING_PLAN M1) — handle for the QMT controller's
	// real_positions/orders/fills access (isolated to live.db). Fully independent of the paper book.
	RealStore *store.DB // 实盘账本库（live.db）
	// D1Store D1 评分历史库（d1_scores 表，研究侧数据）：必须留在研究库 trading.db，不可与实盘账本混库。
	// English: D1 score history store (d1_scores, research-side) — must stay in the research DB (trading.db).
	D1Store *store.DB // D1 评分库（trading.db）
	// ShadowExec §WS-G 影子执行器开关：置真时控制器用 ShadowExecutor（决策落 shadow_orders、
	// 回执受理、永不真下），staging 环境全链路验证而不碰钱。
	// English: §WS-G shadow-executor switch — when true the controller uses ShadowExecutor (decisions
	// persisted to shadow_orders, echoed as accepted, never really placed) for full staging runs.
	ShadowExec bool // 影子执行器（staging）
}

// InitStage 引擎初始化进度阶段。
// English: InitStage describes the per-account engine initialization progress.
type InitStage struct {
	Stage   string `json:"stage"`       // 当前阶段（loading_config / building_engine / loading_data / ready）
	Percent int    `json:"percent"`     // 进度百分比 0~100
	EtaSec  int    `json:"eta_seconds"` // 预计还需秒数
}

// Registry 多账号引擎注册表（懒加载 + 按配置指纹共享计算引擎）：
//   - 账号首次登录时才创建其引擎（懒加载）
//   - 战法配置指纹一致的账号（即使 userID 不同）复用同一个 Engine 实例——
//     战法只算一遍，结果分配给多个一致账号；同一账号不同设备天然返回同一引擎。
//   - 配置指纹不一致的账号各自独立引擎（独立开关/持久化）。
//
// English: the multi-account engine registry with lazy load and config-fingerprint sharing —
// accounts whose strategy config fingerprint matches (even different userIDs) reuse one Engine
// instance, so the strategy is computed once and its results serve all matching accounts; the same
// account on any device gets the same engine. Accounts with different fingerprints get their own.
type Registry struct {
	mu       sync.Mutex            // 保护注册表字段的并发锁
	opts     EngineOptions         // 共享依赖（全局一份）
	cores    map[string]*Engine    // configFingerprint → Engine（共享计算引擎）
	byUser   map[string]*Engine    // userID → Engine（账号归属引擎）
	initDone map[string]bool       // userID → 是否已完成初始化
	initProg map[string]*InitStage // userID → 当前初始化进度

	// 账户级模拟盘：每账号独立 paper 引擎（独立现金/持仓/成交，独立 paper.json）。
	// English: per-account paper engines — each account owns an isolated paper book (cash/positions/
	// trades, own paper.json) under accounts/<userID>/.
	papers    map[string]*paper.Engine // userID → paper 引擎（懒加载创建）
	coreUsers map[*Engine][]string     // 共享引擎 → 服务账号列表（信号按账号分发模拟盘）
	// 盘后落库：每账号导出日期记录（一天一次）+ 导出回调（main 注入 server.ExportPaperToResearch）。
	// English: post-close export — per-account last-export date (once a day) + the export callback
	// (wired by main to server.ExportPaperToResearch).
	paperExportDay map[string]string                     // userID → 最近盘后导出日期
	dayCloseHook   func(userID string, pe *paper.Engine) // 盘后导出回调

	// 战法分仓：全局资金池类型模板（engine.ActivePoolTypes 注入），新老账号模拟盘据此分池，
	// 每个战法池只扣自己战法的预算（防波动突破垄断）。English: strategy pooling — the global
	// pool-type template (injected from engine.ActivePoolTypes); each account's paper splits cash by it.
	paperPoolTypes []string // 启用的战法资金池类型列表
	// paperLabelFn §C 规则池 ID→显示名 解析器（新账号懒加载引擎继承）
	paperLabelFn func(string) string
	// 自动撮合账号判定：仅返回 true 的账号参与按战法自动建仓/自动估值（admin）；
	// 普通用户的模拟盘纯手动 + 静态存储，不联动任何自动行为。nil = 默认全部自动（兼容旧行为）。
	// English: auto-paper account check — only accounts returning true get strategy-driven auto-fills
	// and auto-marks (admin); normal users' paper is purely manual + static. nil = all auto (legacy).
	autoCheck func(userID string) bool
	// §P1-4 管理员判定（与 autoCheck 同源，main 注入 auth.IsAdmin）：build 阶段透传给每账号引擎，
	// 供 primaryMember 优先选择管理员成员作为实盘账本/QMT 控制器归属。
	// English: P1-4 admin predicate (same source as autoCheck, wired from auth.IsAdmin) — forwarded to
	// each account engine so primaryMember can prefer an admin owner.
	isAdminFn func(userID string) bool
}

// NewRegistry 创建引擎注册表。
// English: creates the engine registry.
func NewRegistry(opts EngineOptions) *Registry {
	return &Registry{
		opts:           opts,
		cores:          make(map[string]*Engine),
		byUser:         make(map[string]*Engine),
		initDone:       make(map[string]bool),
		initProg:       make(map[string]*InitStage),
		papers:         make(map[string]*paper.Engine),
		coreUsers:      make(map[*Engine][]string),
		paperExportDay: make(map[string]string),
	}
}

// paperPath 返回某账号模拟盘持久化路径（accounts/<userID>/paper.json）。
// English: returns the per-account paper persistence path.
func (r *Registry) paperPath(userID string) string {
	if r.opts.DataDir == "" || userID == "" {
		return ""
	}
	return filepath.Join(r.opts.DataDir, "accounts", userID, "paper.json")
}

// paperMirror 构造某账号模拟盘的账本镜像回调（阶段1.2 两本账合一）：
//   - open：模拟盘新开仓后写 report 持仓账（稳定键 pap_<code>；止盈/止损按战法映射 + ATR 动态止损；
//     dragon 补 limit_price 炸板基准）。AutoTrackSignals 关闭时不写（沿用 C3 开关语义，退出引擎随之不评估）；
//     已有同码持仓记录（如手动建仓）幂等跳过。
//   - close：整笔清仓时按 pap_<code> 平掉 report 记录（部分减仓不触发，记录保留至最终平仓）。
//
// rpt 未注入时返回 (nil, nil)，镜像整体停用。
// English: builds an account's book-mirror callbacks (unified books): open writes the report holding
// book after a new paper open (stable key pap_<code>; TP/SL from the strategy mapping plus the ATR
// dynamic stop; dragon gets limit_price as the broken-seal baseline). Gated by AutoTrackSignals (C3
// semantics — exits skip when off) and idempotent against existing same-code records. close closes the
// pap_-keyed report record on full exits (partial trims don't fire). Returns (nil, nil) without a report.
func (r *Registry) paperMirror(userID string) (func(paper.Position), func(string, float64, float64, string)) {
	rpt := r.opts.Rpt
	if rpt == nil {
		return nil, nil
	}
	cm := r.opts.CfgMgr
	open := func(pos paper.Position) {
		// AutoTrackSignals 开关：关闭时不写纸面持仓记录（与 C3 行为一致）
		// English: AutoTrackSignals gate — no holding record when off (same as C3).
		if cm != nil {
			if rules := cm.GetRulesFor(userID); rules != nil && !rules.Position.AutoTrackSignals {
				return
			}
		}
		if rpt.HasHoldingFor(userID, pos.Code) {
			return // 已有同码同账号记录，幂等跳过（按账号隔离，避免跨账号误判）
		}
		disc := config.DefaultDisciplineConfig()
		atrOn, atrMult := false, 0.0
		if cm != nil {
			if rules := cm.GetRulesFor(userID); rules != nil {
				disc = rules.Paper.Discipline // §统一纪律 E：镜像止盈止损统一走总纪律（默认+15/−6）
				atrOn = rules.Position.ATREnabled
				atrMult = rules.Position.ATRStopMult
			}
		}
		tp, sl := paperOpenTpSl(disc)
		if atrOn && atrMult > 0 && pos.ATR > 0 && pos.CostPrice > 0 {
			if s := pos.ATR * atrMult / pos.CostPrice * 100; s > 0 {
				sl = s // ATR 动态止损优先（C4），无效回退固定百分比
			}
		}
		meta := map[string]float64{}
		if pos.StrategyType == "dragon" && pos.SignalPrice > 0 {
			meta["limit_price"] = pos.SignalPrice // 炸板回落基准=买入触发价
		}
		// §多账号隔离：镜像建仓写入归属账号 userID，避免多账号下 report 持仓串号
		// （此前漏打 user_id，导致不同账号的纸面持仓在 report 账里互相可见/被错误消费）。
		rpt.LogSignalWithMetaQtyUser("pap_"+pos.Code, pos.Code, pos.Name, "做多", pos.Strategy,
			pos.CostPrice, tp, sl, float64(pos.Qty), meta, userID)
		log.Printf("[registry] 镜像开仓 %s(%s) 战法:%s 数量%d 止盈%.0f%% 止损%.0f%%",
			pos.Name, pos.Code, pos.StrategyType, pos.Qty, tp, sl)
	}
	closeFn := func(code string, price, _ float64, reason string) {
		rpt.LogExit("pap_"+code, price, reason)
	}
	return open, closeFn
}

// GetPaper 返回某账号的独立模拟盘引擎（懒加载创建/恢复；未启用或不可用返回 nil）。
// 每账号独立现金/持仓/成交（初始资金默认取 rules.paper.initial_capital，可经 reset 自定义）。
// English: returns an account's independent paper engine (lazily created/restored; nil when disabled).
// Each account has its own cash/positions/trades (initial capital defaults to rules.paper, overridable
// via reset).
func (r *Registry) GetPaper(userID string) *paper.Engine {
	if userID == "" {
		return nil
	}
	r.mu.Lock()
	if pe, ok := r.papers[userID]; ok {
		r.mu.Unlock()
		return pe
	}
	if r.opts.Paper == nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	// §E9 修复：磁盘恢复 IO（paper.New 读 paper.json）此前发生在全局锁内，会卡住所有账号
	// 的 5s 调度路径；改为锁外构建、重取锁二次检查后再注册。
	cfg := r.opts.Paper.Cfg()
	pe := paper.New(cfg, r.paperPath(userID))
	// §SIGNAL_CONTROLLER 20260917：统一纪律（买入确认窗）注入已随 paper 状态机删除——
	// rules.paper.discipline 现由引擎 paperSignalPolicy 装配进信号控制器 paper 通道裁定。
	// English: discipline injection moved to the signal controller's paper policy builder.
	// 两本账合一（阶段1.2）：paper 为唯一真实账本，开仓/清仓镜像写 report 持仓账，
	// 使 CheckPositionsExits 离场路径、持仓页、打分池消费的 rpt 与模拟盘保持一致。
	// English: unified books — paper is the single source of truth; opens/closes mirror into the report
	// holding book so the rpt consumed by exit engines / positions page / scoring pool stays consistent.
	if open, closeFn := r.paperMirror(userID); open != nil {
		pe.SetMirror(open, closeFn)
		// §P-AS1 存量持仓回灌（2026-09-02 用户实录「模拟盘不自动卖出」根修一层）：
		// GetPaper 从磁盘恢复的既有持仓此前只活在账号 paper.json，从未写回共享 rpt（镜像只在
		// 新成交时触发），导致 CheckPositionsExits/CheckPositionAlerts 只评估 rpt 那一两条镜像持仓，
		// 账号真实持仓（如 20 仓满仓）对退出引擎完全隐形而不产生卖出信号。现按现有持仓逐笔
		// 幂等回灌（open 回调内 HasHoldingFor 去重），重启后退出引擎能看到全部账号持仓。
		// English: backfill restored holdings into the shared report book (root-fix layer 1). Certificates
		// from disk only lived in the account paper.json, never in rpt (mirror only fires on new fills), so
		// the exit engines only ever saw 1-2 mirrored positions and real holdings stayed invisible — no
		// sell signals. Re-firing the open mirror per existing holding is idempotent via HasHoldingFor.
		for _, p := range pe.Positions() {
			open(p)
		}
	}
	// 新账号继承全局战法资金池模板（分仓，防单战法垄断）。
	// English: a new account inherits the global strategy pool template (allocation against monopolies).
	if len(r.paperPoolTypes) > 0 {
		pe.SetStrategyPools(r.paperPoolTypes)
	}
	if r.paperLabelFn != nil {
		pe.SetPoolLabelResolver(r.paperLabelFn)
	}
	r.mu.Lock()
	if cur, ok := r.papers[userID]; ok { // 并发双构建：先到者胜
		r.mu.Unlock()
		return cur
	}
	r.papers[userID] = pe
	r.mu.Unlock()
	return pe
}

// PaperForUser 返回某账号的独立模拟盘引擎（HTTP 层按账号读取模拟盘）。
// English: returns an account's independent paper engine for the HTTP layer.
func (r *Registry) PaperForUser(userID string) *paper.Engine { return r.GetPaper(userID) }

// usersOf 返回共享引擎当前服务的账号列表（模拟盘信号按账号分发）。
// English: returns the account list a shared engine currently serves (per-account paper dispatch).
func (r *Registry) usersOf(e *Engine) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.coreUsers[e]...)
}

// registerUser 把账号绑定到其计算引擎并登记到共享引擎的账号列表（供模拟盘分发）。
// English: binds a user to its compute engine and registers it on the shared engine's account list
// (for per-account paper dispatch).
func (r *Registry) registerUser(e *Engine, userID string) {
	if e == nil || userID == "" {
		return
	}
	seen := false
	for _, u := range r.coreUsers[e] {
		if u == userID {
			seen = true
			break
		}
	}
	if !seen {
		r.coreUsers[e] = append(r.coreUsers[e], userID)
	}
	// §修复 FIX#11：多账号共享同一计算引擎且实盘（QMT）开启时显式告警——
	// 共享引擎只有一套 QMT 控制器（首建账号），多账号下任何成员改动配置/熔断将互相影响，
	// 且实盘账本归属 primaryMember。历史上指纹不含 QMT 段导致同战法配置账号共享实盘控制器。
	// 告警不阻断（单账号部署/collab 共享无实盘场景无影响）；若确认多账号需要实盘，应按账号拆分引擎。
	// English: §FIX#11 — when a compute engine is shared by more than one account AND live trading is
	// on, log an explicit warning (non-blocking): a shared engine carries but one QMT controller,
	// so members would share breaker/config/books and could cross-affect each other's real trades.
	if len(r.coreUsers[e]) > 1 && e.QMTEnabled() {
		log.Printf("[engine] 多账号共享引擎(%d个账号)且实盘开启——共享同一 QMT 控制器,任一成员改 QMT 配置/触发熔断将互相影响;如需多账号实盘请按账号拆分引擎",
			len(r.coreUsers[e]))
		opslog.Logf("quant", "多账号共享引擎且实盘开启(%d个账号)：共享 QMT 控制器存在互相覆盖/熔断风险",
			len(r.coreUsers[e]))
	}
	// §GAP2-W2 成员接线（I-2 根修）：把服务账号全集注入引擎——
	// ①私有消息/SSE 扇出按成员路由；②单成员引擎同步固化 userID，恢复账号级配置热同步
	// （此前 Engine.SetUserID 从未被调用，syncAccountConfig 对所有引擎恒跳过）；
	// ③注入 accountsRoot，私有文件（咨询历史）按账号目录寻址。
	e.SetMembers(r.coreUsers[e])
	// §P1-4 透传管理员判定，使 primaryMember 在多成员共享引擎中优先选择管理员账号。
	if r.isAdminFn != nil {
		e.SetIsAdminFn(r.isAdminFn)
	}
	if r.opts.DataDir != "" {
		e.SetAccountsRoot(filepath.Join(r.opts.DataDir, "accounts"))
	}
}

// dispatchPaperSignals 把本轮翻转信号分发给共享引擎服务的账号中"参与自动撮合"的模拟盘
// （各自独立撮合；普通用户账号不自动建仓，模拟盘纯手动）。仅交易时段运行（盘后省内存）。
// §P-AS2 逐仓路由（2026-09-02 用户实录「模拟盘不自动卖出」根修二层）：
// 卖出信号按账号逐仓投递——只发给【当前持有该 code】的账号引擎（pe.Holds 匹配），
// 无持仓账号不再收到无关卖点（此前整批下发+autoSellLocked 内部 no-op，信号归属错位）。
// 买入信号仍全量下发（各账号独立决定现金/上限/池）。卖出+买入合并后一次性 OnSignals。
// exit 为卖出侧纪律信号（CheckPositionsExits/CheckPositionAlerts 产出：止损/止盈/移动止盈/当日跌幅），
// 同样按持仓路由并入，让模拟盘能因止损/止盈/移动止盈线自动离场（此前只发消息不执行——605177 -11.55% 未止损根因）。
// English: dispatches signals to the auto-fill accounts of a shared engine, each filling
// independently (normal users' books are manual). Trading hours only. Per-account routing for sells —
// a sell signal only reaches accounts whose book currently holds that code (pe.Holds), instead of
// broadcasting the whole batch to every account where autoSellLocked silently no-ops. Buys still go
// to all accounts (each independently gates on cash/cap/pool). exit = sell-side discipline signals
// (CheckPositionsExits/CheckPositionAlerts: stop-loss/TP/trailing/daily-drop) merged by routing so the
// paper book auto-exits on the discipline lines (previously message-only — the 605177 -11.55% no-stop root cause).
func (r *Registry) dispatchPaperSignals(e *Engine, emit []combat_agent.Signal, exit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	if !data.IsFullTradingHours(time.Now()) {
		return
	}
	for _, uid := range r.usersOf(e) {
		if !r.isAutoPaper(uid) {
			continue
		}
		pe := r.GetPaper(uid)
		if pe == nil || !pe.Enabled() {
			continue
		}
		// 按 code 拆分卖出信号：仅投递给本账号持有的（逐仓路由）；买入信号保留全量。
		// English: keep sell signals whose code this account holds; keep all buys.
		acct := make([]combat_agent.Signal, 0, len(emit)+len(exit))
		for _, s := range emit {
			if combat_agent.SellAction(s) == "" || pe.Holds(s.Code) {
				acct = append(acct, s)
			}
		}
		for _, s := range exit {
			if combat_agent.SellAction(s) == "" || pe.Holds(s.Code) {
				acct = append(acct, s)
			}
		}
		// §SIGNAL_CONTROLLER 20260917：模拟盘战法白名单/个股·板块黑名单/买入持续性确认统一在此
		// 按账号裁定（paper 通道），非 pass 的买入信号不下发给撮合引擎——paper.Engine 由此
		// 回归纯执行器（池/撮合/费率/持仓上限），不再内嵌交易裁决。裁定留痕在引擎控制器的
		// 审计环（GET /api/signalctl/verdicts）。
		// English: per-account paper-channel admission (strategy whitelist, blacklists, buy-confirm
		// persistence) replaces the old in-engine confirmation; paper.OnSignals is now execution-only.
		acct = e.filterPaperAdmitted(uid, acct, time.Now())
		if len(acct) > 0 {
			pe.OnSignals(acct, quotes)
		}
	}
}

// dispatchPaperMark 用实时快照刷新"参与自动撮合"账号模拟盘的估值与净值（仅交易时段，盘后省内存）；
// 普通用户模拟盘为静态记账（只按手动录入价/手数），不自动估值/快照。
// 交易时段收盘后（15:00 后首次 tick）触发一次盘后落库：当日成交 + 每日快照写入研究库供自动研究。
// English: refreshes marks/equity for the auto-paper accounts of a shared engine — trading hours only
// (after-hours saves memory). Normal users' paper is static bookkeeping (manual price/lot entries) with
// no auto-marking or snapshots. After the close (first tick past 15:00 on a trading day) it triggers one
// post-close export: the day's fills + daily snapshot go to the research DB for auto-research.
func (r *Registry) dispatchPaperMark(e *Engine, quotes map[string]*data.StockInfo) {
	now := time.Now()
	inSession := data.IsFullTradingHours(now)
	for _, uid := range r.usersOf(e) {
		if !r.isAutoPaper(uid) {
			continue
		}
		if pe := r.GetPaper(uid); pe != nil && pe.Enabled() {
			if inSession {
				// §纸面估值修复：与 engine.paperMark 同口径——快照缺价的持仓用最近收盘价回填估值。
				// English: same backfill as engine.paperMark — held codes missing from the snapshot get
				// marked with their last daily close.
				pe.MarkToMarket(backfillPaperQuotes(e, pe, quotes))
				pe.Snapshot(now)
			}
			r.checkDayClose(uid, pe, now)
		}
	}
}

// allPaperHeldCodes 聚合共享引擎服务的全部账号模拟盘持仓代码（含全局模板 opts.Paper 旧回退账本）。
// 供 5s 监控池 base 重建（engine.syncMonitorBase）把每个账号的纸面持仓永久钉入行情监控。
// 账本集合与懒加载预创建口径由 paperEngineSnapshot 统一提供（§QUOTE_POOL_SPLIT）。
// English: aggregates the paper-held codes of every account served by the shared engine, including the
// global template opts.Paper (legacy fallback book), for the base-pool rebuild to pin all paper holdings
// into perpetual quote monitoring. The book set (with eager creation of lazy per-account engines) comes
// from paperEngineSnapshot.
func (r *Registry) allPaperHeldCodes() []string {
	var out []string
	for _, pe := range r.paperEngineSnapshot() {
		for _, p := range pe.Positions() {
			if p.Code != "" {
				out = append(out, p.Code)
			}
		}
		// §SHORT-3 融券空头同样永久钉入监控：买回/止损估值不因掉出 hot 池而缺行情。
		for _, s := range pe.ShortPositions() {
			if s.Code != "" {
				out = append(out, s.Code)
			}
		}
	}
	return out
}

// paperEngineSnapshot 取"当前全部模拟盘账本"快照：已懒加载的各账号引擎 + 全局模板账本 opts.Paper。
// §QUOTE_POOL_SPLIT 预创建：纸面引擎是懒加载的——若只读 r.papers，重启后未触发过 HTTP 的账号
// 持仓会全部缺失（监控钉入与 §EXIT-RETAIN 的持仓判定都会漏账）。故先对 auto-paper 账号锁外幂等
// GetPaper（首轮即从磁盘恢复持仓），再聚合。所有 Positions() 读取在锁外（磁盘恢复 IO 不占全局锁）。
// English: snapshot of every paper book — each lazily created per-account engine plus the global
// template book, eagerly (idempotently, outside the lock) creating auto-paper accounts' engines so a
// restart without any HTTP traffic cannot hide their holdings.
func (r *Registry) paperEngineSnapshot() []*paper.Engine {
	r.mu.Lock()
	var users []string
	for _, us := range r.coreUsers {
		users = append(users, us...)
	}
	tmpl := r.opts.Paper
	r.mu.Unlock()
	// 锁外预创建：让 auto-paper 账号的纸面引擎先落位（幂等，首轮即恢复磁盘持仓），
	// 否则纸面持仓要等前端首次访问 /api/paper/* 才进监控池。
	if tmpl != nil {
		for _, uid := range users {
			if r.isAutoPaper(uid) {
				r.GetPaper(uid)
			}
		}
	}
	r.mu.Lock()
	pes := make([]*paper.Engine, 0, len(r.papers)+1)
	for _, pe := range r.papers {
		if pe != nil {
			pes = append(pes, pe)
		}
	}
	r.mu.Unlock()
	if tmpl != nil {
		pes = append(pes, tmpl)
	}
	return pes
}

// OpenPositionStrategyCounts 汇总"当前开放持仓按策略键计数"（§EXIT-RETAIN 的单一真相源）：
//   - 实盘账本：opts.RealStore 的 real_positions（跨账号全表——出场覆盖注册表是进程内单份，
//     按账号过滤会让 A 账号的热重载清掉 B 账号本该保留的覆盖）；
//   - 模拟盘账本：paperEngineSnapshot()（各账号 ∪ 全局模板）。
//
// 每笔持仓只入一个键：Strategy 原文（退出引擎正是拿它去匹配覆盖的），Strategy 为空时退回
// StrategyType（=规则池键=规则 ID）。一笔只记一次是战法库 open_positions 不翻倍的前提——
// 同一笔持仓既算进 ID 又算进显示名，页面就会显示 2 笔。
// 融券空头不计：库规则（fac_/pat_）只做多，空头来自四个手写做空战法，其止损链不经出场覆盖注册表。
//
// 只读、不改状态。实盘库未接入或查询失败时按"无持仓"计并留一条日志：保守方向是"不额外保留
// 覆盖"（即旧行为），绝不因读库失败而静默放宽持仓的止盈口径。
// 本方法同时服务 EngineRegistry（server 侧战法库 open_positions 字段）与 combat_agent 的持仓回调。
//
// English: counts open positions by strategy key — the live book (all accounts) plus every paper
// book — and registers one key per position (Strategy, falling back to StrategyType). Used both to
// keep a disabled rule's exit overrides alive while it still owns positions and as the
// open_positions figure in the strategy-library payload.
func (r *Registry) OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys {
	out := combat_agent.HeldStrategyKeys{}
	r.mu.Lock()
	realDB := r.opts.RealStore
	r.mu.Unlock()
	if realDB != nil {
		ps, err := realDB.RealPositions()
		if err != nil {
			log.Printf("[registry] §EXIT-RETAIN 实盘持仓读取失败(按无持仓计，不额外保留覆盖): %v", err)
		}
		for _, p := range ps {
			out.Add(p.Strategy)
		}
	}
	for _, pe := range r.paperEngineSnapshot() {
		for _, p := range pe.Positions() {
			key := p.Strategy
			if key == "" {
				key = p.StrategyType
			}
			out.Add(key)
		}
	}
	return out
}

// checkDayClose 每日盘后（交易日 15:00 后）首次调用时触发一次盘后导出 hook（当日成交 + 每日快照
// 落研究库）。按账号记录导出日期，一天只导一次；幂等写入由 store 的唯一键保证。
// English: fires the post-close export hook once per account per day — on the first call after 15:00 on a
// trading day (exports the day's fills + daily snapshot to the research DB). One export per day per
// account; store unique keys keep the write idempotent.
func (r *Registry) checkDayClose(userID string, pe *paper.Engine, now time.Time) {
	cn := cntime.In(now) // §TZ1 北京 15 点为界（宿主机 Local 曾致 UTC 主机判定漂移 8 小时）
	if !data.IsTradingDay(cn) || cn.Hour() < 15 {
		return
	}
	day := now.Format("2006-01-02")
	r.mu.Lock()
	last := r.paperExportDay[userID]
	if last == day {
		r.mu.Unlock()
		return
	}
	hook := r.dayCloseHook
	r.mu.Unlock()
	// §E7 修复：先执行 hook 成功后再记账——此前先占坑后执行，DB 抖动一次该账号当日
	// 研究数据即缺失且当天永不重试。导出本身幂等（store 唯一键兜底），重跑安全。
	if hook != nil {
		hook(userID, pe)
	}
	r.mu.Lock()
	r.paperExportDay[userID] = day
	r.mu.Unlock()
}

// SetDayCloseExport 注入盘后导出回调（main 接线 server.ExportPaperToResearch：把模拟盘当日成交与
// 每日快照写入研究库，供自动研究消费）。
// English: injects the post-close export callback (main wires server.ExportPaperToResearch, which writes
// the paper day's fills + daily snapshot into the research DB for auto-research).
func (r *Registry) SetDayCloseExport(fn func(userID string, pe *paper.Engine)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dayCloseHook = fn
}

// isAutoPaper 判断某账号是否参与自动撮合/自动估值。autoCheck 未注入（nil）时默认全部自动（兼容旧行为）。
// English: reports whether an account joins auto-fill/auto-mark. A nil autoCheck defaults to all-auto
// (legacy-compatible).
func (r *Registry) isAutoPaper(userID string) bool {
	r.mu.Lock()
	fn := r.autoCheck
	r.mu.Unlock()
	if fn == nil {
		return true
	}
	return fn(userID)
}

// SetAutoPaperCheck 注入"是否参与自动撮合/估值"的判定函数（main 注入 auth.IsAdmin：
// admin 账号自动按战法建仓，普通用户仅手动 + 静态存储）。
// English: injects the auto-paper check (main wires auth.IsAdmin — admin accounts get strategy-driven
// auto-fills; normal users are manual-only + static).
func (r *Registry) SetAutoPaperCheck(fn func(userID string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoCheck = fn
}

// SetAdminCheck §P1-4 注入管理员判定函数（main 用 auth.IsAdmin 装配），透传给各账号引擎。
// English: P1-4 wires the admin predicate (from main's auth.IsAdmin) and forwards it to each engine.
func (r *Registry) SetAdminCheck(fn func(userID string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.isAdminFn = fn
}

// SetPaperPools 设置全局战法资金池类型模板并同步到所有已建账号模拟盘。
// 幂等：pe.SetStrategyPools 在池集合未变时保留各池现金，热加载（因子/形态审批、启停）后调用安全。
// 注入路径：engine.ActivePoolTypes → registry.SetPaperPools → 各账号 pe.SetStrategyPools。
// English: sets the global strategy pool-type template and syncs it to every existing paper engine.
// Idempotent: SetStrategyPools keeps cash while the type set is unchanged, so hot reloads are safe.
// Injected via engine.ActivePoolTypes → registry.SetPaperPools → each account's pe.SetStrategyPools.
func (r *Registry) SetPaperPools(types []string) {
	r.mu.Lock()
	r.paperPoolTypes = append([]string(nil), types...)
	pes := make([]*paper.Engine, 0, len(r.papers))
	for _, pe := range r.papers {
		pes = append(pes, pe) // 锁内快照引擎列表，锁外逐个下发避免长时间持锁
	}
	r.mu.Unlock()
	for _, pe := range pes {
		pe.SetStrategyPools(types)
	}
}

// SetPaperConfig §修复 S2（2026-08-29）：后台更新账户级模拟盘配置后热同步到模板与所有运行中的账号实例。
// 此前 paper.New 在创建时一次性快照 r.opts.Paper.Cfg()，之后改费率/滑点/开关对运行账号不生效。
// English: S2 — update account-level paper config on the template and push it to every running engine.
func (r *Registry) SetPaperConfig(cfg paper.Config) {
	if r.opts.Paper != nil {
		r.opts.Paper.UpdateConfig(cfg)
	}
	r.mu.Lock()
	pes := make([]*paper.Engine, 0, len(r.papers))
	for _, pe := range r.papers {
		pes = append(pes, pe)
	}
	r.mu.Unlock()
	for _, pe := range pes {
		pe.UpdateConfig(cfg)
	}
}

// SetPaperLabelResolver 注入规则池 ID → 显示名 解析器（§C 规则细分池：fac_1→"因子战法#1"）。
// 同步到所有已建账号引擎并记住，供后续懒加载的新账号引擎继承。
// English: injects the rule-pool id→label resolver into every existing paper engine and remembers it
// for lazily created ones.
func (r *Registry) SetPaperLabelResolver(fn func(string) string) {
	r.mu.Lock()
	r.paperLabelFn = fn
	pes := make([]*paper.Engine, 0, len(r.papers))
	for _, pe := range r.papers {
		pes = append(pes, pe)
	}
	r.mu.Unlock()
	for _, pe := range pes {
		pe.SetPoolLabelResolver(fn)
	}
}

// SetInitProgress 更新某账号引擎的初始化进度（供前端登录进度条轮询）。
// English: updates an account engine's init progress for the frontend login progress bar.
func (r *Registry) SetInitProgress(userID, stage string, percent, etaSec int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initProg[userID] = &InitStage{Stage: stage, Percent: percent, EtaSec: etaSec}
}

// InitStatus 返回某账号引擎的初始化状态；未初始化过时返回 nil。
// English: returns an account engine's init status, or nil if never initialized.
func (r *Registry) InitStatus(userID string) *InitStage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.initProg[userID]; ok {
		cp := *p
		if done := r.initDone[userID]; done {
			cp.Percent = 100
			cp.EtaSec = 0
			cp.Stage = "ready"
		}
		return &cp
	}
	return nil
}

// GetOrCreate 返回某账号的引擎实例；不存在时懒加载创建。
// 配置指纹相同的账号复用同一个共享引擎（战法只算一遍）；指纹不同则按账号独立构建。
// 同一账号并发调用返回同一实例（内部加锁 + initDone 防重复构建）。
// English: returns an account's engine, lazily creating it if absent. Accounts with the same config
// fingerprint share one engine (the strategy is computed once); differing fingerprints build their
// own. Concurrent calls for the same account return the same instance (lock + initDone).
func (r *Registry) GetOrCreate(userID string) *Engine {
	if userID == "" {
		return nil
	}
	r.mu.Lock()
	if e, ok := r.byUser[userID]; ok {
		r.mu.Unlock()
		return e
	}
	if r.initDone[userID] {
		r.mu.Unlock()
		return r.byUser[userID]
	}
	r.initProg[userID] = &InitStage{Stage: "loading_config", Percent: 5, EtaSec: 30}

	// 计算该账号的战法配置指纹，优先复用指纹一致的共享引擎（战法只算一遍）
	fp := r.fingerprint(userID)
	if e, ok := r.cores[fp]; ok {
		r.byUser[userID] = e
		r.initDone[userID] = true
		r.initProg[userID] = &InitStage{Stage: "ready", Percent: 100, EtaSec: 0}
		r.registerUser(e, userID)
		r.mu.Unlock()
		return e
	}
	r.mu.Unlock()

	// 无匹配共享引擎 → 构建新引擎（构建在锁外，避免持锁做耗时工作）
	e := r.build(userID)
	r.mu.Lock()
	// §E5 修复：构建期间同指纹用户 B 可能已先注册——二次检查，丢弃重复引擎，
	// 否则相同配置算两遍且两引擎流水线各自为政。
	if cur, ok := r.cores[fp]; ok {
		r.byUser[userID] = cur
		r.initDone[userID] = true
		r.initProg[userID] = &InitStage{Stage: "ready", Percent: 100, EtaSec: 0}
		r.registerUser(cur, userID)
		r.mu.Unlock()
		log.Printf("[registry] 同指纹引擎已被并发构建，复用现有实例 (user=%s fp=%s)", userID, fp[:12])
		return cur
	}
	r.cores[fp] = e
	r.byUser[userID] = e
	r.initDone[userID] = true
	r.initProg[userID] = &InitStage{Stage: "ready", Percent: 100, EtaSec: 0}
	r.registerUser(e, userID)
	r.mu.Unlock()
	return e
}

// build 按账号构建独立引擎实例（独立 combat_agent + 独立持久化目录 + 按账号初始化开关）。
// English: builds a per-account engine instance (independent combat agent, per-account data
// directory, account-initialized toggles).
func (r *Registry) build(userID string) *Engine {
	opts := r.opts
	r.SetInitProgress(userID, "building_engine", 30, 20)

	// 每账号独立持久化目录（signals_today/messages/scores/stage_records 等按账号隔离）
	acctDir := ""
	if opts.DataDir != "" {
		acctDir = filepath.Join(opts.DataDir, "accounts", userID)
		// 账号目录可能尚不存在（首次登录/新账号），必须先建好，否则信号固化/消息/评分落盘全部静默失败。
		// English: the per-account directory may not exist yet (first login / new account); create it first
		// or signal pinning / messages / score persistence all fail silently.
		if err := os.MkdirAll(acctDir, 0755); err != nil {
			log.Printf("[registry] 账号目录创建失败 %s: %v", acctDir, err)
		}
	}
	r.SetInitProgress(userID, "building_engine", 60, 10)

	// 战法代理：按账号策略配置 + 独立 runner（runner 按账号读取配置）
	sc := opts.CfgMgr.GetStrategyConfigFor(userID)
	cAgent := combat_agent.New(sc)
	// §P1-C 改用 per-user getter 装配 D1/ATR：引擎按账号指纹隔离后，其首建用户即 owner，
	// 必须用该账号的覆盖值（而非全局 Rules），否则 build 阶段 D1/ATR 与后续 syncAccountConfig 不一致。
	posCfg := opts.CfgMgr.GetRulesFor(userID).Position
	cAgent.SetLaodengConfig(&opts.CfgMgr.Get().Laodeng) // §0925EVE-D1：全局 rules 快照经加锁访问器取得
	cAgent.SetPositionDailyDropPct(posCfg.DailyDropAlertPct)
	cAgent.SetD1Config(opts.CfgMgr.GetD1ConfigFor(userID))
	cAgent.SetATRStop(posCfg.ATREnabled, posCfg.ATRStopMult)
	// §EXIT-RETAIN 出场覆盖跟随持仓，不跟随启用开关：给战法代理注入**跨账号**开放持仓策略键回调
	// （实盘账本 ∪ 全部已建账号模拟盘），启动装配则取同一逻辑的一份即时快照。
	// 出场覆盖注册表是进程内单份，所以"是否仍持有"的判定必须全局聚合——只看本账号持仓的话，
	// A 账号热重载会把 B 账号本该因持仓而保留的覆盖清掉。回调形态（而非快照值）保证后续热重载
	// 读的是当时的持仓，而不是启动那一刻的。
	// English: exits follow positions, not the enable flag — the agent gets a *global* open-position
	// provider (live book ∪ every account's paper book), while startup assembly seeds one immediate
	// snapshot; the registry is process-wide, so per-account data would clear another account's
	// retained overrides on hot reload.
	heldExit := r.OpenPositionStrategyCounts()
	// 快照只喂启动装配这一次；下面的回调供后续每次热重载重算（两者判定逻辑同源）。
	cAgent.SetExitHeldProvider(r.OpenPositionStrategyCounts)
	cAgent.SetRunners(newAccountRunners(opts.CfgMgr, opts.Matcher, userID, opts.DataDir, heldExit))
	// §SHORT-1 注入做空四战法 runner（高位滞涨/放量破位/龙头断板/利好兑现砸盘）。
	cAgent.SetShortRunners(combat_agent.NewShortRunners(opts.CfgMgr))
	cAgent.SetShortEnabled(opts.CfgMgr.GetLongShortConfigFor(userID).ShortEnabled)
	// 注入盘口因子回调：信号生成后对命中个股拉取买卖压力/封单量（免费五档，Level-2 可扩十档）。
	// English: inject the order-book factor fetcher — after signal generation, pull bid/ask pressure and
	// seal volumes for hit stocks (5 levels free; Level-2 can extend to ten).
	if opts.MarketAPI != nil {
		cAgent.SetDepthFactorFn(func(code string) *data.OrderBookFactors {
			ob, err := opts.MarketAPI.GetOrderBook(code)
			if err != nil || ob == nil {
				return nil
			}
			f := ob.Factors(5)
			return &f
		})
	}

	// 独立看板聚合器（该引擎只更新自己的看板，前端按账号读取）
	agg := display.New()

	e := New(
		opts.MarketAPI,
		opts.NewsAgent,
		opts.StrategyEng,
		opts.SectorAgent,
		cAgent,
		agg,
		opts.Rpt,
		opts.StockTracker,
		opts.WlMgr,
		opts.SSE,
		opts.LLMClient,
		opts.THS,
		acctDir,
	)
	// 共享引擎不绑定单一账号（userID 留空）：其配置已按共享组固化，
	// 运行期不再读取特定账号配置，所有共享账号读取同一份结果。
	// English: shared engines don't bind to a single account (userID stays empty); their config is
	// pinned at build time from the shared group, so all sharing accounts read identical results.
	e.SetCfgMgr(opts.CfgMgr)
	e.SetScanner(opts.Scanner)
	e.SetFetcher(opts.Fetcher)
	e.SetNotifier(opts.Notifier)
	e.SetEmotionConfig(&opts.CfgMgr.Get().Emotion) // §0925EVE-D1：字段裸读改加锁访问器
	if opts.SectorTopN > 0 {
		e.SetSectorConstituentTopN(opts.SectorTopN)
	}
	if opts.D1MaxRetries > 0 {
		e.SetD1MaxRetries(opts.D1MaxRetries)
	}
	if opts.D1MaxTokens > 0 {
		e.SetD1MaxTokens(opts.D1MaxTokens)
	}
	// §MARKET_RISK_GATE P0：注入统一协调器（风险盘口主源）。协调器为空时引擎自动回落东财直连。
	if opts.Coordinator != nil {
		e.SetCoordinator(opts.Coordinator, opts.RiskHithinkPrimary)
	}
	// 模拟盘引擎：账户级独立实例（每账号独立 paper.json），信号/估值按账号分发。
	// 全局模板仅提供配置（opts.Paper）；e.paper 保留为旧单引擎回退。
	// English: per-account paper engines (own paper.json); signals/marks dispatch per account. The global
	// template only supplies config; e.paper stays as the legacy single-engine fallback.
	e.SetPaper(opts.Paper)
	e.SetPaperDispatch(
		func(emit []combat_agent.Signal, exit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
			r.dispatchPaperSignals(e, emit, exit, quotes)
		},
		func(quotes map[string]*data.StockInfo) { r.dispatchPaperMark(e, quotes) },
	)
	// §SELLPOINT-UNIFY P3 模拟盘并轨：注入按账号纸面账卖出裁决回调（ChannelPaper，
	// 键=(通道,账号,纯码)，参数取账号级 paper 纪律）。裁决必须每轮无条件推进
	//（观察窗窗长/结算栅格以真实时钟为准），不能依附"本轮恰好有信号"的撮合分发时机，
	// 故与 dispatchPaperSignals 解耦、由主循环每轮经 judgePaperLedgers 调用。
	// mode=on 时处置经 paper.ApplyUnifiedSell 唯一出口执行；探测器卖出直达撮合
	// 已在 paperSignals 的证据闸（unifiedSellGateSigs）关闭。
	// English: per-account paper-channel unified sell judge hook — runs every round (wall-clock
	// windows), decoupled from fill dispatch; disposals execute only via paper ApplyUnifiedSell.
	e.SetPaperSellJudge(func(feed sellJudgeFeed, sellMode string) {
		if !data.IsFullTradingHours(time.Now()) {
			return
		}
		for _, uid := range r.usersOf(e) {
			if !r.isAutoPaper(uid) {
				continue
			}
			pe := r.GetPaper(uid)
			if pe == nil || !pe.Enabled() {
				continue
			}
			// §M9 sellMode=宿主轮首快照原样透传（回调内不再读配置）。
			e.runPaperUnifiedJudge(uid, pe, feed, e.paperSignalPolicy(uid), sellMode)
		}
	})
	// §QUOTE_POOL_SPLIT: 注入全账号模拟盘持仓聚合——5s 监控池 base 重建（syncMonitorBase）把
	// 每个账号的纸面持仓永久钉入行情监控，持仓估值/自动卖出不再因掉出 hot 池而缺行情。
	// English: inject the all-accounts paper-held aggregator so the base-pool rebuild pins every
	// account's paper holdings into perpetual quote monitoring.
	e.SetPaperHeldCodesFn(r.allPaperHeldCodes)
	// 实盘交易（AUTO_TRADING_PLAN M1）：QMT 执行控制器 + 实盘账本 store（独立于纸面账本）。
	// 引擎每 5s 把 qmt 配置热同步给控制器（syncAccountConfig），熔断/健康探测随分析循环节流执行。
	// English: live trading (AUTO_TRADING_PLAN M1) — QMT controller + real-book store, independent of the
	// paper book. The engine hot-syncs the qmt config each 5s cycle (syncAccountConfig); breaker/health
	// probing runs throttled inside the advice loop.
	if opts.RealStore != nil {
		// §QMT-PENDING 构建期也走账号级配置（GetQMTConfigFor），与运行期热同步（syncAccountConfig）
		// 同源——避免构建时用全局 GetRulesFor().QMT（磁盘 rules.qmt）读到与账号级覆盖不同的
		// enabled 值，导致初始 executor 类型与前端展示不一致。
		qmtCfg := *opts.CfgMgr.GetQMTConfigFor(userID)
		// 熔断/恢复告警走推送器（与 P1 强提醒同通道）
		onAlert := func(level, title, content string) {}
		if opts.Notifier != nil {
			onAlert = func(level, title, content string) {
				// 通用级别映射：仅支持 high/low 两档，其余归中档。
				notifyLevel := notify.LevelMedium
				switch level {
				case "high":
					notifyLevel = notify.LevelHigh
				case "low":
					notifyLevel = notify.LevelLow
				}
				opts.Notifier.Push(notify.Message{
					Level:   notifyLevel,
					Title:   title,
					Content: content,
				})
			}
		}
		// 网关客户端：真实网关或 noop（enabled=false / URL 为空时 noop 降级，仅记账不真下）。
		// §WS-G staging：ShadowExec 置真时无论 qmt.enabled 一律用影子执行器（决策留痕、永不真下）。
		// English: §WS-G staging — ShadowExec=true forces the shadow executor regardless of qmt.enabled
		// so staging exercises the full decision tree without real orders.
		var exec trading.Executor = trading.NoopExecutor{}
		if opts.ShadowExec {
			exec = trading.NewShadowExecutor(opts.RealStore, userID)
		} else if qmtCfg.Enabled && qmtCfg.GatewayURL != "" {
			exec = trading.NewQMTClient(qmtCfg.GatewayURL, qmtCfg.Token,
				time.Duration(qmtCfg.TimeoutSec)*time.Second, 1)
		}
		ctrl := trading.NewController(exec, opts.RealStore, userID, qmtCfg, onAlert)
		// §XCHECK 2026-09-22 C批：价格复核闸接线——把统一行情协调器的独立复核取价
		// （CrossCheckPrice，新浪→腾讯→东财多源链）注入该账号控制器风控闸。
		// Coordinator 缺席（未装配行情源）时不注入，闸自然跳过；闸默认关（cross_check_pct=0）
		// 且开启后默认影子（命中仅留痕放行），本接线本身零行为变化。
		// English: §XCHECK wires the coordinator's independent cross-check price source into the
		// per-account gate; absent coordinator or pct=0 keeps the gate inert (shadow by default).
		if opts.Coordinator != nil {
			ctrl.SetCrossPriceSource(opts.Coordinator.CrossCheckPrice)
		}
		// 引擎侧两个库职责分离（§UAT-2026-09-08 修复）：
		//  - realStore = 实盘账本库（live.db）：pushRealAdvice 读 real_positions 生成建议/自动卖出；
		//  - d1Store   = 研究库（trading.db）：d1_scores 历史落库（研究侧数据）。
		// 旧实现把 D1Store 传入 SetQMT（第二参=realStore），导致 pushRealAdvice 从研究库读持仓
		// 恒为 0——实盘止损/止盈/自动卖出/M8 链路静默失效。
		// English: two stores with separate duties — realStore=live.db for real_positions (advice/auto-sell
		// reads), d1Store=trading.db for d1_scores history. The old wiring passed D1Store as realStore,
		// so pushRealAdvice read 0 positions and the live SL/TP/auto-sell/M8 chain silently stopped.
		e.SetQMT(ctrl, opts.RealStore)
		e.SetD1Store(opts.D1Store)
		// §WMQ-1：共享引擎的 QMT 热同步源固定为本次装配账号（首建/管理员成员，与 FIX#11
		// 契约的控制器归属一致）。旧实现缺此注入：共享引擎 userID=="" 时 syncAccountConfig
		// 整体跳过，QueueConfigUpdate 永不入队、ApplyPendingConfig 永不消费、executor
		// 类型（Noop↔QMTClient）在交易时段无法切换，配置变更只能靠重启生效。
		// English: WMQ-1 — pin the hot-sync source to the member whose config owned the build-time
		// controller; without it a shared engine never queues QMT config updates (restart-only).
		if userID != "" {
			e.SetQMTCfgSource(userID)
		}
	}
	// 账号开关初始化（按共享组配置固化到引擎，运行期不随单账号变化）
	ls := opts.CfgMgr.GetLongShortConfigFor(userID)
	e.SetLongShortConfig(ls.LongEnabled, ls.ShortEnabled)
	r.SetInitProgress(userID, "ready", 100, 0)
	log.Printf("[engine] 账号 %s 引擎构建完成 (数据目录 %q, 指纹 %s)", userID, acctDir, r.fingerprint(userID)[:8])
	return e
}

// newAccountRunners 构建四大战法 runner；runner 设置账号 ID（按账号读取策略配置）。
// matcher 供 N 形战法 D1 事件匹配使用（可为 nil）。dataDir 用于注入审批通过的因子战法规则（E6）。
// heldExit 为当前开放持仓策略键（§EXIT-RETAIN）：已停用但仍持有持仓的规则，其出场覆盖在启动装配时
// 同样保留（详见 combat_agent.SetRuleExitOverrides）。
// English: builds the four strategy runners; each runner is bound to the account so it reads that
// account's strategy config. matcher feeds the N-shape D1 event match (may be nil). dataDir is used to
// inject the approved factor-strategy rule (E6). heldExit carries the open-position keys that keep a
// disabled rule's exit overrides alive.
// §0925EVE-C1：战法库闸判定为 not_loaded / no_enabled 时返回 **nil**（当轮 fail-close 不出新建议，
// 对偶 §95/LIB-GATE 回放侧判红）；出场覆盖照常入表，不触碰熔断。详见 gateLiveStrategyLibrary。
func newAccountRunners(cfgMgr *config.Manager, matcher *data.EventMatcher, userID string, dataDir string, heldExit combat_agent.HeldStrategyKeys) []combat_agent.StrategyRunner {
	runners := buildRunners(cfgMgr, matcher)
	for i := range runners {
		if setter, ok := runners[i].Strategy.(interface{ SetUserID(string) }); ok {
			setter.SetUserID(userID)
		}
	}
	// E6：从 applied_factors.json 注入全部**启用**的因子战法规则（战法库，多规则同时实盘）。
	// English: E6 — inject all **enabled** factor-strategy rules from applied_factors.json (the
	// strategy library; multiple rules run concurrently).
	// §0925EVE-C1：读库失败不再只 log 后继续——三态判定交给 gateLiveStrategyLibrary（见该函数注释），
	// 被闸时本函数返回 nil runner（当轮战法腿不出新建议），旧 log 保留作现场细节。
	rules, err := research.LoadEnabledFactorRules(dataDir)
	if err != nil {
		log.Printf("[registry] 加载因子战法库失败: %v", err)
	}
	// F3：从 applied_patterns.json 注入全部**启用**的形态模板规则（形态战法库，多形态同时实盘）。
	// English: F3 — inject all **enabled** pattern-template rules from applied_patterns.json (the
	// pattern library; multiple patterns run concurrently).
	patterns, errP := research.LoadEnabledPatternRules(dataDir)
	if errP != nil {
		log.Printf("[registry] 加载形态战法库失败: %v", errP)
	}
	// §P2-d 实盘接线：启动装配时同步规则级出场覆盖（扫参审批的止盈/超期对实盘生效）。
	// §0925EVE-C1：这段刻意排在战法库闸**之前**——闸只拦"新建议"这条腿，持仓的出场覆盖
	// （止盈/超期）属于"资金退路"，即使本轮被 fail-close 也必须照常入表（不熔断方向）。
	// English: seed the rule-level exit-override registry at startup assembly. This runs BEFORE the
	// C1 gate because the gate only suppresses new advice; exit overrides for open positions stay live.
	if fe, e1 := research.ListAppliedFactorRules(dataDir); e1 == nil {
		pe, e2 := research.ListAppliedPatternRules(dataDir)
		if e2 == nil {
			if fe == nil {
				fe = []research.AppliedFactorEntry{}
			}
			if pe == nil {
				pe = []research.AppliedPatternEntry{}
			}
			combat_agent.SetRuleExitOverrides(fe, pe, heldExit)
		}
	}
	// §0925EVE-C1 战法库闸（§95/LIB-GATE 实盘对偶）：读库失败或零条启用规则 → 当轮不出单。
	// 返回 nil runner 即 combat_agent 战法腿零信号（SetRunners(nil)），与"静默按 0 条线上战法
	// 跑完一整轮"告别；三态的量规/告警/日志都在 gateLiveStrategyLibrary 内统一出门。
	if g := gateLiveStrategyLibrary(dataDir, rules, patterns, err, errP); g == liveLibraryGateNotLoaded || g == liveLibraryGateNoEnabled {
		return nil
	}
	if len(rules) > 0 {
		for i := range runners {
			if fs, ok := runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
				fs.SetRules(rules)
				log.Printf("[registry] 因子战法库已启用 %d 条规则", len(rules))
			}
		}
	}
	if len(patterns) > 0 {
		for i := range runners {
			if ps, ok := runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
				ps.SetRules(patterns)
				log.Printf("[registry] 形态战法库已启用 %d 条规则", len(patterns))
			}
		}
	}
	return runners
}

// liveLibraryGate §0925EVE-C1 实盘战法库闸的三态（对偶 §95/LIB-GATE 回放侧判红口）。
// English: the three states of the live-side strategy-library gate — the counterpart of the
// §95/LIB-GATE fail-red on the replay side.
type liveLibraryGate int

const (
	// liveLibraryGateSkipped 未配置持久化目录（dataDir==""）：读库根本不发生于本次装配，
	// 闸不表态也不喂量规——防"进程还没装配过任何账号引擎"被误读成"读库失败"（§CB 防误熔同向）。
	liveLibraryGateSkipped liveLibraryGate = iota
	// liveLibraryGateOK 正常态：读取成功且至少一条启用规则，照旧注入实盘。
	liveLibraryGateOK
	// liveLibraryGateNotLoaded 读库失败态：applied_*.json 存在但不可读/JSON 损坏（注意与
	// "文件缺失"区分——research 侧把缺文件判为"读取成功、零条目"，落在 NoEnabled 态）。
	liveLibraryGateNotLoaded
	// liveLibraryGateNoEnabled 零启用态：读取成功，但因子+形态两侧按实盘口径加载到 0 条启用规则
	// （库里真没启用规则：没文件/空文件/全停用，对偶回放侧 ruleFileReason 的三档成因）。
	liveLibraryGateNoEnabled
)

// String 三态的人读名（日志/测试用）。
func (g liveLibraryGate) String() string {
	switch g {
	case liveLibraryGateSkipped:
		return "skipped"
	case liveLibraryGateOK:
		return "ok"
	case liveLibraryGateNotLoaded:
		return "not_loaded"
	case liveLibraryGateNoEnabled:
		return "no_enabled"
	}
	return "unknown"
}

// gateLiveStrategyLibrary §0925EVE-C1 实盘腿战法库闸的唯一判定点（§95/LIB-GATE 的对偶）。
//
// 背景：§95 批把回放侧改成"战法库零条启用规则即判红"（internal/btreplay/replay.go libraryGate），
// 但真正下单这条腿（engine.Registry.build → newAccountRunners）读库失败只 log 继续跑、零条启用
// 不告警——"静默按 0 条线上战法跑完一整轮"在实盘依然可能发生。本函数把实盘读库路径分成三态并
// 逐态处置：
//   - 读库失败（errF/errP 任一非 nil）→ NotLoaded：当轮 fail-close 不出单；
//   - 读取成功但启用规则数==0 → NoEnabled：当轮同样不出单（两种成因的告警文案严格分开）；
//   - 正常 → OK：照旧。
//
// fail-close 的作用面：newAccountRunners 返回 nil → combat_agent.SetRunners(nil) → 战法扫描腿
// 本轮零信号，即"不出新建议"。**不是**资金熔断（§CB 同向）：不触碰 breaker、不清仓、持仓的
// 止损/止盈/M8 出场链与规则级出场覆盖（§P2-d/§EXIT-RETAIN，在闸之前照常入表）全部不受影响。
//
// 量规接法照仓里现成模式（§CAL-GATE/§ADJ-BASIS 同族：SetGauge + DefaultAlertRules + 路由表）：
//   - live_strategy_library_load_errors：最近一次装配读库失败的侧数（0/1/2）——只数真读失败，
//     与"库里没规则"严格分家（两条告警不许混成一条文案的前提）；
//   - live_strategy_enabled_rules：启用规则数读数（因子+形态合计，对偶回放侧出门的库读数）；
//   - live_strategy_no_enabled：0/1，仅"读取成功且零条启用"为 1。
//
// 三个量规都只在 dataDir!="" 时赋值：未赋值时评估器读到 0，恰好落在"不触发"一侧，不会把
// "还没装配过引擎"误报成"库挂了"。多账号先后装配是进程级最后写者口径，与 trading_calendar_loaded
// 等既有量规一致。
//
// English: the single fail-close decision for the LIVE trading leg's strategy library — the
// counterpart of replay-side §95/LIB-GATE. Load errors and a genuinely-empty library are reported
// through separate gauges/alert wordings; a blocked gate returns nil runners (no new advice this
// round) without touching breaker or the exit chain for open positions.
func gateLiveStrategyLibrary(dataDir string, rules []*factorstrat.ActiveRule, patterns []*patternstrat.ActivePattern, errF, errP error) liveLibraryGate {
	if dataDir == "" {
		return liveLibraryGateSkipped
	}
	errCount := 0
	if errF != nil {
		errCount++
	}
	if errP != nil {
		errCount++
	}
	enabled := len(rules) + len(patterns)

	state := liveLibraryGateOK
	switch {
	case errCount > 0:
		state = liveLibraryGateNotLoaded
		enabled = 0 // 读失败侧的规则数不可知，读数如实写 0，成因由 load_errors 量规承载
	case enabled == 0:
		state = liveLibraryGateNoEnabled
	}
	metrics.SetGauge("live_strategy_library_load_errors", int64(errCount))
	metrics.SetGauge("live_strategy_enabled_rules", int64(enabled))
	metrics.SetGauge("live_strategy_no_enabled", boolGauge(state == liveLibraryGateNoEnabled))

	switch state {
	case liveLibraryGateNotLoaded:
		// 告警腿：量规已喂，p1 规则 live_strategy_library_not_loaded 由 30s 评估节拍送出（路由 RoutePush）。
		log.Printf("[registry] §0925EVE-C1 战法库闸=not_loaded：读库失败 %d 侧（因子 %v / 形态 %v），"+
			"当轮 fail-close 不出新建议（持仓出场不受影响）", errCount, errF, errP)
	case liveLibraryGateNoEnabled:
		// 告警腿：p1 规则 live_strategy_no_enabled_rules——文案刻意与上面区分："库里真没启用规则"≠"读库失败"。
		log.Printf("[registry] §0925EVE-C1 战法库闸=no_enabled：读取成功但零条启用规则（目录 %s 下 "+
			"applied_factors.json/applied_patterns.json 缺失、为空或全停用），当轮 fail-close 不出新建议", dataDir)
	case liveLibraryGateOK:
		log.Printf("[registry] §0925EVE-C1 战法库闸=ok：启用规则 %d 条（因子 %d / 形态 %d）",
			enabled, len(rules), len(patterns))
	}
	return state
}

// buildRunners 构建四大战法 runner（龙/双响炮/N形/龙回头），统一委托给 combat_agent.NewRunners（C7）。
// 账号级引擎通过 SetUserID 让 dragon/double_bump 按账号读取策略参数（N形/龙回头当前不使用全局 cfg）。
// English: builds the four strategy runners (Dragon / Double-Bump / N-shape / Dragon-Return), delegating
// to the unified combat_agent.NewRunners factory (C7). Per-account engines call SetUserID so
// Dragon/Double-Bump read that account's strategy params (N-shape/Dragon-Return currently don't consume
// the manager cfg).
func buildRunners(cfgMgr *config.Manager, matcher *data.EventMatcher) []combat_agent.StrategyRunner {
	return combat_agent.NewRunners(cfgMgr, matcher)
}

// GetController 返回某账号的引擎控制面（懒加载创建），未接入时返回 nil。
// 供 HTTP 层按账号读取/切换引擎（做多/做空开关、消息中心等）。
// English: returns the engine controller for an account (lazily created), or nil when unavailable.
// Lets the HTTP layer read/switch per-account engine state (long/short toggles, message center…).
func (r *Registry) GetController(userID string) server.EngineController {
	return r.GetOrCreate(userID)
}

// TriggerPositionReview §DAILY_REVIEW 手动触发指定账号引擎的盘后持仓 LLM 复盘（懒加载该账号引擎后执行），
// 返回成功复盘的股票数；引擎不可用时返回错误。
// English: force-runs the §DAILY_REVIEW position review on an account's engine (lazily created),
// returning the number of stocks reviewed.
func (r *Registry) TriggerPositionReview(userID string) (int, error) {
	e := r.GetOrCreate(userID)
	if e == nil {
		return 0, fmt.Errorf("账号引擎不可用")
	}
	return e.RunPositionReviewNow()
}

// InitStatusJSON 返回某账号引擎的初始化进度（map 形式，前端轮询登录进度条用）。
// English: returns an account engine's init progress as a map for the frontend login progress bar.
func (r *Registry) InitStatusJSON(userID string) map[string]interface{} {
	st := r.InitStatus(userID)
	if st == nil {
		return map[string]interface{}{"initialized": false, "percent": 0, "eta_seconds": 0, "stage": ""}
	}
	return map[string]interface{}{
		"initialized": r.isReady(userID),
		"stage":       st.Stage,
		"percent":     st.Percent,
		"eta_seconds": st.EtaSec,
	}
}

// isReady 报告某账号引擎是否已完成初始化。
func (r *Registry) isReady(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initDone[userID]
}

// AllControllers 返回所有已创建引擎的控制面（共享引擎去重）。
// English: returns controllers for all created engines (shared engines deduplicated).
func (r *Registry) AllControllers() []server.EngineController {
	es := r.All()
	out := make([]server.EngineController, 0, len(es))
	for _, e := range es {
		out = append(out, e)
	}
	return out
}

// Len 返回已创建的计算引擎数量（共享引擎去重）。
// English: returns how many compute engines exist (shared engines deduplicated).
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[*Engine]bool, len(r.cores))
	for _, e := range r.cores {
		seen[e] = true
	}
	return len(seen)
}

// SetLLMClient 热替换全局 LLM 客户端（设置页保存配置时调用）。
// 三处一并更新：① 注册表依赖模板 opts.LLMClient —— 之后懒加载新建的引擎用新客户端；
// ② 共享新闻归因代理 NewsAgent —— 归因主链路（Stage0/1/2）真正用的就是它；
// ③ 全部存活引擎实例。
// §UI-AUTHORITATIVE 修复（2026-09-14）：此前 main 的 llmRecreate 回调只遍历【当时存活】
// 的引擎，注册表为空（无人登录/盘后回收）或保存后又新建账号引擎时，UI 保存的配置被静默
// 旁路——表现为"设置页保存了但归因仍走旧模型"。
// English: hot-swaps the shared LLM client across the registry template (so future lazily
// built engines pick it up), the shared news agent (the real attribution path), and all
// live engines — previously only live engines were updated, silently bypassing the UI save.
func (r *Registry) SetLLMClient(c *llm.Client) {
	r.mu.Lock()
	r.opts.LLMClient = c // 模板更新：后续 GetOrBuild 新建引擎直接带新客户端
	na := r.opts.NewsAgent
	cores := make([]*Engine, 0, len(r.cores))
	for _, e := range r.cores {
		cores = append(cores, e)
	}
	r.mu.Unlock()
	// 锁外分发：引擎/代理内部各自持锁，避免与本锁形成嵌套
	if na != nil {
		na.SetLLMClient(c)
	}
	for _, e := range cores {
		e.SetLLMClient(c)
	}
}

// All 返回所有已创建的计算引擎（共享引擎去重，用于主循环/打分循环驱动）。
// English: returns all created compute engines (shared engines deduplicated), for the main/scoring
// loops to drive.
func (r *Registry) All() []*Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[*Engine]bool, len(r.cores))
	out := make([]*Engine, 0, len(r.cores))
	for _, e := range r.cores {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// refreshAll 对所有已创建引擎执行一次账号配置同步（共享引擎跳过——配置已固化）。
// English: re-syncs config for every created engine (shared engines skip — their config is pinned).
func (r *Registry) refreshAll() {
	for _, e := range r.All() {
		e.syncAccountConfig()
	}
}

// fingerprint 计算账号的战法配置指纹：序列化影响战法结果的全部配置
// （策略参数 + Laodeng + 做多/做空开关 + 持仓提醒阈值 + D1 重试 + §修复 FIX#11 QMT 实盘段），
// 指纹一致的账号共享同一计算引擎（战法只算一遍）。
// §修复 FIX#11（2026-09-04）：指纹追加 QMT 关键字段——不同实盘配置（enabled/gateway/金额/黑名单/
// 白名单等）的账号若因 QMT 段不同而被区分开，将各自构建独立引擎，绝不共享同一实盘控制器，
// 根除"同指纹账号共享引擎时 QMT 配置互相覆盖、账目错位、熔断互相影响"的资损链。
// English: computes an account's strategy-config fingerprint from every setting that affects
// strategy results (strategy params + Laodeng + long/short toggles + position-alert threshold +
// D1 retries + §FIX#11 the live-trading/QMT segment). Accounts with equal fingerprints share one
// compute engine (the strategy runs once). §FIX#11 — the fingerprint now also includes the QMT
// live-trading keys, so accounts with materially different live configs never share one controller
// (QMT overlap / book mismatch / shared circuit-breaker are capital-loss-grade).
func (r *Registry) fingerprint(userID string) string {
	opts := r.opts
	// §P1-C 指纹补全：原指纹漏掉 D1 事件规则与 ATR 止损（均为账号级可覆盖项），
	// 导致两账号 D1/ATR 不同却共享同一引擎 → 战法结果互相串味。此处纳入账号级 D1 与
	// 持仓 ATR/跌幅阈值（均走 per-user getter），确保"配置不同则引擎不同"。
	pos := opts.CfgMgr.GetRulesFor(userID).Position
	// §FIX#11 QMT 实盘配置（账号级 getter；排除 Token 等敏感字段，仅取行为关键项）
	q := opts.CfgMgr.GetQMTConfigFor(userID)
	// f 指纹参与字段（账号级可覆盖项，配置不同则引擎不同）。
	type f struct {
		Strategy  *config.StrategyConfig
		Laodeng   *config.LaodengConfig
		LongShort config.LongShortConfig
		DailyDrop float64
		D1Retry   int
		D1        *config.D1Config
		ATR       struct {
			Enabled bool
			Mult    float64
		}
		QMT struct {
			Enabled      bool     // 实盘开关
			Mode         string   // auto/manual
			GatewayURL   string   // 网关地址（不同网关绝不可同控制器）
			PriceType    string   // 报价类型
			FixedAmount  float64  // 单票金额
			MaxPositions int      // 持仓上限
			AutoSell     bool     // 自动卖出
			Strategies   []string // 策略白名单
			Blacklist    []string // 黑名单
		}
	}
	// 组装控制器指纹字段并序列化：策略/长空/盘前跌停/ATR 等配置逐项透传。
	fp := f{
		Strategy:  opts.CfgMgr.GetStrategyConfigFor(userID),
		Laodeng:   &opts.CfgMgr.Get().Laodeng, // §0925EVE-D1：字段裸读改加锁访问器（快照指针同旧语义）
		LongShort: opts.CfgMgr.GetLongShortConfigFor(userID),
		DailyDrop: pos.DailyDropAlertPct,
		D1Retry:   opts.D1MaxRetries,
		D1:        opts.CfgMgr.GetD1ConfigFor(userID),
		ATR: struct {
			Enabled bool
			Mult    float64
		}{pos.ATREnabled, pos.ATRStopMult},
	}
	// 实盘启用时附带 QMT 网关参数（开关/模式/价格类型/白名单等）。
	if q != nil {
		fp.QMT = struct {
			Enabled      bool
			Mode         string
			GatewayURL   string
			PriceType    string
			FixedAmount  float64
			MaxPositions int
			AutoSell     bool
			Strategies   []string
			Blacklist    []string
		}{q.Enabled, q.Mode, q.GatewayURL, q.PriceType, q.FixedAmount, q.MaxPositions, q.AutoSell, q.Strategies, q.Blacklist}
	}
	b, err := json.Marshal(fp)
	if err != nil {
		log.Printf("[engine] 指纹序列化失败, 回退账号 ID: %v", err)
		return "fp_" + userID
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:16])
}
