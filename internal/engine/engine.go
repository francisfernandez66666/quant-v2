// Package engine 顶层编排引擎：持有全部子代理（NewsAgent/StrategyAgent/SectorAgent/CombatAgent），
// 每 5 分钟驱动一次完整流水线：新闻拉取 → Stage0/Stage1/Stage2 → 固化 → 板块验证 → 战法扫描 → 信号聚合 → SSE 广播。
// 各子模块只输出结果，不直接相互调用；引擎是唯一的编排者，控制流程顺序、阈值过滤和状态同步。
// English: top-level orchestrating engine that holds all sub-agents (NewsAgent/StrategyAgent/SectorAgent/CombatAgent).
// Drives a full pipeline every 5 minutes: news pull → Stage0/1/2 → fixation → sector verification
// → strategy scanning → signal aggregation → SSE broadcast.
// Sub-modules only output results; they do not call each other directly.
// The engine is the sole orchestrator, controlling flow order, threshold filtering, and state sync.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/store"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy_engine"
	"quant-trading-v2/internal/trading"
)

// llmDegradeCount 包级 LLM 降级计数：累计因 LLM 不可用/限流/失败而退化为中性(或规则兜底)的次数，
// 供运维观测 LLM 异常频率（结合日志与 /api/debug）。默认相关个股/板块降级为中性占位，但保证日志可见。
// （llmDegradeCount is a package-level counter of LLM-degradation events (LLM down/throttled/failed) where
// affected stocks/sectors fall back to a neutral placeholder; it lets ops monitor LLM anomaly frequency.）
var llmDegradeCount int64

// Engine 顶层编排引擎，持有全部子代理引用与利好/利空开关。
// 它是唯一被允许跨模块调用的对象：把新闻流水线、板块验证、战法扫描与信号聚合串联成一条完整链路。
type Engine struct {
	mu sync.RWMutex // 保护全部可变字段的读写锁（多 goroutine：主循环 + 近实时打分循环 + SSE/HTTP 调用）

	marketAPI    *data.MarketAPI         // 行情 API（实时价/K线/资金流/涨停池）
	newsAgent    *newsagent.Agent        // 新闻代理（拉取 + Stage0/1/2 归因分析）
	strategy     *strategy_engine.Engine // 策略引擎（事件归因 → 评分池 → 行情数据）
	sectorAgent  *sector_agent.Agent     // 板块验证代理（战法扫描前做板块真伪验证）
	combatAgent  *combat_agent.Agent     // 战法代理（8a/8b 打分 + 多战法信号扫描）
	agg          *display.Aggregator     // 看板聚合器（SSE 数据源）
	rpt          *report.Report          // 持仓/交易报表（止盈止损提醒依赖）
	stockTracker *data.StockTracker      // 个股跟踪池（8a/8b 入池与失效管理）
	wlMgr        *data.WatchlistManager  // 用户自选股管理
	sse          *server.SSEBroker       // SSE 广播器（推送打分/信号到前端）
	llmClient    *llm.Client             // LLM 客户端（D1 评分 / 标题党校正）
	ths          *data.THSClient         // 同花顺客户端（板块名单/行情表/实时报价降级）
	scanner      *data.SectorScanner     // 板块扫描器（板块名单索引，板块验真与归因校验依赖）

	userID  string   // 账号 ID（多账号独立引擎：该引擎只计算本账号的信号/评分）（Account ID; in multi-account mode this engine computes only this account's signals/scores）
	members []string // §GAP2-W2 共享引擎服务的账号全集（registry 注入；私有消息/SSE 扇出依据）
	// §P1-4 管理员判定函数（main 注入 auth.IsAdmin）：primaryMember 优先返回管理员成员，
	// 确保 friends 共享引擎的实盘账本/QMT 控制器默认归属创建者/管理员，而非首个普通成员。
	// English: P1-4 admin predicate (wired from main's auth.IsAdmin) — primaryMember prefers an admin.
	isAdminFn    func(userID string) bool
	accountsRoot string          // §GAP2-W2 <dataDir>/accounts 根（私有文件按账号寻址）
	cfgMgr       *config.Manager // 配置管理器（按账号读取策略/D1/LLM/做多做空配置）（Config manager, reads per-account strategy/D1/LLM/long-short settings）
	longEnabled  bool            // 利好开关（做多分支）
	shortEnabled bool            // 利空开关（做空分支）

	m8PeakTotal float64 // §GAP1.2 实盘组合市值峰值（M8 回撤兜底基线；e.mu 保护，平仓后归零重计）

	asyncBusy int32 // 盘前异步引擎运行标记（忙锁，避免异步 run 重入）

	clockFn func() time.Time // §可注入时钟：默认 time.Now；e2e 固定交易时段用（SetClock）

	debugInfo    *newsagent.DebugInfo  // 最近一轮流水线的调试数据（/api/debug 展示）
	stageRecords []newsagent.DebugInfo // 当日全量轮次记录（固化到磁盘）
	stageRecPath string                // Stage 记录持久化文件路径
	lastStageCap time.Time             // §LLM 面板：近实时循环最近一次 Stage 快照捕获时间（节流用）

	signalRecords []combat_agent.SignalLog // 当日全量信号批次记录（固化到磁盘）
	signalRecPath string                   // 信号批次记录持久化文件路径
	signalStore   *signalStore             // 当日战法信号固化存储（code@strategy 最近一次 Pass，跨重启恢复）
	// English: pinned per-day signal store (latest Pass per code@strategy, restored across restarts)

	msgStore      *data.MessageStore            // 消息中心持久化存储
	consultStore  *data.ConsultStore            // 股票咨询对话持久化存储（跨交易日清空；accountsRoot 未注入时的共享回退）
	consultMu     sync.Mutex                    // §GAP2-W2 保护 consultByUser 懒加载
	consultByUser map[string]*data.ConsultStore // §GAP2-W2 按账号隔离的咨询历史（accountsRoot/<uid>/consult_history.json）
	confrontStore *data.ConfrontationStore      // 政策反制事件持久化存储（跨交易日清空）
	notifier      *notify.Notifier              // 推送器（桌面/Webhook；P1 清仓强提醒用）
	hotRecords    []data.HotRecord              // 当日热点板块轮次记录（固化到磁盘）
	hotRecPath    string                        // 热点板块记录持久化文件路径

	sectorEventTimes map[string]time.Time  // 板块事件时间戳（重复事件衰减状态）
	emotionCfg       *config.EmotionConfig // 情绪周期阈值（SSE 广播情绪阶段）
	sectorConstTopN  int                   // 板块→个股传播每板块成分股数量（默认 20，扩大同板块强势股覆盖）

	fetcher          *data.Fetcher                                                       // 5s 实时行情采集器（近实时打分快照来源）
	scoreStore       *scoreStore                                                         // 8a/8b 主循环打分持久化（scores.json）
	fastScoreStore   *scoreStore                                                         // §P0-8 近实时 5s 循环打分持久化（scores_fast.json），与主循环分池避免互相覆盖
	prevPass         map[string]map[string]bool                                          // 近实时信号状态翻转去重（code → strategy → 上次是否Pass）
	prevBullBuy      map[string]map[string]bool                                          // 主循环 buy 信号状态翻转去重（龙头识别等仅在主循环产生的信号，防重复买入）
	lastD1Scores     map[string]combat_agent.D1Score                                     // 主循环最近一轮 D1 评分（近实时循环复用，不每 5s 调 LLM）
	d1RetryQueue     map[string]bool                                                     // D1 LLM 失败待重试队列（失败股并入下轮打分池重新调 LLM，不兜底）
	lastEmotionPhase string                                                              // 主循环最近一轮情绪阶段（近实时循环复用）
	d1MaxRetries     int                                                                 // D1 评分 LLM 轮询重试次数（<=0 用默认5）
	lastTiming       *RunTiming                                                          // 最近一轮 Run 分段耗时（e2e 实速模拟观测）
	factorMon        *factorMonitor                                                      // 因子战法效果监测（战法库触发信号前向收益结算）
	paper            *paper.Engine                                                       // 模拟盘引擎（独立纸面交易，可空=未启用）
	paperOnSignals   func(emit []combat_agent.Signal, quotes map[string]*data.StockInfo) // 按账号分发 buy 信号撮合（registry 注入）
	paperMarkFn      func(quotes map[string]*data.StockInfo)                             // 按账号分发估值/净值（registry 注入）
	lastTrim         time.Time                                                           // 盘后内存释放最近一次执行时间（节流用）

	// 实盘交易（AUTO_TRADING_PLAN M1）：QMT 控制器 + 实盘账本 store。独立于纸面账本。
	// 仅 qmt.enabled=true 时参与 5s 分析循环（读 real_positions 生成持仓建议 / 熔断 / 自动下单）。
	// English: live trading (AUTO_TRADING_PLAN M1) — QMT controller + real-book store, independent of the
	// paper book. Only active when qmt.enabled=true: reads real_positions for position advice, circuit
	// breaking and auto-orders each 5s cycle.
	qmtCtrl   *trading.Controller // QMT 执行控制器（下单/熔断/健康探测，可空=未启用）
	realStore *store.DB           // 研究库（real_positions/orders/fills 实盘账本存取）

	// §A+B 信号→交易低延迟：异步下单分发器（事件驱动热路径）。
	// autoPlace 完成同步守卫（模式/白名单/涨停封板/金额）后把 OrderRequest 投入 buyCh，
	// 由独立 worker 调用 ctrl.PlaceOrder，避免网关 RTT 阻塞 5s 打分/检测循环。
	buyCh   chan buyTask
	buyStop chan struct{}
	buyWg   sync.WaitGroup
	// §A+B 近实时打分循环间隔（默认 0 → 回退 5s），可配置为 1-2s 以加快信号翻转检出。
	scoringInterval time.Duration
}

// buyTask §A+B 异步下单任务：守卫已过的 buy 信号 + 已折算的 OrderRequest。
// English: A+B async order task — a buy signal that passed all synchronous guards, with its computed OrderRequest.
type buyTask struct {
	req trading.OrderRequest
	sig combat_agent.Signal
}

// LastRunTiming 返回最近一轮 Run 的分段耗时（可能为 nil，Run 未执行过时）。
func (e *Engine) LastRunTiming() *RunTiming {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastTiming
}

// recordLLMDegrade 累计一次 LLM 降级事件并打出结构化告警（原因 + 影响条数 + 累计次数）。
// 用于 sector 解码/归因或 D1 评分因 LLM 故障而默认中性占位时，保证日志可见、便于运维发现 LLM 异常。
// （recordLLMDegrade records one LLM-degradation event and logs a structured alert (reason + affected count
// + cumulative count), used when sector decode/attribution or D1 scoring silently falls back to neutral due
// to LLM failure, keeping it visible to operators.）
func (e *Engine) recordLLMDegrade(reason string, affected int) {
	n := atomic.AddInt64(&llmDegradeCount, 1)
	metrics.LLMDegraded() // §R4-9 LLM 降级计数进指标面
	log.Printf("[engine][LLM降级#%d] %s, 影响条数=%d", n, reason, affected)
}

// LastD1Scores 返回主循环最近一轮 D1 评分结果（副本），含 RetryPending 标记，
// 供 e2e/诊断断言"LLM 失败不兜底、走重试队列"语义。
// English: returns a copy of the main loop's latest D1 scores (incl. RetryPending), for e2e/diagnostic
// assertions of the "no LLM fallback, via retry queue" semantics.
func (e *Engine) LastD1Scores() map[string]combat_agent.D1Score {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]combat_agent.D1Score, len(e.lastD1Scores))
	for code, d := range e.lastD1Scores {
		out[code] = d
	}
	return out
}

// D1RetryQueueCodes 返回当前 D1 LLM 重试队列中的个股代码（副本），
// 供 e2e 断言失败股确实并入重试队列。
// English: returns a copy of the current D1 LLM retry-queue codes, for e2e assertions that failed stocks
// actually joined the retry queue.
func (e *Engine) D1RetryQueueCodes() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.d1RetryQueue))
	for code := range e.d1RetryQueue {
		out = append(out, code)
	}
	return out
}

// SetEmotionConfig 设置情绪周期阈值（线程安全），并把 C5 禁止开仓阶段列表同步给战法代理。
// English: sets the emotion-cycle thresholds (thread-safe) and pushes the C5 block-buy phases to the
// combat agent.
func (e *Engine) SetEmotionConfig(cfg *config.EmotionConfig) {
	e.mu.Lock()
	e.emotionCfg = cfg
	e.mu.Unlock()
	if e.combatAgent != nil && cfg != nil {
		e.combatAgent.SetEmotionBlockPhases(cfg.BlockBuyPhases)
	}
}

// SetSectorConstituentTopN 设置板块→个股传播每板块纳入的成分股数量（>0 时生效）。
// English: sets the per-sector constituent count for sector→stock propagation (effective when >0).
func (e *Engine) SetSectorConstituentTopN(n int) {
	e.mu.Lock()
	if n > 0 {
		e.sectorConstTopN = n
	}
	e.mu.Unlock()
}

// SetD1MaxRetries 设置 D1 评分 LLM 调用的轮询重试次数（含首次）。n<=0 使用默认5。
func (e *Engine) SetD1MaxRetries(n int) {
	e.mu.Lock()
	e.d1MaxRetries = n
	e.mu.Unlock()
}

// TrimAfterHoursIfDue 盘后内存释放：非活跃时段（盘后/休市）按节流间隔执行
// runtime.GC()+debug.FreeOSMemory()，把常驻 Go 堆/缓存归还 OS。
// 服务器物理内存仅 1.6GiB：quant 常驻服务盘后只展示数据快照、不跑全量性能，
// 主动释放内存让给盘后 research 夜间作业，避免两者叠加触发 global_oom。
// 由 main 的 5s 打分调度每轮调用；盘中（活跃时段）不触发，不影响性能。
// English: after-hours memory trim — outside active sessions, periodically runs
// runtime.GC()+debug.FreeOSMemory() (throttled) to return the resident Go heap/cache to the OS.
// The 1.6GiB box can't afford quant + the nightly research job simultaneously; after hours the
// engine only serves data snapshots, so it gives memory back to research. Called by the 5s scoring
// dispatcher each round; never runs during active trading sessions.
func (e *Engine) TrimAfterHoursIfDue(now time.Time) {
	if e.cfgMgr == nil {
		return
	}
	rc := e.cfgMgr.Rules.Runtime
	if !rc.TrimAfterHours || data.IsActiveSession(now) {
		return
	}
	interval := time.Duration(rc.TrimIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	e.mu.Lock()
	due := now.Sub(e.lastTrim) >= interval
	if due {
		e.lastTrim = now
	}
	e.mu.Unlock()
	if !due {
		return
	}
	runtime.GC()
	debug.FreeOSMemory()
	log.Printf("[engine] 盘后内存释放完成 (trim_after_hours, 节流 %v)", interval)
}

// ReloadFactorRules 从战法库 applied_factors.json 重载全部启用规则并注入因子 runner（热生效）。
// 战法库启用/禁用/删除/审批后由 server 调用，无需重启。
// English: reloads all enabled rules from the strategy library and injects them into the factor
// runner (hot-applied). Called by the server after library mutations; no restart needed.
func (e *Engine) ReloadFactorRules(dataDir string) {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		ca.ReloadFactorRules(dataDir)
	}
}

// FactorStats 返回因子 runner 的各规则运行统计（效果监测）。
// English: returns per-rule run stats of the factor runner (effectiveness monitoring).
func (e *Engine) FactorStats() []factorstrat.ActiveRule {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		return ca.FactorStats()
	}
	return nil
}

// RecordFactorForwardReturn 记录某条因子规则一条触发股的 Horizon 日前向收益（效果监测）。
// English: records a rule's Horizon-day forward return for one triggered stock (effectiveness monitoring).
func (e *Engine) RecordFactorForwardReturn(ruleID string, ret float64) {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		ca.RecordFactorForwardReturn(ruleID, ret)
	}
}

// ReloadPatternRules 从形态战法库 applied_patterns.json 重载全部启用规则并注入形态 runner（热生效）。
// English: reloads all enabled rules from the pattern library and injects them (hot-applied).
func (e *Engine) ReloadPatternRules(dataDir string) {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		ca.ReloadPatternRules(dataDir)
	}
}

// PatternStats 返回形态 runner 的各规则运行统计（效果监测）。
// English: returns per-rule run stats of the pattern runner (effectiveness monitoring).
func (e *Engine) PatternStats() []patternstrat.ActivePattern {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		return ca.PatternStats()
	}
	return nil
}

// RecordPatternForwardReturn 记录某条形态规则一条触发股的 Horizon 日前向收益（效果监测）。
// English: records a pattern rule's Horizon-day forward return for one triggered stock (monitoring).
func (e *Engine) RecordPatternForwardReturn(ruleID string, ret float64) {
	e.mu.RLock()
	ca := e.combatAgent
	e.mu.RUnlock()
	if ca != nil {
		ca.RecordPatternForwardReturn(ruleID, ret)
	}
}

// stageRecordFile Stage 记录磁盘持久化结构（按交易日分桶）。
type stageRecordFile struct {
	TradingDay string                `json:"trading_day"`
	Records    []newsagent.DebugInfo `json:"records"`
}

// hotRecordFile 热点板块记录磁盘持久化结构（按交易日分桶）。
type hotRecordFile struct {
	TradingDay string           `json:"trading_day"`
	Records    []data.HotRecord `json:"records"`
}

// signalRecordFile 信号批次记录磁盘持久化结构（按交易日分桶）。
type signalRecordFile struct {
	TradingDay string                   `json:"trading_day"`
	Records    []combat_agent.SignalLog `json:"records"`
}

// New 创建顶层编排引擎。
// New 组装量化引擎主实例：注入行情/新闻/战法/板块/对抗等各子系统与展示聚合器，
// 并按 dataDir 初始化各持久化文件路径（dataDir 为空时不落盘，纯内存模式）。
func New(
	marketAPI *data.MarketAPI,
	newsAgent *newsagent.Agent,
	strategy *strategy_engine.Engine,
	sectorAgent *sector_agent.Agent,
	combatAgent *combat_agent.Agent,
	agg *display.Aggregator,
	rpt *report.Report,
	stockTracker *data.StockTracker,
	wlMgr *data.WatchlistManager,
	sse *server.SSEBroker,
	llmClient *llm.Client,
	ths *data.THSClient,
	dataDir string,
) *Engine {
	// 根据 dataDir 计算各持久化文件路径（dataDir 为空时不落盘，纯内存模式）
	stageRecPath := ""
	msgPath := ""
	scoreRecPath := ""
	fastScoreRecPath := ""
	if dataDir != "" {
		stageRecPath = filepath.Join(dataDir, "stage_records.json")
		msgPath = filepath.Join(dataDir, "messages.json")
		scoreRecPath = filepath.Join(dataDir, "scores.json")
		fastScoreRecPath = filepath.Join(dataDir, "scores_fast.json")
	}
	consultPath := ""
	if dataDir != "" {
		consultPath = filepath.Join(dataDir, "consult_history.json")
	}
	confrontPath := ""
	if dataDir != "" {
		confrontPath = filepath.Join(dataDir, "confrontation.json")
	}
	hotRecPath := ""
	signalRecPath := ""
	signalStorePath := ""
	if dataDir != "" {
		hotRecPath = filepath.Join(dataDir, "hot_records.json")
		signalRecPath = filepath.Join(dataDir, "signal_records.json")
		signalStorePath = filepath.Join(dataDir, "signals_today.json")
	}
	e := &Engine{
		marketAPI:        marketAPI,
		newsAgent:        newsAgent,
		strategy:         strategy,
		sectorAgent:      sectorAgent,
		combatAgent:      combatAgent,
		agg:              agg,
		rpt:              rpt,
		stockTracker:     stockTracker,
		wlMgr:            wlMgr,
		sse:              sse,
		llmClient:        llmClient,
		ths:              ths,
		longEnabled:      true,
		shortEnabled:     false,
		stageRecords:     loadStageRecords(stageRecPath),
		stageRecPath:     stageRecPath,
		signalRecords:    loadSignalRecords(signalRecPath),
		signalRecPath:    signalRecPath,
		signalStore:      newSignalStore(signalStorePath),
		msgStore:         data.NewMessageStore(msgPath),
		consultStore:     data.NewConsultStore(consultPath),
		consultByUser:    make(map[string]*data.ConsultStore),
		confrontStore:    data.NewConfrontationStore(confrontPath),
		hotRecords:       loadHotRecords(hotRecPath),
		hotRecPath:       hotRecPath,
		sectorEventTimes: make(map[string]time.Time),
		sectorConstTopN:  20,
		scoreStore:       newScoreStore(scoreRecPath),
		fastScoreStore:   newScoreStore(fastScoreRecPath),
		prevPass:         make(map[string]map[string]bool),
		prevBullBuy:      make(map[string]map[string]bool),
		lastD1Scores:     make(map[string]combat_agent.D1Score),
		d1RetryQueue:     make(map[string]bool),
		factorMon:        newFactorMonitor(dataDir, 5),
	}
	e.syncMessages(nil, nil, nil, nil, nil) // 首次同步：把历史持仓/止盈止损提示并入消息中心（First sync: merge historical holdings/profit-loss notices into the message center）
	// 启动时回填上次持久化的 8a/8b 打分与当日固化信号（重启后前端立即可见）
	// English: on startup, backfill the last persisted 8a/8b scores and the day's pinned signals so the
	// frontend shows them immediately after a restart.
	loadedScores := e.scoreStore.Load()
	if persisted := e.signalStore.List(); len(persisted) > 0 || len(loadedScores) > 0 {
		e.agg.UpdateFast(loadedScores, persisted, e.rpt)
	}
	return e
}

// SetUserID 设置账号 ID（多账号独占引擎模式；Run/打分循环前应调用 syncAccountConfig 同步账号配置）。
// English: sets the account ID for a per-account (non-shared) engine; call syncAccountConfig
// before Run/scoring to apply that account's config.
func (e *Engine) SetUserID(userID string) {
	e.mu.Lock()
	e.userID = userID
	e.mu.Unlock()
}

// §GAP2-W2 账户隔离：共享引擎的成员账号列表与私有状态根目录。
// 指纹相同的多个账号复用同一计算引擎（战法只算一遍），但"谁的持仓提醒/咨询历史"必须按账号隔离——
// members 由 registry.registerUser 注入（去重后的服务账号全集），是私有消息生成与 SSE 定向扇出的依据；
// accountsRoot（<dataDir>/accounts）用于把咨询历史等私有文件寻址到各自账号目录，
// 根除"共享组全部状态落首建者目录"的历史缺陷（I-1/I-2）。
// English: §GAP2-W2 — members are the accounts served by this (possibly shared) engine, injected by
// the registry; they drive per-user private alert generation and targeted SSE fan-out. accountsRoot
// addresses private files (consult history) into each account's own directory, fixing the legacy
// "everything lands in the first builder's folder" defect.
func (e *Engine) SetMembers(ids []string) {
	e.mu.Lock()
	cp := append([]string(nil), ids...)
	sort.Strings(cp)
	e.members = cp
	// 单成员引擎同时固定 userID：恢复 syncAccountConfig / 定向推送的账号语义
	// （此前 SetUserID 从未被调用，账号级配置热同步永不生效）。
	if len(cp) == 1 {
		e.userID = cp[0]
	}
	e.mu.Unlock()
}

// SetAccountsRoot 注入 <dataDir>/accounts 根目录（私有状态按账号寻址）。
func (e *Engine) SetAccountsRoot(dir string) {
	e.mu.Lock()
	e.accountsRoot = dir
	e.mu.Unlock()
}

// memberIDs 返回本引擎服务的账号列表（快照）；无成员时回退 userID（兼容独占引擎旧路径）。
func (e *Engine) memberIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.members) > 0 {
		return append([]string(nil), e.members...)
	}
	if e.userID != "" {
		return []string{e.userID}
	}
	// 无成员且无 userID（旧装配/e2e）：回退单 "" 账号，保持 ListFor("") 的全局语义
	return []string{""}
}

// primaryMember 返回主账号（首个成员）：实盘账本/QMT 控制器等"单一归属"资源的默认主体。
// §P1-4 多成员时优先返回管理员成员（friends 共享引擎里主账号应是创建者/管理员），
// 无管理员时回退首个成员，单成员/独占引擎回退 userID。
// English: primaryMember returns the default owner for single-owner resources (live book / QMT
// controller). P1-4 prefers an admin member when present; falls back to the first member, then userID.
func (e *Engine) primaryMember() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.members) > 0 {
		if e.isAdminFn != nil {
			for _, m := range e.members {
				if e.isAdminFn(m) {
					return m
				}
			}
		}
		return e.members[0]
	}
	return e.userID
}

// SetIsAdminFn 注入管理员判定函数（main 用 auth.IsAdmin 装配），供 primaryMember 优先选择管理员成员。
// English: wires the admin predicate (from main's auth.IsAdmin) so primaryMember can prefer an admin.
func (e *Engine) SetIsAdminFn(fn func(userID string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isAdminFn = fn
}

// UserID 返回本引擎所属账号 ID。
// English: returns the account ID this engine belongs to.
func (e *Engine) UserID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.userID
}

// SetCfgMgr 设置配置管理器（账号级配置读取来源）。
// English: sets the config manager (source of per-account settings).
func (e *Engine) SetCfgMgr(m *config.Manager) {
	e.mu.Lock()
	e.cfgMgr = m
	e.mu.Unlock()
}

// SetLongShortConfig 固化本引擎的做多/做空开关（共享引擎在构建时由注册表按共享组配置设置）。
// English: pins this engine's long/short toggles at build time (the registry sets them from the
// shared group's config so the engine doesn't need to re-read a specific account at runtime).
func (e *Engine) SetLongShortConfig(longEnabled, shortEnabled bool) {
	e.mu.Lock()
	e.longEnabled = longEnabled
	e.shortEnabled = shortEnabled
	e.mu.Unlock()
	if e.combatAgent != nil {
		e.combatAgent.SetShortEnabled(shortEnabled)
	}
}

// syncAccountConfig 将账号级配置应用到本引擎（仅独占引擎使用）：
//   - 做多/做空开关：按账号持久化的状态覆盖引擎内存开关
//   - 战法参数：热更新到本账号的 combat_agent（runner 按账号读取）
//
// 共享引擎（userID 为空）跳过——其配置已在构建时按共享组固化，所有共享账号配置一致，
// 因此任何设备读到同一份结果。
// English: applies this account's config to the engine (per-account non-shared engines only) — the
// account-persisted long/short toggles override the in-memory switches, and the account's strategy
// params are hot-reloaded into this account's combat agent. Shared engines (empty userID) skip this:
// their config was pinned at build time from the shared group, whose members all share one config.
func (e *Engine) syncAccountConfig() {
	e.mu.RLock()
	cfgMgr, userID := e.cfgMgr, e.userID
	e.mu.RUnlock()
	if cfgMgr == nil || userID == "" {
		return
	}
	ls := cfgMgr.GetLongShortConfigFor(userID)
	e.mu.Lock()
	e.longEnabled = ls.LongEnabled
	e.shortEnabled = ls.ShortEnabled
	e.mu.Unlock()
	if e.combatAgent != nil {
		e.combatAgent.SetShortEnabled(ls.ShortEnabled)
		e.combatAgent.SetD1Config(cfgMgr.GetD1ConfigFor(userID))
		pos := cfgMgr.GetRulesFor(userID).Position
		e.combatAgent.SetPositionDailyDropPct(pos.DailyDropAlertPct)
		e.combatAgent.SetATRStop(pos.ATREnabled, pos.ATRStopMult)
		sc := cfgMgr.GetStrategyConfigFor(userID)
		if sc != nil {
			e.combatAgent.HotReload(sc)
		}
	}
	// QMT 实盘配置热同步：每轮从配置管理器读取，控制器据此切换 enabled/mode/参数（5s 生效）。
	// 必须用账号级 GetQMTConfigFor（而非全局 GetRulesFor().QMT）：账号级覆盖优先，且可避免
	// Watch/Load 重置全局 m.Rules 后，把已开启的实盘链路短暂关掉（开关"秒关"的潜在根因）。
	// §GAP1.7 黑名单接线：Theme.BlackList 一并同步进下单守卫（此前仅死代码 risk.go 消费）。
	// English: hot-sync the per-user QMT config each cycle (GetQMTConfigFor, not the global
	// GetRulesFor().QMT) so a Load()-reset of the global rules can't transiently disable a live link.
	if c := e.QMTController(); c != nil {
		q := *cfgMgr.GetQMTConfigFor(userID)
		q.Blacklist = append(q.Blacklist, cfgMgr.GetRulesFor(userID).Theme.BlackList...)
		// §QMT-PENDING 开关队列：普通配置变更只入队不立即生效，交易时段由 scoreCycle 的
		// ApplyPendingConfig 消费（重建 executor）。防止休市时配置立即翻转实盘行为。
		c.QueueConfigUpdate(q)
	}
	// §GAP5.1 LLM 成本治理：日预算热同步（0=不设限）。
	if c := e.LLMClient(); c != nil {
		lc := cfgMgr.GetRulesFor(userID).LLM
		c.SetBudgets(lc.DailyCallBudget, lc.DailyTokenBudget)
	}
}

// SetNotifier 设置推送器（P1 清仓/止损强提醒走桌面/Webhook）。
func (e *Engine) SetNotifier(n *notify.Notifier) {
	e.mu.Lock()
	e.notifier = n
	e.mu.Unlock()
}

// SetPaper 注入模拟盘引擎（nil 表示未启用）。
// English: injects the paper-trading engine (nil = disabled).
func (e *Engine) SetPaper(p *paper.Engine) {
	e.mu.Lock()
	e.paper = p
	e.mu.Unlock()
}

// SetPaperDispatch 注入按账号的模拟盘分发回调（多账号模式；注入后优先于全局 e.paper）。
// English: injects the per-account paper dispatch callbacks (multi-account mode; take precedence over
// the global e.paper when set).
func (e *Engine) SetPaperDispatch(onSignals func(emit []combat_agent.Signal, quotes map[string]*data.StockInfo), mark func(quotes map[string]*data.StockInfo)) {
	e.mu.Lock()
	e.paperOnSignals = onSignals
	e.paperMarkFn = mark
	e.mu.Unlock()
}

// SetQMT 注入 QMT 实盘执行控制器与实盘账本 store（AUTO_TRADING_PLAN M1）。
// qmtCtrl 可空（未启用）；realStore 为研究库句柄（real_positions 存取）。
// English: injects the QMT live-trading controller and the real-book store (AUTO_TRADING_PLAN M1).
// qmtCtrl may be nil (disabled); realStore is the research-DB handle (real_positions access).
func (e *Engine) SetQMT(qmtCtrl *trading.Controller, realStore *store.DB) {
	e.mu.Lock()
	e.qmtCtrl = qmtCtrl
	e.realStore = realStore
	e.mu.Unlock()
}

// QMTEnabled 实盘链路是否启用（qmt.enabled 热加载）。
// English: QMTEnabled reports whether the live-trading chain is on (qmt.enabled hot-reloaded).
func (e *Engine) QMTEnabled() bool {
	e.mu.RLock()
	c := e.qmtCtrl
	e.mu.RUnlock()
	return c != nil && c.Enabled()
}

// QMTController 返回 QMT 执行控制器（HTTP 层读取熔断/配置用，可空）。
// English: QMTController returns the QMT controller for the HTTP layer (breaker/config reads; may be nil).
func (e *Engine) QMTController() *trading.Controller {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.qmtCtrl
}

// paperSignals 把本轮翻转信号送入模拟盘撮合：优先按账号分发，回退全局引擎。
// 仅交易时段执行（盘后停自动撮合，省内存）。
// English: feeds this round's flipped signals into paper filling — per-account dispatch first, global
// engine as the fallback. Runs only during trading hours (no after-hours auto-fill to save memory).
func (e *Engine) paperSignals(emit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	e.mu.RLock()
	dispatch := e.paperOnSignals
	pe := e.paper
	e.mu.RUnlock()
	if dispatch != nil {
		dispatch(emit, quotes)
		return
	}
	if pe != nil && pe.Enabled() && data.IsFullTradingHours(time.Now()) {
		pe.OnSignals(emit, quotes)
	}
}

// autoPlace AUTO_TRADING_PLAN M1：qmt.enabled + mode=auto 时把做多买入信号直连网关下单。
// 幂等：signal_id 唯一键（Orders 表 UNIQUE），熔断中跳过；现价缺省时用信号触发价。
// 金额按 fixed_amount（受 max_positions 预检约束）；code 补后缀便于网关识别交易所。
// English: AUTO_TRADING_PLAN M1 — when qmt.enabled and mode=auto, places a real buy order for a long
// signal straight to the gateway. Idempotent via the signal_id unique key (Orders table UNIQUE), skipped
// while tripped; the live price is used when available, else the signal trigger price. Amount uses
// fixed_amount (pre-checked against max_positions); the code gets its exchange suffix for the gateway.
func (e *Engine) autoPlace(sig combat_agent.Signal, live map[string]*data.StockInfo) {
	e.mu.RLock()
	ctrl := e.qmtCtrl
	e.mu.RUnlock()
	if ctrl == nil || !ctrl.Enabled() || ctrl.Mode() != "auto" {
		// §DIAG-0921 静默门插桩（2026-09-01 实录：fac_1 buy 信号进主循环却全程零下单/零日志，
		// 无法定位是哪个静默门吞掉的）：每个静默跳过点打一条 DayOnce 节流日志（每码每天一次），
		// 不刷屏但让"信号为何没下单"一眼可见。
		reason := "ctrl-nil"
		if ctrl != nil {
			if !ctrl.Enabled() {
				reason = "qmt-disabled"
			} else if ctrl.Mode() != "auto" {
				reason = "mode-" + ctrl.Mode()
			}
		}
		log.Printf("[qmt-gate] %s(%s) %s/%s 自动下单跳过: %s", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, reason)
		opslog.DayOnce("auto-gate:"+reason+":"+sig.Code, func() {
			opslog.Logf("quant", "auto下单跳过 %s(%s) 策略=%s/%s 原因=%s", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, reason)
		})
		return
	}
	cfg := ctrl.Config()
	// §UAT-FIX 2026-08-31：白名单条目是战法 ID（n_shape/fac_1…，量化交易页保存的就是 ID），
	// 而 sig.Strategy 是中文显示名（如"波动突破战法"）——旧逻辑只比显示名，ID 永远不命中，
	// auto 全程静默跳过（连日志都没有）。现同时匹配 StrategyID（库规则 ID）与显示名。
	if len(cfg.Strategies) > 0 && sig.Strategy != "" {
		allowed := false
		for _, s := range cfg.Strategies {
			if s == sig.Strategy || s == sig.StrategyID {
				allowed = true
				break
			}
		}
		if !allowed {
			// §DIAG-0921 白名单外静默跳过同样打节流日志（此前完全无声，无法区分"没信号"与"被白名单拦"）
			log.Printf("[qmt-gate] %s(%s) %s/%s 白名单外跳过: 允许=%v", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, cfg.Strategies)
			opslog.DayOnce("auto-wl:"+sig.Code, func() {
				opslog.Logf("quant", "auto白名单外跳过 %s(%s) 策略=%s/%s 允许=%v", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, cfg.Strategies)
			})
			return
		}
	}
	price := sig.Price
	if si := live[sig.Code]; si != nil && si.Price > 0 {
		price = si.Price
	}
	if price <= 0 {
		// §DIAG-0921 价格无效静默跳过节流日志（触发价缺失且无实时行情时的无声丢弃点）
		log.Printf("[qmt-gate] %s(%s) %s/%s 价格无效跳过: price=%.2f", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, price)
		opslog.DayOnce("auto-price:"+sig.Code, func() {
			opslog.Logf("quant", "auto价格无效跳过 %s(%s) 策略=%s/%s price=%.2f", sig.Code, sig.Name, sig.StrategyID, sig.Strategy, price)
		})
		return
	}
	// §GAP1.5 涨停封板拒买（与模拟盘 paper.LimitUpPct 同款分板块守卫）：
	// 封板股买单现实中几乎无法排队成交，auto 模式直接报单只会买在炸板瞬间或制造虚假成交。
	// English: §GAP1.5 sealed-board buy guard (same board-aware rule as the paper book) — a buy order
	// against a sealed limit-up board is practically unfillable; skip instead of firing at the blast.
	if si := live[sig.Code]; si != nil && si.ChangePct >= paper.LimitUpPct(sig.Code, sig.Name) {
		log.Printf("[qmt] %s(%s) 涨停封板 %.1f%%≥%.1f%% 拒买跳过", sig.Code, sig.Name,
			si.ChangePct, paper.LimitUpPct(sig.Code, sig.Name))
		// §DAILY_OPSLOG 按天/按股去重（同一封板股每 5s 循环都会触达，全记会刷屏）
		opslog.DayOnce("limitup:"+sig.Code, func() {
			opslog.Logf("quant", "涨停封板拒买 %s(%s) 涨幅=%.1f%% 策略=%s/%s", sig.Code, sig.Name, si.ChangePct, sig.StrategyID, sig.Strategy)
		})
		return
	}
	amount := cfg.FixedAmount
	// §QUANT-TAB 每战法仓位大小：该战法配置了正数金额则覆盖全局 fixed_amount（量化交易页可配）。
	// §UAT-FIX 2026-08-31：面板按战法 ID（fac_1…）配置金额，同键优先，回退显示名，再回退全局。
	if v := cfg.StrategyAmounts[sig.StrategyID]; v > 0 {
		amount = v
	} else if v := cfg.StrategyAmounts[sig.Strategy]; v > 0 {
		amount = v
	}
	if amount <= 0 {
		amount = 10000
	}
	qty := int(amount/price/100) * 100
	// §R0.7 修复：高价股不足一手时不再强凑 1 手（旧逻辑 qty=100 导致订单金额超预算数倍）
	if qty <= 0 {
		log.Printf("[qmt] %s 金额不足以买一手，跳过下单", sig.Code)
		opslog.DayOnce("one-lot:"+sig.Code, func() {
			opslog.Logf("quant", "金额不足一手跳过 %s 现价=%.2f 预算=%.0f 策略=%s/%s", sig.Code, price, amount, sig.StrategyID, sig.Strategy)
		})
		return
	}
	// §UAT-CASH 2026-08-31：按可用资金自动降档——与模拟盘"现金不足时按剩余现金整手买入"
	// （paper.go FixedAmount 注释）同语义。fixed_amount 是预算上限而非死数：最近上报的可用
	// 资金（网关每分钟对账）不足以按预算整手买入时，降到现金可负担的最大整手数；连一手都
	// 买不起才放弃。资金未知/过期时 AvailableCash 返回 0 → 不设限，维持原行为。
	// English: §UAT-CASH — degrade the lot count to what the latest reported available cash
	// affords (whole lots) instead of rejecting the order when cash < fixed_amount; skip only
	// when even one lot is unaffordable. Unknown/stale cash (0) keeps the old behavior.
	if cash := ctrl.AvailableCash(); cash > 0 && qty > 0 {
		// 预留 0.6% 佣金/过户费余量，避免贴着可用资金下单被柜台以"资金不足"废单
		affordable := int(cash*0.994/price/100) * 100
		if affordable < qty {
			if affordable <= 0 {
				log.Printf("[qmt] %s 可用资金 %.0f 不足以买一手(现价 %.2f)，跳过下单", sig.Code, cash, price)
				opslog.DayOnce("nocash:"+sig.Code, func() {
					opslog.Logf("quant", "资金不足跳过 %s 现价=%.2f 可用=%.0f 策略=%s/%s", sig.Code, price, cash, sig.StrategyID, sig.Strategy)
				})
				return
			}
			log.Printf("[qmt] %s 可用资金 %.0f 不足按预算 %d 股买入，自动降档为 %d 股(约 %.0f 元)",
				sig.Code, cash, qty, affordable, float64(affordable)*price)
			opslog.Logf("quant", "资金降档 %s 预算=%d股→%d股 可用=%.0f 现价=%.2f", sig.Code, qty, affordable, cash, price)
			qty = affordable
		}
	}
	// §GAP2-W1 确定性幂等键（资损级修复）：实盘买入 signal_id 改为 buy:<纯代码>:<战法>:<交易日>。
	// 旧实现直接用 sig.ID（seqID="SIG"+UnixNano，每轮扫描重新生成）——主循环每 ~5 分钟重扫一次，
	// 同一股票只要持续满足条件就会拿到全新 signal_id 反复真实下单，直到烧满 daily_max_buys/预算；
	// 且 prevBullBuy 去重状态是纯内存态，盘中重启首轮把全部当前 Pass 信号当"新翻转"重放。
	// 新键与卖出键 sell:<码>:<类>:<日> 同构：orders 表 signal_id 唯一键天然防重——
	// 跨轮次重复触发、近实时+主循环双通道叠加、进程重启重放，全部被数据库唯一约束拦截；
	// 同股同战法当日至多一笔买单，与单日笔数/预算纪律的语义一致。
	// English: §GAP2-W1 deterministic idempotency key (capital-loss-grade fix): the live BUY signal_id
	// becomes buy:<pureCode>:<strategy>:<tradingDay>. The old code reused sig.ID ("SIG"+UnixNano,
	// regenerated every scan round), so a stock persistently satisfying its strategy re-fired a fresh
	// real order every ~5 minutes until the daily caps burned; the in-memory prevBullBuy dedup also
	// replayed everything as "new flips" after an intraday restart. Mirroring the sell key, the DB
	// unique constraint now blocks all repeats across rounds/channels/restarts — at most one buy per
	// (stock, strategy, day), consistent with the daily-count/budget discipline.
	id := fmt.Sprintf("buy:%s:%s:%s", pureTsCode(sig.Code), sig.Strategy, data.TradingDayDate(time.Now()))
	req := trading.OrderRequest{
		SignalID:   id,
		Code:       withSuffix(sig.Code),
		Name:       sig.Name,
		Strategy:   sig.Strategy,
		StrategyID: sig.StrategyID,
		Side:       trading.SideBuy,
		PriceType:  cfg.PriceType,
		Price:      price,
		Qty:        qty,
		Amount:     float64(qty) * price,
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
	// §A+B 事件驱动热路径：若已启动异步分发器（buyCh 非空）则入队，否则同步下单
	// （兼容未启动分发器的调用方，如测试与直调；保持原有行为）。
	if e.buyCh != nil {
		select {
		case e.buyCh <- buyTask{req: req, sig: sig}:
			log.Printf("[trading] auto order queued %s(%s) qty=%d price=%.2f (async)", sig.Code, sig.Name, qty, price)
		default:
			log.Printf("[trading] auto order DROPPED (buy queue full) %s(%s) qty=%d price=%.2f", sig.Code, sig.Name, qty, price)
			opslog.Logf("quant", "auto 买单丢失(队列满) %s(%s) qty=%d price=%.2f", sig.Code, sig.Name, qty, price)
		}
	} else {
		res, err := ctrl.PlaceOrder(req)
		if err != nil {
			log.Printf("[trading] auto order %s(%s): %v", sig.Code, sig.Name, err)
			return
		}
		log.Printf("[trading] auto order %s(%s) qty=%d price=%.2f → %+v", sig.Code, sig.Name, qty, price, res)
	}
}

// StartBuyDispatcher §A+B 启动异步下单 worker 池（事件驱动热路径）。在 RunScoringLoop 启动时调用一次。
// English: A+B — starts the async order worker pool (event-driven hot path). Called once when the scoring loop starts.
func (e *Engine) StartBuyDispatcher(n int) {
	if n <= 0 {
		n = 4
	}
	e.mu.Lock()
	if e.buyCh != nil {
		e.mu.Unlock()
		return // 已启动，避免重复
	}
	e.buyCh = make(chan buyTask, 64)
	e.buyStop = make(chan struct{})
	e.mu.Unlock()
	for i := 0; i < n; i++ {
		e.buyWg.Add(1)
		go func() {
			defer e.buyWg.Done()
			// 加锁读取，避免与 StopBuyDispatcher 写 e.buyStop=nil 形成数据竞争
			// English: read under lock to avoid a data race with StopBuyDispatcher.
			e.mu.RLock()
			stop := e.buyStop
			e.mu.RUnlock()
			if stop == nil {
				return
			}
			for {
				select {
				case t := <-e.buyCh:
					e.placeOrderNow(t.req, t.sig)
				case <-stop:
					return
				}
			}
		}()
	}
}

// StopBuyDispatcher 停止 worker 池（进程退出时）。
// English: stops the worker pool (on process shutdown).
func (e *Engine) StopBuyDispatcher() {
	e.mu.Lock()
	stop := e.buyStop
	e.buyStop = nil
	e.mu.Unlock()
	if stop == nil {
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	e.buyWg.Wait()
}

// placeOrderNow §A+B worker 实际下单：每次读取最新 qmtCtrl（配置热重载安全），调用网关。
// English: A+B worker — performs the actual order via the latest qmtCtrl (config hot-reload safe).
func (e *Engine) placeOrderNow(req trading.OrderRequest, sig combat_agent.Signal) {
	e.mu.RLock()
	ctrl := e.qmtCtrl
	e.mu.RUnlock()
	if ctrl == nil {
		return
	}
	res, err := ctrl.PlaceOrder(req)
	if err != nil {
		log.Printf("[trading] auto order %s(%s): %v", sig.Code, sig.Name, err)
		return
	}
	log.Printf("[trading] auto order %s(%s) qty=%d price=%.2f → %+v", sig.Code, sig.Name, req.Qty, req.Price, res)
	// §DAILY_OPSLOG auto 实际下单结果（受理/业务拒单由 controller 侧另记，此处补策略上下文）
	opslog.Logf("quant", "auto 下单 %s(%s) 策略=%s/%s qty=%d price=%.2f → ok=%v order=%s err=%s",
		sig.Code, sig.Name, req.StrategyID, req.Strategy, req.Qty, req.Price, res.OK, res.OrderID, res.Err)
}

// SetScoringInterval §A+B 设置近实时打分循环间隔（0 → 回退 5s）。
// English: A+B — sets the near-realtime scoring-loop interval (0 → fallback 5s).
func (e *Engine) SetScoringInterval(d time.Duration) {
	e.mu.Lock()
	e.scoringInterval = d
	e.mu.Unlock()
}

// withSuffix 为纯数字股票代码补交易所后缀（600000 → 600000.SH；000001 → 000001.SZ；4/8 开头 → .BJ）。
// English: withSuffix appends the exchange suffix to a bare digit code (600000→600000.SH, 000001→000001.SZ,
// 4/8-prefix→.BJ).
func withSuffix(code string) string {
	if strings.Contains(code, ".") {
		return code
	}
	switch {
	case strings.HasPrefix(code, "6"), strings.HasPrefix(code, "9"):
		return code + ".SH"
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		return code + ".BJ"
	default:
		return code + ".SZ"
	}
}

// paperMark 用实时快照刷新模拟盘估值与净值：优先按账号分发，回退全局引擎。
// 仅交易时段执行（盘后停估值，省内存）；盘后落库由注册表盘后导出 hook 负责。
// English: refreshes paper marks and equity from the live snapshot — per-account dispatch first, global
// engine as the fallback. Runs only during trading hours (no after-hours marking to save memory); the
// post-close research export is handled by the registry's day-close hook.
func (e *Engine) paperMark(quotes map[string]*data.StockInfo) {
	e.mu.RLock()
	mark := e.paperMarkFn
	pe := e.paper
	e.mu.RUnlock()
	if mark != nil {
		mark(quotes)
		return
	}
	if pe != nil && pe.Enabled() && data.IsFullTradingHours(time.Now()) {
		// §纸面估值修复：持仓不在 5s 快照池时用最近收盘价回填估值价，避免 Mark 恒 0 → 现价0/浮亏-100%。
		// English: backfill marks from the last daily close for held codes missing from the live snapshot,
		// so a held position never displays 0.00 / -100%.
		pe.MarkToMarket(backfillPaperQuotes(e, pe, quotes))
		pe.Snapshot(time.Now())
	}
}

// backfillPaperQuotes 为模拟盘持仓中缺失实时价的代码用最近日K收盘价补齐估值行情。
// 仅对 MarkToMarket 生效，不改动快照本身。无法取到收盘价时保持原样（该持仓沿用旧 Mark）。
// English: fills in last-close prices for held paper codes missing a live quote, so MarkToMarket can
// re-mark them. Only affects this valuation pass — the snapshot itself is untouched. Codes with no
// close available are left as-is (their existing Mark is kept).
func backfillPaperQuotes(e *Engine, pe *paper.Engine, quotes map[string]*data.StockInfo) map[string]*data.StockInfo {
	backed := make(map[string]*data.StockInfo, len(quotes)+8)
	for k, v := range quotes {
		backed[k] = v
	}
	if pe == nil || e == nil || e.strategy == nil {
		return backed
	}
	for _, p := range pe.Positions() {
		if p.Code == "" {
			continue
		}
		q, ok := backed[p.Code]
		if ok && q != nil && q.Price > 0 {
			continue
		}
		if c := e.strategy.LastClose(p.Code); c > 0 {
			backed[p.Code] = &data.StockInfo{Code: p.Code, Name: p.Name, Price: c}
		}
	}
	return backed
}

// SetFetcher 设置 5s 实时行情采集器（近实时打分循环的快照来源）。
func (e *Engine) SetFetcher(f *data.Fetcher) {
	e.mu.Lock()
	e.fetcher = f
	e.mu.Unlock()
}

// snapshotQuotes 返回 fetcher 最近一轮 5s 实时行情快照（map: code → quote），
// 供 syncMessages 等服务直接把最新价/涨跌幅带进消息中心；fetcher 未配置时返回 nil。
func (e *Engine) snapshotQuotes() map[string]*data.StockInfo {
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f == nil {
		return nil
	}
	if snap := f.Snapshot(); snap != nil {
		return snap.Stocks
	}
	return nil
}

// updateHotPool 将验证通过的板块成分股并入 5s 实时监控池。
// 热点股随板块轮换替换（上限 60 由 Fetcher 内部裁剪），缺失板块验证结果时保留原热点。
func (e *Engine) updateHotPool(bull, bear []sector_agent.VerifiedSector) {
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f == nil {
		return
	}
	set := make(map[string]bool)
	for _, sec := range bull {
		for _, code := range sec.Stocks {
			set[code] = true
		}
	}
	for _, sec := range bear {
		for _, code := range sec.Stocks {
			set[code] = true
		}
	}
	if len(set) == 0 {
		return // 本轮无验证通过的板块，保持原热点不变
	}
	stocks := make([]string, 0, len(set))
	for code := range set {
		stocks = append(stocks, code)
	}
	f.UpdateHotStocks(stocks)
	log.Printf("[engine] 热点池更新: %d 只板块成分股入 5s 实时池", len(stocks))
}

// syncSignalPool 把本轮展示的做多/做空/提醒信号代码并入 5s 实时监控池（与板块热点池取并集，上限 60）。
// 否则信号股不在"自选+持仓"监控池时，/api/signals、/api/snapshot/hot 等展示接口读不到实时行情，
// 涨跌幅显示 0.00%、现价停留在信号触发时的陈旧值。
// （English: merges the current round's long/short/alert signal codes into the 5s live monitor
// pool (union with the sector hot pool, capped at 60). Without this, signal stocks outside the
// watchlist+positions pool show 0.00% change and a stale trigger price on the display endpoints
// such as /api/signals and /api/snapshot/hot.）
func (e *Engine) syncSignalPool(bull, bear, alert []combat_agent.Signal) {
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f == nil {
		return
	}
	// 与现有热点池取并集，避免覆盖掉板块入池个股
	cur := f.HotStocks()
	set := make(map[string]bool, len(cur)+len(bull)+len(bear)+len(alert))
	for _, c := range cur {
		set[c] = true
	}
	for _, s := range bull {
		if s.Code != "" {
			set[s.Code] = true
		}
	}
	for _, s := range bear {
		if s.Code != "" {
			set[s.Code] = true
		}
	}
	for _, s := range alert {
		if s.Code != "" {
			set[s.Code] = true
		}
	}
	// 当日固化信号也持续展示（看板 FinalSignals 包含它们），一并入池保证现价/涨跌幅真实
	if e.signalStore != nil {
		for _, s := range e.signalStore.List() {
			if s.Code != "" {
				set[s.Code] = true
			}
		}
	}
	if len(set) == 0 {
		return
	}
	stocks := make([]string, 0, len(set))
	for c := range set {
		stocks = append(stocks, c)
	}
	f.UpdateHotStocks(stocks)
}

// pushFreshHotspots 新热点立马进池：把归因产出的有效事件立即归因出板块 → 板块验真 → 并入 5s 实时监控池。
// 与 Run 尾部 9b 的 updateHotPool 幂等。strategy 或 sectorAgent 未初始化时优雅跳过（不 panic）。
// （pushFreshHotspots immediately attributes valid events into sectors, verifies them, and merges the
// constituents into the 5s watch pool. Idempotent with the updateHotPool at the end of Run.）
func (e *Engine) pushFreshHotspots(valid []newsagent.NewsEvent) {
	if len(valid) == 0 || e.strategy == nil || e.sectorAgent == nil {
		return
	}
	_stepHot := time.Now()
	bullCand, bearCand := e.strategy.BuildHotSectors(valid)
	var vb, vbr []sector_agent.VerifiedSector
	if e.LongEnabled() {
		vb = e.sectorAgent.Verify(bullCand)
	}
	if e.ShortEnabled() {
		vbr = e.sectorAgent.Verify(bearCand)
	}
	e.updateHotPool(vb, vbr)
	_hotPoolT := time.Since(_stepHot)
	if _hotPoolT > 500*time.Millisecond {
		log.Printf("[engine] 新热点立即进池: %d利好板块 %d利空板块, 耗时 %v", len(vb), len(vbr), _hotPoolT)
	}
}

// mergeSectorStocksIntoScores 板块→个股归因：把验证通过的板块 top 成分股并入打分行情/D1/PE，
// 使 ScanLong/ScanShort 遍历 sector.Stocks 时 MarketData[code] 有值（否则 evalAll md==nil 丢弃，
// 板块利好永远落不到个股）。
// D1 沿用板块事件分（sector.Score 0~1 → LLMD1Score，仅做多板块种子），不额外调 LLM；
// 只对没有专属 D1 的成分股打底，避免覆盖个股自己的评分。
// mergeSectorStocksIntoScores 板块→个股归因：把已验证利好板块的成分股并入 打分池 + 行情数据，
// 返回"代码→板块事件标题"映射供 D1 评分注入上下文。
// 不做 D1 打分（D1 已与板块利好/利空事件分完全解耦，只由 D1Scorer LLM 独立核定）——
// 板块成分股并入 sr.ScoringPool 后，由随后运行的 D1 batch 统一打分。
// English: sector→stock attribution — merges verified-bull sector constituents into the scoring pool +
// market data, and returns a code→sector-event-title map for D1 scoring context. It does NOT assign D1
// (D1 is fully decoupled from the sector bull/bear event score and graded independently by the D1Scorer
// LLM); constituents are added to sr.ScoringPool so the following D1 batch scores them uniformly.
func (e *Engine) mergeSectorStocksIntoScores(ctx context.Context, sr *strategy_engine.StrategyResult, verifiedBull, verifiedBear []sector_agent.VerifiedSector, peScores map[string]float64) map[string]string {
	// 1. 收拢全部板块成分股（去重），并记录每个 code 所属板块的事件标题（做多板块种子）
	type secInfo struct {
		eventTitle string
	}
	secOf := make(map[string]secInfo)
	for _, vs := range verifiedBull {
		if vs.Score <= 0 {
			continue
		}
		title := vs.Reason
		if title == "" {
			title = vs.Name
		}
		for _, c := range vs.Stocks {
			if _, ok := secOf[c]; !ok {
				secOf[c] = secInfo{eventTitle: title}
			}
		}
	}
	var extras []string
	for c := range secOf {
		if _, ok := sr.MarketData[c]; ok {
			continue // 已在打分池（新闻/持仓/自选）
		}
		extras = append(extras, c)
	}
	// 板块→个股 D1 上下文：所有 verifiedBull 成分股（含已在打分池的自选/持仓）
	// 都映射到所属板块事件标题，使 D1 评分覆盖"属利好板块但未被新闻点名"的池内个股。
	// D1 仍是个股分——板块标题只作为 LLM 评分上下文，由 LLM 对每只个股独立核定受益程度。
	eventMap := make(map[string]string, len(secOf))
	for c, si := range secOf {
		if si.eventTitle != "" {
			eventMap[c] = si.eventTitle
		}
	}
	if e.strategy == nil || e.marketAPI == nil {
		log.Printf("[engine] 板块→个股归因跳过: 策略引擎/行情API未配置")
		return eventMap
	}

	// 2. 补拉成分股行情（K线/实时/资金流，走缓存），merge 进 sr.MarketData 与打分池
	extraMD := e.strategy.BuildScoringData(ctx, extras, nil)
	// 打分池去重集合（ScoringPool 为无序切片，用 map 判重）
	poolSet := make(map[string]bool, len(sr.ScoringPool))
	for _, c := range sr.ScoringPool {
		poolSet[c] = true
	}
	for c, md := range extraMD {
		if _, ok := sr.MarketData[c]; !ok {
			sr.MarketData[c] = md
		}
		// 并入打分池，使板块成分股进入 D1 batch 的统一打分范围
		if !poolSet[c] {
			sr.ScoringPool = append(sr.ScoringPool, c)
			poolSet[c] = true
		}
		// 3. 补 PE（N 形 D3 超跌评分）
		peScores[c] = e.marketAPI.GetStockPE(c)
	}

	log.Printf("[engine] 板块→个股归因: 补 %d 只成分股行情并入打分池, 板块=%d", len(extras), len(secOf))
	return eventMap
}

// loadStageRecords 从磁盘加载当日 Stage 记录；跨交易日自动重置。
func loadStageRecords(path string) []newsagent.DebugInfo {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f stageRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] stage_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistStageRecords 将当日 Stage 记录写入磁盘。
func (e *Engine) persistStageRecords() {
	if e.stageRecPath == "" {
		return
	}
	// §E6 RLock 内值级快照：此前只拷切片头，锁外 Marshal 遍历与写方共享的底层数组（一触即溃模式）
	e.mu.RLock()
	recs := make([]newsagent.DebugInfo, len(e.stageRecords))
	copy(recs, e.stageRecords)
	e.mu.RUnlock()
	f := stageRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: recs}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] stage_records 序列化失败: %v", err)
		return
	}
	mustAtomicWrite("stage_records", e.stageRecPath, raw)
}

// GetStageRecords 返回当日全量 Stage 轮次记录（供复盘 / 策略侧实时调取）。
func (e *Engine) GetStageRecords() []newsagent.DebugInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]newsagent.DebugInfo, len(e.stageRecords))
	copy(out, e.stageRecords)
	return out
}

// loadSignalRecords 从磁盘加载当日信号批次记录；跨交易日自动重置。
func loadSignalRecords(path string) []combat_agent.SignalLog {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f signalRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] signal_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistSignalRecords 将当日信号批次记录写入磁盘。
func (e *Engine) persistSignalRecords() {
	if e.signalRecPath == "" {
		return
	}
	// §E6 同上：值级快照
	e.mu.RLock()
	recsSig := make([]combat_agent.SignalLog, len(e.signalRecords))
	copy(recsSig, e.signalRecords)
	e.mu.RUnlock()
	f := signalRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: recsSig}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] signal_records 序列化失败: %v", err)
		return
	}
	mustAtomicWrite("signal_records", e.signalRecPath, raw)
}

// GetSignalLogs 返回当日全量信号批次记录（供前端"信号日志"弹窗按批次展示）。
func (e *Engine) GetSignalLogs() []combat_agent.SignalLog {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]combat_agent.SignalLog, len(e.signalRecords))
	copy(out, e.signalRecords)
	return out
}

// captureSignalRecords 收集本轮全部信号为一条批次快照，固化到当日信号记录。
func (e *Engine) captureSignalRecords(rawCount int, signals []combat_agent.Signal) {
	e.mu.Lock()
	rec := combat_agent.SignalLog{
		ProcessTime: time.Now(),
		RawCount:    rawCount,
		Signals:     make([]combat_agent.Signal, len(signals)),
	}
	copy(rec.Signals, signals)
	e.signalRecords = append(e.signalRecords, rec)
	if len(e.signalRecords) > 20 {
		e.signalRecords = e.signalRecords[len(e.signalRecords)-20:]
	}
	e.mu.Unlock()
	e.persistSignalRecords()
}

// GetAllNewsEvents 返回持久化到本地的全部已打标新闻事件，供 /api/news?all=true 展示。
func (e *Engine) GetAllNewsEvents() []newsagent.NewsEvent {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return nil
	}
	return na.AllEvents()
}

// SetNewsShowAll 设置"资讯显示全部"开关：开启时落盘过滤分降到 0，
// 弱档/中性事件也出现在 /api/news；关闭时恢复默认 0.25。
func (e *Engine) SetNewsShowAll(v bool) {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return
	}
	if v {
		na.SetMinScore(0)
	} else {
		na.SetMinScore(0.25)
	}
	log.Printf("[engine] 资讯显示全部开关: %v (落盘最低分=%v)", v, na.MinScore())
}

// NewsShowAll 返回"资讯显示全部"开关当前状态。
func (e *Engine) NewsShowAll() bool {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return false
	}
	return na.MinScore() == 0
}

// ── 热点板块记录 ──

// loadHotRecords 从磁盘加载当日热点板块记录；跨交易日自动重置。
func loadHotRecords(path string) []data.HotRecord {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f hotRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] hot_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistHotRecords 将当日热点板块记录写入磁盘。
func (e *Engine) persistHotRecords() {
	if e.hotRecPath == "" {
		return
	}
	// §E6 同上：值级快照
	e.mu.RLock()
	recsHot := make([]data.HotRecord, len(e.hotRecords))
	copy(recsHot, e.hotRecords)
	e.mu.RUnlock()
	f := hotRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: recsHot}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] hot_records 序列化失败: %v", err)
		return
	}
	mustAtomicWrite("hot_records", e.hotRecPath, raw)
}

// GetHotRecords 返回当日全量热点板块轮次记录（供前端展示）。
func (e *Engine) GetHotRecords() []data.HotRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]data.HotRecord, len(e.hotRecords))
	copy(out, e.hotRecords)
	return out
}

// captureHotRecord 将本轮热点板块（匹配同花顺 top-20 行情表后）固化为记录。
// 无板块归因或匹配不到真实板块时跳过。
func (e *Engine) captureHotRecord(sr *strategy_engine.StrategyResult) {
	if sr == nil || len(sr.HotSectors) == 0 {
		return
	}
	e.mu.RLock()
	ths := e.ths
	e.mu.RUnlock()
	if ths == nil {
		return
	}
	boards, err := ths.GetTopBoards()
	if err != nil {
		log.Printf("[engine] 热点记录: 同花顺板块行情获取失败: %v", err)
		return
	}
	sectorMap := make(map[string]data.SectorInfo, len(boards))
	for _, b := range boards {
		sectorMap[b.Name] = b
	}
	rec := data.HotRecord{ProcessTime: time.Now()}
	for _, sec := range sr.HotSectors {
		si, ok := sectorMap[sec.Name]
		if !ok {
			continue
		}
		rec.Sectors = append(rec.Sectors, data.HotSectorRecord{
			Name:       sec.Name,
			Code:       si.Code,
			Score:      sec.Score,
			ChangePct:  si.ChangePct,
			D1:         0,
			Direction:  sec.Direction,
			LimitupCnt: si.LimitupCnt,
			NetInflow:  si.NetInflow,
			Reason:     sec.Reason,
			NewsTitles: sec.NewsTitles,
		})
	}
	if len(rec.Sectors) == 0 {
		return
	}
	e.mu.Lock()
	e.hotRecords = append(e.hotRecords, rec)
	if len(e.hotRecords) > 50 {
		e.hotRecords = e.hotRecords[len(e.hotRecords)-50:]
	}
	e.mu.Unlock()
	e.persistHotRecords()
}

// ── 消息中心 ──

// GetMessages 返回消息中心全部消息（按生成时间倒序）——引擎内部/调试用，含他人私有消息，
// HTTP 展示一律走 GetMessagesFor(uid)（§GAP2-W2 账户隔离读侧）。
func (e *Engine) GetMessages() []data.MessageItem {
	if e.msgStore == nil {
		return nil
	}
	return e.msgStore.List()
}

// GetMessagesFor 返回指定账号可见的消息（公共 ∪ 本人私有）——§GAP2-W2 消息中心读侧收口，
// 朋友看不到 owner 的持仓止盈止损提醒，反之亦然；交易信号等公共消息全员共享（决策 D3）。
// English: §GAP2-W2 read-side isolation — public ∪ own-private messages for the requesting account.
func (e *Engine) GetMessagesFor(userID string) []data.MessageItem {
	if e.msgStore == nil {
		return nil
	}
	return e.msgStore.ListVisible(userID)
}

// ClearMessages 清空消息中心全部消息。
func (e *Engine) ClearMessages() {
	if e.msgStore != nil {
		e.msgStore.ClearAll()
	}
}

// DeleteMessage 手工删除单条消息。
func (e *Engine) DeleteMessage(id string) {
	if e.msgStore != nil {
		e.msgStore.Delete(id)
	}
}

// RefreshMessageName 按代码刷新消息中心的股票名称为最新权威名。
// 由前端加自选等入口调用，用于把消息里旧名/空名同步成 quote 权威名。
func (e *Engine) RefreshMessageName(code, name string) {
	if e.msgStore != nil {
		e.msgStore.RefreshNameByCode(code, name)
	}
}

// authoritativeName 尝试从行情接口取该股权威名称；失败或为空时返回 ""。
// 仅用于持仓消息 Name 为空的兜底，避免消息中心出现空名。
func (e *Engine) authoritativeName(code string) string {
	if e.marketAPI == nil || code == "" {
		return ""
	}
	si, err := e.marketAPI.GetRealtimeQuote(code)
	if err != nil || si == nil || si.Name == "" {
		return ""
	}
	return si.Name
}

// orName 依次返回第一个非空名称，全部为空时返回 ""。
func orName(names ...string) string {
	for _, n := range names {
		if n != "" {
			return n
		}
	}
	return ""
}

// ── 股票咨询（多轮对话）──

// consultHistoryLimit 送入模型的多轮历史上限（最近 N 条消息，约 3 组问答）。
// 只带近期上下文，避免历史劣质回复持续污染后续判断，也降低小模型长上下文注意力漂移。
const consultHistoryLimit = 6

// ConsultLLM 以多轮对话方式调用 LLM 生成咨询回复（股票咨询页使用）。
// 组装顺序：唯一一条 system（角色提示词，专业模式时并入实时行情数据）→ 历史最近 N 条 → 当前提问。
// LLM 未配置时返回错误提示前端引导配置；回复生成后同步追加到当日对话历史（跨交易日自动清空）。
func (e *Engine) ConsultLLM(userID, userMsg string, proMode bool) (string, error) {
	e.mu.RLock()
	client := e.llmClient
	e.mu.RUnlock()
	if client == nil {
		return "", fmt.Errorf("未配置 LLM_API_KEY，请先在股票咨询页配置 API Key")
	}

	// system 起始即角色提示词；专业模式下把实时行情上下文并入同一段 system（只保留一条 system 且置于最前）。
	system := llm.ConsultSystemPrompt()
	if proMode {
		if ctx := e.buildConsultContext(userMsg); ctx != "" {
			system += "\n\n" + ctx
		} else {
			system += "\n\n" + consultNoStockPrompt
		}
	}

	// 历史：仅取最近 consultHistoryLimit 条（正序）。
	messages := make([]llm.Message, 0, consultHistoryLimit+2)
	if store := e.consultStoreFor(userID); store != nil {
		hist := store.List()
		if len(hist) > consultHistoryLimit {
			hist = hist[len(hist)-consultHistoryLimit:]
		}
		for _, m := range hist {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}
	}
	messages = append(messages, llm.Message{Role: "user", Content: userMsg})

	// 完整消息序列：system 在最前，后接历史与当前提问。
	msgs := append([]llm.Message{{Role: "system", Content: system}}, messages...)

	reply, err := client.ChatMessages(msgs)
	if err != nil {
		return "", fmt.Errorf("咨询调用失败: %v", err)
	}

	// 数字审计：剔除模型编造、没有任何可信出处的金钱/数量类数字（金额、成交量、笔数等）。
	// 可信来源=注入的实时行情上下文 + 用户自己的描述 + 此前已落盘的历史（已在此前被审计过）。
	histTexts := make([]string, 0, len(messages))
	for _, m := range messages {
		histTexts = append(histTexts, m.Content)
	}
	trusted := collectTrustedNumbers(append([]string{system, userMsg}, histTexts...)...)
	reply = auditNumbers(reply, trusted)

	// 对话历史落盘：用户提问 + 模型回复（§GAP2-W2 写入本人账号目录）
	if store := e.consultStoreFor(userID); store != nil {
		store.Append("user", userMsg)
		store.Append("assistant", reply)
	}
	return reply, nil
}

// auditedNumberRe 匹配带金融单位的数字：金额（万元/亿元/元）、成交量（万股/亿股）、笔数/手数，
// 及百分比与倍数（% / 倍）——这些同样是模型幻觉的高发区。
// 支持"万/亿"紧邻 笔/手/股 的组合（如 2.3万笔、1.2亿股）。
// 刻意不匹配：时间、股票代码、时长、日期——避免误伤。
var auditedNumberRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?(?:万元|亿元|元|万股|亿股|万|亿|手|笔|[%％]|倍)`)

// collectTrustedNumbers 从可信文本（实时行情上下文、用户描述、历史消息）中收集"有出处的数字"集合。
// 数值归一化后存储，便于匹配不同写法（"-22200.00万元" 与 "-22200万元" 视为同一值）。
func collectTrustedNumbers(texts ...string) map[string]bool {
	trusted := make(map[string]bool)
	for _, t := range texts {
		for _, tok := range auditedNumberRe.FindAllString(t, -1) {
			if v, ok := normNumberToken(tok); ok {
				trusted[v] = true
			}
		}
	}
	return trusted
}

// normNumberToken 抽取带单位数字 token 的数值部分并做浮点归一化，返回规范形式。
// "-22200.00万元" → "-22200"；"2.3万笔" → "2.3"；"12%" → "12"；"3倍" → "3"。归一化失败返回 ok=false。
func normNumberToken(tok string) (string, bool) {
	i := strings.IndexAny(tok, "万亿元亿手股%％倍")
	if i < 0 {
		return "", false
	}
	numStr := tok[:i]
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatFloat(f, 'f', -1, 64), true
}

// auditNumbers 扫描模型回复中的金融数字，凡数值无可信出处（不在 trusted 集合中）即替换为数据缺失标注。
// 仅替换带单位的金钱/数量类数字，避免误伤百分比、时间、代码等。
func auditNumbers(reply string, trusted map[string]bool) string {
	if len(trusted) == 0 {
		return reply
	}
	idx := auditedNumberRe.FindAllStringIndex(reply, -1)
	if len(idx) == 0 {
		return reply
	}
	var sb strings.Builder
	sb.Grow(len(reply))
	last := 0
	for _, m := range idx {
		tok := reply[m[0]:m[1]]
		v, ok := normNumberToken(tok)
		if ok && trusted[v] {
			sb.WriteString(reply[last:m[1]])
		} else {
			sb.WriteString(reply[last:m[0]])
			sb.WriteString("[数据缺失]")
		}
		last = m[1]
	}
	sb.WriteString(reply[last:])
	return sb.String()
}

// consultCodeRe 从文本中提取 6 位股票代码。
var consultCodeRe = regexp.MustCompile(`\b\d{6}\b`)

// consultNoStockPrompt 专业模式下未能从消息中解析出股票时注入的提示词。
// 引导模型：无个股数据时做定性判断＋明确数据缺口，绝不编造个股/板块的任何具体数字。
const consultNoStockPrompt = `当前消息中未识别到明确的股票名称或 6 位代码，因此我这边没有任何该股的实时行情数据（现价/涨跌幅/主力净流入/大单明细/均线/MACD/策略信号都没有）。请你：
1. 先向用户说明：如果要我结合实时数据做分析，需要你指明具体股票（如：卧龙电驱 600580）。
2. 若用户是在问板块/情绪这类开放问题，你可以基于A股的一般规律做定性框架分析（例如"尾盘急拉回落常见于情绪资金抢跑""板块集体冲高回落要防情绪退潮"），也可以引用用户在问题里描述的现象来分析，但要明确标注这是"一般规律/经验判断"。
3. 严禁编造任何个股或板块的具体数字（成交额、净流入、撤单、振幅、持仓、涨幅、收益率、期货合约价、板块内具体个股名等），数据里没有就如实说"没有数据，无法确认"。
4. 措辞审慎，不承诺收益、不给绝对化的买卖指令。`

// buildConsultContext 从用户消息解析提到的股票，拉取真实实时行情组装为上下文文本。
// 返回空串表示未解析出任何股票（调用方应提示用户指明股票）。
// 数据来源：东财 push2 实时价（含主力净流入 F162）+ 东财资金流明细 + 新浪日K/分钟K + 引擎战法信号。
func (e *Engine) buildConsultContext(userMsg string) string {
	codes := make(map[string]string) // code → name

	// 1. 名称 → 代码：解析文本中出现的股票名称，再清洗为代码
	var names []string
	if e.newsAgent != nil {
		names = e.newsAgent.FindStocksInText(userMsg)
		for _, c := range e.newsAgent.CleanStocks(names) {
			parts := strings.SplitN(c, "|", 2)
			if len(parts) != 2 || parts[0] == "" {
				continue
			}
			codes[parts[1]] = parts[0]
		}
	}
	// 2. 文本中的纯 6 位代码
	for _, m := range consultCodeRe.FindAllString(userMsg, -1) {
		if _, ok := codes[m]; !ok {
			codes[m] = ""
		}
	}

	if len(codes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("以下是用户可能关心的股票今日实时行情实测数据（数据获取时间 " +
		time.Now().Format("2006-01-02 15:04:05") + "）：\n")
	sb.WriteString("【要求】仅可引用下列提供的数据；未提供的信息（如大盘资金、期指贴水、撤单、盘口等）如实说明" +
		"无法获取，严禁编造净流入/成交量/涨跌/触发等任何具体数字；净流入口径=主力(超大单+大单)，东方财富。\n")

	for code, name := range codes {
		sb.WriteString(e.buildStockBlock(code, name))
	}
	return sb.String()
}

// buildStockBlock 组装单只股票的实时行情数据块。
func (e *Engine) buildStockBlock(code, name string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n—— 股票 %s", code))
	if name != "" {
		b.WriteString(" " + name)
	}
	b.WriteString(" ——\n")

	// 实时报价（东财 push2，含主力净流入）
	if e.marketAPI == nil {
		b.WriteString("实时行情数据源未初始化。\n")
		return b.String()
	}
	si, err := e.marketAPI.GetRealtimeQuote(code)
	if err == nil && si != nil && si.Price > 0 {
		if si.Name != "" {
			name = si.Name
		}
		b.WriteString(fmt.Sprintf("现价 %.2f元 涨跌幅%.2f%% 今开%.2f 最高%.2f 最低%.2f 昨收%.2f\n",
			si.Price, si.ChangePct, si.Open, si.High, si.Low, si.Close))
		b.WriteString(fmt.Sprintf("成交量 %.0f股 成交额%.0f元 换手率 %.2f%%\n",
			si.Volume, si.Amount, si.Turnover))
		if si.NetInflow != 0 {
			b.WriteString(fmt.Sprintf("主力净流入 %.2f万元\n", si.NetInflow/1e4))
		} else {
			b.WriteString("主力净流入: 数据源未返回（无法获取该字段，请勿编造）\n")
		}
	} else {
		b.WriteString("实时行情获取失败。\n")
	}

	// 资金流明细（超大/大/中/小单，均以万元计）
	if cf, err := e.marketAPI.GetStockMoneyFlow(code); err == nil && cf != nil {
		b.WriteString(fmt.Sprintf("资金明细: 超大单净流入%.0f万 大单净流入%.0f万 中单净流入%.0f万 小单净流入%.0f万\n",
			(cf.SuperLargeIn-cf.SuperLargeOut)/1e4, (cf.LargeIn-cf.LargeOut)/1e4,
			(cf.MediumIn-cf.MediumOut)/1e4, (cf.SmallIn-cf.SmallOut)/1e4))
	}

	// 日K：当日振幅、MA5/MA10、近5日量能
	if kl, err := e.marketAPI.GetSinaKLine(code, 30); err == nil && len(kl) > 0 {
		last := kl[len(kl)-1]
		amp := 0.0
		if last.Close > 0 {
			amp = (last.High - last.Low) / last.Close * 100
		}
		b.WriteString(fmt.Sprintf("日K(最新一根 %s): 振幅%.2f%% 收%.2f 高%.2f 低%.2f\n",
			last.Date.Format("2006-01-02"), amp, last.Close, last.High, last.Low))
		if len(kl) >= 10 {
			b.WriteString(fmt.Sprintf("MA5=%.2f MA10=%.2f %s\n",
				consultMA(kl[len(kl)-5:]), consultMA(kl[len(kl)-10:]), consultMATrend(kl)))
		}
		// 近5日量能
		avg5 := consultMAVolume(kl)
		b.WriteString(fmt.Sprintf("近5日平均成交量 %.0f股，最新一根量 %.0f股\n", avg5, last.Volume))
	}

	// 分钟K（5分钟）MACD 状态
	if minKL, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48); err == nil && len(minKL) >= 2 {
		macd := data.CalcMACD(minKL)
		status := "空头"
		if macd.Bar > 0 {
			status = "多头(红柱)"
		} else if macd.Bar == 0 {
			status = "零轴"
		}
		b.WriteString(fmt.Sprintf("5分钟MACD: DIF=%.4f DEA=%.4f BAR=%.4f(%s)\n",
			macd.DIF, macd.DEA, macd.Bar, status))
	}

	// 引擎战法信号（该股是否已触发某战法）
	sigFound := false
	if e.agg != nil {
		if dash := e.agg.Current(); dash != nil {
			for _, sig := range dash.FinalSignals {
				if sig.Code == code {
					b.WriteString(fmt.Sprintf("策略信号: [%s] %s %s %s 触发价%.2f 理由:%s\n",
						sig.Strategy, sig.Direction, sig.Action, sig.Name, sig.Price, sig.Reason))
					sigFound = true
				}
			}
		}
	}
	// 该股当日信号批次（含触发时间），供模型据实判断"今天是否触发过量化信号、几点、什么性质"。
	if !sigFound {
		e.mu.RLock()
		records := e.signalRecords
		e.mu.RUnlock()
		for _, rec := range records {
			for _, sg := range rec.Signals {
				if sg.Code == code {
					b.WriteString(fmt.Sprintf("今日信号批次: %s触发 [%s] %s %s %s 触发价%.2f\n",
						rec.ProcessTime.Format("15:04"), sg.Strategy, sg.Direction, sg.Action, sg.Name, sg.Price))
				}
			}
		}
	}

	return b.String()
}

// consultMA 计算一段收盘价的简单平均。
func consultMA(kl []data.KLine) float64 {
	if len(kl) == 0 {
		return 0
	}
	var sum float64
	for _, k := range kl {
		sum += k.Close
	}
	return sum / float64(len(kl))
}

// consultMAVolume 计算最近5根日K的平均成交量（不足则取全部）。
func consultMAVolume(kl []data.KLine) float64 {
	if len(kl) == 0 {
		return 0
	}
	n := 5
	if len(kl) < n {
		n = len(kl)
	}
	var sum float64
	for _, k := range kl[len(kl)-n:] {
		sum += k.Volume
	}
	return sum / float64(n)
}

// consultMATrend 判断 MA5 相对 MA10 的多头/空头排列。
func consultMATrend(kl []data.KLine) string {
	if len(kl) < 10 {
		return ""
	}
	ma5 := consultMA(kl[len(kl)-5:])
	ma10 := consultMA(kl[len(kl)-10:])
	if ma5 > ma10 {
		return "均线多头排列"
	}
	return "均线空头排列"
}

// consultStoreFor 返回指定账号的咨询历史存储（§GAP2-W2 I-1 私有状态按账号寻址）：
// accountsRoot 注入时落 accounts/<uid>/consult_history.json（各自目录互不可见），
// 未注入（旧部署/测试）回退引擎级共享 store 保持兼容。懒加载并发安全。
// English: per-account consult history store under accounts/<uid>/; falls back to the shared engine
// store when accountsRoot isn't injected (legacy deploys/tests). Lazily built, concurrency-safe.
func (e *Engine) consultStoreFor(userID string) *data.ConsultStore {
	if userID == "" {
		return e.consultStore
	}
	e.mu.RLock()
	root := e.accountsRoot
	e.mu.RUnlock()
	if root == "" {
		return e.consultStore
	}
	e.consultMu.Lock()
	defer e.consultMu.Unlock()
	if st, ok := e.consultByUser[userID]; ok {
		return st
	}
	dir := filepath.Join(root, userID)
	_ = os.MkdirAll(dir, 0755)
	st := data.NewConsultStore(filepath.Join(dir, "consult_history.json"))
	e.consultByUser[userID] = st
	return st
}

// GetConsultHistory 返回指定账号的当日咨询对话历史（§GAP2-W2 账户隔离）。
func (e *Engine) GetConsultHistoryFor(userID string) []data.ConsultMessage {
	return e.consultStoreFor(userID).List()
}

// ClearConsultHistory 清空指定账号的当日咨询对话历史（§GAP2-W2 只清本人的）。
func (e *Engine) ClearConsultHistoryFor(userID string) {
	e.consultStoreFor(userID).Clear()
}

// buildPolicyRetaliationSignals 将政策反制事件转为可展示信号：
//  1. 事件去重后持久化到 confrontationStore（仅当日首次出现）；
//  2. 生成消息中心"政策反制"提示（利空方向，提醒关注受影响板块）；
//  3. 返回合成后的 NewsEvent（Source="政策反制"，供事件流/资讯展示）。
func (e *Engine) buildPolicyRetaliationSignals(events []data.ConfrontationEvent) []newsagent.NewsEvent {
	if len(events) == 0 {
		return nil
	}
	var out []newsagent.NewsEvent
	for _, ev := range events {
		if e.confrontStore != nil {
			if e.confrontStore.HasTitle(ev.Title) {
				continue // 当日已处理过，跳过避免重复提醒
			}
			e.confrontStore.Append(ev)
		}
		// 方向转带符号分数：利空 -0.75 / 利好 +0.75（高强度涉外政策事件）
		score := 0.75
		if ev.Direction == "利空" {
			score = -0.75
		}
		newsEv := newsagent.NewsEvent{
			Title:       ev.Title,
			Content:     ev.Content,
			Datetime:    ev.Datetime,
			Source:      "政策反制",
			Level:       "宏观",
			Direction:   ev.Direction,
			Score:       score,
			Sectors:     ev.Sectors,
			ImpactLevel: ev.Impact,
			EventType:   "政策",
			Urgency:     "紧急",
			Reason:      "涉外政策反制事件，直接影响相关板块",
		}
		out = append(out, newsEv)

		// 消息中心提示：提醒关注受影响板块（利空/利好方向由事件决定）
		if e.msgStore != nil {
			e.msgStore.Sync([]data.MessageItem{{
				ID:          "confront@" + ev.Title,
				Code:        "",
				Name:        "",
				Level:       "政策反制",
				Action:      "提示",
				Strategy:    "政策反制",
				Time:        nowTimeString(),
				Title:       "政策反制事件",
				Body:        ev.Title + "（影响板块：" + strings.Join(ev.Sectors, "、") + "）",
				Direction:   ev.Direction,
				GeneratedAt: time.Now(),
			}})
		}
	}
	return out
}

// nowTimeString 返回当前时间的 HH:MM:SS 字符串，用于消息中心提示的时间戳。
func nowTimeString() string {
	return time.Now().Format("15:04:05")
}

// syncMessages 汇总本轮交易信号（做多/做空）、止盈止损告警与持仓提示，合并进消息存储（按稳定键去重）。
// （syncMessages merges this round's trade signals (long/short), profit-loss alerts and holding notices into the
// message store, deduplicated by stable keys.)）
// bearSectors/bearStocks 为本轮利空板块与利空个股，用于扫出"命中利空板块的持仓"并提醒卖出。
// quotes 为调用方可复用的实时行情（5s fetcher 快照），trade 信号优先取用，缺失走 新浪批量→单查 回退。
func (e *Engine) syncMessages(bull, bear, alertSignals []combat_agent.Signal, sr *strategy_engine.StrategyResult, quotes map[string]*data.StockInfo) {
	if e.msgStore == nil {
		return
	}
	items := make([]data.MessageItem, 0, len(bull)+len(bear)+len(alertSignals)+2)
	// 交易信号：做多/做空，消息中心级别为"交易信号"，Action 按其方向定为 买入/卖出
	// （Trade signals: long/short, message level is "交易信号", Action mapped to 买入/卖出 by direction）
	trade := make([]combat_agent.Signal, 0, len(bull)+len(bear))
	trade = append(trade, bull...)
	trade = append(trade, bear...)
	// 预取 trade 信号的实时行情：5s 快照 → 新浪批量（一次请求）→ 实时单查回退，避免单查被限流时消息里"现价:0.00/无涨跌幅"。
	// English: prefetch live quotes for trade signals once — 5s snapshot → Sina batch (one call) → per-code fallback,
	// so rate-limited single lookups can't leave messages with 现价:0.00 or a missing 涨跌幅.
	live := make(map[string]*data.StockInfo, len(trade))
	if len(trade) > 0 && e.marketAPI != nil {
		var miss []string
		if quotes != nil {
			for _, sig := range trade {
				if si := quotes[sig.Code]; si != nil && si.Price > 0 {
					live[sig.Code] = si
				} else {
					miss = append(miss, sig.Code)
				}
			}
		} else {
			for _, sig := range trade {
				miss = append(miss, sig.Code)
			}
		}
		if len(miss) > 0 {
			for code, si := range e.marketAPI.GetSinaQuotes(miss) {
				if si != nil && si.Price > 0 {
					live[code] = si
				}
			}
			for _, sig := range trade {
				if _, ok := live[sig.Code]; ok {
					continue
				}
				if si, err := e.marketAPI.GetRealtimeQuote(sig.Code); err == nil && si != nil && si.Price > 0 {
					live[sig.Code] = si
				}
			}
		}
	}
	for _, sig := range trade {
		direction := sig.Direction
		if direction == "" {
			if sig.Action == "买入" || sig.Action == "buy" {
				direction = "做多"
			} else {
				direction = "做空"
			}
		}
		action := "买入"
		if direction == "做空" {
			action = "卖出"
		} else if sig.Action != "" {
			action = sig.Action
		}
		// C3 自动纸面开仓已由「两本账合一」镜像取代（阶段1.2）：模拟盘 fillLocked 成交后经
		// SetMirror 回调写 report 持仓账（registry.paperMirror），paper 为唯一真实账本，
		// rpt 由镜像保持一致 → CheckPositionsExits 离场路径照常激活，且不再双账本漂移。
		// English: C3 auto-paper-open is superseded by the unified-book mirror (unified books): after a
		// paper fillLocked, the SetMirror callback writes the report holding book (registry.paperMirror);
		// paper is the single source of truth and rpt stays consistent via mirroring — the exit path
		// activates as before, with no more dual-book drift.
		// AUTO_TRADING_PLAN M1：qmt.enabled 且 mode=auto 时，做多买入信号直连网关真实下单
		// （幂等：signal_id 唯一键，网关/首尔双端去重，熔断中自动跳过）。manual 模式不下单，
		// 由前端持仓页实盘 tab 确认后经 POST /api/positions/execute 执行。
		// English: AUTO_TRADING_PLAN M1 — when qmt.enabled and mode=auto, place a real order straight to the
		// gateway for long buy signals (idempotent via signal_id; double-deduped gateway & Seoul; skipped
		// while the breaker is open). manual mode sends nothing — the frontend live tab confirms first via
		// POST /api/positions/execute.
		if direction == "做多" && action == "买入" {
			e.autoPlace(sig, live)
		}
		// 现价与涨跌幅：优先实时行情（比信号触发价更新），行情失败则回退信号触发价，避免消息里"现价:0.00"
		// English: prefer the live quote for the price and change% (fresher than the trigger price); fall
		// back to the trigger price when the quote fails, so the message never reads "现价:0.00".
		price := sig.Price
		changePct := 0.0
		hasQuote := false
		if si := live[sig.Code]; si != nil && si.Price > 0 {
			price = si.Price
			changePct = si.ChangePct
			hasQuote = true
		}
		body := fmt.Sprintf("%s 战法:%s 置信度:%.0f%% 现价:%.2f %s", action, sig.Strategy, sig.Confidence*100, price, sig.Reason)
		if hasQuote {
			// 有实时行情时在现价后补涨跌幅（%+.2f 自带 +/- 符号）
			// English: when a live quote exists, append the change% after the price (%+.2f adds the +/- sign).
			body = fmt.Sprintf("%s 战法:%s 置信度:%.0f%% 现价:%.2f 涨跌幅:%+.2f%% %s",
				action, sig.Strategy, sig.Confidence*100, price, changePct, sig.Reason)
		}
		items = append(items, data.MessageItem{
			ID:          sig.Code + "@交易信号@" + sig.Strategy,
			Code:        sig.Code,
			Name:        sig.Name,
			Level:       "交易信号",
			Action:      action,
			Strategy:    sig.Strategy,
			Time:        sig.GeneratedAt.Format("15:04:05"),
			Title:       fmt.Sprintf("交易信号 %s %s", sig.Code, sig.Name),
			Body:        body,
			Direction:   direction,
			GeneratedAt: sig.GeneratedAt,
		})
	}
	for _, sig := range alertSignals {
		level := sig.AlertType
		if level == "" {
			level = "策略信号"
		}
		items = append(items, data.MessageItem{
			ID:          sig.Code + "@" + level,
			Code:        sig.Code,
			Name:        sig.Name,
			Level:       level,
			Action:      sig.Action,
			Strategy:    sig.Strategy,
			Time:        sig.GeneratedAt.Format("15:04:05"),
			Title:       fmt.Sprintf("%s %s", level, sig.Code),
			Body:        sig.Reason,
			Direction:   sig.Direction,
			GeneratedAt: sig.GeneratedAt,
		})
	}

	// §GAP2-W2 账户隔离（I-3）：持仓派生消息改为逐成员私有生成——
	// 旧实现用无过滤 rpt.HeldPositions()/List() 把【所有人】的持仓止盈止损并进共享消息中心，
	// 朋友能看到 owner 的仓位与成本，反之亦然。现在公共区只保留交易信号/策略信号（D3 共享口径），
	// 私有区按 memberIDs 逐账号用 ListFor(uid) 生成，Scope=uid 且 ID 加 "u<uid>|" 前缀防跨账号碰撞。
	// English: §GAP2-W2 — position-derived alerts are now generated per member from ListFor(uid) with
	// Scope=uid and a "u<uid>|" ID prefix; public items (trade/policy signals) stay shared per D3.
	if sr != nil {
		bearCodes := bearHitCodes(sr)
		now := time.Now()
		for _, uid := range e.memberIDs() {
			for _, pos := range e.rpt.HeldPositionsFor(uid) {
				if bearCodes[pos.Code] {
					items = append(items, data.MessageItem{
						ID:          fmt.Sprintf("u%s|bearhold@%s", uid, pos.Code),
						Scope:       uid,
						Code:        pos.Code,
						Name:        pos.Name,
						Level:       "利空提示",
						Action:      "卖出",
						Strategy:    pos.Strategy,
						Time:        now.Format("15:04:05"),
						Title:       fmt.Sprintf("利空提示 %s", pos.Code),
						Body:        fmt.Sprintf("持仓 %s(%s) 命中利空板块,建议考虑减仓/卖出", pos.Name, pos.Code),
						Direction:   "利空",
						GeneratedAt: now,
					})
				}
			}
			for _, l := range e.rpt.ListFor(uid) {
				if l.Status != "持仓中" && l.ExitAt == nil {
					continue
				}
				pct := ""
				if l.ProfitPct != nil {
					pct = fmt.Sprintf("%.1f%%", *l.ProfitPct)
				}
				items = append(items, data.MessageItem{
					ID:          fmt.Sprintf("u%s|hold@%s", uid, l.SignalID),
					Scope:       uid,
					Code:        l.Code,
					Name:        orName(l.Name, e.authoritativeName(l.Code), l.Code),
					Level:       "持仓提示",
					Action:      l.Status,
					Strategy:    l.Strategy,
					Time:        l.EntryAt.Format("15:04:05"),
					Title:       fmt.Sprintf("%s %s", l.Status, l.Code),
					Body:        fmt.Sprintf("策略:%s 入场:%.2f %s", l.Strategy, l.EntryPrice, pct),
					Direction:   l.Direction,
					GeneratedAt: l.EntryAt,
				})
			}
		}
	}
	// P1 强提醒：本轮新产生的 清仓/止损 告警，首次出现时走桌面通知 + Webhook 推送。
	// 依据消息去重键（code@level）判新：已在消息中心存在则说明前几轮已提醒过，不再重复推送。
	// English: strong P1 push — brand-new close-out / stop-loss alerts get a desktop + Webhook
	// notification on first appearance; deduped by the message-center key so repeats stay quiet.
	e.pushCriticalAlerts(items)
	e.pushSSEMessages(items)
	e.msgStore.Sync(items)
}

// messageVisibleExisting 构建指定作用域的"已存在键"集合（判新去重用）：
// 公共项对照公共可见集，私有项对照该账号可见集。
// English: builds the existing-key set for one item's scope (public vs that user's visible view).
func (e *Engine) messageVisibleExisting(scope string) map[string]bool {
	existing := make(map[string]bool)
	for _, m := range e.msgStore.ListVisible(scope) {
		existing[m.ID] = true
	}
	return existing
}

// pushSSEMessages 把本轮新增的关键消息经 SSE 定向推送（§GAP2-W2 按作用域路由）：
// 私有消息（Scope=uid）只推给该账号；公共消息扇出给本引擎服务的全部账号前端。
// 仅推送止盈/止损/清仓/交易信号等关键级别，且只在消息中心首次出现时推送（按作用域判新）。
// English: routes critical messages over SSE — private items go to their owner only; public ones fan
// out to every account served by this engine; deduped per scope on first appearance.
func (e *Engine) pushSSEMessages(items []data.MessageItem) {
	if e.sse == nil {
		return
	}
	members := e.memberIDs()
	// 所有级别的新消息首次出现时都经 SSE 推给前端——消息中心页据此实时刷新；
	// 系统弹窗仍由前端按关键级别（止盈/止损/清仓/交易信号）自行过滤，互不冲突。
	// English: push every newly-appearing message (any level) over SSE so the message
	// center reloads immediately; the frontend still gatekeeps system notifications by level.
	for _, it := range items {
		key := it.ID
		if key == "" {
			key = it.Code + "@" + it.Level
		}
		if e.messageVisibleExisting(it.Scope)[key] {
			continue
		}
		payload := map[string]interface{}{
			"type": "message",
			"item": it,
		}
		if it.Scope != "" {
			e.sse.BroadcastTo(it.Scope, payload)
			continue
		}
		// 公共关键消息：扇出全部成员；无成员信息时回退 userID（独占引擎旧语义）
		if len(members) == 0 {
			e.sse.BroadcastTo(e.userID, payload)
			continue
		}
		for _, uid := range members {
			e.sse.BroadcastTo(uid, payload)
		}
	}
}

// pushCriticalAlerts 对本次待同步消息中新增的关键告警（清仓/止损/交易信号/止盈）推送桌面+Webhook+外部推送网关强提醒。
// 仅推送消息中心尚未存在的键，避免 5s 循环重复轰炸；未配置推送器时直接跳过。
// 推送级别与 SSE 定向推送一致（止盈/止损/清仓/交易信号），让 APK 后台/离线也能收到交易信号与关键通知。
// （pushCriticalAlerts pushes desktop + Webhook + external-push-gateway alerts for newly-added critical
// messages (close-out / stop-loss / trade signals / take-profit) whose dedup key is not yet in the
// message center, so the 5s loop stays quiet on repeats; no-op when no notifier is wired. The level
// set matches the SSE push so APK background/offline still receives trade signals and key alerts.）
func (e *Engine) pushCriticalAlerts(items []data.MessageItem) {
	e.mu.RLock()
	nt := e.notifier
	e.mu.RUnlock()
	if nt == nil {
		return
	}
	for _, it := range items {
		switch it.Level {
		case "清仓", "止损", "止盈", "交易信号":
		default:
			continue
		}
		key := it.ID
		if key == "" {
			key = it.Code + "@" + it.Level
		}
		// §GAP2-W2 按作用域判新：私有告警对照本人可见集，公共告警对照公共集——
		// 避免不同账号的同键消息互相"顶掉"首次推送时机。
		if e.messageVisibleExisting(it.Scope)[key] {
			continue
		}
		title := fmt.Sprintf("%s %s(%s)", it.Level, it.Name, it.Code)
		msg := notify.Message{
			Level:   notify.LevelHigh,
			Title:   title,
			Content: it.Body,
			// §GAP2-W2 别名路由：私有告警推给归属账号的设备别名（quant_<uid>），
			// 公共信号保持默认别名（owner 手机）——朋友的止损提醒不再打到 owner 手机上。
			Alias: it.Scope,
		}
		nt.Push(msg)
		// 同步转发到外部推送网关（若已配置），让关键提醒触达 APK 后台/离线场景
		nt.PushGateway(msg)
	}
}

// SetScanner 设置板块扫描器（线程安全，透传给策略引擎）。
// §E8 修复：锁内只换引用，子模块同步放锁外——与 SetEmotionConfig/SetNotifier 的快照模式一致，
// 避免持 e.mu 调外部对象形成锁序隐患。
func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.mu.Unlock()
	e.strategy.SetScanner(scanner)
}

// SetSectorSource 设置同花顺出口（板块名单/行情表）。
func (e *Engine) SetSectorSource(ths *data.THSClient) {
	e.mu.Lock()
	e.ths = ths
	e.mu.Unlock()
}

// LLMClient 返回当前 LLM 客户端（可空）。
func (e *Engine) LLMClient() *llm.Client {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.llmClient
}

// SetClock 注入时钟（e2e 固定交易时段用）；nil 恢复真实时间。
// 修复：主循环/近实时循环的"盘前抑制信号"门控此前读真实 time.Now——
// 凌晨跑 e2e 时全部信号被抑制（日期/时刻漂移型 flaky 根因）。
func (e *Engine) SetClock(fn func() time.Time) {
	e.mu.Lock()
	e.clockFn = fn
	e.mu.Unlock()
}

// nowTime 引擎统一时钟读取。
func (e *Engine) nowTime() time.Time {
	e.mu.RLock()
	fn := e.clockFn
	e.mu.RUnlock()
	if fn == nil {
		return time.Now()
	}
	return fn()
}

// SetLLMClient 热重建 LLM 客户端（前端改配置时调用）。§E8 同上：锁外透传。
func (e *Engine) SetLLMClient(c *llm.Client) {
	e.mu.Lock()
	e.llmClient = c
	e.mu.Unlock()
	e.newsAgent.SetLLMClient(c)
}

// ── 利好/利空开关 ──

// SetLongEnabled 设置做多开关状态（线程安全，前端控制面板调用）。
func (e *Engine) SetLongEnabled(v bool) {
	e.mu.Lock()
	e.longEnabled = v
	e.mu.Unlock()
}

// LongEnabled 返回做多开关是否开启。
func (e *Engine) LongEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.longEnabled
}

// SetShortEnabled 设置做空开关状态。
// 关闭（切回仅做多）时顺带清除消息中心残留的做空方向消息（Level/Direction=做空），
// 避免仅做多界面误展示历史或测试产生的做空条目；不记录墓碑，重新开启后可正常同步。
func (e *Engine) SetShortEnabled(v bool) {
	e.mu.Lock()
	e.shortEnabled = v
	e.mu.Unlock()
	if !v && e.msgStore != nil {
		n := e.msgStore.PurgeShortLevel()
		log.Printf("[engine] 做空已关闭, 已清除消息中心 %d 条做空残留", n)
	}
}

// ShortEnabled 返回做空开关是否开启。
func (e *Engine) ShortEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.shortEnabled
}

// DashboardData 返回当前看板快照（引擎内部 agg 的 Current()）。多账号模式下各引擎独立看板。
// English: returns the current dashboard snapshot from this engine's agg. In multi-account mode
// each engine has its own agg, so results are per-account.
func (e *Engine) DashboardData() *display.DashboardData {
	return e.agg.Current()
}

// GetDebugInfo 返回最近一轮流水线的调试数据。
func (e *Engine) GetDebugInfo() *newsagent.DebugInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.debugInfo
}

// produceOut 新闻流水线的中间产物，供 Run 后续环节复用（策略评估/D1评分/调试快照）。
type produceOut struct {
	rawNews []data.NewsItem        // 原始新闻（标题党校正后）
	st0     newsagent.Stage0Result // Stage0 归因分类结果
	events  []newsagent.NewsEvent  // 全量已打标事件（含中性/一般）
	valid   []newsagent.NewsEvent  // 阈值过滤+聚簇+衰减后的有效事件（进引擎）
	timing  NewsTiming             // 本轮新闻流水线分段耗时（e2e 实速模拟观测）
}

// NewsTiming 新闻生产子环节耗时（均为实测墙钟时长，含该环节内 LLM/网络等待）。
type NewsTiming struct {
	Sectors time.Duration // 板块名单刷新
	Fetch   time.Duration // 原始新闻拉取
	Stage0  time.Duration // Stage0 归因分类(LLM)
	Stage2  time.Duration // Stage2 深度分析(LLM) + 事件构建
	Events  time.Duration // 聚簇/衰减/验真/传播
}

// RunTiming 一轮 engine.Run 的完整分段耗时（e2e 实速模拟 + 性能观测）。
// 各字段为实测墙钟时长，单位纳秒；聚合关系见报告输出。
type RunTiming struct {
	Total       time.Duration // 整轮总耗时
	News        NewsTiming    // 0-6 新闻流水线
	Evaluate    time.Duration // 7  策略评估(归因+分流+评分池+行情)
	HotRec      time.Duration // 7b 热点板块记录固化
	D1          time.Duration // 8  D1 批量评分(LLM)
	PE          time.Duration // 8a PE 预取
	Pool        time.Duration // 8b 涨停池+新闻简报
	Verify      time.Duration // 9  板块验证
	HotPool     time.Duration // 9b 热点池更新(成分股并监控池)
	MergeSector time.Duration // 9c 板块→个股归因
	Signals     time.Duration // 10-12 出信号(做多/涨停增强/做空/个股直入)
	Tracker     time.Duration // 12b 跟踪池收尾
	Alerts      time.Duration // 13 持仓止盈/止损提醒
	Agg         time.Duration // 14 看板更新+信号日志
	SSE         time.Duration // 16 SSE 广播
}

// produceNews 执行新闻流水线：拉取→Stage0 归因→Stage2 深度分析→固化→阈值→聚簇→衰减→归因验真传播。
// 独立成方法以便盘前以异步 goroutine 触发，避免 LLM 同步重试阻塞主循环。
func (e *Engine) produceNews(ctx context.Context, since time.Time) produceOut {
	out := produceOut{}

	// 0. 刷新同花顺板块名单到 scanner（FindSectorsByNames/归因校验依赖真实板块名单）
	_stepSectors := time.Now()
	e.refreshSectors()
	out.timing.Sectors = time.Since(_stepSectors)

	// 1. 拉取原始新闻（新抓入未归因队列）+ 历史未归因新闻（LLM 上一轮失败留队重试）
	// 合并去重后统一跑 Stage0/Stage2：成功归因的从队列移除并标记 seen，失败留队下一轮重试，
	// 保证"昨夜新闻因 LLM 慢/失败"也能在盘前多轮内补上归因。
	_stepFetch := time.Now()
	newNews := e.newsAgent.Fetch(ctx, since)
	pending := e.newsAgent.UnattributedItems()
	out.rawNews = dedupNews(append(newNews, pending...))
	out.timing.Fetch = time.Since(_stepFetch)

	if len(out.rawNews) == 0 {
		log.Printf("[engine] 无新新闻且无待归因新闻 (since=%s), 本轮仅执行打分", since.Format("01-02 15:04"))
		return out
	}

	// 2. Stage0 归因分类：个股 / 板块 / 一般（合并垃圾过滤+价值初筛+标题党复核）
	_stepStage0 := time.Now()
	out.st0 = e.newsAgent.Stage0(out.rawNews)
	out.timing.Stage0 = time.Since(_stepStage0)
	if out.st0.Err != nil {
		// Stage0 失败（如 LLM 未配置/连不通）：整批不归一般（避免误判丢失），
		// 全部标记 FailedIdx 留未归因队列，下一轮轮询重新调 LLM；仅记录原因便于排障。
		// English: Stage0 failure (e.g. LLM unconfigured/unreachable): the whole batch is NOT misclassified as
		// general news; all items stay in the unattributed queue (FailedIdx) and are re-attributed next round
		// once the LLM is reachable.
		log.Printf("[engine][news漏斗] Stage0失败, 原始%d条留待重试(入未归因队列), 无LLM分析: %v", len(out.rawNews), out.st0.Err)
	}
	log.Printf("[engine][news漏斗] 原始=%d 个股=%d 板块=%d IPO=%d 一般=%d (板块material保留=%d)",
		len(out.rawNews), len(out.st0.StockIdx), len(out.st0.SectorIdx), len(out.st0.IpoIdx), len(out.st0.GeneralIdx), len(out.st0.Material))

	// 2b. 标题党修复：LLM 校正标题应用到原文（供 Stage2 分析、事件与展示使用）
	for i, t := range out.st0.CorrectedTitle {
		if i >= 0 && i < len(out.rawNews) && t != "" {
			out.rawNews[i].Title = t
		}
	}

	// 3. 收集全量事件
	_stepStage2 := time.Now()
	// 本轮归因失败的新闻（留未归因队列，下轮重试）
	var failedNews []data.NewsItem
	// 3a. 个股新闻：跳过 Stage1，直接 Stage2 深度分析
	if len(out.st0.StockIdx) > 0 {
		stockItems := pickItems(out.rawNews, out.st0.StockIdx)
		evs, failed := e.newsAgent.Stage2(stockItems)
		out.events = append(out.events, evs...)
		failedNews = append(failedNews, failed...)
	}

	// 3b. 板块新闻：material 价值初筛（合并进 Stage0 单次调用）→ Stage2 深度分析
	if len(out.st0.SectorIdx) > 0 {
		sectorItems := pickItems(out.rawNews, out.st0.SectorIdx)
		var kept []int
		for j := range sectorItems {
			if out.st0.Material[out.st0.SectorIdx[j]] {
				kept = append(kept, j)
			}
		}
		if len(kept) > 0 {
			evs, failed := e.newsAgent.Stage2(pickItems(sectorItems, kept))
			out.events = append(out.events, evs...)
			failedNews = append(failedNews, failed...)
		}
	}

	// 3c. IPO 新闻：直构事件（不走 LLM）
	if len(out.st0.IpoIdx) > 0 {
		ipoItems := pickItems(out.rawNews, out.st0.IpoIdx)
		out.events = append(out.events, e.newsAgent.BuildIPOFeedEvents(ipoItems)...)
	}

	// 3d. 一般新闻：不入引擎，仅由 SaveEvents 保存展示

	// 4. 注入 IPO 日历事件
	out.events = append(out.events, e.newsAgent.BuildIPOEvents()...)

	// 4a. IPO 启动板块事件：即将上市新股（宇树科技等）LLM 分析产业链价值传导，
	// 归因出热点板块与上下游影响个股（卧龙电驱/三花智控），灌入板块监测与打分池。
	// （IPO boot events: LLM chain analysis for soon-to-list IPOs turns their listing into a hot sector
	// with upstream/downstream beneficiaries, feeding sector monitoring and the scoring pool.）
	out.events = append(out.events, e.newsAgent.BuildIPOBootEvents()...)

	// 4b. 政策反制事件：从涉外政策新闻关键词识别（直构，不走 LLM），并入事件流
	retEvents := e.buildPolicyRetaliationSignals(e.newsAgent.DeriveRetaliation(out.rawNews))
	out.events = append(out.events, retEvents...)

	// 5. 持久化全量事件供 /api/news 展示（含中性/一般新闻）
	e.newsAgent.SaveEvents(out.events)
	log.Printf("[engine][news漏斗] 事件共=%d (>=0.25落盘), 个股+板块+IPO来源", len(out.events))

	// 5b. 固化 Stage2 带价值事件（|score|≥0.25 且方向=利好/利空，含关联个股），跨刷新/跨日持续存在。
	// 同板块+同方向新事件覆盖（分数取最新）；随下一交易日到期清理。
	e.newsAgent.SaveFrozen(out.events)

	// 6. 阈值过滤：仅 |score| ≥ 0.50 进引擎（弱/中性丢弃）
	out.valid = filterThreshold(out.events, 0.50)
	// 6a. 固化事件回填：本轮没有产出有效事件时，回填昨日/上一轮固化的事件，避免弱刷新或 LLM
	// 偶发失败把已固化的利好/利空事件（及关联个股）直接打没。正常连产场景未复用避免重复。
	if len(out.valid) == 0 {
		frozen := e.newsAgent.FrozenEvents()
		if len(frozen) > 0 {
			out.valid = filterThreshold(frozen, 0.50)
			if len(out.valid) > 0 {
				log.Printf("[engine][news漏斗] 本轮无新有效事件, 回填固化 %d 条保底", len(out.valid))
			}
		}
	}
	log.Printf("[engine][news漏斗] 有效=%d (阈值0.5, 含固化回填)", len(out.valid))
	if len(out.valid) > 0 {
		// 6b. 事件聚簇：同板块/同方向的重复新闻合并为单条（去重避免刷屏）
		out.valid = clusterEvents(out.valid)

		// 6c. 事件衰减：同板块同方向事件在窗口内重复出现时按 0.5^(h/4) 降权
		e.applyEventDecay(out.valid)

		// 6d. 衰减后再次阈值过滤（重复事件降权后可掉出 0.5 线）
		out.valid = filterThreshold(out.valid, 0.50)
		log.Printf("[engine][news漏斗] 聚簇+衰减后再滤 -> 有效=%d", len(out.valid))
		if len(out.valid) > 0 {
			// 6e. 板块验真回填：剔除 LLM 幻觉板块名（命中真实板块名单才保留）
			e.verifySectorAttribution(out.valid)

			// 6f. 板块→个股事件级传播：板块 top 成分股注入 RelatedStocks 进个股监测池
			e.propagateSectorToStocks(out.valid)
		}
	}
	if len(out.valid) == 0 {
		log.Printf("[engine] 无有效事件(|score|>=0.5), 本轮仅执行打分")
	}
	logDroppedFromPool(out.events, out.valid)
	// 6g. 归因完成判定并从未归因队列移除：
	//    - 成功归因 = 产出事件（含中性，只要 Stage2 分析完成）或 Stage0 明确分类为一般/IPO；
	//    - 失败留队 = Stage0 FailedIdx（LLM 重试耗尽未判定）或 Stage2 failedItems（深度分析失败）。
	// 用标题匹配把成功者移出 pending（MarkAttributedTitles），失败者由下一轮盘前/盘中重试，
	// 避免"LLM 慢/失败一次 = 昨夜有价值的新闻永久丢失"。
	e.markAttributedFromProduce(out, failedNews)
	// 阶段2(深度分析 LLM)+事件构建总耗时（含聚簇/衰减/验真/传播）
	out.timing.Stage2 = time.Since(_stepStage2)
	out.timing.Events = out.timing.Stage2
	return out
}

// markAttributedFromProduce 从未归因队列移除本轮成功归因的新闻（保留失败者留队重试）。
// 成功集合 = 产出事件新闻 ∪ Stage0 判为一般/IPO 的新闻；失败集合 = Stage0 FailedIdx ∪ Stage2 failedItems。
// （markAttributedFromProduce removes this round's successfully-attributed news from the unattributed queue,
// keeping failures queued for retry. Success = emitted events ∪ Stage0-classified general/IPO; failure =
// Stage0 FailedIdx ∪ Stage2 failedItems.）
func (e *Engine) markAttributedFromProduce(out produceOut, failedNews []data.NewsItem) {
	failed := make(map[string]bool, len(failedNews))
	for _, it := range failedNews {
		failed[it.Title] = true
	}
	for _, f := range out.st0.FailedIdx {
		if f >= 0 && f < len(out.rawNews) {
			failed[out.rawNews[f].Title] = true
		}
	}
	// 事件标题集合（成功归因）
	success := make(map[string]bool)
	for _, ev := range out.events {
		success[ev.Title] = true
	}
	// Stage0 判为一般（Official=false 等明确分类）或 IPO 的新闻也算归因完成（无需 LLM 深度分析）
	for _, i := range out.st0.GeneralIdx {
		if i >= 0 && i < len(out.rawNews) {
			success[out.rawNews[i].Title] = true
		}
	}
	for _, i := range out.st0.IpoIdx {
		if i >= 0 && i < len(out.rawNews) {
			success[out.rawNews[i].Title] = true
		}
	}
	// 移除成功者中不冲突的（失败优先：失败标题覆盖成功标题）
	for t := range failed {
		delete(success, t)
	}
	if len(success) > 0 {
		e.newsAgent.MarkAttributedTitles(success)
	}
}

// dedupNews 按标题去重合并两批新闻（保留先出现者，通常新抓在前、历史未归因在后）。
// （dedupNews merges two news slices by title, keeping the first occurrence (new fetches first, then pending).）
func dedupNews(items []data.NewsItem) []data.NewsItem {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]bool, len(items))
	out := make([]data.NewsItem, 0, len(items))
	for _, it := range items {
		if it.Title == "" || seen[it.Title] {
			continue
		}
		seen[it.Title] = true
		out = append(out, it)
	}
	return out
}

// logDroppedFromPool 可观测旁路：消息中心已落盘展示(≥0.25)的事件，若未进入 ≥0.5 有效事件池
// （因而不会参与 D1 打分 / N 形信号），打印事件标题、关联个股与丢弃原因，便于排查"打了利好却没进 D1"。
// 返回被丢弃计数（供测试断言）。
func logDroppedFromPool(shown, valid []newsagent.NewsEvent) int {
	if len(shown) == 0 {
		return 0
	}
	keep := make(map[string]bool, len(valid))
	for _, ev := range valid {
		keep[ev.Title] = true
	}
	var dropped int
	var sb strings.Builder
	for _, ev := range shown {
		if absScore(ev.Score) < 0.25 {
			continue // 未落盘展示的不在考察范围
		}
		if keep[ev.Title] {
			continue
		}
		dropped++
		code := ""
		if len(ev.RelatedStocks) > 0 {
			code = ev.RelatedStocks[0]
		}
		sb.WriteString(fmt.Sprintf("[%s|%s|score=%+.2f|%s] %s\n", ev.Level, ev.Direction, ev.Score, code, ev.Title))
	}
	if dropped > 0 {
		log.Printf("[观察] 消息中心已展示但未进打分池 %d 条(请逐条核对 Level/关联股是否落入打分池):\n%s", dropped, sb.String())
	}
	return dropped
}

// TryAsyncRun 尝试异步触发一次引擎 run（盘前用）：已有异步 run 进行中则返回 false 跳过本轮，
// 避免多 goroutine 并发重入导致状态互相覆盖；其余时段仍由主循环同步调用 Run。
// §E1 修复：goroutine 加 panic recovery——主流水线（LLM 解析/策略扫描/板块传播）任何未预期
// panic 此前会直接杀死整个交易进程，现降级为跳过本轮并留完整堆栈日志。
// English: E1 fix — the async Run goroutine now recovers from panics (full stack logged) instead of
// crashing the whole trading process; the cycle is simply skipped and asyncBusy is still released.
func (e *Engine) TryAsyncRun(ctx context.Context, since time.Time) bool {
	if !atomic.CompareAndSwapInt32(&e.asyncBusy, 0, 1) {
		return false
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				metrics.PanicRecovered() // §R4-9 panic 恢复计数进指标面
				log.Printf("[engine] 异步Run panic(本轮跳过): %v\n%s", r, debug.Stack())
			}
			atomic.StoreInt32(&e.asyncBusy, 0)
		}()
		e.Run(ctx, since)
	}()
	return true
}

// AsyncIdle 返回异步引擎是否空闲（上一轮异步 Run 已完成）。
// 供盘前主循环"跑完即排下一轮"轮询，替代固定 5min 间隔。
// （AsyncIdle reports whether the async engine is idle (previous async Run finished), for the premarket
// main loop to trigger the next round immediately instead of waiting a fixed 5 minutes.）
func (e *Engine) AsyncIdle() bool {
	return atomic.LoadInt32(&e.asyncBusy) == 0
}

// HasNewNews 委托新闻代理做轻量"新新闻到达"探测（无新闻代理则视为无新料），
// 供盘中调度器决定是否立即触发新一轮扫描（而非等固定 5min 心跳）。
// （HasNewNews delegates to the news agent's cheap "new news arrived" probe, telling the intraday
// scheduler whether to trigger a fresh round immediately instead of waiting on the fixed heartbeat.）
func (e *Engine) HasNewNews() bool {
	if e.newsAgent == nil {
		return false
	}
	return e.newsAgent.HasNewNews()
}

// Run 驱动一轮完整流水线：拉取 → Stage0 → Stage1/2 → 阈值过滤 → 归因 → 板块验证 → 战法扫描 → 信号聚合 → 广播。
// since 为本次追回起始时间，由调用方（主循环）根据市场时段计算。
func (e *Engine) Run(ctx context.Context, since time.Time) *strategy_engine.StrategyResult {
	t0 := time.Now()
	// 同步本账号配置（做多/做空开关 + 战法参数），保证账号内各设备一致
	// English: sync this account's config (long/short toggles + strategy params) for cross-device consistency.
	e.syncAccountConfig()

	// 0-6. 新闻流水线：拉取→Stage0/1/2→固化→阈值→聚簇→衰减→归因验真传播
	pOut := e.produceNews(ctx, since)
	rawNews, st0, events, valid := pOut.rawNews, pOut.st0, pOut.events, pOut.valid

	// 6g. 新热点立马进池：新闻归因一产出有效事件，立即归因出板块 → 验证 → 更新 5s 实时监控池，
	// 不等主循环末尾。这样归因出板块成分股的瞬间就能被近实时循环盯上（减少"板块已热但个股迟迟不入池"）。
	// 与 9b 的 updateHotPool 幂等（相同板块重复写入无害）。
	// English: push fresh hotspots into the watch pool immediately after attribution, without waiting for
	// the round's end, so sector constituents are picked up by the near-realtime loop at once (idempotent
	// with the later updateHotPool at step 9b).
	e.pushFreshHotspots(valid)

	// 7. 策略评估：归因 + 分流 + 评分池 + 行情数据（无事件时仅覆盖 持仓+自选 打分池）
	// §P1-2 多账号隔离：持仓/自选按本账号过滤（共享引擎且 userID 为空时回退全局并集）。
	positions := e.rpt.HeldPositionCodesFor(e.userID)
	if e.userID == "" {
		positions = e.rpt.HeldPositionCodes()
	}
	watchlist := e.wlMgr.List(e.userID)
	if e.userID == "" {
		watchlist = e.wlMgr.All()
	}
	_stepEval := time.Now()
	sr := e.strategy.Evaluate(ctx, valid, positions, watchlist)
	_evalT := time.Since(_stepEval)

	// 7b. 固化本轮热点板块记录（同花顺 top-20 匹配后，供前端展示历史）
	_stepHotRec := time.Now()
	e.captureHotRecord(sr)
	_hotRecT := time.Since(_stepHotRec)

	td := data.TradingDayDate(time.Now())

	// 8b. 当日涨停池 + 事件新闻简报（龙头识别 / 涨停分类 / 预期差检测）
	_stepPool := time.Now()
	pool, poolErr := e.marketAPI.GetLimitUpPool("")
	if poolErr != nil {
		log.Printf("[engine] 涨停池拉取失败: %v", poolErr)
	}
	// 事件简报取当日全量已打标事件（比本轮 valid 更全：个股级事件即使本轮未过阈值也可关联信号标题）
	// English: build news briefs from today's full attributed-event store (richer than this round's `valid`,
	// so individual-stock events below this round's threshold can still title D1 events on their signals).
	newsBriefs := newsBriefsByCode(e.newsAgent.AllEvents())
	_poolT := time.Since(_stepPool)

	// 8c. 情绪阶段（供 N 形评分硬闸）+ 8a/8b 持续打分输出容器
	// §E2 修复：emotionCfg/llmClient 与热更新写方（SetEmotionConfig/SetLLMClient）构成数据竞争，
	// 统一改为 RLock 快照后使用。
	e.mu.RLock()
	emotionCfg := e.emotionCfg
	llmClient := e.llmClient
	e.mu.RUnlock()
	emotionPhase := ""
	if emotionCfg != nil {
		emotionPhase = data.DetectEmotionPhaseV2(pool, 0, 0, emotionCfg)
	}
	e.mu.Lock()
	e.lastEmotionPhase = emotionPhase
	e.mu.Unlock()
	stockScores := make(map[string]combat_agent.StockScores)

	// 9. 板块验证（开关控制），结果同时用于战法扫描与看板展示
	_stepVerify := time.Now()
	var verifiedBull, verifiedBear []sector_agent.VerifiedSector
	if e.LongEnabled() {
		verifiedBull = e.sectorAgent.Verify(sr.HotSectors)
		// 板块解码/归因失败降级检测：输入有热点板块却验证出 0 个，说明 LLM/板块解析异常，
		// 板块默认退化为中性(无信号)。记告警 + 计入降级计数，保证日志可见而非静默吞掉。
		if len(sr.HotSectors) > 0 && len(verifiedBull) == 0 {
			e.recordLLMDegrade(fmt.Sprintf("板块验真失败: 输入%d个利好板块验证为0(解码/归因异常, 默认中性无信号)", len(sr.HotSectors)), len(sr.HotSectors))
		}
	}
	if e.ShortEnabled() {
		verifiedBear = e.sectorAgent.Verify(sr.BearSectors)
		if len(sr.BearSectors) > 0 && len(verifiedBear) == 0 {
			e.recordLLMDegrade(fmt.Sprintf("板块验真失败: 输入%d个利空板块验证为0(解码/归因异常, 默认中性无信号)", len(sr.BearSectors)), len(sr.BearSectors))
		}
	}
	_verifyT := time.Since(_stepVerify)

	// 9b. 验证通过的板块成分股并入 5s 实时监控池（fetcher 新浪批量拉取，热点股随板块轮换）
	_stepHotPool := time.Now()
	e.updateHotPool(verifiedBull, verifiedBear)
	_hotPoolT := time.Since(_stepHotPool)

	// 9c. 板块→个股归因：热点板块 top 成分股并入打分池 + 行情。
	// 修复"板块利好只有板块、没有个股"：此前成分股不在 sr.MarketData 里，
	// ScanLong 遍历 sector.Stocks 时 md==nil 直接丢弃 → 板块永远归因不到个股。
	// 现在 D1 与板块利好/利空事件分完全解耦：不再用 SectorHot.Score 直接当 D1，
	// 而是把成分股并入 ScoringPool、由随后的 D1 batch 统一 LLM 打分；
	// 板块事件标题经 eventMap 注入 D1 评分上下文，供 LLM 合理核定。
	// English: sector→stock attribution merges verified-bull top constituents into the scoring pool +
	// market data. D1 is fully decoupled from the sector bull/bear event score: constituents join the pool
	// and the following D1 batch grades them via LLM, with the sector event title injected as D1 context.
	_stepMerge := time.Now()
	peScores := make(map[string]float64, len(sr.ScoringPool))
	eventMap := e.mergeSectorStocksIntoScores(ctx, sr, verifiedBull, verifiedBear, peScores)
	_mergeT := time.Since(_stepMerge)

	// 8. D1 评分（扩展后的打分池个股：新闻/持仓/自选 + 板块成分股 + LLM重试队列）。
	// 板块事件标题一并注入 D1 评分上下文（个股不在新闻点名里也能按板块事件合理打分）；
	// LLM 失败/漏项不兜底（不回退上一轮、不归0占位）：标记 RetryPending 并入重试队列，
	// 下轮随打分池重新调 LLM，避免断链归零。D1 与板块利好/利空事件分解耦，独立 40 分制。
	// English: D1 batch scores the expanded pool (news/holdings/watchlist + sector constituents + the LLM
	// retry queue). Sector event titles are injected into D1 context so non-news-named constituents still
	// grade fairly. LLM failures are NEVER padded (no prior-round fallback, no plain-0 placeholder): they are
	// marked RetryPending and merged back into the retry queue for a fresh LLM call next round.
	_stepD1 := time.Now()
	d1Scorer := combat_agent.NewD1Scorer(llmClient, "")
	e.mu.RLock()
	retries := e.d1MaxRetries
	e.mu.RUnlock()
	d1Scorer.SetMaxRetries(retries)
	e.mu.RLock()
	retryQueue := make([]string, 0, len(e.d1RetryQueue))
	for code := range e.d1RetryQueue {
		retryQueue = append(retryQueue, code)
	}
	e.mu.RUnlock()
	// 重试队列并入本轮打分池：失败股下轮重新走 LLM，不回退、不丢弃。
	// English: merge the retry queue into this round's pool so failed stocks are re-scored via LLM.
	if len(retryQueue) > 0 {
		inPool := make(map[string]bool, len(sr.ScoringPool))
		for _, c := range sr.ScoringPool {
			inPool[c] = true
		}
		for _, c := range retryQueue {
			if !inPool[c] {
				sr.ScoringPool = append(sr.ScoringPool, c)
			}
		}
		log.Printf("[engine] D1重试队列%d只并入本轮打分池", len(retryQueue))
	}
	d1Scorer.SetSectorEvents(eventMap)
	d1Scores := d1Scorer.BatchScore(sr.ScoringPool, valid, sr.MarketData)
	// 收集 LLM 失败待重试个股：并入重试队列（下轮重调 LLM），并清理已成功个股。
	// English: collect RetryPending stocks into the retry queue (re-scored next round); drop the resolved ones.
	nextRetry := make(map[string]bool)
	for code, d := range d1Scores {
		if d.RetryPending {
			nextRetry[code] = true
		}
	}
	// LLM 评分失败降级检测：失败个股本轮无 D1 分(等价于中性占位 0 分)，记告警 + 计入降级计数，
	// 保证日志可见（而非静默置 0），失败股并入下轮重试队列继续尝试真实 LLM 评分。
	if len(nextRetry) > 0 {
		e.recordLLMDegrade(fmt.Sprintf("D1 LLM 评分失败 %d 只, 本轮降级为中性占位并进入重试队列", len(nextRetry)), len(nextRetry))
	}
	e.mu.Lock()
	e.lastD1Scores = d1Scores
	e.d1RetryQueue = nextRetry
	e.mu.Unlock()

	// 历史 D1 方案B·攒数据：本轮真实 LLM 评分按日落库 d1_scores（幂等覆盖），
	// 攒够数据后 N 形回放按触发日 JOIN 当日真实分。重试占位（RetryPending，分数 0）
	// 不入库；落库失败仅记日志，绝不影响打分主流程。
	// English: persist this round's real LLM D1 scores per day (idempotent) so N-shape replay can
	// later join the real trigger-day score. Retry placeholders are skipped; failures only log.
	if e.realStore != nil && len(d1Scores) > 0 {
		rows := make([]store.D1ScoreRow, 0, len(d1Scores))
		for code, d := range d1Scores {
			if d.RetryPending {
				continue
			}
			rows = append(rows, store.D1ScoreRow{Code: code, Score: d.Score, Blocked: d.Blocked, Reason: d.Reason})
		}
		if err := e.realStore.UpsertD1Scores(time.Now().Format("2006-01-02"), rows); err != nil {
			log.Printf("[engine] D1 评分落库失败(不影响主流程): %v", err)
		}
	}
	_d1T := time.Since(_stepD1)

	// 8a. 打分池 PE 预取（N 形 D3 超跌评分；东财 clist f9，TTL 缓存降低限流压力）。
	// 板块成分股的 PE 已在 9c 内补，这里只补其余池内个股。
	// English: prefetch PE for the scoring pool (N-shape D3 oversold). Constituent PE was filled in 9c;
	// only remaining pool codes are fetched here.
	_stepPE := time.Now()
	for _, code := range sr.ScoringPool {
		if _, ok := peScores[code]; ok {
			continue
		}
		peScores[code] = e.marketAPI.GetStockPE(code)
	}
	_peT := time.Since(_stepPE)

	// 10-12. 出信号：做多 + 涨停增强 + 做空 + 个股直入（D1 复用，不重复调 LLM）
	_stepSignals := time.Now()
	// 10. 利好开关开 → 做多分支
	var bullSignals []combat_agent.Signal
	if e.LongEnabled() {
		bullInput := combat_agent.ScanInput{
			Sectors:      verifiedBull,
			L1Score:      sr.L1Score,
			L1Blocked:    sr.L1Blocked,
			MarketData:   sr.MarketData,
			D1Scores:     d1Scores,
			PE:           peScores,
			LimitUpPool:  pool,
			News:         newsBriefs,
			Scores:       stockScores,
			EmotionPhase: emotionPhase,
		}
		bullSignals = e.combatAgent.ScanLong(bullInput)
	}

	// 10b. 涨停池增强：龙头识别 + 涨停分类 + 预期差（并入做多信号流）
	// §E4 修复：此前不受 LongEnabled 门控——关掉做多开关后龙头识别仍发 buy 并进模拟盘建仓。
	// 关闭时整体跳过（含 watch：用户已明确表达不关注做多侧）。
	var gapCodes []string
	for code := range newsBriefs {
		gapCodes = append(gapCodes, code)
	}
	if e.LongEnabled() {
		limitSignals := e.combatAgent.ScanLimitUp(combat_agent.ScanInput{
			LimitUpPool:      pool,
			IndividualStocks: gapCodes,
			MarketData:       sr.MarketData,
			L1Blocked:        sr.L1Blocked,
			News:             newsBriefs,
			Scores:           stockScores,
			EmotionPhase:     emotionPhase,
			PE:               peScores,
		})
		bullSignals = append(bullSignals, limitSignals...)
	}

	// 11. 利空开关开 → 做空分支
	var bearSignals []combat_agent.Signal
	if e.ShortEnabled() {
		bearInput := combat_agent.ScanInput{
			Sectors:      verifiedBear,
			L1Score:      sr.L1Score,
			L1Blocked:    sr.L1Blocked,
			MarketData:   sr.MarketData,
			D1Scores:     d1Scores,
			PE:           peScores,
			LimitUpPool:  pool,
			News:         newsBriefs,
			Scores:       stockScores,
			EmotionPhase: emotionPhase,
		}
		bearSignals = e.combatAgent.ScanShort(bearInput)
	}

	// 12. 个股直入（跳过板块验证）：分做多/做空两组
	// 先收集本轮的个股事件候选（来自新闻事件的 stage2 个股），再与已跟踪个股/持仓/自选合并
	var newLong, newShort []string
	for _, st := range sr.LongStocks {
		newLong = append(newLong, st.Code)
	}
	for _, st := range sr.ShortStocks {
		newShort = append(newShort, st.Code)
	}

	// 取当日仍在跟踪期内的个股池（按方向区分做多/做空）
	var trackedLong, trackedShort []*data.TrackedStock
	// §P1-B nil stockTracker 防御：未注入跟踪池时按空池处理，避免 panic。
	if e.stockTracker != nil {
		trackedLong = e.stockTracker.GetActiveByDirection(td, "利好")
		trackedShort = e.stockTracker.GetActiveByDirection(td, "利空")
	}

	// 8a/8b 个股监测池 = 新闻个股 + 已跟踪个股 + 持仓 + 自选（去重）
	longCodes := mergeCodes(trackedCodes(trackedLong), newLong, positions, watchlist)
	shortCodes := mergeCodes(trackedCodes(trackedShort), newShort, positions, watchlist)

	// 分别对做多/做空监测池执行战法扫描（D1 评分复用，避免重复调 LLM）
	var individualSignals []combat_agent.Signal
	if len(longCodes) > 0 && e.LongEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: longCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
			PE:               peScores,
			Scores:           stockScores,
			EmotionPhase:     emotionPhase,
		}
		individualSignals = append(individualSignals, e.combatAgent.ScanLong(in)...)
	}
	if len(shortCodes) > 0 && e.ShortEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: shortCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
			PE:               peScores,
			Scores:           stockScores,
			EmotionPhase:     emotionPhase,
		}
		individualSignals = append(individualSignals, e.combatAgent.ScanShort(in)...)
	}

	// 将本轮的个股信号写入跟踪池：有效期至下一交易日（到期后自动移出监测池）
	_stepTracker := time.Now()
	if e.stockTracker != nil {
		expiry := data.AddTradingDays(td, 1)
		for _, sig := range individualSignals {
			// 按信号方向映射为跟踪池的 利好/利空 标记
			dir := "利好"
			if sig.Direction == "做空" {
				dir = "利空"
			}
			e.stockTracker.Add(sig.Code, sig.Name, dir, sig.Reason, td, expiry)
		}

		// 收拢本轮全部有信号的个股代码，通知跟踪池做当日轮次收尾（失效未命中的个股）
		allSigCodes := make([]string, len(individualSignals))
		for i, sig := range individualSignals {
			allSigCodes[i] = sig.Code
		}
		e.stockTracker.OnCycleDone(td, allSigCodes)
	}

	// 个股信号并入做多信号流统一展示/广播
	bullSignals = append(bullSignals, individualSignals...)

	// 午休(11:30-13:00)行情冻结，压制做多/做空买卖信号（与近实时循环一致）：只保留
	// 止盈/止损/卖点/情绪退潮等提醒信号，不发布新的买入/watch/watch 战法信号。
	// prevPass 由 filterTransitionSignals 维护，13:00 开盘后首个 Pass 仍会正常翻转。
	// English: quotes freeze at lunch (11:30-13:00), so suppress long/short trade signals exactly like the
	// near-realtime loop — keep take-profit/stop-loss/sell-point/emotion-retreat reminders, but emit no new
	// buy/watch strategy signals. prevPass is maintained by filterTransitionSignals, so the first Pass
	// after 13:00 re-flips normally.
	if data.IsPreAfternoon(time.Now()) {
		bullSignals = nil
		bearSignals = nil
	}
	// 开盘(9:30)前同样压制做多/做空战法信号：盘前无实盘成交量/最新价，
	// 动量/量价齐升等易基于昨日存量 K 线误报（与近实时循环 BeforeOpenTrade 同口径）。
	// 新闻归因/D1 评分仍正常推进，只是不对外发布买入/watch 信号。
	// English: also suppress long/short strategy signals before the 9:30 open — pre-open there is no live
	// volume or latest price, so momentum/volume-price strategies would false-fire on stale daily bars
	// (same gate as the near-realtime loop's BeforeOpenTrade). News attribution / D1 scoring still run;
	// only buy/watch signals are held back.
	if data.BeforeOpenTrade(e.nowTime()) {
		bullSignals = nil
		bearSignals = nil
	}

	// 主循环把可交易 buy 信号送入模拟盘撮合（龙头识别/涨停增强等仅在主循环产生的信号，
	// 近实时循环的 ScorePool 不含它们；watch/提醒不撮合，只做翻转去重防重复买入）。
	// English: the main loop feeds its tradeable buy signals into the paper fill (leader-ID / limit-up
	// enhancements only exist here — the near-realtime ScorePool excludes them); watch/alert signals are
	// skipped, and only non-Pass→Pass flips are emitted to avoid repeat buys.
	{
		var buys []combat_agent.Signal
		for _, sig := range bullSignals {
			if sig.Action == "buy" {
				buys = append(buys, sig)
			}
		}
		if len(buys) > 0 {
			e.mu.RLock()
			prev := e.prevBullBuy
			e.mu.RUnlock()
			emit, next := filterTransitionSignals(buys, prev)
			e.mu.Lock()
			e.prevBullBuy = next
			e.mu.Unlock()
			if len(emit) > 0 {
				e.paperSignals(emit, e.snapshotQuotes())
			}
		}
	}

	// 10-12 出信号结束
	_signalsT := time.Since(_stepSignals)
	// 12b 跟踪池收尾结束
	_trackerT := time.Since(_stepTracker)

	// 13. 持仓止盈/止损提醒（传入当轮打分表 + 利空板块信号：有反向信号才硬推止盈/止损）
	// §R4-6：行情优先走 5s 快照（批量），缺失持仓才逐票兜底单查。
	_stepAlerts := time.Now()
	alertSignals := e.combatAgent.CheckPositionAlerts(e.rpt, e.marketAPI, e.snapshotQuotes(), stockScores, bearHitCodes(sr))
	_alertsT := time.Since(_stepAlerts)

	// 13b. 战法退出引擎实时评估（移动止盈/分批止盈/尾盘强平/破位/超期 等卖点提醒，仅提醒不自动执行）。
	// 行情与日K复用本轮打分池数据（sr.MarketData），持仓由 HeldPositions 覆盖。
	// English: live wiring of the strategy exit engines (trailing stop / staged take-profit / intraday
	// close / breakdown / timeout…), reminder-only; reuses this round's scoring-pool quotes and daily bars.
	exitQuotes := make(map[string]*data.StockInfo, len(sr.MarketData))
	exitDayK := make(map[string][]data.KLine, len(sr.MarketData))
	for code, md := range sr.MarketData {
		if md == nil {
			continue
		}
		if md.Quote != nil && md.Quote.Price > 0 {
			exitQuotes[code] = md.Quote
		}
		if len(md.KLines) > 0 {
			exitDayK[code] = md.KLines
		}
	}
	alertSignals = append(alertSignals, e.combatAgent.CheckPositionsExits(e.rpt, exitQuotes, exitDayK, time.Now())...)

	// 13c. 情绪退潮/背离 → 对做多持仓整体减仓建议（仅提醒）。
	// English: when the emotion cycle turns to retreat/divergence, advise trimming all long positions.
	alertSignals = append(alertSignals, e.combatAgent.EmotionRetreatAlerts(e.rpt, exitQuotes, emotionPhase, time.Now())...)

	// 13c'. 利空归因持仓抛售提醒（E4）：做多持仓命中利空板块/利空个股 → 独立于价格止损提醒尽快抛售。
	// 使用 bearHitReasons 提供归因说明（板块名/上榜原因/关联新闻），让用户理解为何抛售。
	// English: E4 bearish-attribution sell alerts — long holdings hit by bearish sectors/stocks get an
	// independent "sell soon" reminder decoupled from price stops, with an attribution reason (sector
	// name / listing reason / linked news) explaining why.
	alertSignals = append(alertSignals, e.combatAgent.BearishAttributionAlerts(e.rpt, exitQuotes, bearHitReasons(sr), time.Now())...)

	// 13d. 逐股卖点评估：对打分池全量个股（含未持仓的自选/跟踪股）评估利空D1/破位/派发/动量衰竭，
	// 命中即产出"卖点"提醒（仅提醒不自动执行）；消息中心按 code@卖点评估 稳定键去重，5s 循环同键刷新。
	// 仅做多（shortEnabled=false）时非持仓个股不评估、不发减仓/清仓提醒（非持仓无从减仓，纯噪音）；
	// 做多+做空（shortEnabled=true）时评估全打分池，级别徽标按卖出方向显示为"做空"。
	// English: per-stock sell-point assessment over the whole scoring pool (including watchlist/tracked
	// stocks not yet held) — bearish D1 / breakdown / distribution / momentum-exhaustion; reminder-only,
	// deduped in the message center by code@卖点评估, refreshed by the 5s loop on the same key. In
	// long-only mode non-held codes are skipped (no point trimming what you don't hold); in long+short
	// mode the whole pool is assessed and the level badge reads 做空 (sell direction).
	if len(sr.ScoringPool) > 0 {
		sellCodes := sr.ScoringPool
		if !e.ShortEnabled() {
			// §P1-2 多账号隔离：仅本账号持仓参与卖出侧评估（共享引擎 userID 为空时回退全局）。
			sellCodes = e.rpt.HeldPositionCodesFor(e.userID)
			if e.userID == "" {
				sellCodes = e.rpt.HeldPositionCodes()
			}
		}
		alertSignals = append(alertSignals, e.combatAgent.AssessSellSide(sellCodes, sr.MarketData, d1Scores, stockScores, e.ShortEnabled())...)
	}

	// 13e 卖出提醒自动执行（阶段1.1 全自动卖出）：把本轮 清仓/减仓/硬止盈/硬止损 告警
	// （combat_agent.SellAction 归一为 close/trim）送入模拟盘自动成交——清仓类全平、
	// 减仓类半仓（paper 引擎内每码每日一次去重）。仅提醒级（提示/关注/跌幅提醒）不动作。
	// 行情复用 exitQuotes（本轮打分池实时快照）。
	// English: auto-execute sell alerts (full-auto selling) — this round's 清仓/减仓/hard-TP/hard-SL
	// alerts (normalized to close/trim by combat_agent.SellAction) go straight into the paper engine:
	// close-type exits fully, trim-type halves (deduped once per code per day inside paper). Reminder-only
	// levels (提示/关注/跌幅提醒) never act. Quotes reuse exitQuotes (this round's live snapshot).
	{
		var sells []combat_agent.Signal
		for _, s := range alertSignals {
			if combat_agent.SellAction(s) != "" {
				sells = append(sells, s)
			}
		}
		if len(sells) > 0 {
			e.paperSignals(sells, exitQuotes)
		}
	}

	// 14. 聚合器更新看板
	_stepAgg := time.Now()
	// 本轮全部展示信号并入 5s 实时监控池，保证展示接口的现价/涨跌幅真实（信号股不在自选池时也能读到行情）
	e.syncSignalPool(bullSignals, bearSignals, alertSignals)
	// 固化当日信号：本轮做多/做空 Pass 信号按 code@strategy 覆盖写盘
	// English: pin today's signals — this round's long/short Passed signals overwrite the store per code@strategy.
	// 先为做多/做空信号补全真实 D1 事件信息（评分/负面拦截/LLM理由/事件标题），随信号一并固化展示
	// English: backfill real D1 event info onto long/short signals first so it rides along when pinned/displayed.
	enrichSignalsWithD1(bullSignals, d1Scores, newsBriefs)
	enrichSignalsWithD1(bearSignals, d1Scores, newsBriefs)
	if e.signalStore != nil {
		tradeSignals := append([]combat_agent.Signal{}, bullSignals...)
		tradeSignals = append(tradeSignals, bearSignals...)
		e.signalStore.Upsert(tradeSignals)
	}
	// 展示信号 = 当日固化信号 + 本轮信号（固化信号未被新一轮评分替换前持续显示）
	// English: displayed signals = pinned day signals + current round, so pinned signals stay visible
	// all day until replaced by a newer score.
	e.agg.Update(sr, verifiedBull, verifiedBear,
		mergeSignals(bullSignals, e.signalStore.List()), bearSignals, alertSignals, stockScores, e.rpt)
	// 14 看板更新结束
	_aggT := time.Since(_stepAgg)

	// 14b. 信号产生日志：逐条输出本轮生成的做多/做空/提醒信号（带日期时间戳，便于排障）
	for _, sig := range bullSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}
	for _, sig := range bearSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}
	for _, sig := range alertSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}

	// 8a/8b 打分持久化（与近实时循环同口径，当日最新分）
	e.scoreStore.Save(td, stockScores)

	// 14b. 交易信号/告警/持仓提示合并进消息中心（持久化），带 5s 快照行情（现价+涨跌幅）
	e.syncMessages(bullSignals, bearSignals, alertSignals, sr, e.snapshotQuotes())

	// 15. 调试数据
	e.captureDebug(rawNews, st0, events)

	// 15b. 信号批次快照：收拢本轮全部信号（做多/做空/提醒）供"信号日志"弹窗按批次复盘
	allSignals := make([]combat_agent.Signal, 0, len(bullSignals)+len(bearSignals)+len(alertSignals))
	allSignals = append(allSignals, bullSignals...)
	allSignals = append(allSignals, bearSignals...)
	allSignals = append(allSignals, alertSignals...)
	e.captureSignalRecords(len(rawNews), allSignals)

	// 16. SSE 广播通知前端（附信号摘要）
	// bull/bear 只统计可操作买入（Action=="buy"）信号：watch/brief 观察信号仍进消息中心与信号列表，
	// 但不计入浏览器通知数量，避免观察类信号频繁弹系统通知。
	// English: bull/bear count only actionable buy (Action=="buy") signals — watch/brief observations still
	// land in the message center and signal list, but aren't counted for browser notifications, so
	// watch-only signals don't spam the system notification.
	_stepSSE := time.Now()
	if e.sse != nil && e.sse.Len() > 0 {
		payload := map[string]string{
			"type":   "scan",
			"status": "done",
			"bull":   fmt.Sprintf("%d", countAction(bullSignals, "buy")),
			"bear":   fmt.Sprintf("%d", countAction(bearSignals, "buy")),
			"alert":  fmt.Sprintf("%d", len(alertSignals)),
			"time":   time.Now().Format("15:04:05"),
		}
		if pool != nil {
			payload["zt_pool"] = fmt.Sprintf("%d", len(pool))
		}
		if emotionCfg != nil { // §E2 复用本轮 RLock 快照
			payload["emotion"] = data.DetectEmotionPhaseV2(pool, 0, 0, emotionCfg)
		}
		e.sse.Broadcast(payload)
	}
	_sseT := time.Since(_stepSSE)

	// 记录本轮分段耗时（供 e2e 实速模拟 + /api/debug 观测）
	e.mu.Lock()
	e.lastTiming = &RunTiming{
		Total:       time.Since(t0),
		News:        pOut.timing,
		Evaluate:    _evalT,
		HotRec:      _hotRecT,
		D1:          _d1T,
		PE:          _peT,
		Pool:        _poolT,
		Verify:      _verifyT,
		HotPool:     _hotPoolT,
		MergeSector: _mergeT,
		Signals:     _signalsT,
		Tracker:     _trackerT,
		Alerts:      _alertsT,
		Agg:         _aggT,
		SSE:         _sseT,
	}
	e.mu.Unlock()

	log.Printf("[engine] 流水线完成: %d条原始 → %d事件 → %d有效 (%v)",
		len(rawNews), len(events), len(valid), time.Since(t0))

	// 本轮无 LLM 分析原因的显式提示：新闻有但未产出有效事件时，一眼定位是"LLM失败"还是"被阈值过滤"
	if len(rawNews) > 0 {
		if st0.Err != nil {
			log.Printf("[engine] 本轮无LLM分析原因: Stage0失败(%v), 原始%d条全部归一般(仅展示)", st0.Err, len(rawNews))
		} else if len(events) == 0 {
			log.Printf("[engine] 本轮无LLM分析原因: Stage0成功但无事件产出(个股/板块/IPO来源全空, 或全被material初筛过滤)")
		} else if len(valid) == 0 {
			log.Printf("[engine] 本轮无有效事件原因: 共%d事件但均|score|<0.50 被阈值过滤", len(events))
		}
	}

	return sr
}

// ReanalyzeNews 手动 LLM 补推：强制重新拉取最近新闻（忽略 tracker 去重），
// 走 Stage0 归因分类 + Stage2 深度分析，落盘事件，供前端"补推"按钮触发。
// 适用场景：早盘/盘中 LLM 上游抖动导致 Stage0 失败、整批新闻被归"一般"未进 LLM。
// 返回统计（原始条数/个股/板块/IPO/一般/事件数），以便前端提示结果。
func (e *Engine) ReanalyzeNews() (map[string]int, error) {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return nil, fmt.Errorf("新闻代理未启动")
	}

	// 强制拉取（忽略历史去重，仅按本轮打重）
	raw := na.FetchForce()
	if len(raw) == 0 {
		log.Printf("[engine] 补推: 未拉到新闻")
		return map[string]int{"raw": 0}, nil
	}
	log.Printf("[engine] 补推: 强制拉取 %d 条新闻", len(raw))

	// Stage0 归因分类（失败时整批归一般并保留错误原因）
	st0 := na.Stage0(raw)
	if st0.Err != nil {
		log.Printf("[engine] 补推: Stage0失败, %d条归一般: %v", len(raw), st0.Err)
	}

	// 标题党校正
	for i, t := range st0.CorrectedTitle {
		if i >= 0 && i < len(raw) && t != "" {
			raw[i].Title = t
		}
	}

	// 收集事件：个股 → Stage2；板块 → material 初筛后 Stage2；IPO → 直构
	var events []newsagent.NewsEvent
	var failedNews []data.NewsItem
	if len(st0.StockIdx) > 0 {
		evs, failed := na.Stage2(pickItems(raw, st0.StockIdx))
		events = append(events, evs...)
		failedNews = append(failedNews, failed...)
	}
	if len(st0.SectorIdx) > 0 {
		sectorItems := pickItems(raw, st0.SectorIdx)
		var kept []int
		for j := range sectorItems {
			if st0.Material[st0.SectorIdx[j]] {
				kept = append(kept, j)
			}
		}
		if len(kept) > 0 {
			evs, failed := na.Stage2(pickItems(sectorItems, kept))
			events = append(events, evs...)
			failedNews = append(failedNews, failed...)
		}
	}
	if len(st0.IpoIdx) > 0 {
		events = append(events, na.BuildIPOFeedEvents(pickItems(raw, st0.IpoIdx))...)
	}
	events = append(events, na.BuildIPOEvents()...)

	// 落盘事件（供 /api/news 展示）
	na.SaveEvents(events)

	stat := map[string]int{
		"raw":     len(raw),
		"stock":   len(st0.StockIdx),
		"sector":  len(st0.SectorIdx),
		"ipo":     len(st0.IpoIdx),
		"general": len(st0.GeneralIdx),
		"events":  len(events),
		"failed":  len(failedNews),
	}
	log.Printf("[engine] 补推完成: 原始%d 个股%d 板块%d IPO%d 一般%d 事件%d 未归因%d (err=%v)",
		stat["raw"], stat["stock"], stat["sector"], stat["ipo"], stat["general"], stat["events"], stat["failed"], st0.Err)
	return stat, nil
}

// TestAttribution 单条归因测试：传一条标题+正文摘要，直接走 Stage2 LLM 深度分析
// （含产业链价值传导推导 + 差分事件拆分），返回拆分后的 NewsEvent 供快速验证归因逻辑。
// 用于 B2 单条验证：如"诺基亚收购恩智浦一工厂 计划自产磷化铟半导体"应归因上游磷化铟厂商。
func (e *Engine) TestAttribution(title, digest string) ([]newsagent.NewsEvent, error) {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return nil, fmt.Errorf("新闻代理未启动")
	}
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("标题不能为空")
	}
	item := data.NewsItem{
		Title:    title,
		Content:  digest,
		Source:   "测试",
		Datetime: time.Now().Format("2006-01-02 15:04:05"),
	}
	events, _ := na.Stage2([]data.NewsItem{item})
	if len(events) == 0 {
		log.Printf("[engine] 单条归因测试: 无事件产出 (title=%s)", title[:min(len(title), 40)])
	}
	return events, nil
}

// refreshSectors 每轮拉取同花顺全量板块名单并喂给 scanner，保证 FindSectorsByNames 可命中真实板块。
func (e *Engine) refreshSectors() {
	e.mu.RLock()
	scanner, ths := e.scanner, e.ths
	e.mu.RUnlock()
	if scanner == nil || ths == nil {
		return
	}
	boards, err := ths.GetBoardList()
	if err != nil {
		log.Printf("[engine] 同花顺板块名单刷新失败: %v", err)
		return
	}
	scanner.Update(boards, 0, 0, 0)
	e.feedRPS(boards)
	log.Printf("[engine] 板块名单刷新: %d 个 (一级行业+概念)", len(boards))
}

// feedRPS §修复 D3（2026-08-29）：按多个可得周期构造 RPS 近似值（此前 RPS20/RPS60 用同一
// 单日涨幅，等价无效）。可用字段：当日涨跌幅 ChangePct（短周期≈RPS20）、两日涨幅 Gain2d
// （中周期≈RPS60）。真实多周期（5/10/20/60/120/250）百分位仍需历史 K 线（Phase F 升级），
// 但至少让两个维度反映不同时间窗口，支撑 RPSRank 排序去伪。
func (e *Engine) feedRPS(boards []data.SectorInfo) {
	e.mu.RLock()
	sa := e.sectorAgent
	e.mu.RUnlock()
	if sa == nil || len(boards) == 0 {
		return
	}
	// br 板块行情行：代码 + 名称 + 当日/次日涨跌幅。
	type br struct {
		code, name string
		d1, d2     float64
	}
	rows := make([]br, 0, len(boards))
	for _, b := range boards {
		if b.Name == "" {
			continue
		}
		rows = append(rows, br{code: b.Code, name: b.Name, d1: b.ChangePct, d2: b.Gain2d})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].d1 > rows[j].d1 })
	rps := make([]data.SectorRPS, 0, len(rows))
	for i, r := range rows {
		rank20 := 0.0
		if len(rows) > 1 {
			// 按当日涨幅排名线性映射 RPS20 近似值（第一名≈100，最后一名≈0）
			rank20 = 100 * (1 - float64(i)/float64(len(rows)-1))
		}
		// 中周期：用两日涨幅 Gain2d 重新排名（若全 0 退化为单日涨幅）
		rank60 := rank20
		if rows[0].d2 != 0 || rows[len(rows)-1].d2 != 0 {
			// 显式按 Gain2d 排名（避免与上面 d1 排序混淆）
			order := make([]int, len(rows))
			for k := range order {
				order[k] = k
			}
			sort.SliceStable(order, func(x, y int) bool { return rows[order[x]].d2 > rows[order[y]].d2 })
			pos := 0
			for k, idx := range order {
				if rows[idx].code == r.code {
					pos = k
					break
				}
			}
			if len(rows) > 1 {
				rank60 = 100 * (1 - float64(pos)/float64(len(rows)-1))
			}
		}
		rps = append(rps, data.SectorRPS{
			Code:  r.code,
			Name:  r.name,
			RPS20: rank20,
			RPS60: rank60,
		})
	}
	sa.FeedRPS(rps)
}

// verifySectorAttribution 板块验真回填：对 level=板块 且 |score|≥0.5 的事件，
// 用真实板块名单（FindSectorsByNames 精确命中）校验 Sectors，剔除 LLM 幻觉板块。
func (e *Engine) verifySectorAttribution(events []newsagent.NewsEvent) {
	e.mu.RLock()
	scanner := e.scanner
	e.mu.RUnlock()
	if scanner == nil {
		return
	}
	removed := 0
	for i := range events {
		ev := &events[i]
		if ev.Level != "板块" || absScore(ev.Score) < 0.5 || len(ev.Sectors) == 0 {
			continue
		}
		kept := make([]string, 0, len(ev.Sectors))
		for _, s := range ev.Sectors {
			if len(scanner.FindSectorsByNames([]string{s})) > 0 {
				kept = append(kept, s)
			}
		}
		if len(kept) != len(ev.Sectors) {
			removed += len(ev.Sectors) - len(kept)
			ev.Sectors = kept
		}
	}
	if removed > 0 {
		log.Printf("[engine] 板块验真回填: 剔除 %d 个非真实板块名", removed)
	}
}

// propagateSectorToStocks 板块→个股事件级传播：对命中真实板块的板块级事件，
// 取板块前 N 成分股注入 RelatedStocks（"名称(代码)"），并同步清洗 CleanedStocks，
// 使板块权重沿 事件→个股监测池(8a/8b) 传递。同一板块每轮只取一次成分股。
// N 由 sectorConstTopN（默认 20）决定，扩大覆盖以纳入更多同板块强势股。
// 遍历范围含直接板块 + 上游 + 下游板块，补足 LLM 未点名的产业链个股。
func (e *Engine) propagateSectorToStocks(events []newsagent.NewsEvent) {
	e.mu.RLock()
	scanner := e.scanner
	topN := e.sectorConstTopN
	e.mu.RUnlock()
	if scanner == nil {
		return
	}
	if topN <= 0 {
		topN = 20
	}

	// 第一遍：仅收集需要拉成分股的板块（串行、无网络 IO），按所属事件下标分组。
	// 同一板块每轮只取一次成分股，避免重复注入。
	// （Pass 1: collect which sectors need constituents, grouped by owning event index,
	// serially and without any network I/O. Each sector is fetched once per round.）
	type needFetch struct {
		evIdx int    // 所属事件下标（events 切片）
		code  string // 板块代码
		name  string // 板块名（日志用）
	}
	fetched := make(map[string]bool)
	var needs []needFetch
	for i := range events {
		ev := &events[i]
		if ev.Level != "板块" || absScore(ev.Score) < 0.5 {
			continue
		}
		// 汇总直接板块 + 上游 + 下游板块一并传播
		allSectors := append([]string{}, ev.Sectors...)
		allSectors = append(allSectors, ev.UpstreamSectors...)
		allSectors = append(allSectors, ev.DownstreamSectors...)
		for _, name := range allSectors {
			si := e.sectorByName(name)
			if si == nil {
				continue
			}
			if fetched[si.Code] {
				continue
			}
			fetched[si.Code] = true
			needs = append(needs, needFetch{evIdx: i, code: si.Code, name: name})
		}
	}
	if len(needs) == 0 {
		return
	}

	// 第二遍：并发拉取成分股（有界 worker 池）。实际并发受各数据源限流器约束，
	// 不会打爆同花顺/东财；串行 N 板块 O(N×T) 因此压到 ~O(T)。
	// （Pass 2: fetch constituents concurrently with a bounded worker pool. Real concurrency is
	// capped by the per-source rate limiters, so THS/EastMoney are never hammered; the serial
	// O(N×T) cost over N sectors drops to ~O(T).）
	type result struct {
		code   string
		stocks []data.StockInfo
		err    error
	}
	// sectorFetchWorkers 板块行情并发拉取的工作协程数。
	const sectorFetchWorkers = 6
	jobs := make(chan needFetch)
	results := make(chan result, len(needs))
	var wg sync.WaitGroup
	for w := 0; w < sectorFetchWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for nf := range jobs {
				stocks, err := e.sectorConstituents(nf.code, topN)
				results <- result{code: nf.code, stocks: stocks, err: err}
			}
		}()
	}
	for _, nf := range needs {
		jobs <- nf
	}
	close(jobs)
	wg.Wait()
	close(results)

	// 第三遍：按板块代码暂存结果，再串行注入各自事件（避免并发写 events 切片）。
	// （Pass 3: stage results by sector code, then inject serially to avoid racing on events.）
	byCode := make(map[string][]data.StockInfo, len(needs))
	for r := range results {
		if r.err != nil {
			log.Printf("[engine] 板块成分股获取失败 %s: %v", r.code, r.err)
			continue
		}
		byCode[r.code] = r.stocks
	}
	injected := 0
	for _, nf := range needs {
		ev := &events[nf.evIdx]
		stocks, ok := byCode[nf.code]
		if !ok {
			continue
		}
		added := 0
		for _, st := range stocks {
			label := fmt.Sprintf("%s(%s)", st.Name, st.Code)
			if strContains(ev.RelatedStocks, label) || strContains(ev.RelatedStocks, st.Name) {
				continue // 已注入过同名/同标签则跳过
			}
			ev.RelatedStocks = append(ev.RelatedStocks, label)
			added++
		}
		injected += added
		if added > 0 || len(ev.RelatedStocks) > 0 {
			ev.CleanedStocks = e.newsAgent.CleanStocks(ev.RelatedStocks)
		}
	}
	if injected > 0 {
		log.Printf("[engine] 板块→个股传播: 注入 %d 只成分股", injected)
	}
}

// sectorConstituents 获取板块成分股：同花顺优先（东财限流时的兜底源），失败/为空回退东财。
// THS 板块代码（881xxx/308xxx）与扫描器缓存一致，可直接调 GetBoardStocks。
// （sectorConstituents returns a sector's constituents: THS first (fallback source when
// EastMoney is throttled), EastMoney otherwise. THS board codes match the scanner cache.）
func (e *Engine) sectorConstituents(sectorCode string, topN int) ([]data.StockInfo, error) {
	e.mu.RLock()
	ths := e.ths
	marketAPI := e.marketAPI
	e.mu.RUnlock()
	if ths != nil {
		if list, err := ths.GetBoardStocks(sectorCode, topN); err == nil && len(list) > 0 {
			return list, nil
		}
	}
	return marketAPI.GetSectorStocks(sectorCode, topN)
}

// sectorByName 精确匹配板块名称返回 SectorInfo，未命中返回 nil。
func (e *Engine) sectorByName(name string) *data.SectorInfo {
	if e.scanner == nil {
		return nil
	}
	// FindSectorsByNames 已含精确+包含匹配，直接取首个命中（板块名噪声也能落到真实板块）
	infos := e.scanner.FindSectorsByNames([]string{name})
	if len(infos) > 0 {
		return &infos[0]
	}
	return nil
}

// absScore 返回带符号分数的绝对值。
func absScore(s float64) float64 {
	if s < 0 {
		return -s
	}
	return s
}

// strContains 判断字符串切片中是否包含指定元素。
func strContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// captureDebug 收集本轮流水线调试数据，并固化到当日 Stage 记录。
func (e *Engine) captureDebug(rawNews []data.NewsItem, st0 newsagent.Stage0Result, events []newsagent.NewsEvent) {
	titles := make([]string, len(rawNews))
	for i, n := range rawNews {
		titles[i] = n.Title
	}
	idx := append([]int{}, st0.StockIdx...)
	idx = append(idx, st0.SectorIdx...)
	idx = append(idx, st0.IpoIdx...)

	e.mu.Lock()
	e.debugInfo = &newsagent.DebugInfo{
		Stage1Mode:    "combined",
		RawCount:      len(rawNews),
		SelectedCount: len(idx),
		RawTitles:     titles,
		SelectedIdx:   idx,
		Stage2Events:  events,
		ProcessTime:   time.Now(),
	}
	e.stageRecords = append(e.stageRecords, *e.debugInfo)
	if len(e.stageRecords) > 20 {
		e.stageRecords = e.stageRecords[len(e.stageRecords)-20:]
	}
	e.mu.Unlock()

	e.persistStageRecords()
}

// captureNearRealtimeStage §LLM 面板修复：近实时打分循环每轮也会触发一次 Stage 快照，
// 让 LLM 诊断页主面板在主循环空闲/无 L2 新闻时也能看到"原始新闻缓存"（待归因队列 + 已归因事件）。
// 节流：距上次捕获 ≥ 60s 才落盘，避免每 5s 一次磁盘写入（低配服务器友好）。
// English: near-realtime stage snapshot — the 5s scoring loop also emits a Stage record so the LLM
// debug main panel always has a raw-news cache (pending queue + attributed events) even when the main
// loop is idle or no L2 news arrived. Throttled to ≥ 60s between writes to avoid per-5s disk I/O.
func (e *Engine) captureNearRealtimeStage() {
	if e.newsAgent == nil {
		return
	}
	e.mu.RLock()
	last := e.lastStageCap
	e.mu.RUnlock()
	if !last.IsZero() && time.Since(last) < 60*time.Second {
		return
	}
	pending := e.newsAgent.UnattributedItems()
	events := e.newsAgent.AllEvents()
	titles := make([]string, 0, len(pending))
	for _, it := range pending {
		if it.Title != "" {
			titles = append(titles, it.Title)
		}
	}
	e.mu.Lock()
	e.lastStageCap = time.Now()
	e.debugInfo = &newsagent.DebugInfo{
		Stage1Mode:    "combined",
		RawCount:      len(titles),
		SelectedCount: 0,
		RawTitles:     titles,
		Stage2Events:  events,
		ProcessTime:   time.Now(),
	}
	e.stageRecords = append(e.stageRecords, *e.debugInfo)
	if len(e.stageRecords) > 20 {
		e.stageRecords = e.stageRecords[len(e.stageRecords)-20:]
	}
	e.mu.Unlock()
	e.persistStageRecords()
}

// pickItems 按索引从新闻列表中选取条目。
func pickItems(items []data.NewsItem, indices []int) []data.NewsItem {
	var out []data.NewsItem
	for _, i := range indices {
		if i >= 0 && i < len(items) {
			out = append(out, items[i])
		}
	}
	return out
}

// filterThreshold 过滤事件：仅保留 |score| ≥ 阈值的（弱/中性丢弃）。
func filterThreshold(events []newsagent.NewsEvent, threshold float64) []newsagent.NewsEvent {
	var out []newsagent.NewsEvent
	for _, ev := range events {
		s := ev.Score
		if s < 0 {
			s = -s
		}
		if s >= threshold {
			out = append(out, ev)
		}
	}
	return out
}

// trackedCodes 提取跟踪个股代码列表。
func trackedCodes(tracked []*data.TrackedStock) []string {
	out := make([]string, 0, len(tracked))
	for _, s := range tracked {
		out = append(out, s.Code)
	}
	return out
}

// mergeCodes 按顺序合并多组个股代码并去重（保留首次出现顺序）。
func mergeCodes(groups ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, g := range groups {
		for _, c := range g {
			c = strings.TrimSpace(c)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// newsBriefsByCode 将有效新闻事件转为 code → 新闻简报映射（供预期差检测）。
// 方向由事件 Score 符号推导（score≥0 视为利好）。
func newsBriefsByCode(events []newsagent.NewsEvent) map[string][]combat_agent.NewsBrief {
	m := make(map[string][]combat_agent.NewsBrief)
	codeCandidates := func(ev newsagent.NewsEvent) []string {
		codes := make([]string, 0, len(ev.RelatedStocks)+len(ev.CleanedStocks))
		// RelatedStocks 可能是 "名称"、"名称(代码)" 或裸代码；CleanedStocks 统一为 "名称|代码"。
		// 优先用 CleanedStocks（格式最规范），再补 RelatedStocks，去重。
		// English: RelatedStocks may hold names, "name(code)" or bare codes; CleanedStocks is uniformly
		// "name|code". Prefer CleanedStocks (most normalized), then RelatedStocks, deduped.
		seen := make(map[string]bool)
		clean := func(raw string) string {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return ""
			}
			if _, after, ok := strings.Cut(raw, "|"); ok {
				raw = strings.TrimSpace(after)
			} else if i := strings.Index(raw, "("); i > 0 {
				raw = strings.TrimSpace(strings.TrimSuffix(raw[i+1:], ")"))
			}
			raw = strings.TrimSpace(raw)
			// 仅接受可判定为代码的条目：6 位数字，或含字母的港美股/指数代码（如 0700.HK）。
			// 纯名称（如 "中兴商业"）无法映射到信号，跳过以免脏关联。
			// English: only accept entries resolvable to codes — 6-digit A-share codes, or alphanumeric
			// HK/US/index codes (e.g. 0700.HK). Bare names (e.g. "中兴商业") can't map to signals; skip.
			digits := 0
			alnum := 0
			for _, r := range raw {
				switch {
				case r >= '0' && r <= '9':
					digits++
					alnum++
				case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
					alnum++
				}
			}
			if alnum == 0 {
				return ""
			}
			if digits == 6 && alnum == 6 {
				return raw
			}
			if digits > 0 && alnum > digits {
				return raw // 含字母的代码（港美股/带后缀）
			}
			return ""
		}
		for _, cs := range ev.CleanedStocks {
			if c := clean(cs); c != "" && !seen[c] {
				seen[c] = true
				codes = append(codes, c)
			}
		}
		for _, rs := range ev.RelatedStocks {
			if c := clean(rs); c != "" && !seen[c] {
				seen[c] = true
				codes = append(codes, c)
			}
		}
		return codes
	}
	for _, ev := range events {
		if ev.Title == "" {
			continue
		}
		positive := ev.Score >= 0
		for _, code := range codeCandidates(ev) {
			m[code] = append(m[code], combat_agent.NewsBrief{
				Title:    ev.Title,
				Positive: positive,
				Time:     ev.Datetime,
			})
		}
	}
	return m
}

// enrichSignalsWithD1 为信号补全真实 D1 事件信息（区别于策略 Reason）：
// 从引擎最近一轮 D1 评分缓存（d1Scores[code].Score/Blocked/Reason）和新闻事件简报
// （newsBriefs[code] 的标题）回填 Signal.D1Score/D1Blocked/D1Reason/D1Event，
// 供前端"信号"列表单独展示 D1 事件分析，而不混入策略信号本身的原因。
// 复用各战法已扫描结果里的 D1 上下文，不额外调 LLM。
// English: backfills real D1 event info onto signals (distinct from the strategy Reason) — takes the
// latest D1 score cache (d1Scores[code].Score/Blocked/Reason) and news briefs (newsBriefs[code] titles)
// into Signal.D1Score/D1Blocked/D1Reason/D1Event, so the frontend signal list can show the D1 event
// analysis separately instead of mixing it into the strategy reason. Reuses existing D1 context; no new LLM call.
func enrichSignalsWithD1(sigs []combat_agent.Signal, d1Scores map[string]combat_agent.D1Score, newsBriefs map[string][]combat_agent.NewsBrief) {
	if len(sigs) == 0 {
		return
	}
	for i := range sigs {
		s := &sigs[i]
		// D1 事件标题（新闻归因）独立于 D1 评分缓存：即使 LLM D1 评分缺失/降级为 0，事件仍应展示。
		// English: the D1 event title (news attribution) is independent of the D1 score cache — even when the
		// LLM D1 score is missing or degraded to 0, the event should still be shown.
		if briefs := newsBriefs[s.Code]; len(briefs) > 0 {
			// 取与信号方向一致的事件标题（利好→做多；利空→做空），无匹配则取首条
			// English: pick a title matching the signal direction (bullish→long; bearish→short),
			// falling back to the first brief when none matches.
			pos := s.Direction == "做多"
			picked := ""
			for _, b := range briefs {
				if b.Positive == pos {
					picked = b.Title
					break
				}
			}
			if picked == "" {
				picked = briefs[0].Title
			}
			s.D1Event = picked
		}
		d1, ok := d1Scores[s.Code]
		if !ok {
			continue
		}
		s.D1Score = d1.Score
		s.D1Blocked = d1.Blocked
		s.D1Reason = d1.Reason
	}
}

// clusterEvents 事件聚簇：同方向且共享任一板块的事件合并为单条。// 簇内标题用" | "连接（最多保留 3 条），个股/相关股票去重合并，Score 取 |score| 最大者。
// 防止同一主题的多条快讯在信号流中刷屏。
func clusterEvents(events []newsagent.NewsEvent) []newsagent.NewsEvent {
	if len(events) < 2 {
		return events
	}
	// 为每个"板块+方向"维护簇索引：同板块不同方向的事件不得合并
	// （对抗制裁型上游利好/下游利空拆分事件方向相反，误合并会吞掉方向）。
	clusterOf := make(map[string]int)
	clusters := make([][]int, 0, len(events))
	assign := func(ev newsagent.NewsEvent) int {
		if ev.Level == "个股" {
			return -1 // 个股级事件不参与聚簇，各自独立
		}
		for _, sec := range ev.Sectors {
			key := sec + "|" + ev.Direction
			if idx, ok := clusterOf[key]; ok {
				return idx // 命中已有同板块同方向簇则归入该簇
			}
		}
		return -1 // 无共享板块，新建独立簇
	}
	for i, ev := range events {
		idx := assign(ev)
		if idx < 0 {
			idx = len(clusters)
			clusters = append(clusters, nil)
		}
		clusters[idx] = append(clusters[idx], i)
		for _, sec := range ev.Sectors {
			clusterOf[sec+"|"+ev.Direction] = idx
		}
	}

	out := make([]newsagent.NewsEvent, 0, len(clusters))
	for _, idxs := range clusters {
		if len(idxs) == 0 {
			continue
		}
		merged := events[idxs[0]]
		if len(idxs) == 1 {
			out = append(out, merged)
			continue
		}
		// 合并标题（最多3条）
		titles := []string{merged.Title}
		for _, i := range idxs[1:] {
			ev := events[i]
			if len(titles) < 3 && ev.Title != "" && !containsStr(titles, ev.Title) {
				titles = append(titles, ev.Title)
			}
			// 保留 |score| 最大的事件属性
			if absScore(ev.Score) > absScore(merged.Score) {
				merged.Score = ev.Score
				merged.Direction = ev.Direction
				merged.Reason = ev.Reason
				merged.EventType = ev.EventType
				merged.Level = ev.Level
			}
			merged.RelatedStocks = mergeStr(merged.RelatedStocks, ev.RelatedStocks)
			merged.CleanedStocks = mergeStr(merged.CleanedStocks, ev.CleanedStocks)
		}
		merged.Title = strings.Join(titles, " | ")
		out = append(out, merged)
	}
	if len(out) != len(events) {
		log.Printf("[engine] 事件聚簇: %d → %d 条", len(events), len(out))
	}
	return out
}

// applyEventDecay 板块事件衰减：同板块同方向事件在 H 小时内重复出现时，
// Score 乘以 0.5^(H/4)（1h→0.84, 2h→0.71, 4h→0.50, 8h→0.25），弱化重复消息。
// §E 修复：map 加锁（结构体注释声明 mu 保护全部可变字段，此字段是历史例外——并发
// 手动触发/测试即 fatal concurrent map read/write）+ 清理 >24h 过期键防慢性泄漏。
func (e *Engine) applyEventDecay(events []newsagent.NewsEvent) {
	now := time.Now()
	for i := range events {
		ev := &events[i]
		if ev.Level == "个股" || len(ev.Sectors) == 0 {
			continue
		}
		key := strings.Join(ev.Sectors, "+") + "|" + ev.Direction
		e.mu.Lock()
		if last, ok := e.sectorEventTimes[key]; ok {
			hours := now.Sub(last).Hours()
			if hours > 0 && hours < 24 {
				ev.Score *= math.Pow(0.5, hours/4)
				log.Printf("[engine] 事件衰减 %s(%s): 距上次%.1fh, score→%.2f", key, ev.Title, hours, ev.Score)
			}
		}
		// 惰性清理：衰减窗口仅 24h，过期条目已无作用
		for k, t := range e.sectorEventTimes {
			if now.Sub(t).Hours() >= 24 {
				delete(e.sectorEventTimes, k)
			}
		}
		e.sectorEventTimes[key] = now
		e.mu.Unlock()
	}
}

// containsStr 判断字符串切片是否包含目标。
func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// mergeStr 合并两个字符串切片（去重，保留顺序）。
func mergeStr(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(a, b...) {
		if s == "" || containsStr(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// bearHitCodes 收拢本轮全部利空标的（利空板块领跌股 + 利空个股），返回 code→true 映射。
// 供持仓利空提醒使用：凡命中该集合的持仓提示卖出。
func bearHitCodes(sr *strategy_engine.StrategyResult) map[string]bool {
	out := make(map[string]bool)
	for _, bs := range sr.BearSectors {
		for _, code := range bs.LeadStocks {
			out[code] = true
		}
	}
	for _, code := range sr.BearStocks {
		out[code] = true
	}
	return out
}

// bearHitReasons 返回 利空个股 → 归因说明 的映射（E4：利空归因持仓抛售提醒用）。
// 说明拼接命中的利空板块名、上榜原因与关联新闻标题，供"利空归因到持仓"时向用户解释为何抛售。
// English: returns a map of bearish stock → attribution reason (E4: bearish-attribution sell alerts).
// The reason concatenates the hit bear sector name, its listing reason and linked news titles so the
// user understands why their holding should be sold.
func bearHitReasons(sr *strategy_engine.StrategyResult) map[string]string {
	out := make(map[string]string)
	for _, bs := range sr.BearSectors {
		desc := bs.Name
		if bs.Reason != "" {
			desc += "(" + bs.Reason + ")"
		}
		if len(bs.NewsTitles) > 0 {
			desc += " 事件:" + strings.Join(bs.NewsTitles, ";")
		}
		for _, code := range bs.LeadStocks {
			if prev, ok := out[code]; ok {
				out[code] = prev + " | " + desc
			} else {
				out[code] = desc
			}
		}
	}
	for _, code := range sr.BearStocks {
		if _, ok := out[code]; !ok {
			out[code] = "利空个股事件"
		}
	}
	return out
}
