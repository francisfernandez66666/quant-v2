// 队列 worker（子系统统一改造一期核心）：出队 → spawn 唯一入口 run-task →
// 进度解析 / 控制标志轮询 / 看门狗 / kill 抢占 / 夜间链入队与收尾。
// 盘后硬门控：NightlyEligible 对一切任务生效（含手动 high），交易时段绝不出队。
// English: queue worker (phase-1 core) — dequeues, spawns the single run-task entry, parses progress,
// polls control flags, watchdogs stalls, kill-preempts, and enqueues/drains the nightly chain. The
// after-hours gate applies to every task including manual high-priority ones.
package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// taskProgressRe 统一进度协议：兼容各子命令既有的"回测/发现/任务进度 xx%"输出行。
var taskProgressRe = regexp.MustCompile(`(?:任务|回测|发现)进度 (\d+)%`)

// avgExcessRe 匹配 B4 回测 CLI 的平均超额（done 后写 result_num）。
var avgExcessRe = regexp.MustCompile(`平均超额=(-?\d+(?:\.\d+)?)`)

// btSummaryRe 匹配战法库回放汇总块（胜率/盈亏比等行集合，done 后拼 result_text）。
var btSummaryRe = regexp.MustCompile(`(?m)^(触发信号数|胜率|平均盈利|平均亏损|盈亏比|平均持仓天数):.*$`)

// parseBtSummary 从 bt_strategy 输出提取汇总行拼接为报告文本。
func parseBtSummary(out string) string {
	lines := btSummaryRe.FindAllString(out, -1)
	if len(lines) == 0 {
		return "无触发信号"
	}
	return strings.Join(lines, "；")
}

// openStore 懒加载队列库句柄；首次打开执行启动恢复（崩溃遗留 running→preempted 自动续跑）。
func (s *Scheduler) openStore(cfg config.SchedulerConfig) *store.DB {
	s.mu.Lock()
	db := s.storeDB
	reset := s.storeReset
	s.mu.Unlock()
	if db != nil {
		return db
	}
	p := cfg.DB
	if p == "" {
		p = defaultDB()
	}
	opened, err := store.Open(p)
	if err != nil {
		log.Printf("[scheduler] 打开研究队列失败(%s): %v", p, err)
		return nil
	}
	if !reset {
		if n, err := opened.ResetStaleRunningTasks(); err != nil {
			log.Printf("[scheduler] 启动恢复失败: %v", err)
		} else if n > 0 {
			log.Printf("[scheduler] 启动恢复：%d 个遗留运行任务标记为 preempted（盘后自动续跑）", n)
		}
		s.mu.Lock()
		s.storeReset = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.storeDB = opened
	s.mu.Unlock()
	return opened
}

// preemptCurrent 抢占/终止当前运行中的子进程（标 preemptReq，等待 runner 落终态）。
// 触发方：交易时段开始 / 调度器禁用 / 服务退出 / high 抢占 low。幂等。
// 竞态安全：即使子进程尚未 Start（taskCancel 未就绪），请求也先粘住，runner 就绪后立即补杀。
// English: kills the running child (sets a sticky preemptReq; the runner lands the terminal state).
// Race-safe: if the child hasn't started yet, the request sticks and the runner honors it on readiness.
func (s *Scheduler) preemptCurrent(reason string) {
	s.mu.Lock()
	if !s.busy || s.preemptReq {
		s.mu.Unlock()
		return
	}
	id, typ := int64(0), ""
	if s.curTask != nil {
		id, typ = s.curTask.ID, s.curTask.Type
	}
	s.preemptReq = true
	cancel := s.taskCancel // 可能为 nil（子进程尚未 Start）
	s.mu.Unlock()
	log.Printf("[scheduler] 终止当前任务 #%d(%s): %s", id, typ, reason)
	if cancel != nil {
		cancel() // CommandContext 取消 → SIGKILL 子进程
	}
}

// tryStartNext 空闲则出队下一个任务并启动（带盘后门控）。任务完成后的自驱排水入口，
// 不依赖 30s tick，长队列可在夜班窗口内连续消费。
// English: dequeues and starts the next task when idle (after-hours gated). Self-draining entry so a
// long queue drains continuously without waiting for ticks.
func (s *Scheduler) tryStartNext(db *store.DB, cfg config.SchedulerConfig) {
	if !NightlyEligible(s.now(), cfg) {
		return // 盘后硬门控（需求#4）：含手动任务
	}
	next, err := db.DequeueHighestTask()
	if err != nil || next == nil {
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = true
	cp := *next
	s.curTask = &cp
	s.preemptReq, s.cancelReq, s.paused = false, false, false
	s.lastProgress = time.Now().Unix()
	s.mu.Unlock()
	ok, err := db.ClaimResearchTask(next.ID)
	if err != nil || !ok {
		s.mu.Lock()
		s.busy = false
		s.curTask = nil
		s.mu.Unlock()
		if err != nil {
			log.Printf("[scheduler] 认领任务 #%d 失败: %v", next.ID, err)
		}
		return
	}
	go s.runTask(db, cfg, cp)
}

// workerTick 盘后队列驱动：门控 → 夜间链入队 → 抢占检查 / 出队执行。
func (s *Scheduler) workerTick(cfg config.SchedulerConfig, now time.Time) {
	db := s.openStore(cfg)
	if db == nil {
		return
	}
	// 盘后硬门控（需求#4）：未到启动时间/盘中一律不取任务——手动 high 同样排队等待。
	if !NightlyEligible(now, cfg) {
		return
	}
	s.ensureNightlyEnqueue(db, cfg, now)

	s.mu.Lock()
	busy := s.busy
	var curID int64
	curPrio := ""
	if s.curTask != nil {
		curID = s.curTask.ID
		curPrio = s.curTask.Priority
	}
	s.mu.Unlock()

	next, err := db.DequeueHighestTask()
	if err != nil || next == nil {
		return
	}
	if busy {
		// 决策#1 kill 抢占：high 到来且当前是 low → 杀掉当前子进程（preempted 自动回队首）。
		if next.ID != curID && next.Priority == "high" && curPrio == "low" {
			s.preemptCurrent(fmt.Sprintf("高优先级任务 #%d(%s) 抢占", next.ID, next.Type))
		}
		return
	}
	s.tryStartNext(db, cfg)
}

// ensureNightlyEnqueue 幂等入队当日夜间步骤链（low、chain_day=今天、chain_seq 递增）。
// 队列即断点：researchd 重启后已入队未完成的任务天然续跑，不再依赖 research_state.json 步骤下标。
// 跨日残留的旧链任务按 chain_day 升序先于今日执行（保留已完成工作量，优于旧的直接杀掉重来）。
// English: idempotently enqueues today's nightly step chain (low priority). The queue itself is the
// checkpoint — restarts resume naturally; leftover chains from prior days drain first by chain_day.
func (s *Scheduler) ensureNightlyEnqueue(db *store.DB, cfg config.SchedulerConfig, now time.Time) {
	today := now.Format("20060102")
	has, err := db.ChainHasTasks(today)
	if err != nil || has {
		return
	}
	steps := cfg.Nightly.Steps
	if len(steps) == 0 {
		steps = config.DefaultSchedulerConfig().Nightly.Steps
	}
	// 回测开关：开启时在 discover_factors 之后追加一次 B4 全链路回测（回填候选 avg_excess）。
	if cfg.Nightly.BacktestEnabled && !containsStep(steps, "backtest") {
		steps = insertAfter(steps, "discover_factors", "backtest")
		log.Printf("[scheduler] 回测开关开启：夜间链追加 backtest 任务")
	}
	for i, step := range steps {
		typ, payload, ok := stepTask(step, cfg, today)
		if !ok {
			log.Printf("[scheduler] 未知夜间步骤 %q 跳过", step)
			continue
		}
		if _, err := db.EnqueueResearchTask(&store.ResearchTask{
			Type: typ, Priority: "low", Status: store.TaskQueued,
			Payload: payload, ChainDay: today, ChainSeq: i,
		}); err != nil {
			log.Printf("[scheduler] 入队夜间任务 %s 失败: %v", step, err)
			return
		}
	}
	s.mu.Lock()
	s.state.Day = today
	s.state.Done = false
	s.mu.Unlock()
	s.saveState()
	log.Printf("[scheduler] 夜间链 %s 已入队 %d 个 low 任务: %v", today, len(steps), steps)
}

// containsStep 报告 steps 中是否包含指定步骤。
func containsStep(steps []string, step string) bool {
	for _, s := range steps {
		if s == step {
			return true
		}
	}
	return false
}

// insertAfter 在 steps 中 anchor 之后插入 step；anchor 不存在则追加到末尾。
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

// stepTask 夜间步骤 → (任务类型, payload JSON)。payload 与旧 buildCommand 参数一一对应，
// 由 run-task 分发器展平为子命令 CLI 参数。
func stepTask(step string, cfg config.SchedulerConfig, today string) (string, string, bool) {
	switch step {
	case "dataload":
		pyurl := cfg.PyURL
		if pyurl == "" {
			pyurl = "http://127.0.0.1:8787"
		}
		return store.TaskDataload, mustJSON(map[string]any{"pyurl": pyurl}), true
	case "sector_rebuild":
		return store.TaskSectorRebuild, "{}", true
	case "discover_factors":
		return store.TaskDiscoverFactors, mustJSON(map[string]any{
			"start": researchStart, "end": today,
			"h": 5, "min-stocks": 20, "max-factors": 8,
			"split": 0.7, "min-ir": 0.3, "min-days": 30,
		}), true
	case "discover_patterns":
		return store.TaskDiscoverPatterns, mustJSON(map[string]any{
			"start": researchStart, "end": today,
			"h": 5, "min-trigger": 20, "min-excess": 0.01, "split": 0.7,
		}), true
	case "backtest":
		p := map[string]any{"start": researchStart, "end": today, "h": 5}
		if ev := cfg.Nightly.BacktestEvents; ev > 0 {
			p["max-per-day"] = ev
		}
		return store.TaskBacktestNightly, mustJSON(p), true
	case "paper_research":
		return store.TaskPaperResearch, "{}", true
	case "list":
		return store.TaskList, "{}", true
	}
	return "", "", false
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// taskCommand 组装任务的二进制与参数：dataload 直连专用二进制；
// 其余类型统一走 research run-task --task-id（唯一入口，进程名与 verify_nightly.sh 兼容）。
// English: builds the child command — dataload runs its own binary; everything else funnels through
// `research run-task --task-id N`, keeping the process name compatible with verify_nightly.sh.
func (s *Scheduler) taskCommand(cfg config.SchedulerConfig, tk *store.ResearchTask) (string, []string, error) {
	dbPath := cfg.DB
	if dbPath == "" {
		dbPath = defaultDB()
	}
	if tk.Type == store.TaskDataload {
		bin, err := s.resolveBin(cfg.DataloadBin)
		if err != nil {
			return "", nil, err
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(tk.Payload), &p)
		pyurl := "http://127.0.0.1:8787"
		if v, ok := p["pyurl"].(string); ok && v != "" {
			pyurl = v
		}
		return bin, []string{"--db", dbPath, "--pyurl", pyurl, "daily"}, nil
	}
	bin, err := s.resolveBin(cfg.ResearchBin)
	if err != nil {
		return "", nil, err
	}
	return bin, []string{"--db", dbPath, "run-task", "--task-id", strconv.FormatInt(tk.ID, 10)}, nil
}

// runTask 执行单个队列任务子进程：进度逐行解析回写、控制标志轮询（pause/resume/cancel）、
// 高优先级任务的进度停滞看门狗（15 分钟无进展 kill 置 error；低优先级沿用单步硬超时）、
// 终态落库 + 链收尾（AbortOnError 取消同链后续 / 链排空置 Done）。
// English: runs one queued task — streams and parses output into progress, polls control flags,
// stall-watchdogs only high-priority tasks (lows rely on the per-step hard timeout), lands terminal
// state, then handles chain bookkeeping (AbortOnError sibling cancellation / chain-drained Done).
func (s *Scheduler) runTask(db *store.DB, cfg config.SchedulerConfig, tk store.ResearchTask) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[scheduler] 任务 #%d panic: %v", tk.ID, r)
		}
		s.mu.Lock()
		s.busy = false
		s.taskCancel = nil
		s.curTask = nil
		s.paused = false
		s.mu.Unlock()
		// 自驱排水：本任务终态落库后立即尝试下一个（不依赖 30s tick）。
		s.tryStartNext(db, cfg)
	}()
	// preemptReq/cancelReq/paused 已在 tryStartNext 预占时清零；此处不重复复位，
	// 避免抹掉"预占到启动之间"到达的抢占请求（竞态窗口）。
	base := func() context.Context {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.baseCtx == nil {
			return context.Background()
		}
		return s.baseCtx
	}()
	log.Printf("[scheduler] 任务 #%d(%s prio=%s ref=%d chain=%s/%d) 启动",
		tk.ID, tk.Type, tk.Priority, tk.RefID, tk.ChainDay, tk.ChainSeq)

	fail := func(errMsg string) {
		log.Printf("[scheduler] 任务 #%d(%s) 失败: %s", tk.ID, tk.Type, errMsg)
		_ = db.UpdateTaskRunState(tk.ID, store.TaskError, "", 0, "", errMsg)
		s.finishTask(db, cfg, &tk, store.TaskError, errMsg)
	}

	bin, args, err := s.taskCommand(cfg, &tk)
	if err != nil {
		fail(err.Error())
		return
	}
	timeout := time.Duration(cfg.StepTimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 90 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dirOfDB(cfg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail("打开子进程输出失败: " + err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fail("打开子进程错误输出失败: " + err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		fail("启动子进程失败: " + err.Error())
		return
	}
	s.mu.Lock()
	s.taskCancel = cancel
	pendingPreempt := s.preemptReq
	s.mu.Unlock()
	if pendingPreempt {
		// 预占到启动之间到达的抢占请求：子进程已就绪，立即补杀。
		cancel()
	}

	// 输出收集 + 进度解析（stdout/stderr 合并按行处理）
	var rs struct {
		mu       sync.Mutex
		out      bytes.Buffer
		progress string
	}
	scan := func(r io.Reader) {
		br := bufio.NewReader(r)
		for {
			line, rerr := br.ReadString('\n')
			if len(line) > 0 {
				rs.mu.Lock()
				rs.out.WriteString(line)
				rs.mu.Unlock()
				log.Printf("[task#%d:%s] %s", tk.ID, tk.Type,
					strings.TrimRight(line, "\r\n"))
				if m := taskProgressRe.FindStringSubmatch(line); len(m) == 2 {
					rs.mu.Lock()
					rs.progress = m[1] + "%"
					rs.mu.Unlock()
					s.mu.Lock()
					s.lastProgress = time.Now().Unix()
					s.mu.Unlock()
					_ = db.UpdateTaskRunState(tk.ID, store.TaskRunning, m[1]+"%", 0, "", "")
				}
			}
			if rerr != nil {
				return
			}
		}
	}
	done := make(chan struct{})
	go func() { scan(stdout); scan(stderr); close(done) }()

	// 控制标志轮询（~2s）：API 只写 control 列，这里消费并转成进程信号。
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-runCtx.Done():
				return
			case <-t.C:
			}
			c, err := db.ConsumeTaskControl(tk.ID)
			if err != nil || c == "" {
				continue
			}
			switch c {
			case store.ControlPause:
				s.mu.Lock()
				paused := s.paused
				s.mu.Unlock()
				if !paused && cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGSTOP)
					s.mu.Lock()
					s.paused = true
					s.mu.Unlock()
					rs.mu.Lock()
					pg := rs.progress
					rs.mu.Unlock()
					_ = db.UpdateTaskRunState(tk.ID, store.TaskPaused, pg, 0, "", "")
				}
			case store.ControlResume:
				s.mu.Lock()
				paused := s.paused
				if !paused {
					s.mu.Unlock()
					continue
				}
				s.paused = false
				s.lastProgress = time.Now().Unix()
				s.mu.Unlock()
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGCONT)
				}
				rs.mu.Lock()
				pg := rs.progress
				rs.mu.Unlock()
				_ = db.UpdateTaskRunState(tk.ID, store.TaskRunning, pg, 0, "", "")
			case store.ControlCancel:
				s.mu.Lock()
				s.cancelReq = true
				s.mu.Unlock()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		}
	}()

	// 看门狗：仅高优先级手动任务启用（与旧 server 行为一致）；夜间链任务靠硬超时兜底。
	if tk.Priority == "high" {
		const stallSecs = 15 * 60
		watchStop := make(chan struct{})
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-watchStop:
					return
				case <-runCtx.Done():
					return
				case <-t.C:
					s.mu.Lock()
					stalled := !s.paused && !s.cancelReq && !s.preemptReq &&
						time.Now().Unix()-s.lastProgress > stallSecs
					s.mu.Unlock()
					if stalled {
						log.Printf("[scheduler] 任务 #%d 进度停滞>%dm，看门狗终止", tk.ID, stallSecs/60)
						_ = cmd.Process.Kill()
						return
					}
				}
			}
		}()
		defer close(watchStop)
	}

	waitErr := cmd.Wait()
	<-done
	close(quit)
	cancel() // 唤醒可能阻塞在 select 的轮询 goroutine

	rs.mu.Lock()
	progress := rs.progress
	fullOut := rs.out.String()
	rs.mu.Unlock()
	if progress == "" {
		progress = "100%"
	}

	// 终态判定（优先级：抢占 > 用户取消 > 单步超时 > 运行错误 > 成功）
	status := store.TaskDone
	errMsg := ""
	var resultNum float64
	resultText := ""
	s.mu.Lock()
	preempted, cancelled := s.preemptReq, s.cancelReq
	s.mu.Unlock()
	switch {
	case preempted:
		status = store.TaskPreempted
		errMsg = "被抢占或会话终止，断点缓存有效（盘后自动回队续跑）"
	case cancelled:
		status = store.TaskCancelled
		errMsg = "用户取消"
	case runCtx.Err() == context.DeadlineExceeded && base.Err() == nil:
		status = store.TaskError
		errMsg = fmt.Sprintf("单步超时(%v)被终止", timeout)
	case waitErr != nil:
		status = store.TaskError
		errMsg = waitErr.Error()
	}
	if status == store.TaskDone {
		switch tk.Type {
		case store.TaskBacktestCandidate, store.TaskBacktestNightly:
			if m := avgExcessRe.FindStringSubmatch(fullOut); len(m) == 2 {
				resultNum, _ = strconv.ParseFloat(m[1], 64)
			}
		case store.TaskBacktestStrategy:
			resultText = parseBtSummary(fullOut)
		}
	}
	if err := db.UpdateTaskRunState(tk.ID, status, progress, resultNum, resultText, errMsg); err != nil {
		log.Printf("[scheduler] 任务 #%d 终态落库失败: %v", tk.ID, err)
	}
	if status == store.TaskPreempted {
		_ = db.RequeueTask(tk.ID) // preempted → queued（队首优先级由出队排序保证）
	}
	log.Printf("[scheduler] 任务 #%d(%s) -> %s%s", tk.ID, tk.Type, status, tailOf(errMsg))
	s.finishTask(db, cfg, &tk, status, errMsg)
}

// finishTask 任务收尾：展示状态上报 + 链治理（AbortOnError 取消同链剩余；链排空置 Done）。
func (s *Scheduler) finishTask(db *store.DB, cfg config.SchedulerConfig, tk *store.ResearchTask, status, errMsg string) {
	// 展示兼容映射：preempted/cancelled 对外沿用旧语义 "interrupted"。
	disp := status
	switch status {
	case store.TaskPreempted, store.TaskCancelled:
		disp = "interrupted"
	case store.TaskPaused:
		disp = "paused"
	}
	s.recordStepState(tk.Type, disp, errMsg)

	if tk.ChainDay == "" {
		return
	}
	if status == store.TaskError && cfg.Nightly.AbortOnError {
		if n, err := db.CancelChainTasks(tk.ChainDay); err == nil && n > 0 {
			log.Printf("[scheduler] AbortOnError：取消同链 %s 剩余 %d 个任务", tk.ChainDay, n)
		}
	}
	active, err := db.ActiveResearchTasks()
	if err != nil {
		return
	}
	for _, t := range active {
		if t.ChainDay == tk.ChainDay {
			return // 链尚未排空
		}
	}
	s.mu.Lock()
	if s.state.Day == tk.ChainDay {
		s.state.Done = true
	}
	s.mu.Unlock()
	s.saveState()
	log.Printf("[scheduler] 夜间链 %s 已全部完成", tk.ChainDay)
}

// recordStepState 把任务结果写入状态文件（前端 /api/research/progress 可见，排障用）。
func (s *Scheduler) recordStepState(step, status, errMsg string) {
	s.mu.Lock()
	s.state.LastStep = step
	s.state.LastStatus = status
	s.state.LastError = errMsg
	s.state.LastAt = time.Now().Format("2006-01-02 15:04:05")
	s.mu.Unlock()
	s.saveState()
}

func tailOf(msg string) string {
	if msg == "" {
		return ""
	}
	return "（" + msg + "）"
}

// execDataload 交易时段增量下载直连通道（不入研究队列；只下载绝不研究）。
// English: intraday incremental dataload lane — direct execution, never queued with research.
func (s *Scheduler) execDataload(ctx context.Context, cfg config.SchedulerConfig, now time.Time) error {
	tk := &store.ResearchTask{Type: store.TaskDataload}
	bin, args, err := s.taskCommand(cfg, tk)
	if err != nil {
		return err
	}
	timeout := time.Duration(cfg.StepTimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 90 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, bin, args...)
	cmd.Dir = dirOfDB(cfg)
	logger := &lineLogger{prefix: "[dataload] "}
	cmd.Stdout = logger
	cmd.Stderr = logger
	log.Printf("[dataload] 交易时段增量下载启动")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 dataload: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		logger.flush()
		if stepCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return fmt.Errorf("dataload 单步超时(%v): %w", timeout, err)
		}
		return err
	}
	logger.flush()
	return nil
}
