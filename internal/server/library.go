// 战法库（因子战法）HTTP 端点：列出已应用战法 + 启用/禁用/删除 + 运行效果监测 + 单条候选全量回测。
// English: strategy-library (factor) HTTP endpoints — list applied strategies, enable/disable/delete,
// live-effect monitoring, and per-candidate full backtest.
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

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
	}
	var out []libItem
	stats := map[string]research.AppliedFactorEntry{}
	for _, e := range entries {
		stats[e.ID] = e
	}
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
	for _, e := range stats {
		item := libItem{
			Kind: "factor", ID: e.ID, Name: e.Name, Enabled: e.Enabled, CandID: e.CandID,
			AppliedAt: e.AppliedAt, SignalCount: e.SignalCount, Win: e.Win, Loss: e.Loss, CumReturn: e.CumReturn,
			Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
			BuyThreshold: e.BuyThreshold, Horizon: e.Horizon, IR: e.IR, Excess: e.Excess,
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
		})
	}
	writeJSON(w, 200, map[string]any{"library": out})
}

// handleResearchLibraryToggle 处理 POST /api/research/library/{id}/enable|disable：
// 启用/禁用某条已应用战法（因子 fac_ 或形态 pat_，写入文件 + 热重载引擎）。
// English: enable/disable an applied strategy (factor fac_ or pattern pat_; write file + hot-reload).
func (s *Server) handleResearchLibraryToggle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		enabled := action == "enable"
		if s.researchDir == "" {
			writeError(w, 503, "研究库未接入")
			return
		}
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
	if s.researchDir == "" {
		writeError(w, 503, "研究库未接入")
		return
	}
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
	s.registry.SetPaperPools(ActivePaperPoolTypes(s.researchDir))
}

// ActivePaperPoolTypes 构建"当前启用战法"资金池类型列表：
// 4 形态战法恒启用；factor/pattern 视 research 是否有启用规则才计入（当前唯一因子规则=波动突破 → factor 池激活）。
// 供 quant 启动与战法库热加载注入 registry.SetPaperPools（分仓防单战法垄断）。
// English: builds the "currently enabled strategies" pool-type list — the four pattern strategies are
// always on; factor/pattern join only when the research store has enabled rules (the sole enabled rule
// today, 波动突破, activates the factor pool). Feeds registry.SetPaperPools at startup and hot reload.
func ActivePaperPoolTypes(dataDir string) []string {
	types := []string{
		string(strategy.SignalDragon),
		string(strategy.SignalDoubleBump),
		string(strategy.SignalNShape),
		string(strategy.SignalDragonReturn),
	}
	if dataDir != "" {
		if rules, err := research.LoadEnabledFactorRules(dataDir); err == nil && len(rules) > 0 {
			types = append(types, string(strategy.SignalFactor))
		}
		if pats, err := research.LoadEnabledPatternRules(dataDir); err == nil && len(pats) > 0 {
			types = append(types, string(strategy.SignalPattern))
		}
	}
	return types
}

// ---- 单条候选全量回测（异步）----

// backtestJobs 内存回测任务表（前端轮询进度）。单实例，进程内即可，无需持久化。
// English: in-memory backtest job table (frontend polls progress). Single instance, no persistence.
var backtestJobs = struct {
	sync.Mutex
	m map[int64]*backtestJob
}{m: map[int64]*backtestJob{}}

// backtestJob 一条回测任务状态。
// English: one backtest job state.
type backtestJob struct {
	CandID    int64     `json:"candidate_id"`
	Status    string    `json:"status"` // running | done | error
	Started   time.Time `json:"started"`
	Progress  string    `json:"progress"`
	AvgExcess float64   `json:"avg_excess,omitempty"`
	Err       string    `json:"error,omitempty"`
}

// handleCandidateBacktest 处理 POST /api/research/candidates/{id}/backtest：对指定候选跑一次 B4 全量回测
// （异步后台执行，前端轮询 GET /api/research/backtest/{id} 拿进度与结果）。
// 任务状态同步落库 backtest_jobs（kind='candidate'），quant 重启后任务可查、可续跑。
// English: POST /api/research/candidates/{id}/backtest — run a full B4 backtest on a specific candidate
// (async background; frontend polls GET /api/research/backtest/{id} for progress/result). The job is
// persisted to backtest_jobs (kind='candidate') so it survives quant restarts and can be resumed.
func (s *Server) handleCandidateBacktest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	backtestJobs.Lock()
	if j, ok := backtestJobs.m[id]; ok && j.Status == "running" {
		backtestJobs.Unlock()
		writeJSON(w, 200, map[string]any{"status": "running", "job": j})
		return
	}
	// 建任务即落库（Progress="0%"），消灭 CLI 首个 10% 之前的前端进度空窗。
	// English: the job is persisted immediately with Progress="0%" so the frontend has a visible bar
	// before the CLI prints its first "回测进度" line.
	job := &backtestJob{CandID: id, Status: "running", Started: time.Now(), Progress: "0%"}
	backtestJobs.m[id] = job
	backtestJobs.Unlock()
	s.persistBacktestJob(id, job.Status, job.Progress, 0, "")

	// 后台执行
	go func() {
		avg, err := s.runCandidateBacktest(id, job)
		backtestJobs.Lock()
		defer backtestJobs.Unlock()
		j := backtestJobs.m[id]
		if j == nil {
			j = job
			backtestJobs.m[id] = j
		}
		if err != nil {
			j.Status, j.Err = "error", err.Error()
		} else {
			j.Status, j.AvgExcess = "done", avg
		}
		// 完成态同步落库（done 回填 avg_excess；error 记录原因）。
		// English: persist the terminal state (done backfills avg_excess; error stores the reason).
		s.persistBacktestJob(id, j.Status, j.Progress, j.AvgExcess, j.Err)
	}()
	writeJSON(w, 202, map[string]any{"status": "started", "job": job})
}

// persistBacktestJob 把单候选回测任务状态写入 backtest_jobs（kind='candidate'）。
// researchDB 未接入（nil）时静默跳过，不影响现有内存态行为。
// English: persists a per-candidate backtest job to backtest_jobs (kind='candidate'). Silently skips
// when the research DB isn't wired (nil), keeping the in-memory behavior intact.
func (s *Server) persistBacktestJob(candID int64, status, progress string, avgExcess float64, errMsg string) {
	if s.researchDB == nil {
		return
	}
	if err := s.researchDB.UpsertBacktestJob(&store.BacktestJob{
		Kind: "candidate", CandidateID: candID, Status: status,
		Progress: progress, AvgExcess: avgExcess, Error: errMsg,
	}); err != nil {
		log.Printf("[research] 持久化回测任务失败 cand=%d: %v", candID, err)
	}
}

// handleBacktestStatus 处理 GET /api/research/backtest/{id}：返回回测任务状态与结果。
// 内存优先；quant 重启后内存表为空，回退读 backtest_jobs（任务可查/可续跑）。
// English: GET /api/research/backtest/{id} — returns a backtest job's status and result. In-memory is
// checked first; after a quant restart the in-memory table is empty, so it falls back to backtest_jobs.
func (s *Server) handleBacktestStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	backtestJobs.Lock()
	j := backtestJobs.m[id]
	backtestJobs.Unlock()
	if j == nil && s.researchDB != nil {
		dbj, err := s.researchDB.GetBacktestJob("candidate", id)
		if err == nil && dbj != nil {
			j = &backtestJob{
				CandID:    id,
				Status:    dbj.Status,
				Progress:  dbj.Progress,
				AvgExcess: dbj.AvgExcess,
				Err:       dbj.Error,
			}
			if t, terr := time.Parse("2006-01-02 15:04:05", dbj.StartedAt); terr == nil {
				j.Started = t
			}
		}
	}
	if j == nil {
		writeError(w, 404, "回测任务不存在")
		return
	}
	writeJSON(w, 200, j)
}

// handleBacktestRunning 处理 GET /api/research/backtest/running：返回所有运行中的回测任务。
// 前端页面刷新后据此恢复 loading 态与轮询（配合 onMounted 恢复逻辑）。
// English: GET /api/research/backtest/running — returns every running backtest job. The frontend uses it
// after a page refresh to restore loading states and polling (paired with the onMounted recovery hook).
func (s *Server) handleBacktestRunning(w http.ResponseWriter, r *http.Request) {
	jobs := []store.BacktestJob{}
	if s.researchDB != nil {
		rows, err := s.researchDB.RunningBacktestJobs()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		jobs = rows
	}
	// 合并内存运行中任务（兜底 DB 写入失败 / 尚未落库的瞬态），内存态最新。
	// English: merge in-memory running jobs (fallback for transient/DB-write-failure cases); in-memory
	// state is the freshest.
	backtestJobs.Lock()
	defer backtestJobs.Unlock()
	for id, j := range backtestJobs.m {
		if j.Status != "running" {
			continue
		}
		found := false
		for i := range jobs {
			if jobs[i].CandidateID == id {
				jobs[i] = store.BacktestJob{Kind: "candidate", CandidateID: id, Status: j.Status, Progress: j.Progress}
				found = true
				break
			}
		}
		if !found {
			jobs = append(jobs, store.BacktestJob{Kind: "candidate", CandidateID: id, Status: j.Status, Progress: j.Progress})
		}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// handleBacktestList 处理 GET /api/research/backtest/list：返回全部回测任务（含夜间全量），最新在前，
// 供前端「回测」tab 的进度查看列表。
// English: GET /api/research/backtest/list — returns all backtest jobs (including nightly runs), newest
// first, powering the progress list of the frontend's "backtest" tab.
func (s *Server) handleBacktestList(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	jobs, err := s.researchDB.ListBacktestJobs()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// runCandidateBacktest 对候选跑一次 B4 全量回测并回填 avg_excess。
// 以独立子进程（research backtest CLI）执行，避免回测的内存/CPU 压垮 quant 服务
// （quant 有 MemoryMax=1G cgroup，进程内跑全量回测曾触发 OOM 把整个引擎杀掉）。
// English: runs a full B4 backtest on a candidate and backfills avg_excess, spawning the
// `research backtest` CLI as a separate child process so the backtest's memory/CPU can't OOM the
// quant engine (quant runs under a 1G MemoryMax cgroup; in-process full backtests have OOM-killed it).
func (s *Server) runCandidateBacktest(id int64, job *backtestJob) (float64, error) {
	// 定位 research 二进制：优先常见部署路径，其次 PATH，最后 researchDir 同目录。
	// English: locate the research binary — common deploy paths first, then PATH, then next to researchDir.
	bin := ""
	for _, cand := range []string{
		"/opt/quant/research",
		"/usr/local/bin/research",
	} {
		if _, err := os.Stat(cand); err == nil {
			bin = cand
			break
		}
	}
	if bin == "" {
		if p, err := exec.LookPath("research"); err == nil {
			bin = p
		}
	}
	if bin == "" && s.researchDir != "" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(s.researchDir), "research")); err == nil {
			bin = filepath.Join(filepath.Dir(s.researchDir), "research")
		}
	}
	if bin == "" {
		return 0, fmt.Errorf("找不到 research 二进制")
	}
	dbPath := ""
	if s.researchDir != "" {
		// 从 researchDir 推导研究库路径（trading.db 与 config 同目录）
		dbPath = filepath.Join(s.researchDir, "trading.db")
	}
	if dbPath == "" {
		return 0, nil
	}
	args := []string{"--db", dbPath, "backtest", "--id", strconv.FormatInt(id, 10)}
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("打开子进程输出失败: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("打开子进程错误输出失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("回测子进程启动失败: %v", err)
	}
	// 逐行解析子进程输出：识别"回测进度 xx%"实时更新任务进度（前端 5s 轮询），
	// 末尾"平均超额=%.4f"为最终结果；stdout/stderr 合并按行处理，任一行崩溃不拖垮轮询。
	// English: read the child's output line-by-line — "回测进度 xx%" updates the job progress in
	// real time (frontend polls every 5s); the trailing "平均超额=%.4f" is the final result; stdout
	// and stderr are both scanned; a bad line can never break the polling loop.
	var out bytes.Buffer
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				out.WriteString(line)
				if m := backtestProgressRe.FindStringSubmatch(line); len(m) == 2 {
					backtestJobs.Lock()
					if job != nil {
						job.Progress = m[1] + "%"
					}
					backtestJobs.Unlock()
					// 进度同步落库（CLI 每 10% 打印一次，写入频率低；重启后进度可从 DB 恢复）。
					// English: persist the progress line to DB (CLI prints every 10%, so writes are
					// low-frequency; progress is recoverable from DB after a restart).
					s.persistBacktestJob(id, "running", m[1]+"%", 0, "")
				}
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		return 0, fmt.Errorf("回测子进程失败: %v: %s", waitErr, out.String())
	}
	// 解析 CLI 输出的"平均超额=%.4f"
	avg := 0.0
	m := avgExcessRe.FindStringSubmatch(out.String())
	if len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			avg = v
		}
	}
	log.Printf("[research] 候选 #%d 全量回测完成, 平均超额=%.4f", id, avg)
	return avg, nil
}

// avgExcessRe 匹配 research backtest CLI 输出的平均超额。
// English: matches the avg-excess printed by the research backtest CLI.
var avgExcessRe = regexp.MustCompile(`平均超额=(-?\d+\.?\d*)`)

// backtestProgressRe 匹配 research backtest CLI 的阶段进度输出（"回测进度 xx%"）。
// English: matches the stage-progress output of the research backtest CLI ("回测进度 xx%").
var backtestProgressRe = regexp.MustCompile(`回测进度 (\d+)%`)

// BacktestJobsSnapshot 供测试/诊断读取回测任务表。
// English: exposes the backtest job table for tests/diagnostics.
func BacktestJobsSnapshot() map[int64]*backtestJob {
	backtestJobs.Lock()
	defer backtestJobs.Unlock()
	out := make(map[int64]*backtestJob, len(backtestJobs.m))
	for k, v := range backtestJobs.m {
		cp := *v
		out[k] = &cp
	}
	return out
}
