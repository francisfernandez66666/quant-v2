// 战法库（因子战法）HTTP 端点：列出已应用战法 + 启用/禁用/删除 + 运行效果监测 + 单条候选全量回测。
// English: strategy-library (factor) HTTP endpoints — list applied strategies, enable/disable/delete,
// live-effect monitoring, and per-candidate full backtest.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy"
)

// handleResearchLibrary 处理 GET /api/research/library：返回战法库全部已应用因子战法
// （含启用状态 + 运行效果统计，效果统计与引擎内存运行值合并）。
// English: GET /api/research/library — returns all applied factor strategies in the library
// (enabled state + run-effect stats merged with the engine's in-memory values).
func (s *Server) handleResearchLibrary(w http.ResponseWriter, r *http.Request) {
	if s.researchDir == "" {
		writeError(w, 503, "研究库未接入")
		return
	}
	entries, err := research.ListAppliedFactorRules(s.researchDir)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 合并引擎内存运行统计（SignalCount/Win/Loss/CumReturn 以引擎为准，文件为落盘备份）
	type libItem struct {
		Kind         string                 `json:"kind"`
		ID           string                 `json:"id"`
		Name         string                 `json:"name"`
		Enabled      bool                   `json:"enabled"`
		CandID       int64                  `json:"candidate_id"`
		AppliedAt    string                 `json:"applied_at"`
		SignalCount  int                    `json:"signal_count"`
		Win          int                    `json:"win"`
		Loss         int                    `json:"loss"`
		CumReturn    float64                `json:"cum_return"`
		Factors      []string               `json:"factors,omitempty"`
		Weights      map[string]float64     `json:"weights,omitempty"`
		Directions   map[string]int         `json:"directions,omitempty"`
		BuyThreshold float64                `json:"buy_threshold,omitempty"`
		Horizon      int                    `json:"horizon,omitempty"`
		IR           float64                `json:"ir,omitempty"`
		Excess       float64                `json:"excess,omitempty"`
		ICMean       float64                `json:"ic_mean,omitempty"`    // 全样本 IC 均值（候选表回填）
		AvgExcess    float64                `json:"avg_excess,omitempty"` // 全链路回测超额（候选表 avg_excess，>0 表示已回测）
		BacktestDone bool                   `json:"backtest_done"`        // 全链路回测是否已跑过（avg_excess 已回填）
		Reason       string                 `json:"reason,omitempty"`     // 候选证据文本（样本内外 IR / 反推超额）
		Conds        []research.PatternCond `json:"conds,omitempty"`
		// §ADJ-BASIS-2 复权口径基线戳 + 失效判定（载入侧算出的派生值，非文件字段）。
		// 前端据此在战法卡片上打「基线口径已失效」红标：这条战法的 weights/buy_threshold 是在
		// §ADJ 修正前的复权面板上拟合的，参数已无成立的历史依据。
		// English: adjustment-basis stamp + staleness verdict; the UI badges entries whose fitted
		// parameters lost their historical basis.
		AdjBasis      string `json:"adj_basis,omitempty"`
		StaleAdjBasis bool   `json:"stale_adj_basis,omitempty"`
		// §EXIT-RETAIN 该战法名下的开放持仓笔数（按持仓 Strategy 匹配规则 ID 或显示名）。
		// 出场参数跟随持仓而非启用开关，所以"停用"只切断新开仓：前端停用确认文案要有这个数，
		// 操作员点下去之前就得看见"这条还压着 N 笔仓"。
		// English: count of open positions owned by this rule (matched by rule id or display name);
		// disabling cuts new buys only, so the confirm dialog can state how many positions remain.
		OpenPositions int `json:"open_positions"`
	}
	var out []libItem
	// §EXIT-RETAIN 每次请求取一份持仓计数（实盘账本 ∪ 各账号模拟盘），因子/形态两张卡共用。
	held := s.openPositionCounts()
	stats := map[string]research.AppliedFactorEntry{}
	for _, e := range entries {
		stats[e.ID] = e
	}
	// 用各实盘控制器上报的因子绩效覆盖统计：信号数/胜平负/累计收益。
	if s.registry != nil {
		for _, c := range s.registry.AllControllers() {
			for _, rl := range c.FactorStats() {
				if e, ok := stats[rl.ID]; ok {
					e.SignalCount, e.Win, e.Loss, e.CumReturn = rl.SignalCount, rl.Win, rl.Loss, rl.CumReturn
					stats[rl.ID] = e
				}
			}
		}
	}
	// 组装因子卡片：合并绩效统计与候选表验证信息。
	for _, e := range stats {
		item := libItem{
			Kind: "factor", ID: e.ID, Name: e.Name, Enabled: e.Enabled, CandID: e.CandID,
			AppliedAt: e.AppliedAt, SignalCount: e.SignalCount, Win: e.Win, Loss: e.Loss, CumReturn: e.CumReturn,
			Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
			BuyThreshold: e.BuyThreshold, Horizon: e.Horizon, IR: e.IR, Excess: e.Excess,
			AdjBasis: e.AdjBasis, StaleAdjBasis: e.StaleAdjBasis,
			OpenPositions: openPositionsFor(held, e.ID, e.Name),
		}
		// 关联候选表验证信息：全样本 IC / 全链路回测超额与状态 / 证据文本（样本内外 IR、反推超额），
		// 让战法库卡片完整展示"这条规律电脑验证过吗"。
		// English: join the candidate's validation info — full-sample IC, full-chain backtest excess &
		// status, and the evidence text (in/out-of-sample IR, extrapolated excess) — so each library card
		// shows the full "was this rule computer-validated" story.
		if s.researchDB != nil && e.CandID > 0 {
			if c, err := s.researchDB.CandidateByID(e.CandID); err == nil && c != nil {
				item.ICMean = c.ICMean
				item.AvgExcess = c.AvgExcess
				item.BacktestDone = c.AvgExcess != 0
				item.Reason = c.Reason
			}
		}
		out = append(out, item)
	}
	// 形态战法库
	patterns, err := research.ListAppliedPatternRules(s.researchDir)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	pstats := map[string]research.AppliedPatternEntry{}
	for _, e := range patterns {
		pstats[e.ID] = e
	}
	if s.registry != nil {
		for _, c := range s.registry.AllControllers() {
			for _, rl := range c.PatternStats() {
				if e, ok := pstats[rl.ID]; ok {
					e.SignalCount, e.Win, e.Loss, e.CumReturn = rl.SignalCount, rl.Win, rl.Loss, rl.CumReturn
					pstats[rl.ID] = e
				}
			}
		}
	}
	for _, e := range pstats {
		out = append(out, libItem{
			Kind: "pattern", ID: e.ID, Name: e.Name, Enabled: e.Enabled, CandID: e.CandID,
			AppliedAt: e.AppliedAt, SignalCount: e.SignalCount, Win: e.Win, Loss: e.Loss, CumReturn: e.CumReturn,
			Conds: e.Conds,
			// §ADJ-BASIS-2P 形态卡与因子卡共用同一红标（条件因子同样跑在复权价上）。
			AdjBasis:      e.AdjBasis,
			StaleAdjBasis: e.StaleAdjBasis,
			OpenPositions: openPositionsFor(held, e.ID, e.Name),
		})
	}
	writeJSON(w, 200, map[string]any{"library": out})
}

// openPositionCounts 取"当前开放持仓按策略键计数"（§EXIT-RETAIN）：
//   - 已接入引擎注册表（生产形态）：注册表是实盘账本 + 全部账号模拟盘账本的单一真相源，直接透传；
//   - 未接入（独立 server / 单测）：退回只读实盘账本 + server 自身持有的模板模拟盘账本。
//
// 两条分支都不写状态；取数失败按"无持仓"计并在调用侧留痕，绝不因读库失败放宽出场参数保留口径。
// English: open-position counts — delegated to the engine registry (live book ∪ all account paper
// books) when wired, otherwise read straight from the live book plus the server's template book.
func (s *Server) openPositionCounts() combat_agent.HeldStrategyKeys {
	if s.registry != nil {
		if c := s.registry.OpenPositionStrategyCounts(); c != nil {
			return c
		}
		return combat_agent.HeldStrategyKeys{}
	}
	out := combat_agent.HeldStrategyKeys{}
	if db := s.realDB(); db != nil {
		ps, err := db.RealPositions()
		if err != nil {
			log.Printf("[library] §EXIT-RETAIN 实盘持仓读取失败(open_positions 按 0 计): %v", err)
		}
		for _, p := range ps {
			out.Add(p.Strategy)
		}
	}
	if s.paper != nil {
		for _, p := range s.paper.Positions() {
			key := p.Strategy
			if key == "" {
				key = p.StrategyType
			}
			out.Add(key)
		}
	}
	return out
}

// openPositionsFor 该条战法的开放持仓数：持仓 Strategy 可能存规则 ID，也可能存显示名（历史形态），
// 两个键都是同一批持仓的别名，因此归一化后同值时只计一次（否则一笔会被数成两笔）。
// English: open-position count for a rule — positions are keyed by either its id or its display name;
// when both normalize to the same string the count is taken once instead of double-added.
func openPositionsFor(held combat_agent.HeldStrategyKeys, id, name string) int {
	n := held.CountFor(id)
	if combat_agent.NormalizeStrategyKey(id) == combat_agent.NormalizeStrategyKey(name) {
		return n
	}
	return n + held.CountFor(name)
}

// handleResearchLibraryToggle 处理 POST /api/research/library/{id}/enable|disable：
// 启用/禁用某条已应用战法（因子 fac_ 或形态 pat_，写入文件 + 热重载引擎）。
// English: enable/disable an applied strategy (factor fac_ or pattern pat_; write file + hot-reload).
func (s *Server) handleResearchLibraryToggle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		enabled := action == "enable"
		// 研究库未接入时拒绝操作。
		if s.researchDir == "" {
			writeError(w, 503, "研究库未接入")
			return
		}
		// 按 ID 前缀区分形态/因子战法，分别走对应开关持久化。
		var err error
		if isPatternID(id) {
			err = research.SetAppliedPatternEnabled(s.researchDir, id, enabled)
		} else {
			err = research.SetAppliedFactorEnabled(s.researchDir, id, enabled)
		}
		if err != nil {
			writeError(w, 404, err.Error())
			return
		}
		s.reloadLibraries()
		writeJSON(w, 200, map[string]any{"status": "ok", "id": id, "enabled": enabled})
	}
}

// handleResearchLibraryDelete 处理 POST /api/research/library/{id}/delete：删除某条已应用战法。
// English: POST /api/research/library/{id}/delete — remove an applied strategy.
func (s *Server) handleResearchLibraryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// 研究库未接入时拒绝操作。
	if s.researchDir == "" {
		writeError(w, 503, "研究库未接入")
		return
	}
	// 按 ID 前缀分发：形态/因子分别走对应删除持久化。
	var err error
	if isPatternID(id) {
		err = research.RemoveAppliedPatternRule(s.researchDir, id)
	} else {
		err = research.RemoveAppliedFactorRule(s.researchDir, id)
	}
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	s.reloadLibraries()
	writeJSON(w, 200, map[string]any{"status": "ok", "id": id})
}

// handleResearchLibraryRename 处理 POST /api/research/library/{id}/rename：重命名某条已应用战法。
// 请求体 {"name":"..."}。English: POST /api/research/library/{id}/rename — rename an applied strategy.
func (s *Server) handleResearchLibraryRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.researchDir == "" {
		writeError(w, 503, "研究库未接入")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	var err error
	if isPatternID(id) {
		err = research.RenameAppliedPattern(s.researchDir, id, body.Name)
	} else {
		err = research.RenameAppliedFactor(s.researchDir, id, body.Name)
	}
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	// 重命名后刷新引擎（信号名/去重键跟随新名）
	s.reloadLibraries()
	writeJSON(w, 200, map[string]any{"status": "ok", "id": id, "name": body.Name})
}

// isPatternID 判断战法库 ID 是否为形态战法（pat_ 前缀），否则按因子战法处理。
// English: reports whether a library ID is a pattern strategy (pat_ prefix), else treated as factor.
func isPatternID(id string) bool {
	return len(id) >= 4 && id[:4] == "pat_"
}

// handleResearchBacktestToggle 处理 GET/POST /api/research/backtest-toggle：查询/设置"全量回测全局开关"
// （rules.scheduler.nightly.backtest_enabled，控制夜间研究是否在发现候选后自动跑 B4 回测）。
// English: GET/POST /api/research/backtest-toggle — read/set the global "full-backtest" toggle
// (rules.scheduler.nightly.backtest_enabled, controlling whether the nightly job auto-backtests discovered candidates).
func (s *Server) handleResearchBacktestToggle(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"enabled": s.cfg.Rules.Scheduler.Nightly.BacktestEnabled})
		return
	}
	// POST
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	cfg := s.cfg.Get()
	cfg.Scheduler.Nightly.BacktestEnabled = body.Enabled
	s.cfg.SetSchedulerConfig(&cfg.Scheduler)
	writeJSON(w, 200, map[string]any{"enabled": body.Enabled})
}

// reloadLibraries 对注册表内全部引擎热重载因子+形态战法库（启用/禁用/删除/重命名后立即生效），
// 并按最新启用战法集合同步模拟盘资金池（新增/停用战法后分仓随之更新）。
// English: hot-reloads the factor and pattern libraries on every engine in the registry (immediately
// effective after enable/disable/delete/rename) and syncs the paper strategy pools to the current
// enabled set (pools follow strategy add/disable changes).
func (s *Server) reloadLibraries() {
	if s.registry == nil {
		return
	}
	for _, c := range s.registry.AllControllers() {
		c.ReloadFactorRules(s.researchDir)
		c.ReloadPatternRules(s.researchDir)
	}
	s.registry.SetPaperPools(ActivePaperPoolTypes(s.researchDir, s.paperStrategiesForOperator()))
	// §C 规则池显示名同步（改名/新增后前端分仓条立即用新名字）
	s.registry.SetPaperLabelResolver(LibraryLabelResolver(s.researchDir))
}

// paperStrategiesForOperator 读取运营账号（管理员）的模拟盘战法白名单（rules.paper.strategies）——
// 资金池模板按运营配置装配（§SIGNAL_CONTROLLER P3：momentum 池仅在显式列名时开立）。
// English: reads the operator account's paper strategy whitelist that drives the pool template.
func (s *Server) paperStrategiesForOperator() []string {
	if s.cfg == nil {
		return nil
	}
	rules := s.cfg.GetRulesFor(s.operatorID())
	if rules == nil {
		return nil
	}
	return rules.Paper.Strategies
}

// LibraryLabelResolver 构建规则池 ID → 显示名 解析器（含停用规则——历史持仓仍需可读名）。
// English: builds a rule-pool id→label resolver from the applied library files (disabled included
// so legacy holdings keep readable names).
func LibraryLabelResolver(dataDir string) func(string) string {
	names := map[string]string{}
	if dataDir != "" {
		if es, err := research.ListAppliedFactorRules(dataDir); err == nil {
			for _, e := range es {
				names[e.ID] = e.Name
			}
		}
		if ps, err := research.ListAppliedPatternRules(dataDir); err == nil {
			for _, p := range ps {
				names[p.ID] = p.Name
			}
		}
	}
	return func(id string) string { return names[id] }
}

// ActivePaperPoolTypes 构建"当前启用战法"资金池类型列表：
// 4 内置形态战法恒开立；factor/pattern 视 research 是否有启用规则才计入。
// §SIGNAL_CONTROLLER P3（20260917 动量误交易根因之一）：momentum 池不再恒开立——
// 仅当账号模拟盘战法白名单（rules.paper.strategies）显式列名动量时才分配动量池资金；
// 未列名时不占资金、无信号准入（信号控制器 paper 通道白名单同样拒绝，双端一致）。
// 供 quant 启动与战法库热加载注入 registry.SetPaperPools（分仓防单战法垄断）。
// English: builds the enabled-strategy pool list — the four form strategies always; factor/pattern
// join only when the research store has enabled rules. §SIGNAL_CONTROLLER P3: the momentum pool is
// opened ONLY when the account's paper whitelist explicitly names momentum (previously always-open —
// one root cause of the accidental momentum fill). Feeds registry.SetPaperPools at startup & hot reload.
func ActivePaperPoolTypes(dataDir string, paperStrategies []string) []string {
	types := []string{
		string(strategy.SignalDragon),
		string(strategy.SignalDoubleBump),
		string(strategy.SignalNShape),
		string(strategy.SignalDragonReturn),
	}
	// 动量池显式 opt-in：白名单列名才开立（与 signalctl 默认全集不含动量同语义）。
	for _, s := range paperStrategies {
		if s == string(strategy.SignalMomentum) {
			types = append(types, string(strategy.SignalMomentum))
			break
		}
	}
	// §C 规则细分池：每条启用规则独立成池（fac_1/pat_2 即池 key），不再用聚合池。
	// 信号端 StrategyType 已改填规则 ID（combat_agent.libraryIDFromMeta），归池一一对应；
	// 池集合变更时 rebuildPoolsLocked 均分重排（与基础类型启停同语义）。
	if dataDir != "" {
		if rules, err := research.LoadEnabledFactorRules(dataDir); err == nil {
			for _, r := range rules {
				if r.ID != "" {
					types = append(types, r.ID)
				}
			}
		}
		if pats, err := research.LoadEnabledPatternRules(dataDir); err == nil {
			for _, p := range pats {
				if p.ID != "" {
					types = append(types, p.ID)
				}
			}
		}
	}
	return types
}

// ---- 回测任务（子系统统一改造一期）：入队 + 查询 ----
//
// quant 不再 spawn 研究子进程：手动回测只把 high 任务写入 research_tasks 队列，
// 由 researchd worker 在盘后窗口唯一执行（盘后门控对手动任务同样生效）。
// 本文件保留旧 REST 契约与 JSON 形状，前端零改动；状态新增 queued/preempted
// （preempted 对外映射为旧语义 interrupted）。
// English: phase-1 queue refactor — manual backtests only enqueue a high-priority task; the researchd
// worker executes them after hours. Legacy REST shapes preserved so the frontend is untouched.

// backtestTaskKinds 任务类型 → 旧 kind 字段（列表/状态接口兼容输出用）。
var backtestTaskKinds = map[string]string{
	store.TaskBacktestCandidate: "candidate",
	store.TaskBacktestStrategy:  "library",
	store.TaskBacktestNightly:   "nightly",
}

// isBacktestTask 是否回测类任务（回测 tab 只展示这些类型）。
func isBacktestTask(t string) bool { _, ok := backtestTaskKinds[t]; return ok }

// apiStatusOf 对外展示状态映射：preempted 沿用旧名 interrupted（断点有效可续跑语义一致）。
func apiStatusOf(s string) string {
	if s == store.TaskPreempted {
		return "interrupted"
	}
	return s
}

// taskToLegacyJob 把队列行映射为旧 BacktestJob 形状（前端契约兼容）。
func taskToLegacyJob(t store.ResearchTask) store.BacktestJob {
	kind := backtestTaskKinds[t.Type]
	// §8.6-B：形态候选回放任务的 payload 带 candidate_id——对外映射回 kind=candidate，
	// 前端"候选 #N"标签/取消键/续跑键语义才正确（否则会显示成"规则 N"）。
	var p map[string]any
	if json.Unmarshal([]byte(t.Payload), &p) == nil {
		if _, ok := p["candidate_id"]; ok {
			kind = "candidate"
		}
	}
	// §P1-c：透出回放子类型（内置战法名 / factor / pattern），前端按战法挂最新结果、失败重跑重建 ID。
	sk := ""
	if ks, ok := p["kind"].(string); ok {
		sk = strings.ToLower(strings.TrimSpace(ks))
	}
	// §P3 反馈：透出入队运行参数原文——回测行展示"具体参数"（区间/选股数/最小样本等）。
	pj, _ := json.Marshal(p)
	return store.BacktestJob{
		ID:           t.ID,
		Kind:         kind,
		CandidateID:  t.RefID,
		StrategyKind: sk,
		ParamsJSON:   string(pj),
		Status:       apiStatusOf(t.Status),
		Progress:     t.Progress,
		AvgExcess:    t.ResultNum,
		Error:        t.Error,
		ResultText:   t.ResultText,
		StartedAt:    t.StartedAt,
		FinishedAt:   t.FinishedAt,
		UpdatedAt:    t.UpdatedAt,
	}
}

// enqueueBacktestTask 入队一条手动回测任务（high 优先级）。payload 为运行参数。
func (s *Server) enqueueBacktestTask(taskType string, refID int64, payload map[string]any) (int64, *store.ResearchTask, error) {
	if s.researchDB == nil {
		return 0, nil, fmt.Errorf("研究库未接入")
	}
	// §回测自动增强 A0：战法回放/寻优任务统一在此注入 backtest 配置（enabled 才注入），
	// 让回放与寻优口径同步升级；无记录/停用 = 不注入 = 旧行为。
	if taskType == store.TaskBacktestStrategy {
		s.injectBacktestPayload(payload)
	}
	// 同 ref 幂等：已有排队/运行中任务则直接返回现态，不重复入队。
	has, err := s.researchDB.HasActiveTaskByRef(taskType, refID)
	if err != nil {
		return 0, nil, err
	}
	if has {
		t, err := s.researchDB.LatestTaskByRef(taskType, refID)
		if err != nil || t == nil {
			return 0, nil, fmt.Errorf("读取现有任务失败")
		}
		return t.ID, t, nil
	}
	pj, _ := json.Marshal(payload)
	id, err := s.researchDB.EnqueueResearchTask(&store.ResearchTask{
		Type:     taskType,
		RefID:    refID,
		Priority: "high",
		Status:   store.TaskQueued,
		Payload:  string(pj),
	})
	if err != nil {
		return 0, nil, err
	}
	log.Printf("[research] 已入队 %s #%d（high，盘后执行）: %s", taskType, id, string(pj))
	t, _ := s.researchDB.GetResearchTask(id)
	return id, t, nil
}

// handleCandidateBacktest 处理 POST /api/research/candidates/{id}/backtest：
// 入队一条 backtest_candidate 高优先级任务（异步、盘后由 researchd 执行），前端照旧轮询进度。
// 可选 query：start/end/top_k/min_stocks 透传进 payload。同候选已有活跃任务时幂等返回现态。
// English: POST /api/research/candidates/{id}/backtest — enqueues a high-priority candidate backtest;
// executed by the researchd worker after hours. Optional params pass through into the payload.
func (s *Server) handleCandidateBacktest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	// §8.6-B 按候选 kind 路由：pattern 候选走战法库回放引擎（btreplay 候选直读模式，
	// 不依赖审批入库）；factor 及其他仍走 B4 全链路。前端端点保持不变。
	if s.researchDB != nil {
		if cand, cerr := s.researchDB.CandidateByID(id); cerr == nil && cand != nil && cand.Kind == "pattern" {
			s.enqueuePatternCandidateBacktest(w, r, id)
			return
		}
	}
	q := r.URL.Query()
	// 默认回测起点与夜间研究窗口对齐（20230801）：CLI 裸默认是 20200101，
	// 手动不传参时会在 8 年区间上做事件合成/装配，小机器上分钟级零反馈。
	// English: default the manual backtest start to the nightly research window — the CLI bare
	// default (2020) sweeps 8 years and stalls the small box with zero feedback.
	payload := map[string]any{"start": "20230801"}
	for k, key := range map[string]string{"start": "start", "end": "end"} {
		if v := q.Get(k); v != "" {
			payload[key] = v
		}
	}
	if v, err := strconv.Atoi(q.Get("top_k")); err == nil && v > 0 {
		payload["top-k"] = v
	}
	if v, err := strconv.Atoi(q.Get("min_stocks")); err == nil && v > 0 {
		payload["min-stocks"] = v
	}
	taskID, t, err := s.enqueueBacktestTask(store.TaskBacktestCandidate, id, payload)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	status := "queued"
	if t != nil {
		status = apiStatusOf(t.Status)
	}
	writeJSON(w, 202, map[string]any{"status": status, "task_id": taskID, "job": map[string]any{
		"candidate_id": id, "status": status, "progress": "0%",
	}})
}

// builtinStrategies 实盘常驻的四大手写形态战法（引擎 8a/8b 链路始终运行，不经战法库文件）：
// 名称 → 合成规则序号（回测任务 ref_id；前端据此映射显示名）。
// English: builtin always-on pattern strategies (engine-resident, not library-file driven):
// name → synthetic rule number used as the replay task's ref_id.
var builtinStrategies = map[string]int64{
	"double_bump":   901, // 双响炮
	"dragon":        902, // 龙头
	"dragon_return": 903, // 龙回头
	"n_shape":       904, // N形（日K近似）
}

// parseLibraryRuleID 解析 fac_<n> / pat_<n> 规则 ID → (kind, 序号)。
// 另接受内置战法名（double_bump/dragon/dragon_return/n_shape）→ ("builtin", 固定序号)。
func parseLibraryRuleID(id string) (string, int64, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if num, ok := builtinStrategies[id]; ok {
		return "builtin", num, true
	}
	switch {
	case strings.HasPrefix(id, "fac_"):
		n, err := strconv.ParseInt(strings.TrimPrefix(id, "fac_"), 10, 64)
		return "factor", n, err == nil && n > 0
	case strings.HasPrefix(id, "pat_"):
		n, err := strconv.ParseInt(strings.TrimPrefix(id, "pat_"), 10, 64)
		return "pattern", n, err == nil && n > 0
	}
	return "", 0, false
}

// resolveBacktestRef 解析 /api/research/backtest/{id} 的 id：候选 ID 直通；
// 战法库沿用旧合成键空间 1e9+规则序号（libraryJobKey）。
// English: resolves the path id — plain candidate ids vs the legacy 1e9+rule-num library key space.
func resolveBacktestRef(id int64) (string, int64) {
	if id >= 1_000_000_000 {
		return store.TaskBacktestStrategy, id - 1_000_000_000
	}
	return store.TaskBacktestCandidate, id
}

// handleLibraryBacktest 处理 POST /api/research/library/{id}/backtest：
// 入队一条 backtest_strategy 高优先级任务（战法库规则历史回放，researchd 盘后执行），
// 结果汇总落 result_text，前端「回测」tab 展示。可选 query：start/end/maxstocks。
// English: POST /api/research/library/{id}/backtest — enqueues a high-priority strategy-replay task;
// its summary lands in result_text for the frontend's backtest tab.
func (s *Server) handleLibraryBacktest(w http.ResponseWriter, r *http.Request) {
	ruleID := r.PathValue("id")
	kind, num, ok := parseLibraryRuleID(ruleID)
	if !ok {
		writeError(w, 400, "无效规则 ID（fac_<n>/pat_<n> 或内置战法名）")
		return
	}
	// 内置战法：payload kind 直接用战法名——btreplay 单战法模式原生支持，
	// 与库规则共用同一任务类型/回测 tab 展示。
	if kind == "builtin" {
		kind = strings.ToLower(strings.TrimSpace(ruleID))
	}
	q := r.URL.Query()
	payload := map[string]any{"kind": kind, "start": "20230801"} // 默认与夜间窗口/候选回测对齐
	if v := q.Get("start"); v != "" {
		payload["start"] = v
	}
	if v := q.Get("end"); v != "" {
		payload["end"] = v
	}
	if v, err := strconv.Atoi(q.Get("maxstocks")); err == nil && v > 0 {
		payload["maxstocks"] = v
	}
	// §质控：quality=true 时以质控池（剔 ST/退市/多年亏损/地量股）替代全量 StockCodes()，
	// 供前端"重新回测"手动全量回放时复用夜间 library_replay 的口径。
	// English: quality=true builds the universe from the quality screen (drops ST/delisted/
	// multi-year-loss/illiquid names) — same caliber as the nightly full-market library replay.
	if q.Get("quality") == "true" || q.Get("quality") == "1" {
		payload["quality"] = true
	}
	if _, _, err := s.enqueueBacktestTask(store.TaskBacktestStrategy, num, payload); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"status": "queued"})
}

// handleBacktestCancel 处理 POST /api/research/backtest/{id}/cancel：
// 写 control=cancel（worker kill 子进程或把排队任务置 cancelled）；断点缓存保持有效。
// English: POST .../cancel — writes control=cancel; the worker kills the child or cancels a queued row.
func (s *Server) handleBacktestCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	t, err := s.latestBacktestTask(id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil || (t.Status != store.TaskQueued && t.Status != store.TaskRunning && t.Status != store.TaskPaused && t.Status != store.TaskPreempted) {
		writeError(w, 404, "该候选没有可取消的回测任务")
		return
	}
	if err := s.researchDB.SetTaskControl(t.ID, store.ControlCancel); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cancelling"})
}

// handleBacktestPause 处理 POST /api/research/backtest/{id}/pause：写 control=pause
// （worker SIGSTOP 子进程并标 paused）。仅 running 状态可暂停。
func (s *Server) handleBacktestPause(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	t, err := s.latestBacktestTask(id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil || t.Status != store.TaskRunning {
		writeError(w, 404, "该候选没有可暂停的运行中任务")
		return
	}
	if err := s.researchDB.SetTaskControl(t.ID, store.ControlPause); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "paused"})
}

// handleBacktestResume 处理 POST /api/research/backtest/{id}/resume：写 control=resume
// （worker SIGCONT 恢复子进程并回到 running）。
func (s *Server) handleBacktestResume(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	t, err := s.latestBacktestTask(id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil || t.Status != store.TaskPaused {
		writeError(w, 404, "该候选没有已暂停的任务")
		return
	}
	if err := s.researchDB.SetTaskControl(t.ID, store.ControlResume); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "running"})
}

// handleBacktestStatus 处理 GET /api/research/backtest/{id}：返回最新任务的旧形状 JSON
// （candidate_id/status/progress/avg_excess/error/started），前端轮询逻辑不变。
func (s *Server) handleBacktestStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	t, err := s.latestBacktestTask(id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil {
		writeError(w, 404, "回测任务不存在")
		return
	}
	started := t.StartedAt
	if tp, terr := time.Parse("2006-01-02 15:04:05", started); terr == nil {
		_ = tp
	}
	ref := t.RefID
	respKind := backtestTaskKinds[t.Type]
	if respKind == "library" {
		// §8.6-B：形态候选回放对外映射回 candidate；内置战法保持 library+固定序号
		var p map[string]any
		if json.Unmarshal([]byte(t.Payload), &p) == nil {
			if _, ok := p["candidate_id"]; ok {
				respKind = "candidate"
			}
		}
	}
	if respKind == "candidate" {
		_, ref = resolveBacktestRef(id)
	}
	writeJSON(w, 200, map[string]any{
		"candidate_id": ref,
		"kind":         respKind,
		"status":       apiStatusOf(t.Status),
		"progress":     t.Progress,
		"avg_excess":   t.ResultNum,
		"error":        t.Error,
		"started":      started,
		"task_id":      t.ID,
	})
}

// handleBacktestRunning 处理 GET /api/research/backtest/running：返回所有未终结的回测任务
// （queued/running/paused/preempted），前端刷新后恢复轮询与 loading 态。
func (s *Server) handleBacktestRunning(w http.ResponseWriter, r *http.Request) {
	jobs := []store.BacktestJob{}
	if s.researchDB != nil {
		tasks, err := s.researchDB.ActiveResearchTasks()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		for _, t := range tasks {
			if !isBacktestTask(t.Type) {
				continue
			}
			jobs = append(jobs, taskToLegacyJob(t))
		}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// handleBacktestList 处理 GET /api/research/backtest/list：返回全部回测任务（含夜间与队列中），
// 最新在前，供「回测」tab 进度查看。
func (s *Server) handleBacktestList(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	tasks, err := s.researchDB.ListResearchTasks()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	jobs := []store.BacktestJob{}
	for _, t := range tasks {
		if !isBacktestTask(t.Type) {
			continue
		}
		jobs = append(jobs, taskToLegacyJob(t))
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// enqueuePatternCandidateBacktest 形态候选回测入口（§8.6-B）：入队 backtest_strategy
// 高优先级任务（payload 带 candidate_id），worker 经 run-task → btreplay 候选直读模式
// 回放该候选条件集；汇总报告落 result_text，「回测」tab 展示。
// English: enqueues a high-priority strategy-replay task for a pattern candidate (candidate-direct
// btreplay mode); the summary lands in result_text for the backtest tab.
func (s *Server) enqueuePatternCandidateBacktest(w http.ResponseWriter, r *http.Request, id int64) {
	q := r.URL.Query()
	payload := map[string]any{"kind": "pattern", "candidate_id": id, "start": "20230801"}
	if v := q.Get("start"); v != "" {
		payload["start"] = v
	}
	if v := q.Get("end"); v != "" {
		payload["end"] = v
	}
	taskID, t, err := s.enqueueBacktestTask(store.TaskBacktestStrategy, id, payload)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	status := "queued"
	if t != nil {
		status = apiStatusOf(t.Status)
	}
	writeJSON(w, 202, map[string]any{"status": status, "task_id": taskID, "job": map[string]any{
		"candidate_id": id, "status": status, "progress": "0%",
	}})
}

// latestBacktestTask 取某 id 对应的最新回测任务行：普通 id 同时可能命中
// backtest_candidate（B4）与 backtest_strategy（形态候选回放，§8.6-B），取最新者；
// 战法库合成键(≥1e9)仍按规则序号解析。
// English: resolves the newest task across both types for plain ids; library synthetic keys
// (>=1e9) still resolve by rule number.
func (s *Server) latestBacktestTask(id int64) (*store.ResearchTask, error) {
	if s.researchDB == nil {
		return nil, fmt.Errorf("研究库未接入")
	}
	if id >= 1_000_000_000 {
		return s.researchDB.LatestTaskByRef(store.TaskBacktestStrategy, id-1_000_000_000)
	}
	var best *store.ResearchTask
	for _, typ := range []string{store.TaskBacktestCandidate, store.TaskBacktestStrategy} {
		t, err := s.researchDB.LatestTaskByRef(typ, id)
		if err == nil && t != nil && (best == nil || t.ID > best.ID) {
			best = t
		}
	}
	return best, nil
}
