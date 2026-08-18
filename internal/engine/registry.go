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

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy_engine"
)

// EngineOptions 注册表的共享依赖（数据源全局一份，所有账号引擎复用）。
// English: shared dependencies for the registry — data sources are global and reused by every account engine.
type EngineOptions struct {
	MarketAPI    *data.MarketAPI
	NewsAgent    *newsagent.Agent
	StrategyEng  *strategy_engine.Engine
	SectorAgent  *sector_agent.Agent
	Scanner      *data.SectorScanner
	Matcher      *data.EventMatcher
	Rpt          *report.Report
	StockTracker *data.StockTracker
	WlMgr        *data.WatchlistManager
	SSE          *server.SSEBroker
	LLMClient    *llm.Client
	THS          *data.THSClient
	Fetcher      *data.Fetcher
	CfgMgr       *config.Manager
	DataDir      string
	Notifier     *notify.Notifier
	SectorTopN   int
	D1MaxRetries int
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
	mu       sync.Mutex
	opts     EngineOptions
	cores    map[string]*Engine    // configFingerprint → Engine（共享计算引擎）
	byUser   map[string]*Engine    // userID → Engine（账号归属引擎）
	initDone map[string]bool       // userID → 是否已完成初始化
	initProg map[string]*InitStage // userID → 当前初始化进度
}

// NewRegistry 创建引擎注册表。
// English: creates the engine registry.
func NewRegistry(opts EngineOptions) *Registry {
	return &Registry{
		opts:     opts,
		cores:    make(map[string]*Engine),
		byUser:   make(map[string]*Engine),
		initDone: make(map[string]bool),
		initProg: make(map[string]*InitStage),
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
		r.mu.Unlock()
		return e
	}
	r.mu.Unlock()

	// 无匹配共享引擎 → 构建新引擎（构建在锁外，避免持锁做耗时工作）
	e := r.build(userID)
	r.mu.Lock()
	r.cores[fp] = e
	r.byUser[userID] = e
	r.initDone[userID] = true
	r.initProg[userID] = &InitStage{Stage: "ready", Percent: 100, EtaSec: 0}
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
	cAgent.SetLaodengConfig(&opts.CfgMgr.Rules.Laodeng)
	cAgent.SetPositionDailyDropPct(opts.CfgMgr.Rules.Position.DailyDropAlertPct)
	cAgent.SetD1Config(opts.CfgMgr.GetD1Config())
	cAgent.SetATRStop(opts.CfgMgr.Rules.Position.ATREnabled, opts.CfgMgr.Rules.Position.ATRStopMult)
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
	// E6：从 applied_factors.json 注入审批通过的因子战法规则（未启用时该文件不存在 → 跳过）。
	// English: E6 — inject the approved factor-strategy rule from applied_factors.json (skipped when
	// the file is absent, i.e. the factor strategy is not enabled).
	rule, err := research.LoadAppliedFactorRule(dataDir)
	if err != nil {
		log.Printf("[registry] 加载因子战法规则失败: %v", err)
	}
	if rule != nil {
		for i := range runners {
			if fs, ok := runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
				fs.SetRule(factorstrat.Rule{
					Factors: rule.Factors, Weights: rule.Weights,
					Directions: rule.Directions, BuyThreshold: rule.BuyThreshold,
				})
				log.Printf("[registry] 因子战法已启用: %d 个因子 阈值=%.0f", len(rule.Factors), rule.BuyThreshold)
			}
		}
	}
	// F3：从 applied_patterns.json 注入审批通过的形态模板规则。
	// English: F3 — inject the approved pattern-template rule from applied_patterns.json.
	pr, err := research.LoadAppliedPatternRule(dataDir)
	if err != nil {
		log.Printf("[registry] 加载形态战法规则失败: %v", err)
	}
	if pr != nil {
		for i := range runners {
			if ps, ok := runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
				conds := make([]patternstrat.Cond, len(pr.Conds))
				for j, c := range pr.Conds {
					conds[j] = patternstrat.Cond{Factor: c.Factor, Min: c.Min, Max: c.Max}
				}
				ps.SetRule(patternstrat.PatternRule{Name: pr.Name, Conds: conds})
				log.Printf("[registry] 形态战法已启用: %d 个条件", len(conds))
			}
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
	type f struct {
		Strategy  *config.StrategyConfig
		Laodeng   *config.LaodengConfig
		LongShort config.LongShortConfig
		DailyDrop float64
		D1Retry   int
	}
	fp := f{
		Strategy:  opts.CfgMgr.GetStrategyConfigFor(userID),
		Laodeng:   &opts.CfgMgr.Rules.Laodeng,
		LongShort: opts.CfgMgr.GetLongShortConfigFor(userID),
		DailyDrop: opts.CfgMgr.Rules.Position.DailyDropAlertPct,
		D1Retry:   opts.D1MaxRetries,
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
