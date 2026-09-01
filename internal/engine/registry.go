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
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
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
	DataDir      string                  // 数据目录根路径
	Notifier     *notify.Notifier        // 通知推送器（全局共享）
	SectorTopN   int                     // 主线板块纳入成分股数量
	D1MaxRetries int                     // D1 评分 LLM 调用最大重试次数
	D1MaxTokens  int                     // D1 评分 LLM 单次调用推理长度上限（§S3）
	Paper        *paper.Engine           // 模拟盘引擎模板（配置来源；每账号独立实例+独立 paper.json）
	// 实盘账本（AUTO_TRADING_PLAN M1）：QMT 控制器存取 real_positions/orders/fills 的库句柄。
	// §OPT-3 已隔离至独立 live.db。与纸面账本完全独立。nil = 未接入实盘（QMT 链路整体禁用）。
	// English: real book (AUTO_TRADING_PLAN M1) — handle for the QMT controller's
	// real_positions/orders/fills access (isolated to live.db). Fully independent of the paper book.
	RealStore *store.DB // 实盘账本库（live.db）
	// D1Store D1 评分历史库（d1_scores 表，研究侧数据）：必须留在研究库 trading.db，不可与实盘账本混库。
	// English: D1 score history store (d1_scores, research-side) — must stay in the research DB (trading.db).
	D1Store *store.DB // D1 评分库（trading.db）
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
		var sc *config.StrategyConfig
		atrOn, atrMult := false, 0.0
		if cm != nil {
			if rules := cm.GetRulesFor(userID); rules != nil {
				scfg := rules.Strategy
				sc = &scfg
				atrOn = rules.Position.ATREnabled
				atrMult = rules.Position.ATRStopMult
			}
		}
		tp, sl := paperOpenTpSl(pos.StrategyType, sc)
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
	// 两本账合一（阶段1.2）：paper 为唯一真实账本，开仓/清仓镜像写 report 持仓账，
	// 使 CheckPositionsExits 离场路径、持仓页、打分池消费的 rpt 与模拟盘保持一致。
	// English: unified books — paper is the single source of truth; opens/closes mirror into the report
	// holding book so the rpt consumed by exit engines / positions page / scoring pool stays consistent.
	if open, closeFn := r.paperMirror(userID); open != nil {
		pe.SetMirror(open, closeFn)
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
// English: dispatches this round's flipped signals to the paper engines of the accounts served by the
// shared engine that participate in auto-fill (each fills independently; normal users' books are manual).
// Runs only during trading hours (after-hours skips to save memory).
func (r *Registry) dispatchPaperSignals(e *Engine, emit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	if !data.IsFullTradingHours(time.Now()) {
		return
	}
	for _, uid := range r.usersOf(e) {
		if !r.isAutoPaper(uid) {
			continue
		}
		if pe := r.GetPaper(uid); pe != nil && pe.Enabled() {
			pe.OnSignals(emit, quotes)
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
		pes = append(pes, pe)
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
	cAgent.SetLaodengConfig(&opts.CfgMgr.Rules.Laodeng)
	cAgent.SetPositionDailyDropPct(posCfg.DailyDropAlertPct)
	cAgent.SetD1Config(opts.CfgMgr.GetD1ConfigFor(userID))
	cAgent.SetATRStop(posCfg.ATREnabled, posCfg.ATRStopMult)
	cAgent.SetRunners(newAccountRunners(opts.CfgMgr, opts.Matcher, userID, opts.DataDir))
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
	e.SetEmotionConfig(&opts.CfgMgr.Rules.Emotion)
	if opts.SectorTopN > 0 {
		e.SetSectorConstituentTopN(opts.SectorTopN)
	}
	if opts.D1MaxRetries > 0 {
		e.SetD1MaxRetries(opts.D1MaxRetries)
	}
	if opts.D1MaxTokens > 0 {
		e.SetD1MaxTokens(opts.D1MaxTokens)
	}
	// 模拟盘引擎：账户级独立实例（每账号独立 paper.json），信号/估值按账号分发。
	// 全局模板仅提供配置（opts.Paper）；e.paper 保留为旧单引擎回退。
	// English: per-account paper engines (own paper.json); signals/marks dispatch per account. The global
	// template only supplies config; e.paper stays as the legacy single-engine fallback.
	e.SetPaper(opts.Paper)
	e.SetPaperDispatch(
		func(emit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
			r.dispatchPaperSignals(e, emit, quotes)
		},
		func(quotes map[string]*data.StockInfo) { r.dispatchPaperMark(e, quotes) },
	)
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
		var exec trading.Executor = trading.NoopExecutor{}
		if qmtCfg.Enabled && qmtCfg.GatewayURL != "" {
			exec = trading.NewQMTClient(qmtCfg.GatewayURL, qmtCfg.Token,
				time.Duration(qmtCfg.TimeoutSec)*time.Second, 1)
		}
		ctrl := trading.NewController(exec, opts.RealStore, userID, qmtCfg, onAlert)
		// 引擎侧 realStore 仅用于 D1 评分落库（d1_scores，研究侧数据），必须留在研究库而非 live.db。
		e.SetQMT(ctrl, opts.D1Store)
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
// English: builds the four strategy runners; each runner is bound to the account so it reads that
// account's strategy config. matcher feeds the N-shape D1 event match (may be nil). dataDir is used to
// inject the approved factor-strategy rule (E6).
func newAccountRunners(cfgMgr *config.Manager, matcher *data.EventMatcher, userID string, dataDir string) []combat_agent.StrategyRunner {
	runners := buildRunners(cfgMgr, matcher)
	for i := range runners {
		if setter, ok := runners[i].Strategy.(interface{ SetUserID(string) }); ok {
			setter.SetUserID(userID)
		}
	}
	// E6：从 applied_factors.json 注入全部**启用**的因子战法规则（战法库，多规则同时实盘）。
	// English: E6 — inject all **enabled** factor-strategy rules from applied_factors.json (the
	// strategy library; multiple rules run concurrently).
	rules, err := research.LoadEnabledFactorRules(dataDir)
	if err != nil {
		log.Printf("[registry] 加载因子战法库失败: %v", err)
	}
	if len(rules) > 0 {
		for i := range runners {
			if fs, ok := runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
				fs.SetRules(rules)
				log.Printf("[registry] 因子战法库已启用 %d 条规则", len(rules))
			}
		}
	}
	// F3：从 applied_patterns.json 注入全部**启用**的形态模板规则（形态战法库，多形态同时实盘）。
	// English: F3 — inject all **enabled** pattern-template rules from applied_patterns.json (the
	// pattern library; multiple patterns run concurrently).
	patterns, err := research.LoadEnabledPatternRules(dataDir)
	if err != nil {
		log.Printf("[registry] 加载形态战法库失败: %v", err)
	}
	if len(patterns) > 0 {
		for i := range runners {
			if ps, ok := runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
				ps.SetRules(patterns)
				log.Printf("[registry] 形态战法库已启用 %d 条规则", len(patterns))
			}
		}
	}
	// §P2-d 实盘接线：启动装配时同步规则级出场覆盖（扫参审批的止盈/超期对实盘生效）。
	// English: seed the rule-level exit-override registry at startup assembly.
	if fe, e1 := research.ListAppliedFactorRules(dataDir); e1 == nil {
		pe, e2 := research.ListAppliedPatternRules(dataDir)
		if e2 == nil {
			if fe == nil {
				fe = []research.AppliedFactorEntry{}
			}
			if pe == nil {
				pe = []research.AppliedPatternEntry{}
			}
			combat_agent.SetRuleExitOverrides(fe, pe)
		}
	}
	return runners
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
// （策略参数 + Laodeng + 做多/做空开关 + 持仓提醒阈值 + D1 重试），
// 指纹一致的账号共享同一计算引擎（战法只算一遍）。
// English: computes an account's strategy-config fingerprint from every setting that affects
// strategy results (strategy params + Laodeng + long/short toggles + position-alert threshold +
// D1 retries). Accounts with equal fingerprints share one compute engine (the strategy runs once).
func (r *Registry) fingerprint(userID string) string {
	opts := r.opts
	// §P1-C 指纹补全：原指纹漏掉 D1 事件规则与 ATR 止损（均为账号级可覆盖项），
	// 导致两账号 D1/ATR 不同却共享同一引擎 → 战法结果互相串味。此处纳入账号级 D1 与
	// 持仓 ATR/跌幅阈值（均走 per-user getter），确保"配置不同则引擎不同"。
	pos := opts.CfgMgr.GetRulesFor(userID).Position
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
	}
	fp := f{
		Strategy:  opts.CfgMgr.GetStrategyConfigFor(userID),
		Laodeng:   &opts.CfgMgr.Rules.Laodeng,
		LongShort: opts.CfgMgr.GetLongShortConfigFor(userID),
		DailyDrop: pos.DailyDropAlertPct,
		D1Retry:   opts.D1MaxRetries,
		D1:        opts.CfgMgr.GetD1ConfigFor(userID),
		ATR: struct {
			Enabled bool
			Mult    float64
		}{pos.ATREnabled, pos.ATRStopMult},
	}
	b, err := json.Marshal(fp)
	if err != nil {
		log.Printf("[engine] 指纹序列化失败, 回退账号 ID: %v", err)
		return "fp_" + userID
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:16])
}

// ticker 占位保留：避免 time 未使用。
var _ = time.Now
