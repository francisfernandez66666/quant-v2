// 队列 worker（子系统统一改造一期核心）：出队 → spawn 唯一入口 run-task →
// 进度解析 / 控制标志轮询 / 看门狗 / kill 抢占 / 夜间链入队与收尾。
// 盘后硬门控：NightlyEligible 对一切任务生效（含手动 high），交易时段绝不出队。
// English: queue worker (phase-1 core) — dequeues, spawns the single run-task entry, parses progress,
// polls control flags, watchdogs stalls, kill-preempts, and enqueues/drains the nightly chain. The
// after-hours gate applies to every task including manual high-priority ones.
package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// taskProgressRe 统一进度协议：兼容各子命令既有的"回测/发现/任务/参数优化进度 xx%"输出行。
// 扩充"参数优化进度"以覆盖 §P2 全库寻优（sweep）任务的进度回报，避免其进度长期为空被误判停滞。
// English: progress protocol — matches backtest/discover/task/param-optimize progress lines.
var taskProgressRe = regexp.MustCompile(`(?:任务|回测|发现|参数优化)进度 (\d+)%`)

// avgExcessRe 匹配 B4 回测 CLI 的平均超额（done 后写 result_num）。
var avgExcessRe = regexp.MustCompile(`平均超额=(-?\d+(?:\.\d+)?)`)

// btNameZh 内置战法适配器英文名 → 中文显示名（与前端 builtinPatterns/序号映射一致）。
var btNameZh = map[string]string{
	"DoubleBump": "双响炮", "Dragon": "龙头", "DragonReturn": "龙回头", "NShape": "N形",
}

// 回放汇总报告解析：btreplay printReport 每个战法输出一个 ===== 包围的块，
// 头行为「战法历史回测: <名>（N 只股票）」。旧实现抓指标行丢名字，多战法块无法区分。
var (
	btNameRe    = regexp.MustCompile(`(?m)^战法历史回测: (.+?)（`)
	btTriggerRe = regexp.MustCompile(`(?m)^触发信号数: (\d+)`)
	btWinRe     = regexp.MustCompile(`(?m)^胜率: (\d+(?:\.\d+)?)%`)
	btPfRe      = regexp.MustCompile(`(?m)^盈亏比: (\d+(?:\.\d+)?)`)
	btHoldRe    = regexp.MustCompile(`(?m)^平均持仓天数: (\d+(?:\.\d+)?)`)
	btExpectRe  = regexp.MustCompile(`(?m)^期望收益: ([+-]\d+(?:\.\d+)?)%`)
)

// parseBtSummary 按 ===== 分隔块解析回放汇总，每个战法一行、冠以名称标签：
// 【双响炮】胜率 47.78% 盈亏比 1.31 触发 270 持仓 1.0天
// English: one labeled line per strategy block, e.g. 【双响炮】win 47.78% PF 1.31 ...
func parseBtSummary(out string) string {
	blocks := strings.Split(out, "==============================================")
	var rows []string
	for _, blk := range blocks {
		var name string
		if m := btNameRe.FindStringSubmatch(blk); len(m) == 2 {
			name = strings.TrimSpace(m[1])
			if zh, ok := btNameZh[name]; ok {
				name = zh
			}
		}
		if name == "" || !strings.Contains(blk, "胜率:") && !strings.Contains(blk, "无触发信号") {
			continue // 分隔线之间的非报告文本（进度行等）
		}
		if strings.Contains(blk, "无触发信号") {
			rows = append(rows, fmt.Sprintf("【%s】无触发信号", name))
			continue
		}
		row := fmt.Sprintf("【%s】", name)
		if m := btWinRe.FindStringSubmatch(blk); len(m) == 2 {
			row += fmt.Sprintf("胜率 %s%% ", m[1])
		}
		if m := btPfRe.FindStringSubmatch(blk); len(m) == 2 {
			row += fmt.Sprintf("盈亏比 %s ", m[1])
		}
		if m := btTriggerRe.FindStringSubmatch(blk); len(m) == 2 {
			row += fmt.Sprintf("触发 %s ", m[1])
		}
		if m := btHoldRe.FindStringSubmatch(blk); len(m) == 2 {
			row += fmt.Sprintf("持仓 %s天", m[1])
		}
		if m := btExpectRe.FindStringSubmatch(blk); len(m) == 2 {
			row += fmt.Sprintf(" 期望 %s%%", m[1])
		}
		rows = append(rows, strings.TrimSpace(row))
	}
	if len(rows) == 0 {
		return "无触发信号"
	}
	return strings.Join(rows, "\n")
}

// numField 从结果 map 抽取数值字段：section 非空时从嵌套对象（如 params）取，否则取顶层。
// English: extracts a numeric field; when section is set, reads from a nested object (e.g. params).
func numField(r map[string]any, section, key string) float64 {
	if section != "" {
		if sec, ok := r[section].(map[string]any); ok {
			if v, ok := sec[key].(float64); ok {
				return v
			}
		}
		return 0
	}
	if v, ok := r[key].(float64); ok {
		return v
	}
	return 0
}

// buildOptimizeSummary 把 optimize 任务的 SWEEP_JSON 输出整理为可读的回测结果文本：
// 战法说明（寻优目标/组合数）+ 各战法冠军参数与指标 + 核心结论。替代原先仅截取前 100 字符的占位日志。
// English: turns the optimize task's SWEEP_JSON into a readable result: objective/combination count,
// per-strategy champion params & metrics, and a core verdict.
func buildOptimizeSummary(out string) string {
	lines := strings.Split(out, "\n")
	var rows []string
	objectives := map[string]bool{}
	total := 0
	bestEv := math.Inf(-1)
	bestLine := ""
	for _, line := range lines {
		m := sweepJSONRe.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		var payload struct {
			Strategy  string           `json:"strategy"`
			Objective string           `json:"objective"`
			Results   []map[string]any `json:"results"`
		}
		if err := json.Unmarshal([]byte(m[1]), &payload); err != nil {
			continue
		}
		if payload.Objective != "" {
			objectives[payload.Objective] = true
		}
		for _, r := range payload.Results {
			strat, _ := r["strategy"].(string)
			if ns, _ := r["no_signal"].(bool); ns {
				rows = append(rows, fmt.Sprintf("【%s】无触发信号", strat))
				continue
			}
			total++
			tp := numField(r, "params", "take_profit_pct")
			sl := numField(r, "params", "stop_loss_pct")
			hold := numField(r, "params", "hold_days")
			thr := numField(r, "params", "min_score")
			wr := numField(r, "", "win_rate")
			pf := numField(r, "", "profit_factor")
			ev := numField(r, "", "expectancy")
			ln := fmt.Sprintf("【%s】止盈%.0f%% 止损%.0f%% 持仓%.0f天 门槛%.0f → 胜率%.1f%% 盈亏比%.2f 期望%.2f%%",
				strat, tp, sl, hold, thr, wr, pf, ev)
			if ev > bestEv {
				bestEv = ev
				bestLine = ln
			}
			rows = append(rows, ln)
		}
	}
	if len(rows) == 0 {
		return parseBtSummary(out)
	}
	header := fmt.Sprintf("全库参数寻优完成：共 %d 组参数组合", total)
	if len(objectives) > 0 {
		objs := make([]string, 0, len(objectives))
		for o := range objectives {
			objs = append(objs, o)
		}
		header += "（目标：" + strings.Join(objs, "/") + "）"
	}
	concl := "核心结论："
	if bestEv > 0 {
		concl += "存在正期望参数组合，建议取冠军参数小仓位实盘验证；"
	} else {
		concl += "未发现正期望组合，当前样本下该目标暂不宜实盘；"
	}
	concl += "完整排名见「优化结果 / 参数寻优中心」。"
	if bestEv > math.Inf(-1) && bestLine != "" {
		concl += "\n冠军：" + bestLine
	}
	return header + "\n" + strings.Join(rows, "\n") + "\n" + concl
}

// sweepJSONRe 匹配子进程输出的机器可读扫参结果行。
var sweepJSONRe = regexp.MustCompile(`(?m)^SWEEP_JSON:(\{.*\})\s*$`)

// saveSweepResults 解析 optimize 任务输出的 SWEEP_JSON（每战法一条），把排名落 optimization_results。
// 失败只记日志不回滚任务状态——排名表是展示/审批增强，主结果在 result_text 已保底。
// English: parses SWEEP_JSON lines (one per strategy) and persists rankings; failures are logged
// without failing the task because the main result is already in result_text.
func (s *Scheduler) saveSweepResults(db *store.DB, taskID int64, out string) {
	lines := strings.Split(out, "\n")
	log.Printf("[scheduler] 任务 #%d saveSweepResults: 共 %d 行", taskID, len(lines))
	matched := 0
	// 遍历输出中所有 SWEEP_JSON 行（每战法一条，独立落库）
	for _, line := range lines {
		m := sweepJSONRe.FindStringSubmatch(line)
		if len(m) != 2 {
			if strings.Contains(line, "SWEEP_JSON") {
				log.Printf("[scheduler] 任务 #%d SWEEP_JSON 行未匹配 regex: 行前30=%q", taskID, line[:min(len(line), 30)])
			}
			continue
		}
		matched++
		var payload struct {
			Strategy  string           `json:"strategy"`
			Objective string           `json:"objective"`
			Batches   []map[string]any `json:"batches"`
			Grid      []map[string]any `json:"grid"`
			Results   []map[string]any `json:"results"`
		}
		if err := json.Unmarshal([]byte(m[1]), &payload); err != nil {
			log.Printf("[scheduler] 任务 #%d SWEEP_JSON 解析失败: %v", taskID, err)
			continue
		}
		if err := db.SaveOptimizationResults(taskID, payload.Objective, payload.Results); err != nil {
			log.Printf("[scheduler] 任务 #%d 扫参排名落库失败: %v", taskID, err)
			continue
		}
		// §D2 冠军行附带信息（热力网格 + 批次冠军明细）回写 grid_json，前端详情渲染源
		if len(payload.Grid) > 0 || len(payload.Batches) > 0 {
			if extra, jerr := json.Marshal(map[string]any{
				"grid": payload.Grid, "batches": payload.Batches,
			}); jerr == nil && len(payload.Results) > 0 {
				strategy, _ := payload.Results[0]["strategy"].(string)
				_ = db.UpdateOptimizationGrid(taskID, strategy, string(extra))
			}
		}
		log.Printf("[scheduler] 任务 #%d 扫参排名已落库：%s %d 条（目标 %s）",
			taskID, payload.Strategy, len(payload.Results), payload.Objective)
	}
	log.Printf("[scheduler] 任务 #%d saveSweepResults 完成: 匹配 %d 条 SWEEP_JSON", taskID, matched)
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
			opslog.Logf("research", "启动恢复 %d 个遗留任务为 preempted（盘后续跑）", n)
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

// drainAllowed 出队许可：盘后窗口内恒可；窗口外仅当存在 preempted 遗留任务
// （续跑排水，见 workerTick 注释）。English: dequeue permission — always inside the evening
// window; outside it only to drain preempted leftovers.
func (s *Scheduler) drainAllowed(db *store.DB, cfg config.SchedulerConfig) bool {
	if NightlyEligible(s.nowTime(), cfg) {
		return true
	}
	leftovers, err := db.ActiveResearchTasks()
	if err != nil {
		return false
	}
	for _, t := range leftovers {
		if t.Status == store.TaskPreempted {
			return true
		}
	}
	return false
}

// PreemptForShutdown 停机前置钩子（researchd 收到 SIGTERM 时最先调用）：把当前运行任务
// 标记抢占态，使其终态落 preempted（断点续跑）而非 error。必须在取消调度 ctx 之前调用；
// 与 Run 循环里的"服务退出"抢占互为双保险（D-state 子进程退出慢时存在竞态）。
// English: shutdown pre-hook — mark sticky preemptReq so the running task lands 'preempted'.
func (s *Scheduler) PreemptForShutdown() {
	s.mu.Lock()
	if s.busy {
		s.preemptReq = true
	}
	s.mu.Unlock()
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
	cur := s.curCmd        // 当前子进程（可能为 nil），用于整组击杀
	s.mu.Unlock()
	log.Printf("[scheduler] 终止当前任务 #%d(%s): %s", id, typ, reason)
	opslog.Logf("research", "抢占任务 #%d(%s): %s", id, typ, reason)
	// §修复 S3（2026-08-29）：整组击杀而非仅 SIGKILL 直接子进程。子进程设了 Setpgid，
	// 孙进程（如 research run-task 派生的计算进程）若只杀直接子进程会成为孤儿继续写库。
	if cur != nil {
		killProcessGroup(cur)
	}
	if cancel != nil {
		cancel() // CommandContext 取消兜底（单杀 + 取消 context）
	}
}

// memGateMB 内存总闸阈值（可经 rules.scheduler.min_free_mem_mb 覆盖，默认 400）：
// 系统 MemAvailable 低于该值时一律不出队——研究任务再重要也不能把整机挤进
// swap 死锁（2026-08-23 实录：discover_factors 峰值 + quant 并发 → SSH/HTTPS 全僵死，
// OOM 重启 → 排水重跑 → 再 OOM 的 crash loop）。English: global memory gate — when system
// MemAvailable drops below the threshold, no task is dequeued (everything stays queued).
const memGateDefaultMB = 400

// readMemAvailableMB 系统可用内存（MB）；读取失败返回 -1（闸门放行，不因读数失败卡死队列）。
// 平台实现见 memgate_unix.go（/proc/meminfo）/ memgate_windows.go（GlobalMemoryStatusEx）。
func readMemAvailableMB() int {
	return platformMemAvailableMB()
}

// memGateOpen 内存总闸判定：MemAvailable ≥ 阈值（或无法读取）时放行。
// English: gate passes when MemAvailable is above the threshold (or unreadable).
func memGateOpen(cfg config.SchedulerConfig) bool {
	thresh := cfg.MinFreeMemMB
	if thresh <= 0 {
		thresh = memGateDefaultMB
	}
	return memGateDecide(readMemAvailableMB(), thresh)
}

// memGateDecide 纯决策：avail<0（无法读数）放行不卡队；其余按阈值比较。
func memGateDecide(availMB, threshMB int) bool {
	if availMB < 0 {
		return true
	}
	return availMB >= threshMB
}

// tryStartNext 空闲则出队下一个任务并启动（带盘后门控+内存总闸）。任务完成后的自驱排水入口，
// 不依赖 30s tick，长队列可在夜班窗口内连续消费。
// English: dequeues and starts the next task when idle (after-hours + memory gated). Self-draining.
func (s *Scheduler) tryStartNext(db *store.DB, cfg config.SchedulerConfig) {
	if !s.drainAllowed(db, cfg) {
		return // 盘后硬门控（需求#4）：未到窗口且无遗留续跑时不出队
	}
	if !memGateOpen(cfg) {
		// 内存总闸：系统可用内存不足——一切任务留队等待，绝不与量化主程序/系统抢内存。
		opslog.OncePer("memgate", time.Hour, func() {
			opslog.Logf("research", "内存总闸拦截 MemAvailable=%dMB 阈值=%dMB 任务留队", readMemAvailableMB(), cfg.MinFreeMemMB)
		})
		log.Printf("[scheduler] 内存总闸拦截：MemAvailable=%dMB < %dMB，本轮不出队（任务留队）",
			readMemAvailableMB(), func() int {
				t := cfg.MinFreeMemMB
				if t <= 0 {
					return memGateDefaultMB
				}
				return t
			}())
		return
	}
	next, err := db.DequeueHighestTask()
	if err != nil || next == nil {
		return
	}
	// §失败重排队防自旋：刚失败回队尾的任务在冷却期内不出队（队列非空时其他任务先行，
	// 空队时空转间隔=冷却窗），避免快速失败任务烧 CPU。成功/取消后清除记录。
	s.mu.Lock()
	if failAt, cooling := s.failCool[next.ID]; cooling && s.nowTime().Sub(failAt) < failRetryCooldown {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// 窗口外排水限制：仅 preempted（被抢占遗留）可续跑；普通 queued（含手动新提交）
	// 必须等到盘后窗口——否则"有遗留"会变成绕过门控的后门。
	// English: outside the window only preempted rows may run; plain queued (incl. fresh manual
	// submissions) must wait — otherwise leftovers become a gate bypass.
	if !NightlyEligible(s.nowTime(), cfg) && next.Status != store.TaskPreempted {
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
		s.curCmd = nil // §修复 S3：任务结束后清空当前子进程引用
		s.mu.Unlock()
		if err != nil {
			log.Printf("[scheduler] 认领任务 #%d 失败: %v", next.ID, err)
		}
		return
	}
	// 认领即预写 1% 基线（§8.6-A）：子进程起步装配期（分钟级）进度条不再空窗。
	_ = db.UpdateTaskClaimed(next.ID)
	go s.runTask(db, cfg, cp)
}

// workerTick 盘后队列驱动：门控 → 夜间链入队 → 抢占检查 / 出队执行。
func (s *Scheduler) workerTick(cfg config.SchedulerConfig, now time.Time) {
	db := s.openStore(cfg)
	if db == nil {
		return
	}
	// 盘后硬门控（需求#4）：未到启动时间/盘中不**新起**任务——手动 high 同样排队等待。
	// 例外（续跑语义，对齐旧版"在跑的作业让它跑完"）：存在 preempted 遗留任务时，
	// 非交易时段即允许排水续跑——它们是被会话边界/重启打断的半成品，拖到次日 15:30
	// 只会让断点缓存白白过期。English: hard gate blocks NEW tasks before the evening window,
	// except draining preempted leftovers outside sessions (resume semantics).
	if !NightlyEligible(now, cfg) {
		hasLeftover := false
		if leftovers, err := db.ActiveResearchTasks(); err == nil {
			for _, t := range leftovers {
				if t.Status == store.TaskPreempted {
					hasLeftover = true
					break
				}
			}
		}
		if !hasLeftover {
			return
		}
		log.Printf("[scheduler] 盘后窗口未到，但存在被抢占遗留任务——仅排水续跑，不新起任务")
	} else {
		s.ensureNightlyEnqueue(db, cfg, now)
	}

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
	today := cntime.DayCompactOf(now) // §TZ1 北京日历定链日
	has, err := db.ChainHasTasks(today)
	if err != nil || has {
		return
	}
	steps := cfg.Nightly.Steps
	if len(steps) == 0 {
		steps = config.DefaultSchedulerConfig().Nightly.Steps
	}
	// 回测开关：开启时在 discover_factors 之后追加一次 B4 全链路回测（回填候选 avg_excess）；
	// 并在 discover_patterns 之后追加战法库全量回放（因子+形态启用规则，实盘口径回归验证）——
	// 修复"自动研究没有形态战法回测"的不对称。
	// English: when the toggle is on, append the B4 chain backtest after factor discovery AND a
	// full library replay (factor+pattern rules) after pattern discovery.
	if cfg.Nightly.BacktestEnabled && !containsStep(steps, "backtest") {
		steps = insertAfter(steps, "discover_factors", "backtest")
		log.Printf("[scheduler] 回测开关开启：夜间链追加 backtest 任务")
	}
	if cfg.Nightly.BacktestEnabled && !containsStep(steps, "library_replay") {
		steps = insertAfter(steps, "discover_patterns", "library_replay")
		log.Printf("[scheduler] 回测开关开启：夜间链追加 library_replay 任务（战法库因子+形态回放）")
	}
	// §O1 策略自优化引擎：夜间链追加全库参数寻优（默认开启，推荐制——结果需人工审批应用）
	if cfg.OptimizeEnabled && !containsStep(steps, "optimize") {
		steps = insertAfter(steps, "library_replay", "optimize")
		log.Printf("[scheduler] 策略自优化引擎开启：夜间链追加 optimize 任务（全库参数寻优）")
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
	opslog.Logf("research", "夜间链 %s 入队 %d 个任务: %v", today, len(steps), steps)
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
	case "library_replay":
		// 战法库全量回放（因子+形态启用规则一起）：夜间对现行战法做实盘口径的
		// 胜率/盈亏比回归验证，结果落 backtest_jobs（kind=library）供「回测」tab 查看。
		// §质控：全池（maxstocks=0）+ quality=true——以质控池（剔 ST/退市/多年亏损/地量股）
		// 替代字母序 300 截断，回归验证覆盖真实可用交易标的。
		// §节流：replay_throttle_ms>0 时逐股 sleep 摊平全量回放对 2核4G 服务器的瞬时
		// CPU/内存挤压（盘后十几个小时足够，拉长时长换稳定性）。
		// English: replays every enabled factor+pattern rule on the quality-screened full universe —
		// no more maxstocks=300 alphabetical truncation; optional per-stock throttle to flatten
		// instantaneous load over the long post-close window.
		p := map[string]any{
			"kind": "all", "start": researchStart, "end": today, "maxstocks": 0, "quality": true,
		}
		if cfg.ReplayThrottleMs > 0 {
			p["throttle_ms"] = cfg.ReplayThrottleMs
		}
		return store.TaskBacktestStrategy, mustJSON(p), true
	case "optimize":
		// §策略自优化引擎：全库寻优（贝叶斯搜索+细粒度网格），结果自动排名落库
		p := map[string]any{"kind": "optimize", "start": researchStart, "end": today, "top_n": 20}
		return store.TaskBacktestStrategy, mustJSON(p), true
	case "list":
		return store.TaskList, "{}", true
	}
	return "", "", false
}

// mustJSON 序列化为 JSON；失败兜底返回 "{}"（保证 payload 列永远是合法 JSON）。
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
			// §S2 修复：panic 后任务此前永远停留 running（直到进程重启才被捞回），
			// 期间夜间链 Done 永不置位。现落失败重排队终态并走链收尾。
			log.Printf("[scheduler] 任务 #%d panic: %v\n%s", tk.ID, r, stackTrace())
			errMsg := fmt.Sprintf("worker panic: %v", r)
			if e := db.RequeueFailedTask(tk.ID, errMsg); e != nil {
				log.Printf("[scheduler] 任务 #%d panic 后回队落库失败: %v", tk.ID, e)
			}
			s.finishTask(db, cfg, &tk, store.TaskError, errMsg)
			s.noteFailure(tk.ID)
		}
		s.mu.Lock()
		s.busy = false
		s.taskCancel = nil
		s.curTask = nil
		s.curCmd = nil // §修复 S3：清空当前子进程引用
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
	opslog.Logf("research", "任务 #%d(%s) 启动 prio=%s chain=%s/%d", tk.ID, tk.Type, tk.Priority, tk.ChainDay, tk.ChainSeq)

	// §失败重排队：失败不再落 error 终态——回队尾（updated_at 尾键沉底），不设重试上限；
	// error 列保留最后失败原因。冷却窗防快速失败自旋。
	fail := func(errMsg string) {
		log.Printf("[scheduler] 任务 #%d(%s) 失败→回队尾重试: %s", tk.ID, tk.Type, errMsg)
		opslog.Logf("research", "任务 #%d(%s) 失败回队重试: %s", tk.ID, tk.Type, errMsg)
		_ = db.RequeueFailedTask(tk.ID, errMsg)
		s.noteFailure(tk.ID)
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
	// §质控全量回放超时加固：library_replay 已从 maxstocks=300 改为全池（maxstocks=0）质控池，
	// 全市场数千标的 × 全库规则回放合法耗时可达数小时——无论 step_timeout_min 配置为何值，
	// TaskBacktestStrategy 类任务一律至少放宽至 6h（过程持续有"回测进度 xx%"输出，不会误判停滞）。
	// English: full-market quality-screened replays legitimately take hours — always give
	// TaskBacktestStrategy at least a 6h budget regardless of the configured per-step timeout;
	// steady "回测进度 xx%" output means this can't mask a stall.
	if tk.Type == store.TaskBacktestStrategy && timeout < 6*time.Hour {
		timeout = 6 * time.Hour
	}
	runCtx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dirOfDB(cfg)
	// §S1 孤儿防护：独立进程组 + Linux Pdeathsig（父死内核杀子），抢占/超时整组击杀
	configureSysProcAttr(cmd)

	// 输出文件化（架构修复，根除管道死锁）：子进程 stdout/stderr 直写每任务日志文件。
	// 旧"StdoutPipe+扫描协程"链路一旦消费端停摆（journald 抖动/诊断性 QUIT 巨量输出），
	// 64KB 管道灌满 → 子进程冻结在 write → 零进度 → 看门狗误杀（#16 实录）。
	// 写文件永不阻塞；worker 以尾随方式增量解析进度，完整日志同时留存排障。
	// English: file-based output — the child writes to a per-task log file (never blocks), and the
	// worker tails it incrementally for progress parsing; full log kept on disk for debugging.
	logDir := filepath.Join(filepath.Dir(func() string {
		p := cfg.DB
		if p == "" {
			p = defaultDB()
		}
		return p
	}()), "task_logs")
	_ = os.MkdirAll(logDir, 0o755)
	// §P1-12 任务日志轮转：每次建任务前清理 30 天前的日志，避免 task_logs 无限膨胀占满磁盘。
	cleanupTaskLogs(logDir, 30*24*time.Hour)
	logPath := filepath.Join(logDir, fmt.Sprintf("task_%d.log", tk.ID))
	_ = os.Remove(logPath) // §P1-12 先清掉同名旧日志（若重跑同 ID），避免与轮转清理/续跑混淆
	lf, lerr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if lerr != nil {
		fail("创建任务日志失败: " + lerr.Error())
		return
	}
	defer lf.Close()
	cmd.Stdout = lf
	cmd.Stderr = lf

	if err := cmd.Start(); err != nil {
		// §停机语义修正：调度器停止（ctx 取消）导致的启动失败落 preempted 断点续跑，
		// 而不是 error 终态——否则每次 systemctl restart 都会把排队任务打成永久错误
		// （2026-08-23 实录：restart 后 #35 落 error 需手工回队）。
		if base.Err() != nil || errors.Is(err, context.Canceled) {
			errMsg := "调度器停止，断点续跑"
			log.Printf("[scheduler] 任务 #%d 启动被停机打断 → preempted", tk.ID)
			opslog.Logf("research", "任务 #%d 启动被停机打断 → preempted", tk.ID)
			s.finishTask(db, cfg, &tk, store.TaskPreempted, errMsg)
			return
		}
		fail("启动子进程失败: " + err.Error())
		return
	}
	s.mu.Lock()
	s.taskCancel = cancel
	s.curCmd = cmd // §修复 S3：记录当前子进程，抢占时整组击杀含孙进程
	pendingPreempt := s.preemptReq
	s.mu.Unlock()
	if pendingPreempt {
		// 预占到启动之间到达的抢占请求：子进程已就绪，立即补杀。
		cancel()
	}

	// 输出收集 + 进度解析：尾随任务日志文件（2s 周期，断行缓存跨读拼接）
	var rs struct {
		mu       sync.Mutex
		out      bytes.Buffer
		progress string
		off      int64
		carry    string
	}
	handleLine := func(line string) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return
		}
		rs.mu.Lock()
		rs.out.WriteString(line)
		rs.out.WriteString("\n")
		rs.mu.Unlock()
		log.Printf("[task#%d:%s] %s", tk.ID, tk.Type, line)
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
	tailFile := func() {
		lr, err := os.Open(logPath)
		if err != nil {
			return
		}
		defer lr.Close()
		rs.mu.Lock()
		off := rs.off
		rs.mu.Unlock()
		st, serr := lr.Stat()
		if serr != nil || st.Size() <= off {
			return
		}
		if _, err := lr.Seek(off, 0); err != nil {
			return
		}
		data := make([]byte, st.Size()-off)
		n, _ := io.ReadFull(lr, data)
		chunk := rs.carry + string(data[:n])
		lines := strings.Split(chunk, "\n")
		rs.mu.Lock()
		rs.carry = lines[len(lines)-1]
		rs.off += int64(n)
		rs.mu.Unlock()
		for _, ln := range lines[:len(lines)-1] {
			handleLine(ln)
		}
	}
	tailStop := make(chan struct{})
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-tailStop:
				tailFile() // 收尾冲刷最后一段（含无换行的尾部）
				for {      // 持续读到 EOF 稳定，确保汇总数据完整
					rs.mu.Lock()
					off := rs.off
					beforeOut := rs.out.Len()
					rs.mu.Unlock()
					st, serr := os.Stat(logPath)
					if serr != nil || st.Size() <= int64(off) {
						return
					}
					tailFile()
					rs.mu.Lock()
					afterOut := rs.out.Len()
					rs.mu.Unlock()
					if afterOut == beforeOut && rs.off == off {
						return
					}
				}
			case <-t.C:
				tailFile()
			}
		}
	}()

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
			// §运行时熔断：任务跑着跑着把系统内存吃到危急线（如夜间排水撞上其他占用）
			// ——主动抢占自己落 preempted，断点续跑；绝不拖垮 SSH/caddy/quant。
			// 阈值=总闸一半（默认200MB）：入口闸放行后环境恶化时这里是最后一道防线。
			// §W4-b 运行时熔断阈值从配置派生：入口闸 min_free_mem_mb 可配，
			// 最后防线取其一半（默认 400/2=200 与旧行为一致），不再与入口闸脱钩。
			floorSrc := cfg.MinFreeMemMB // §W4-b：与入口闸同一配置源（外层 tick 持有 cfg）
			if floorSrc <= 0 {
				floorSrc = memGateDefaultMB
			}
			runtimeFloor := floorSrc / 2
			if av := readMemAvailableMB(); av >= 0 && av < runtimeFloor {
				log.Printf("[scheduler] 运行时熔断：MemAvailable=%dMB < %dMB，抢占当前任务 #%d(%s) 留队续跑",
					av, runtimeFloor, tk.ID, tk.Type)
				opslog.Logf("research", "运行时熔断抢占 #%d(%s) MemAvailable=%dMB<%dMB", tk.ID, tk.Type, av, runtimeFloor)
				s.preemptCurrent("系统内存危急(运行时熔断)")
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
					pauseProcess(cmd)
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
					resumeProcess(cmd)
				}
				rs.mu.Lock()
				pg := rs.progress
				rs.mu.Unlock()
				_ = db.UpdateTaskRunState(tk.ID, store.TaskRunning, pg, 0, "", "")
			case store.ControlCancel:
				s.mu.Lock()
				s.cancelReq = true
				s.mu.Unlock()
				killProcessGroup(cmd)
			}
		}
	}()

	// 看门狗：所有「已对齐进度协议」的任务启用进度停滞保护，避免单个卡死任务长期霸占
	// 唯一 busy 槽、饿死整条队列（"一直卡排队"根因之一：低优先级夜间/战法库回放任务挂死时，
	// 旧逻辑仅高优先级有看门狗，低优先级只能等单步硬超时(90min~3h)才被回收，期间全队停滞）。
	// 高优先级手动任务更敏感（30min）；低优先级夜间任务放宽至单步硬超时的 2/3
	// （默认 90min 超时→60min；战法库回放 3h→120min）。
	// 仅对已对齐进度协议（回测进度/发现进度/参数优化进度）的类型启用——其余类型
	// （dataload/sector_rebuild/list 等）未输出匹配进度行，仍靠单步硬超时兜底，避免误杀健康长任务。
	// English: stall watchdog now covers progress-emitting tasks of any priority, so a hung task
	// cannot starve the single-slot queue; types without a matching progress protocol keep relying
	// on the per-step hard timeout instead.
	watchTypes := map[string]bool{
		store.TaskBacktestCandidate: true, store.TaskBacktestNightly: true,
		store.TaskBacktestStrategy: true, // library 回放 emit 回测进度；optimize 经 sweep 输出"参数优化进度"
		store.TaskDiscoverFactors:  true, store.TaskDiscoverPatterns: true,
	}
	stallSecs := 30 * 60
	if tk.Priority != "high" {
		// low 夜间任务：以单步硬超时的 2/3 为停滞阈值。
		// §质控全量回放：TaskBacktestStrategy 单步已放宽至 6h，停滞阈值随其 2/3
		// （4h）联动，不再硬编码 120min——全市场质控池回放合法耗时可达数小时，
		// 硬编码会误杀仍在产出"回测进度 xx%"的健康长任务。
		// English: low-priority stall threshold rides 2/3 of the (6h, for replays) step timeout,
		// so legitimate multi-hour full-market replays aren't killed while emitting progress.
		if timeout > 0 {
			stallSecs = int(timeout.Seconds() * 2 / 3)
		} else {
			stallSecs = 60 * 60
		}
	}
	if watchTypes[tk.Type] {
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
						time.Now().Unix()-s.lastProgress > int64(stallSecs)
					s.mu.Unlock()
					if stalled {
						log.Printf("[scheduler] 任务 #%d 进度停滞>%dm，看门狗终止（腾出队列槽位）", tk.ID, stallSecs/60)
						killProcessGroup(cmd)
						return
					}
				}
			}
		}()
		defer close(watchStop)
	}

	waitErr := cmd.Wait()
	close(tailStop)
	<-tailDone
	cancel() // 唤醒可能阻塞在 select 的控制轮询 goroutine

	rs.mu.Lock()
	progress := rs.progress
	fullOut := rs.out.String()
	rs.mu.Unlock()
	if progress == "" {
		progress = "100%"
	}

	// 终态判定（优先级：抢占 > 用户取消 > 单步超时 > 运行错误 > 成功）
	// §失败重排队：超时/运行错误不再落 error 终态——统一回队尾重试（不设上限），
	// error 列记最后一次原因；仅用户取消保持终态。
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
		status = store.TaskFailedRetry
		errMsg = fmt.Sprintf("单步超时(%v)，已回队尾重试", timeout)
	case base.Err() != nil:
		// §停机语义修正：调度器 ctx 取消（SIGTERM/restart）→ preempted，断点续跑
		status = store.TaskPreempted
		errMsg = "调度器停止，断点缓存有效（下次启动自动回队续跑）"
	case waitErr != nil:
		status = store.TaskFailedRetry
		errMsg = fmt.Sprintf("运行失败(%v)，已回队尾重试", waitErr)
	}
	if status == store.TaskDone {
		switch tk.Type {
		case store.TaskBacktestCandidate, store.TaskBacktestNightly:
			if m := avgExcessRe.FindStringSubmatch(fullOut); len(m) == 2 {
				resultNum, _ = strconv.ParseFloat(m[1], 64)
			}
		case store.TaskBacktestStrategy:
			if strings.Contains(fullOut, "SWEEP_JSON:") {
				// §P2 扫参任务：SWEEP_JSON 解析后 TOP-N 落 optimization_results，
				// 并把冠军参数与核心结论整理进 result_text（替代原先仅截取前 100 字符的占位日志）。
				resultText = buildOptimizeSummary(fullOut)
				s.saveSweepResults(db, tk.ID, fullOut)
			} else {
				log.Printf("[scheduler] 任务 #%d 未检测到 SWEEP_JSON（fullOut len=%d, 前100=%q）",
					tk.ID, len(fullOut), fullOut[:min(len(fullOut), 100)])
				resultText = parseBtSummary(fullOut)
			}
		}
	}
	// 先落运行终态，再做状态翻转（顺序不可换：Requeue* 的 WHERE 依赖前置状态，
	// 且翻转后不得再被 Update 覆盖回去——否则盘后门控会把 preempted 无限重启）。
	if status != store.TaskFailedRetry {
		if err := db.UpdateTaskRunState(tk.ID, status, progress, resultNum, resultText, errMsg); err != nil {
			log.Printf("[scheduler] 任务 #%d 终态落库失败: %v", tk.ID, err)
		}
	}
	if status == store.TaskPreempted {
		_ = db.RequeueTask(tk.ID) // preempted → queued（队首优先级由出队排序保证）
	}
	if status == store.TaskFailedRetry {
		// §失败重排队：回队尾（updated_at 沉底），error 列留最后失败原因，冷却后重试。
		// 状态落库交给 RequeueFailedTask 一步完成（避免中间态被 peek 到）。
		if err := db.RequeueFailedTask(tk.ID, errMsg); err != nil {
			log.Printf("[scheduler] 任务 #%d 失败回队落库失败: %v", tk.ID, err)
			_ = db.UpdateTaskRunState(tk.ID, store.TaskError, "", 0, "", errMsg)
		}
		s.noteFailure(tk.ID)
	} else if status == store.TaskDone || status == store.TaskCancelled {
		s.clearFailure(tk.ID) // 成功/取消清除冷却记录
	}
	log.Printf("[scheduler] 任务 #%d(%s) -> %s%s", tk.ID, tk.Type, status, tailOf(errMsg))
	opslog.Logf("research", "任务 #%d(%s) 终态=%s%s", tk.ID, tk.Type, status, tailOf(errMsg))
	s.finishTask(db, cfg, &tk, status, errMsg)
}

// failRetryCooldown §失败重排队防自旋冷却窗：刚失败回队的任务在此窗口内不出队。
// English: cooldown window before a requeued failed task may start again (anti busy-spin).
const failRetryCooldown = 5 * time.Minute

// noteFailure 记录任务失败时间（冷却起点）。
func (s *Scheduler) noteFailure(taskID int64) {
	s.mu.Lock()
	if s.failCool == nil {
		s.failCool = make(map[int64]time.Time)
	}
	s.failCool[taskID] = s.nowTime()
	s.mu.Unlock()
}

// clearFailure 成功/取消后清除冷却记录（并防 map 慢性增长）。
func (s *Scheduler) clearFailure(taskID int64) {
	s.mu.Lock()
	delete(s.failCool, taskID)
	s.mu.Unlock()
}

// stackTrace 当前 goroutine 堆栈（panic 日志用）。
func stackTrace() string {
	return string(debug.Stack())
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
	case store.TaskFailedRetry:
		disp = "retrying" // §失败重排队：对外展示重试中（任务仍在队列）
	}
	s.recordStepState(tk.Type, disp, errMsg)

	if tk.ChainDay == "" {
		return
	}
	// §失败重排队：链步骤失败同样触发 AbortOnError——失败步骤回队尾重试直至成功，
	// 后续步骤取消（避免用残缺数据继续跑；次日链自动重建）。
	if (status == store.TaskError || status == store.TaskFailedRetry) && cfg.Nightly.AbortOnError {
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
	opslog.Logf("research", "夜间链 %s 全部完成", tk.ChainDay)
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

// tailOf 日志拼接用：错误信息非空时包一层全角括号，空串原样返回。
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
