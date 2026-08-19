// 战法库（因子战法）HTTP 端点：列出已应用战法 + 启用/禁用/删除 + 运行效果监测 + 单条候选全量回测。
// English: strategy-library (factor) HTTP endpoints — list applied strategies, enable/disable/delete,
// live-effect monitoring, and per-candidate full backtest.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		out = append(out, libItem{
			Kind: "factor", ID: e.ID, Name: e.Name, Enabled: e.Enabled, CandID: e.CandID,
			AppliedAt: e.AppliedAt, SignalCount: e.SignalCount, Win: e.Win, Loss: e.Loss, CumReturn: e.CumReturn,
			Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
			BuyThreshold: e.BuyThreshold, Horizon: e.Horizon, IR: e.IR, Excess: e.Excess,
		})
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

// reloadLibraries 对注册表内全部引擎热重载因子+形态战法库（启用/禁用/删除/重命名后立即生效）。
// English: hot-reloads the factor and pattern libraries on every engine in the registry.
func (s *Server) reloadLibraries() {
	if s.registry == nil {
		return
	}
	for _, c := range s.registry.AllControllers() {
		c.ReloadFactorRules(s.researchDir)
		c.ReloadPatternRules(s.researchDir)
	}
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
// English: POST /api/research/candidates/{id}/backtest — run a full B4 backtest on a specific candidate
// (async background; frontend polls GET /api/research/backtest/{id} for progress/result).
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
	job := &backtestJob{CandID: id, Status: "running", Started: time.Now()}
	backtestJobs.m[id] = job
	backtestJobs.Unlock()

	// 后台执行
	go func() {
		avg, err := s.runCandidateBacktest(id)
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
	}()
	writeJSON(w, 202, map[string]any{"status": "started", "job": job})
}

// handleBacktestStatus 处理 GET /api/research/backtest/{id}：返回回测任务状态与结果。
// English: GET /api/research/backtest/{id} — returns a backtest job's status and result.
func (s *Server) handleBacktestStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	backtestJobs.Lock()
	j := backtestJobs.m[id]
	backtestJobs.Unlock()
	if j == nil {
		writeError(w, 404, "回测任务不存在")
		return
	}
	writeJSON(w, 200, j)
}

// runCandidateBacktest 对候选跑一次 B4 全量回测并回填 avg_excess。
// 以独立子进程（research backtest CLI）执行，避免回测的内存/CPU 压垮 quant 服务
// （quant 有 MemoryMax=1G cgroup，进程内跑全量回测曾触发 OOM 把整个引擎杀掉）。
// English: runs a full B4 backtest on a candidate and backfills avg_excess, spawning the
// `research backtest` CLI as a separate child process so the backtest's memory/CPU can't OOM the
// quant engine (quant runs under a 1G MemoryMax cgroup; in-process full backtests have OOM-killed it).
func (s *Server) runCandidateBacktest(id int64) (float64, error) {
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
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("回测子进程失败: %v: %s", err, out.String())
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
