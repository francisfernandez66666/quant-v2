// §M14（2026-09-22）同因连败熔断 worker 侧守护测试（反例锁）：
//   - 确定性失败（同因）连败达阈值 → failed_needs_attention 挂起终态，此后绝不再被出队执行，
//     高优告警恰好一次，挂起全程留痕（error 列 / opslog / state 文件）；
//   - 异因失败不误伤：每次失败指纹不同则连败恒从 1 重计，维持无上限回队重试主策略。
//
// English: M14 guard tests — identical-cause streak parks the task (one-shot alert, no more
// requeue); mixed-cause failures never trip the breaker.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// alertCapture §M14 SetAlertFunc 线程安全收集器（record 内容 = "title|content"）。
type alertCapture struct {
	mu    sync.Mutex
	calls []string
}

func (a *alertCapture) fn(title, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, title+"|"+content)
}

func (a *alertCapture) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// manualClock §flaky/竞态约定：setNow 注入的时钟会被后台排水 goroutine 读取，
// 测试推进时间必须经互斥保护的取值闭包（直接改共享 time.Time 变量会被 -race 判竞争）。
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// failRunTaskScript 假 research 二进制：run-task 调用固定失败（exit 2）并打印末行原因；
// rotate=true 时末行附带自增轮次（指纹每次不同，模拟异因）。非 run-task（dataload 直连）
// 一律成功退出 0。日志路径/计数文件路径烘焙进脚本（§flaky 约定，同 fakeScript）。
func failRunTaskScript(t *testing.T, dir, logPath string, rotate bool) string {
	t.Helper()
	script := filepath.Join(dir, "failbin.sh")
	cnt := filepath.Join(dir, "attempt.cnt")
	body := "  *run-task*) echo \"" + "FATAL 数据缺失: stock_daily 无 20260822 分区\" >&2; exit 2 ;;\n"
	if rotate {
		body = "  *run-task*)\n" +
			"    n=0\n" +
			"    [ -f " + shQuote(cnt) + " ] && n=$(cat " + shQuote(cnt) + ")\n" +
			"    n=$((n+1)); echo $n > " + shQuote(cnt) + "\n" +
			"    echo \"TRANSIENT 网络抖动 第$n 次原因各不相同\" >&2\n" +
			"    exit 2 ;;\n"
	}
	content := "#!/bin/sh\n" +
		"echo \"FAKE $@\" >> " + shQuote(logPath) + "\n" +
		"case \"$*\" in\n" +
		body +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

// runTaskCalls 统计假二进制 run-task 执行次数（dataload 直连调用不计）。
func runTaskCalls(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "FAKE ") && strings.Contains(l, "run-task") {
			n++
		}
	}
	return n
}

// TestSameCauseBreakerParksTask §M14 反例锁（同因连败 10 次后不再重排）：
// 一条恒定失败的 high 任务被反复回队重试，触阈值后必须挂起为 failed_needs_attention、
// 告警一次、留痕可审计，且后续 tick 绝不再拉起子进程（不再烧算力）。
func TestSameCauseBreakerParksTask(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	bin := failRunTaskScript(t, dir, logPath, false)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(bin, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	alerts := &alertCapture{}
	// §flake 根修（2026-09-22 收尾核验）：LastStatus 是「最近一次覆盖式」全局字段，
	// 后台排水 goroutine 随时可能跑完夜链 dataload 把它冲成 done——快照直读是时序竞态
	// （压测 -count=20 约 15% 概率秒挂在本断言）。改经 stepStateObserver 测试缝收集
	// 留痕事件流，确定性锁定「needs_attention 确实被记录过且仅一次」。
	// 注意：观察器必须先于任何 tick 安装，否则挂起留痕可能在安装前已落过。
	var trMu sync.Mutex
	var trails []string // "step|status"
	s.mu.Lock()
	s.stepStateObserver = func(step, status, errMsg string) {
		trMu.Lock()
		trails = append(trails, step+"|"+status)
		trMu.Unlock()
	}
	s.mu.Unlock()
	s.SetAlertFunc(alerts.fn) // §M14 告警通道注入（生产由 researchd 接 notify.PushGateway）
	loc := time.FixedZone("CST", 8*3600)
	clk := &manualClock{t: time.Date(2026, 8, 22, 16, 0, 0, 0, loc)} // 周六盘后
	s.setNow(clk.now)

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 77, Priority: "high", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	prevRetry := 0
	tripped := false
	for round := 0; round < store.SameReasonFailLimit+3; round++ {
		clk.advance(10 * time.Minute) // 越过 5min 失败冷却窗，模拟夜间逐轮慢速重试
		s.tick()
		waitFor(t, 15*time.Second, func() bool {
			tk, _ := qdb.GetResearchTask(id)
			return tk != nil && (tk.RetryCount > prevRetry || tk.Status == store.TaskNeedsAttention)
		}, fmt.Sprintf("第 %d 轮失败任务应重试或挂起", round))
		tk, _ := qdb.GetResearchTask(id)
		prevRetry = tk.RetryCount
		if tk.Status == store.TaskNeedsAttention {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatalf("同因 %d 连败后应挂起 failed_needs_attention", store.SameReasonFailLimit)
	}
	tk, _ := qdb.GetResearchTask(id)
	if tk.FailStreak != store.SameReasonFailLimit {
		t.Fatalf("同因连败计数应为 %d, 实际 %d", store.SameReasonFailLimit, tk.FailStreak)
	}
	if !strings.Contains(tk.Error, "同因连败熔断") || !strings.Contains(tk.Error, "数据缺失") {
		t.Fatalf("挂起留痕应含熔断标记+原始原因, got %q", tk.Error)
	}
	if got := runTaskCalls(t, logPath); got != store.SameReasonFailLimit {
		t.Fatalf("子进程应恰好被执行 %d 次（触阈值即停）, 实际 %d", store.SameReasonFailLimit, got)
	}
	// §M14 高优告警恰好一次（挂起是瞬时跃迁，天然去重）
	// §flake 根修（2026-09-22 收尾核验）：告警在 runner 协程里于「DB 挂起落库」之后才异步
	// 触发——上方 waitFor 观察到 NeedsAttention 即放行，高负载下断言可能抢在 fn() 执行前
	// 读到 0（本包 waitIdleAndSettle 注释所述同款异步尾巴）。改轮询等待告警落地再断言恰好 1。
	waitFor(t, 15*time.Second, func() bool { return alerts.count() >= 1 }, "熔断告警应送达")
	if got := alerts.count(); got != 1 {
		t.Fatalf("熔断告警应恰好 1 次, 实际 %d", got)
	}
	// 挂起留痕落到展示状态上报（recordStepState → research_state.json）：
	// 用测试开头安装的 stepStateObserver 事件流判定（根修说明见安装处注释）。
	waitFor(t, 15*time.Second, func() bool {
		trMu.Lock()
		defer trMu.Unlock()
		for _, tr := range trails {
			if tr == string(store.TaskBacktestCandidate)+"|needs_attention" {
				return true
			}
		}
		return false
	}, "state 文件应留痕 needs_attention")
	trMu.Lock()
	naCount := 0
	for _, tr := range trails {
		if tr == string(store.TaskBacktestCandidate)+"|needs_attention" {
			naCount++
		}
	}
	trMu.Unlock()
	if naCount != 1 {
		t.Fatalf("needs_attention 留痕应恰好 1 次（瞬时跃迁天然去重）, 实际 %d", naCount)
	}
	// 此后继续 tick：绝不复活、绝不再出队执行
	for round := 0; round < 3; round++ {
		clk.advance(10 * time.Minute)
		s.tick()
	}
	time.Sleep(200 * time.Millisecond)
	if tk2, _ := qdb.GetResearchTask(id); tk2.Status != store.TaskNeedsAttention {
		t.Fatalf("挂起终态不应被后续 tick 改动, 实际 %s", tk2.Status)
	}
	if got := runTaskCalls(t, logPath); got != store.SameReasonFailLimit {
		t.Fatalf("挂起后不得再拉起子进程（不再重排队烧算力）, 实际执行 %d 次", got)
	}
	if got := alerts.count(); got != 1 {
		t.Fatalf("挂起后重复 tick 不应再告警, 实际 %d 次", got)
	}
	waitIdleAndSettle(t, s)
}

// TestDifferentCauseFailuresNeverBreaker §M14 反例锁（不同因失败不误伤）：
// 每次失败原因指纹不同（瞬态抖动族），远超同因阈值仍维持回队重试、不挂起、不告警，
// 连败计数恒为 1（异因从 1 重计）。
func TestDifferentCauseFailuresNeverBreaker(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	bin := failRunTaskScript(t, dir, logPath, true) // 每次末行原因带自增轮次
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(bin, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	alerts := &alertCapture{}
	s.SetAlertFunc(alerts.fn)
	loc := time.FixedZone("CST", 8*3600)
	clk := &manualClock{t: time.Date(2026, 8, 22, 16, 0, 0, 0, loc)}
	s.setNow(clk.now)

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 88, Priority: "high", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	prevRetry := 0
	rounds := store.SameReasonFailLimit + 2 // 超过阈值：若是同因早已熔断挂起
	for round := 0; round < rounds; round++ {
		clk.advance(10 * time.Minute)
		s.tick()
		waitFor(t, 15*time.Second, func() bool {
			tk, _ := qdb.GetResearchTask(id)
			return tk != nil && tk.RetryCount > prevRetry
		}, fmt.Sprintf("第 %d 轮异因失败应回队重试", round))
		tk, _ := qdb.GetResearchTask(id)
		prevRetry = tk.RetryCount
	}
	tk, _ := qdb.GetResearchTask(id)
	if tk.Status != store.TaskQueued {
		t.Fatalf("异因循环 %d 次应维持 queued 重试（不设上限主策略）, 实际 %s", rounds, tk.Status)
	}
	if tk.FailStreak != 1 {
		t.Fatalf("异因每次应从 1 重计, 实际 fail_streak=%d", tk.FailStreak)
	}
	if got := alerts.count(); got != 0 {
		t.Fatalf("异因失败绝不应触发熔断告警, 实际 %d 次", got)
	}
	waitIdleAndSettle(t, s)
}
