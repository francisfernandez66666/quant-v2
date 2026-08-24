// Package scheduler 研究任务队列的唯一消费者（独立服务 quant-research 使用）。
//
// 子系统统一改造后的编排模型（详见 docs/RESEARCH_TASK_QUEUE_PLAN.md）：
//   - quant(API) 与夜间作业只把任务写入 trading.db 的 research_tasks 队列；
//     本服务是唯一出队执行者——单飞由架构保证，不再有第二个编排者；
//   - 交易时段硬门控（需求#4）：NightlyEligible 对一切任务生效（含手动 high）——
//     绝不进入 A 股活跃时段；盘后/休市归研究，无写死钟点；
//   - 优先级抢占（决策#1）：high 入队时若 low 在跑 → kill 当前子进程并标 preempted
//     （断点缓存有效，自动回队首续跑）；
//   - 资源治理（需求#5）：所有研究子进程继承本服务的 cgroup 限流
//     （quant-research.service：Nice=10 / CPUQuota=100% 单核 / MemoryHigh=700M /
//     IOSchedulingClass=idle / GOMAXPROCS=1），数据层统一窗口分块装配，不吃满机器。
//
// research_state.json 仅作展示兼容（最近步骤结果、交易时段下载节流时间戳）；
// 任务断点状态已由队列表接管，跨重启天然恢复。
//
// English: sole consumer of the research task queue (standalone quant-research service).
// After the unified-subsystem refactor, quant(API) and the nightly chain only enqueue into
// research_tasks; this daemon is the single executor — single-flight is architectural. A hard
// session gate applies to every task including manual high-priority ones — never during A-share
// trading hours; trading days start post-close, non-trading days allow research all day. High-priority arrival kills a running low-priority child
// (preempted, auto-requeued). Children inherit this unit's cgroup caps so research never saturates
// the box.
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
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// researchStart 夜间研究窗口起点（近 3 年，与既有 run_auto_research_full.sh 一致）。
const researchStart = "20230801"

// Scheduler 研究任务队列唯一执行器：30s tick 检查会话门控并驱动队列消费。
type Scheduler struct {
	cfgPath   string // config.json 路径
	statePath string // research_state.json 路径（展示兼容状态）
	now       func() time.Time

	mu            sync.Mutex
	baseCtx       context.Context     // 服务级上下文（退出时取消当前子进程）
	storeDB       *store.DB           // 队列表句柄（懒加载，进程内复用）
	storeReset    bool                // 启动恢复是否已执行（running→preempted）
	busy          bool                // 是否有任务子进程在跑（原 jobRunning）
	taskCancel    context.CancelFunc  // 当前任务取消函数（kill 用）
	curTask       *store.ResearchTask // 当前运行任务
	preemptReq    bool                // 抢占请求（会话开始/禁用/high 到来）
	cancelReq     bool                // 用户取消请求（control=cancel 消费后置位）
	paused        bool                // 当前任务是否 SIGSTOP 暂停
	lastProgress  int64               // unix 秒，最近一次进度行（看门狗用）
	tradingDLBusy bool                // 交易时段 dataload 是否在跑（防重叠）
	lastTrim      time.Time           // 盘中内存释放最近一次执行时间（节流用）
	state         stateFile
}

// stateFile 展示兼容状态：交易时段下载节流时间戳 + 最近任务结果上报（前端可见）。
// English: display-compat state — intraday download throttle timestamp plus latest task outcome.
type stateFile struct {
	Day          string `json:"day"`                   // 最近入队的夜间链运行日 YYYYMMDD
	Done         bool   `json:"done"`                  // 该日链是否已全部完成
	LastDataload int64  `json:"last_dataload_ts"`      // 交易时段最近一次 dataload UnixNano（节流）
	LastStep     string `json:"last_step,omitempty"`   // 最近完成任务类型
	LastStatus   string `json:"last_status,omitempty"` // done/error/interrupted/paused
	LastError    string `json:"last_error,omitempty"`
	LastAt       string `json:"last_at,omitempty"`
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
	log.Printf("[scheduler] 研究调度器启动(队列模式): cfg=%s state=%s", s.cfgPath, s.statePath)
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
	s.loadState()
	// 注：storeDB 不随 Run 退出关闭——任务 runner 协程可能在 ctx 取消后仍需落终态；
	// researchd 为长驻进程，进程退出时由 OS 回收。
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.tick() // 启动立即检查一次（跨天/重启场景）
	for {
		select {
		case <-ctx.Done():
			s.preemptCurrent("服务退出")
			log.Printf("[scheduler] 研究调度器停止")
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick 单次调度检查：读配置 → 会话分派（盘后门控在 workerTick 内统一执行）。
func (s *Scheduler) tick() {
	cfg := config.LoadSchedulerConfig(s.cfgPath)
	// §数据源路由装配（热生效）：primary_source=hithink 时回测取数优先 ths_ 表；
	// 复权门禁独立开关——两者都通过前引擎不会混合两套复权体系。
	store.PrimarySourceThsDaily = strings.EqualFold(cfg.PrimarySource, "hithink")
	store.ThsFactorsReady = cfg.ThsFactorsReady
	now := s.now()
	if !cfg.Enabled {
		s.preemptCurrent("调度器已禁用")
		return
	}
	if data.IsTradingWindow(now) {
		// 交易日交易窗口：终止遗留任务（标 preempted 次日自动续跑）+ 内存治理 + 增量下载（只下载不研究）
		s.preemptCurrent("交易时段开始")
		s.trimInSession(cfg, now)
		s.maybeTradingDataload(cfg, now)
		return
	}
	s.workerTick(cfg, now)
}

// trimInSession 盘中内存治理：活跃时段对 researchd 自身 GC+FreeOSMemory 归还堆内存。
// （旧 killOrphanResearch pkill 已删——worker 独占子进程后无孤儿概念。）
// English: in-session memory governance — GC+FreeOSMemory on the daemon itself on a throttled cadence;
// the legacy pkill orphan-sweeper is gone since the worker exclusively owns children now.
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
	if due {
		runtime.GC()
		debug.FreeOSMemory()
		log.Printf("[scheduler] 盘中内存释放完成 (trim_interval_min=%d)", cfg.TrimIntervalMin)
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
		if err := s.execDataload(ctx, cfg, now); err != nil && ctx.Err() == nil {
			log.Printf("[scheduler] 交易时段 dataload 失败: %v", err)
		}
	}()
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

// saveState 持久化状态（展示兼容字段落盘）。
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

// Write 实现 io.Writer：把子进程输出按行拆分、带前缀转写进服务日志
// （journalctl -u quant-research 可见，排障与 verify_nightly.sh 巡检依赖）。
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

// NightlyEligible 纯函数：当前时刻是否允许执行任何研究任务（含手动 high）。
// 门控完全由系统会话模型驱动（data.CurrentSession），不含任何写死钟点：
//   - 盘前/上午盘/午前/下午盘 → 禁止（这段时间归 quant：新闻归因流水线 + 近实时打分）
//   - 盘后(15:00 收盘后)/休市(周末·节假日·凌晨) → 允许
//
// 与 quant 自身的时段判断共用同一谓词集合，两边永远一致，不会出现"日历说休市、
// 时钟却拦着"的错位。cfg 参数保留以兼容调用方与测试（门控不再消费钟点配置，
// Nightly.StartHHMM / WeekendStartHHMM 已废弃）。
// English: gate driven purely by the session model — blocked during premarket/trading windows
// (quant owns those), allowed after close and on closed days. No hardcoded clock times.
func NightlyEligible(now time.Time, cfg config.SchedulerConfig) bool {
	_ = cfg // 会话化后不再消费钟点配置（kept for call-site stability）
	// §统一口径（用户约定）：交易日交易窗口（9:15~收盘）禁止，其余全放行——
	// 盘前凌晨/盘后/周末全天均可研究。直接复用 data.IsTradingWindow。
	return !data.IsTradingWindow(now)
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
