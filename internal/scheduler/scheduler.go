// Package scheduler 按时段切换的研究调度器（独立服务 quant-research 使用）。
//
// 目标：让量化主程序与自动研究完全解耦，按 A 股交易时段切换，避免研究/回测
// 在盘中争抢 CPU：
//   - 交易时段（盘前/上午盘/午前/下午盘）：只跑 dataload 增量下载（绝不回测/研究），
//     让 quant 主程序的行情抓取 + 近实时打分独占 CPU；
//   - 盘后/周末：跑完整夜间研究作业（dataload → sector-rebuild → discover-factors
//     → discover-patterns → list），顺序子进程、单作业串行；
//   - 强制停止：下一交易日盘前 8:30 起（SessionPreMarket）若夜间作业仍在跑则终止，
//     把 CPU 交还量化主程序；跨日（每日 15:30 新作业）也会终止上一作业，实现
//     "周六、周日各跑一次"。
//
// 幂等：research_state.json 记录运行日 + 步骤进度，进程重启后从上次断点续跑，
// 已完成作业跨天自动重置。子进程直接执行二进制（不走 shell），kill 即杀。
//
// English: session-based research scheduler (for the standalone quant-research service).
// Decouples auto-research from the trading engine so research/backtest never contends with
// intraday market fetching:
//   - Trading sessions: dataload incremental download only (never backtest/research);
//   - After-hours/weekends: full nightly research job, sequential child processes;
//   - Forced stop: on the next trading day's premarket (8:30) a still-running nightly job is
//     killed to hand CPU back to the engine; a new calendar day's job also terminates the
//     previous day's, giving Saturday and Sunday each their own run.
//
// research_state.json keeps the run day + step index for crash-resume idempotency and resets
// across days. Children are launched directly (no shell), so kill is immediate.
package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// researchStart 夜间研究窗口起点（近 3 年，与既有 run_auto_research_full.sh 一致）。
const researchStart = "20230801"

// Scheduler 研究调度器：30s 节奏检查会话切换并驱动作业生命周期。
type Scheduler struct {
	cfgPath   string // config.json 路径
	statePath string // research_state.json 路径（幂等断点）
	now       func() time.Time

	mu            sync.Mutex
	baseCtx       context.Context    // 服务级上下文（退出时取消全部子进程）
	jobCancel     context.CancelFunc // 当前作业取消函数（kill 用）
	jobRunning    bool               // 是否有作业在跑
	tradingDLBusy bool               // 交易时段 dataload 是否在跑（防重叠）
	lastRunStep   string             // 最近执行步骤（日志）
	lastTrim      time.Time          // 盘中内存释放最近一次执行时间（节流用）
	state         stateFile
}

// stateFile 幂等状态：运行日 + 步骤进度 + 最近下载时间 + 最近步骤结果（阶段2.4 状态上报）。
// English: idempotent state — run day + step index + last download time + last step outcome.
type stateFile struct {
	Day          string `json:"day"`              // 当前作业所属运行日 YYYYMMDD
	StepIndex    int    `json:"step_index"`       // 已完成步骤下标（下一待跑步骤）
	Done         bool   `json:"done"`             // 当日作业是否已完成
	LastDataload int64  `json:"last_dataload_ts"` // 交易时段最近一次 dataload UnixNano（节流，与本地时区无关）
	// 最近一步的执行结果（前端 /api/research/progress 可见；排障用——曾发生 dataload 卡 21h 无感知）。
	// English: the latest step's outcome (surfaced via /api/research/progress; a dataload once hung 21h unnoticed).
	LastStep   string `json:"last_step,omitempty"`   // 步骤名
	LastStatus string `json:"last_status,omitempty"` // running/done/error/timeout
	LastError  string `json:"last_error,omitempty"`  // 失败原因
	LastAt     string `json:"last_at,omitempty"`     // 该结果的落盘时间
}

// New 创建调度器。cfgPath/statePath 为空时由 dataDir 推导。
func New(dataDir, cfgPath, statePath string) *Scheduler {
	if cfgPath == "" {
		cfgPath = filepath.Join(dataDir, "config.json")
	}
	if statePath == "" {
		statePath = filepath.Join(dataDir, "research_state.json")
	}
	return &Scheduler{
		cfgPath:   cfgPath,
		statePath: statePath,
		now:       time.Now,
	}
}

// Run 启动调度循环，直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	log.Printf("[scheduler] 研究调度器启动: cfg=%s state=%s", s.cfgPath, s.statePath)
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
	s.loadState()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.tick() // 启动立即检查一次（尤其跨天/重启场景）
	for {
		select {
		case <-ctx.Done():
			s.killRunning("服务退出")
			log.Printf("[scheduler] 研究调度器停止")
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick 单次调度检查：读配置 → 按会话分派。
func (s *Scheduler) tick() {
	cfg := config.LoadSchedulerConfig(s.cfgPath)
	if !cfg.Enabled {
		s.killRunning("调度器已禁用")
		return
	}
	now := s.now()
	if data.IsActiveSession(now) {
		// 交易时段：终止遗留夜间作业 + 盘中内存释放 + 可选的增量下载（只下载，不研究）
		s.killRunning("交易时段开始")
		s.trimInSession(cfg, now)
		s.maybeTradingDataload(cfg, now)
		return
	}
	// 盘后/周末：夜间作业
	s.ensureNightly(cfg, now)
}

// trimInSession 盘中内存治理：活跃时段按节流间隔执行
//  1. 防御性清理残留的 research 研究子进程（夜间作业被 OOM 击杀后遗留的孤儿进程，
//     绝不会让研究在盘中继续占用内存/CPU）；
//  2. 对 researchd 自身 runtime.GC()+debug.FreeOSMemory() 归还堆内存。
//
// 服务器物理内存仅 1.6GiB：盘中把内存让给 quant 常驻服务，盘后 quant 又让给 research，
// 两者互补，避免叠加 OOM。
// English: in-session memory governance — on a throttled cadence during active sessions it (1)
// defensively kills leftover `research` study child processes (orphans left behind when the nightly
// job was OOM-killed, so research never lingers intraday) and (2) GC+FreeOSMemory on researchd itself
// to return its heap. On the 1.6GiB box this hands RAM to quant during the day, which hands it back
// to research at night — complementary, no stacking OOM.
func (s *Scheduler) trimInSession(cfg config.SchedulerConfig, now time.Time) {
	interval := time.Duration(cfg.TrimIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	s.mu.Lock()
	due := now.Sub(s.lastTrim) >= interval
	if due {
		s.lastTrim = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	s.killOrphanResearch()
	runtime.GC()
	debug.FreeOSMemory()
	log.Printf("[scheduler] 盘中内存释放完成 (trim_interval_min=%d)", cfg.TrimIntervalMin)
}

// killOrphanResearch 防御性清理残留的 research 研究子进程：
// 只匹配"research 二进制且命令行含 discover"的进程（夜间 discover-factors 是唯一重内存任务，
// OOM 被击杀后可能遗留孤儿）。不碰 researchd 本身（进程名 researchd）、
// 不碰 quant 手动触发的回测子进程（命令行是 backtest 不含 discover）、不碰 dataload。
// English: defensively kills leftover `research` study child processes, matching only processes whose
// command line runs the research binary with a discover arg (the nightly discover-factors job is the
// only heavy-memory task and may leave orphans when OOM-killed). Never touches researchd itself
// (process name researchd), the quant-triggered backtest child (its arg is backtest, not discover),
// or dataload.
func (s *Scheduler) killOrphanResearch() {
	if _, err := exec.LookPath("pkill"); err != nil {
		return
	}
	if err := exec.Command("pkill", "-f", "research .*discover").Run(); err == nil {
		log.Printf("[scheduler] 盘中防御性清理残留 research 子进程")
	}
}

// maybeTradingDataload 交易时段按间隔跑 dataload daily（仅增量下载，绝不研究/回测）。
// 后台 goroutine 执行，不阻塞 30s 调度 tick；服务退出时随 baseCtx 一并终止。
func (s *Scheduler) maybeTradingDataload(cfg config.SchedulerConfig, now time.Time) {
	if !cfg.DataloadDuringTrade.Enabled {
		return
	}
	s.mu.Lock()
	if s.tradingDLBusy {
		s.mu.Unlock()
		return
	}
	last := s.state.LastDataload
	s.mu.Unlock()
	var lastT time.Time
	if last > 0 {
		lastT = time.Unix(0, last)
	}
	if !TradingDataloadDue(now, lastT, cfg) {
		return
	}
	s.mu.Lock()
	s.state.LastDataload = now.UnixNano()
	s.tradingDLBusy = true
	ctx := s.baseCtx
	if ctx == nil {
		ctx = context.Background() // 防御：直接调用 tick 的测试场景
	}
	s.mu.Unlock()
	s.saveState()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[scheduler] 交易时段 dataload panic: %v", r)
			}
			s.mu.Lock()
			s.tradingDLBusy = false
			s.mu.Unlock()
		}()
		if err := s.runStep(ctx, cfg, "dataload", now); err != nil {
			if ctx != nil && ctx.Err() == nil {
				log.Printf("[scheduler] 交易时段 dataload 失败: %v", err)
			}
		}
	}()
}

// ensureNightly 盘后/周末确保当日夜间作业正在运行。
func (s *Scheduler) ensureNightly(cfg config.SchedulerConfig, now time.Time) {
	s.mu.Lock()
	running := s.jobRunning
	day := s.state.Day
	done := s.state.Done
	s.mu.Unlock()

	today := now.Format("20060102")
	if running && day == today {
		return // 今日作业在跑，继续
	}
	// 上一日作业跨天仍在跑但今日已到启动时间 → 终止并启动今日作业（保证周六/周日各一轮）
	if running && day != today && NightlyEligible(now, cfg) {
		log.Printf("[scheduler] 上一日作业(%s)跨天仍在跑, 终止并启动今日作业(%s)", day, today)
		s.startNightly(cfg, now)
		return
	}
	if running {
		return // 上一日作业在跑但今日未到启动时间，让它继续跑完
	}
	// 无作业：当日已完成则跳过
	if day == today && done {
		return
	}
	if !NightlyEligible(now, cfg) {
		return
	}
	s.startNightly(cfg, now)
}

// startNightly 启动夜间作业：终止上一作业（跨日场景）后后台跑步骤链。
func (s *Scheduler) startNightly(cfg config.SchedulerConfig, now time.Time) {
	today := now.Format("20060102")
	s.killRunning("启动今日夜间作业") // 上一日未完成作业先终止，保证每日一跑
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.jobCancel = cancel
	s.jobRunning = true
	s.state.Day = today
	s.state.Done = false
	s.state.StepIndex = 0
	s.mu.Unlock()
	s.saveState()
	log.Printf("[scheduler] 夜间研究作业启动 (运行日 %s, 共 %d 步): %v",
		today, len(cfg.Nightly.Steps), cfg.Nightly.Steps)
	go s.runNightly(ctx, cfg, today)
}

// runNightly 顺序执行夜间作业各步骤，从断点续跑；作业内崩溃不拖垮调度循环。
func (s *Scheduler) runNightly(ctx context.Context, cfg config.SchedulerConfig, day string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[scheduler] 夜间作业 panic: %v", r)
		}
	}()
	steps := cfg.Nightly.Steps
	if len(steps) == 0 {
		steps = config.DefaultSchedulerConfig().Nightly.Steps
	}
	// 回测开关：开启时在 discover_factors 之后追加一次 B4 全链路回测（回填候选 avg_excess）。
	// 若用户已显式配置含 backtest 步骤，则不重复追加。
	// English: backtest toggle — when enabled, append a B4 full-chain backtest right after
	// discover_factors (backfills the candidate's avg_excess). No duplicate if already configured.
	if cfg.Nightly.BacktestEnabled && !containsStep(steps, "backtest") {
		steps = insertAfter(steps, "discover_factors", "backtest")
		log.Printf("[scheduler] 回测开关开启：夜间作业追加 backtest 步骤")
	}
	s.mu.Lock()
	startIdx := s.state.StepIndex
	s.mu.Unlock()
	for i := startIdx; i < len(steps); i++ {
		log.Printf("[scheduler] 夜间作业 %s 步骤 %d/%d: %s", day, i+1, len(steps), steps[i])
		err := s.runStep(ctx, cfg, steps[i], s.now())
		if err != nil {
			// 作业被终止（交易开市/跨日）时上下文取消导致 CommandContext 报错，属正常，直接返回。
			if ctx.Err() != nil {
				log.Printf("[scheduler] 夜间作业 %s 步骤 %s 被终止: %v", day, steps[i], ctx.Err())
				return
			}
			log.Printf("[scheduler] 夜间作业 %s 步骤 %s 失败: %v", day, steps[i], err)
			if cfg.Nightly.AbortOnError {
				s.mu.Lock()
				s.state.Done = true
				s.mu.Unlock()
				s.saveState()
				log.Printf("[scheduler] 夜间作业 %s 因步骤失败中止", day)
				s.finishNightly()
				return
			}
			// 不中止：标记当前步完成，继续下一步
		}
		s.mu.Lock()
		s.state.StepIndex = i + 1
		s.mu.Unlock()
		s.saveState()
	}
	s.mu.Lock()
	s.state.Done = true
	s.state.StepIndex = len(steps)
	s.mu.Unlock()
	s.saveState()
	log.Printf("[scheduler] 夜间研究作业完成 (运行日 %s)", day)
	s.finishNightly()
}

// finishNightly 清空作业运行态。
func (s *Scheduler) finishNightly() {
	s.mu.Lock()
	s.jobRunning = false
	s.jobCancel = nil
	s.mu.Unlock()
}

// killRunning 若当前有作业在跑则取消（kill 子进程），幂等。
func (s *Scheduler) killRunning(reason string) {
	s.mu.Lock()
	cancel := s.jobCancel
	s.jobCancel = nil
	s.jobRunning = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		log.Printf("[scheduler] 终止夜间作业: %s", reason)
	}
}

// runStep 执行单个步骤子进程：单步超时（阶段2.4，默认 90 分钟）超时 kill 并记 timeout；
// stdout/stderr 转日志；每步结果（running/done/error/timeout）写入 research_state.json 上报前端。
// ctx 取消时（CommandContext）直接杀子进程（SIGKILL）。
// English: runs one step child process with a per-step timeout (default 90 minutes) — on expiry the
// child is killed and the step marked timeout; stdout/stderr stream to logs; every outcome
// (running/done/error/timeout) lands in research_state.json for the frontend.
func (s *Scheduler) runStep(ctx context.Context, cfg config.SchedulerConfig, step string, now time.Time) error {
	bin, args, err := s.buildCommand(cfg, step, now)
	if err != nil {
		return err
	}
	dbPath := cfg.DB
	if dbPath == "" {
		dbPath = defaultDB()
	}
	// 夜间全量回测任务生命周期落库（kind='nightly', candidate_id=0）：running → done/error，
	// 与单候选回测共用 backtest_jobs 表，前端「回测」tab 可查看进度与结果。
	// English: persist the nightly full backtest job lifecycle (kind='nightly', candidate_id=0):
	// running → done/error, sharing the backtest_jobs table with per-candidate runs so the frontend
	// "backtest" tab can show its progress and result.
	if step == "backtest" {
		s.persistNightlyJob(dbPath, "running", "")
	}
	s.recordStepState(step, "running", "")
	// 单步超时：一步挂死不再拖死整链（曾发生 dataload 卡 21h、step_index 停在 0）。
	// English: per-step timeout so one hung step can't stall the chain (a dataload once hung 21h).
	timeout := time.Duration(cfg.StepTimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 90 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, bin, args...)
	cmd.Dir = dirOfDB(cfg)
	cmd.Stdout = &lineLogger{prefix: fmt.Sprintf("[scheduler:%s] ", step)}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		if step == "backtest" {
			s.persistNightlyJob(dbPath, "error", err.Error())
		}
		s.recordStepState(step, "error", err.Error())
		return fmt.Errorf("启动 %s: %w", step, err)
	}
	s.mu.Lock()
	s.lastRunStep = step
	s.mu.Unlock()
	if err := cmd.Wait(); err != nil {
		msg := err.Error()
		status := "error"
		if stepCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			// 单步超时（非作业被终止）：显式标记，便于排障区分。
			// English: per-step timeout (not a job cancellation) — flagged explicitly.
			status = "timeout"
			msg = fmt.Sprintf("单步超时(%s)被终止: %v", timeout, err)
			log.Printf("[scheduler] 步骤 %s 超时: %v", step, timeout)
		}
		if step == "backtest" {
			s.persistNightlyJob(dbPath, "error", msg)
		}
		s.recordStepState(step, status, msg)
		return fmt.Errorf("%s 退出异常: %w", step, err)
	}
	if step == "backtest" {
		s.persistNightlyJob(dbPath, "done", "")
	}
	s.recordStepState(step, "done", "")
	return nil
}

// recordStepState 把步骤执行结果写入幂等状态文件并落盘（阶段2.4 状态上报，前端可见）。
// English: records the step outcome into the idempotent state file (status reporting for the frontend).
func (s *Scheduler) recordStepState(step, status, errMsg string) {
	s.mu.Lock()
	s.state.LastStep = step
	s.state.LastStatus = status
	s.state.LastError = errMsg
	s.state.LastAt = time.Now().Format("2006-01-02 15:04:05")
	s.mu.Unlock()
	s.saveState()
}

// persistNightlyJob 把夜间全量回测任务状态写入 backtest_jobs（kind='nightly', candidate_id=0）。
// 临时打开研究库写入后即关闭（夜间低频，开库代价可忽略；WAL + busy_timeout 兼容多进程并发）。
// English: writes the nightly full backtest job state to backtest_jobs (kind='nightly', candidate_id=0).
// The research DB is opened briefly for the write and closed immediately (nightly writes are rare;
// WAL + busy_timeout already tolerate cross-process concurrency).
func (s *Scheduler) persistNightlyJob(dbPath, status, errMsg string) {
	db, err := store.Open(dbPath)
	if err != nil {
		log.Printf("[scheduler] 打开研究库写夜间回测任务失败: %v", err)
		return
	}
	defer db.Close()
	if err := db.UpsertBacktestJob(&store.BacktestJob{Kind: "nightly", CandidateID: 0, Status: status, Error: errMsg}); err != nil {
		log.Printf("[scheduler] 写夜间回测任务失败: %v", err)
		return
	}
	log.Printf("[scheduler] 夜间全量回测任务 -> %s", status)
}

// buildCommand 根据步骤名组装二进制与参数。
func (s *Scheduler) buildCommand(cfg config.SchedulerConfig, step string, now time.Time) (string, []string, error) {
	today := now.Format("20060102")
	db := cfg.DB
	if db == "" {
		db = defaultDB()
	}
	pyurl := cfg.PyURL
	if pyurl == "" {
		pyurl = "http://127.0.0.1:8787"
	}
	switch step {
	case "dataload":
		bin, err := s.resolveBin(cfg.DataloadBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{"--db", db, "--pyurl", pyurl, "daily"}, nil
	case "sector_rebuild":
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{"--db", db, "sector-rebuild"}, nil
	case "discover_factors":
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{
			"--db", db, "discover-factors",
			"--start", researchStart, "--end", today,
			"--h", "5", "--min-stocks", "20", "--max-factors", "8",
			"--split", "0.7", "--min-ir", "0.3", "--min-days", "30",
		}, nil
	case "discover_patterns":
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{
			"--db", db, "discover-patterns",
			"--start", researchStart, "--end", today,
			"--h", "5", "--min-trigger", "20", "--min-excess", "0.01", "--split", "0.7",
		}, nil
	case "backtest":
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		args := []string{"--db", db, "backtest", "--start", researchStart, "--end", today, "--h", "5"}
		if ev := cfg.Nightly.BacktestEvents; ev > 0 {
			args = append(args, "--max-per-day", strconv.Itoa(ev))
		}
		return bin, args, nil
	case "paper_research":
		// 模拟盘研究：读取盘后落库的模拟盘成交/净值快照，生成信号质量与绩效报告并落库。
		// English: paper research — reads the post-close paper fills/daily snapshots and produces a
		// signal-quality & performance report, saving it into the research DB.
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{"--db", db, "paper-research"}, nil
	case "list":
		bin, err := s.resolveBin(cfg.ResearchBin)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{"--db", db, "list"}, nil
	default:
		return "", nil, fmt.Errorf("未知步骤: %s", step)
	}
}

// containsStep 报告 steps 中是否包含指定步骤。
// （containsStep reports whether steps contains the given step.）
func containsStep(steps []string, step string) bool {
	for _, s := range steps {
		if s == step {
			return true
		}
	}
	return false
}

// insertAfter 在 steps 中 anchor 之后插入 step；anchor 不存在则追加到末尾。
// （insertAfter inserts step right after anchor; appends to the end if anchor is absent.）
func insertAfter(steps []string, anchor, step string) []string {
	for i, s := range steps {
		if s == anchor {
			out := make([]string, 0, len(steps)+1)
			out = append(out, steps[:i+1]...)
			out = append(out, step)
			out = append(out, steps[i+1:]...)
			return out
		}
	}
	return append(steps, step)
}

// defaultDB 返回研究库默认路径：QUANT_DATA_DIR 优先，否则 ~/.quant-trading-v2。
func defaultDB() string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return filepath.Join(d, "trading.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2", "trading.db")
}

// resolveBin 解析二进制路径：绝对/相对路径直接用，裸名先 PATH 再回退 researchd 同目录。
func (s *Scheduler) resolveBin(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("二进制路径为空")
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("二进制不存在: %s (%v)", name, err)
		}
		return name, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("找不到二进制 %s（配置 scheduler.research_bin / dataload_bin）", name)
}

// dirOfDB 返回研究库所在目录作为子进程工作目录（研究输出默认落库旁）。
func dirOfDB(cfg config.SchedulerConfig) string {
	if cfg.DB == "" {
		return ""
	}
	return filepath.Dir(cfg.DB)
}

// NightlyEligible 纯函数：当前时刻是否应启动今日夜间作业。
// 条件：非活跃时段 + 今日未完成 + 已达启动时间（交易日 15:30 / 周末 weekend_start_hhmm）。
// English: pure predicate — whether the nightly job should start now: inactive session, not yet
// done today, and past the start time (15:30 on trading days / weekend_start_hhmm on weekends).
func NightlyEligible(now time.Time, cfg config.SchedulerConfig) bool {
	if data.IsActiveSession(now) {
		return false
	}
	if now.Hour()*100+now.Minute() < nightStartHHMM(now, cfg) {
		return false
	}
	return true
}

// nightStartHHMM 返回适用当日的夜间启动时间：交易日 StartHHMM，周末 WeekendStartHHMM。
func nightStartHHMM(now time.Time, cfg config.SchedulerConfig) int {
	if data.IsTradingDay(now) {
		return cfg.Nightly.StartHHMM
	}
	return cfg.Nightly.WeekendStartHHMM
}

// TradingDataloadDue 纯函数：交易时段是否应触发一轮增量下载（按间隔节流）。
func TradingDataloadDue(now time.Time, last time.Time, cfg config.SchedulerConfig) bool {
	if !cfg.DataloadDuringTrade.Enabled {
		return false
	}
	if !data.IsActiveSession(now) {
		return false
	}
	interval := cfg.DataloadDuringTrade.IntervalMinutes
	if interval <= 0 {
		interval = 30
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= time.Duration(interval)*time.Minute
}

// loadState 从磁盘加载幂等状态（损坏/缺失则用零值）。
func (s *Scheduler) loadState() {
	raw, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	if err := json.Unmarshal(raw, &s.state); err != nil {
		log.Printf("[scheduler] 状态解析失败(重置): %v", err)
		s.state = stateFile{}
	}
}

// saveState 持久化状态（幂等断点）。
func (s *Scheduler) saveState() {
	s.mu.Lock()
	raw, err := json.MarshalIndent(s.state, "", "  ")
	s.mu.Unlock()
	if err != nil {
		log.Printf("[scheduler] 状态序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(s.statePath, raw, 0644); err != nil {
		log.Printf("[scheduler] 状态写入失败: %v", err)
	}
}

// lineLogger 把子进程 stdout/stderr 逐行带前缀转 log。
type lineLogger struct {
	prefix string
	buf    []byte
}

func (w *lineLogger) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		if len(line) > 0 {
			log.Printf("%s%s", w.prefix, line)
		}
	}
	return len(p), nil
}

// flush 兜底打印剩余无换行内容（Run 结束/测试用）。
func (w *lineLogger) flush() {
	if len(w.buf) > 0 {
		log.Printf("%s%s", w.prefix, w.buf)
		w.buf = nil
	}
}
