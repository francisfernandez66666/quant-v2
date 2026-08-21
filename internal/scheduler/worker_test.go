// 队列 worker 核心行为守护测试：盘后门控下队列自驱排水 + high 抢占 low（kill→preempted→回队首续跑）。
package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestWorkerPreemptsLowByHigh 手动 high 入队后，正在跑的夜间 low 任务被 kill
// 并标 preempted 自动回队首；high 随即执行完成。
func TestWorkerPreemptsLowByHigh(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	// 本测试专用假二进制：dataload 直连调用长睡（模拟重任务），run-task 立即返回。
	script := filepath.Join(dir, "fakebin.sh")
	content := `#!/bin/sh
echo "FAKE $@" >> "$FAKE_LOG"
case "$*" in
  *run-task*) ;;
  *) [ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP" ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SLEEP", "120")
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(script, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) } // 周六 16:00 盘后

	// 第一次 tick：入队并启动夜间链（dataload，low，长睡）
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return s.busy }, "low 任务应已启动")

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()

	// 模拟 quant 手动入队 high 回测任务
	highID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 100,
		Priority: "high", Payload: `{"h":5}`,
	})
	if err != nil {
		t.Fatalf("入队 high: %v", err)
	}

	// 第二次 tick：应触发抢占
	s.tick()
	waitFor(t, 5*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(highID)
		return tk != nil && tk.Status == store.TaskDone
	}, "抢占后 high 任务应被执行完成")

	// 被抢占的 low 任务应回队首等待续跑（preempted → queued）
	lowTask, _ := qdb.LatestTaskByRef(store.TaskDataload, 0)
	if lowTask == nil {
		t.Fatal("找不到被抢占的 low 任务")
	}
	if lowTask.Status != store.TaskQueued && lowTask.Status != store.TaskRunning {
		t.Fatalf("low 任务状态=%s，期望 queued(回队)或已被重新拉起 running", lowTask.Status)
	}
	if lowTask.Error == "" {
		t.Fatal("被抢占任务应记录抢占原因")
	}

	// 收尾：切到交易时段杀掉重新拉起的 low 子进程，不留孤儿
	s.now = func() time.Time { return time.Date(2026, 8, 24, 10, 0, 0, 0, loc) }
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return !s.busy }, "交易时段应终止遗留任务")
}

// TestWorkerDrainsQueueContinuously 多任务链不依赖 30s tick 连续排空。
func TestWorkerDrainsQueueContinuously(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("FAKE_LOG", logPath)
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath)
	cfg.Nightly.BacktestEnabled = true // 链含 2 个任务：dataload + backtest_nightly
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }

	s.tick() // 仅此一次 tick：排水应由任务完成后的 tryStartNext 自驱完成
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "两次 tick 之间链也应全部排空(Done=true)")
	if got := callCount(t, logPath); got != 2 {
		t.Fatalf("链应有 2 个任务被执行, 实际 %d", got)
	}
}

// TestManualHighQueuedDuringSession 盘中入队的手动 high 任务不被执行（需求#4 盘后硬门控），
// 状态保持 queued；到盘后才被消费。
func TestManualHighQueuedDuringSession(t *testing.T) {
	dir := t.TempDir()
	fake := fakeScript(t, dir)
	dbPath := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, dbPath))
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.now = func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, loc) } // 周二盘中

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 1, Priority: "high", Payload: "{}",
	})
	if err != nil {
		t.Fatalf("入队: %v", err)
	}

	s.tick() // 盘中 tick：绝不出队
	time.Sleep(200 * time.Millisecond)
	tk, _ := qdb.GetResearchTask(id)
	if tk == nil || tk.Status != store.TaskQueued {
		status := ""
		if tk != nil {
			status = tk.Status
		}
		t.Fatalf("盘中手动任务应保持 queued, 实际 %q", status)
	}
	if s.busy {
		t.Fatal("盘中不应启动任何研究子进程")
	}
	_ = config.DefaultSchedulerConfig // 引用防 unused（cfgSamples 已覆盖默认）
}
