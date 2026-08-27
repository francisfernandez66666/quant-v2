// 队列 worker 核心行为守护测试：盘后门控下队列自驱排水 + high 抢占 low（kill→preempted→回队首续跑）。
package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestWorkerPreemptsLowByHigh 手动 high 入队后，正在跑的夜间 low 任务被 kill
// 并标 preempted 自动回队首；high 随即执行完成。
func TestWorkerPreemptsLowByHigh(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	// 本测试专用假二进制：dataload 直连调用长睡（模拟重任务），run-task 立即返回。
	// §flaky 根修：日志路径烘焙进脚本（不再依赖进程级 $FAKE_LOG，跨测试零串扰）。
	script := filepath.Join(dir, "fakebin.sh")
	content := "#!/bin/sh\n" +
		"echo \"FAKE $@\" >> '" + logPath + "'\n" +
		"case \"$*\" in\n" +
		"  *run-task*) ;;\n" +
		"  *) [ -n \"$FAKE_SLEEP\" ] && exec sleep \"$FAKE_SLEEP\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SLEEP", "5")
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(script, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六 16:00 盘后

	// 第一次 tick：入队并启动夜间链（dataload，low，长睡）
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return s.busyLocked() }, "low 任务应已启动")

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
	s.setNow(func() time.Time { return time.Date(2026, 8, 24, 10, 0, 0, 0, loc) })
	s.tick()
	waitFor(t, 5*time.Second, func() bool { return !s.busyLocked() }, "交易时段应终止遗留任务")
	waitIdleAndSettle(t, s)
}

// TestWorkerDrainsQueueContinuously 多任务链不依赖 30s tick 连续排空。
func TestWorkerDrainsQueueContinuously(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0")
	logPath := filepath.Join(t.TempDir(), "fake.log")
	dir := t.TempDir()
	fake := fakeScript(t, logPath)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath)
	cfg.Nightly.BacktestEnabled = true // 链含 3 个任务：dataload + backtest_nightly + library_replay
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) })
	s.tick() // 仅此一次 tick：排水应由任务完成后的 tryStartNext 自驱完成
	waitFor(t, 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.Done && s.state.Day == "20260822"
	}, "两次 tick 之间链也应全部排空(Done=true)")
	if got := callCount(t, logPath); got != 3 {
		t.Fatalf("链应有 3 个任务被执行, 实际 %d", got)
	}
	waitIdleAndSettle(t, s)
}

// TestManualHighQueuedDuringSession 盘中入队的手动 high 任务不被执行（需求#4 盘后硬门控），
// 状态保持 queued；到盘后才被消费。
func TestManualHighQueuedDuringSession(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	fake := fakeScript(t, logPath)
	dbPath := filepath.Join(dir, "trading.db")
	cfgPath := mustConfig(t, cfgSamples(fake, dbPath))
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, loc) }) // 周二盘中

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
	if s.busyLocked() {
		t.Fatal("盘中不应启动任何研究子进程")
	}
	_ = config.DefaultSchedulerConfig // 引用防 unused（cfgSamples 已覆盖默认）
	waitIdleAndSettle(t, s)
}

// TestLibraryReplayStepMappedAndInserted 回测开关开启时，夜间链应在 discover_patterns 后
// 追加 library_replay 步骤，且该步骤映射为 kind=all 的战法库回放任务（修复形态战法无自动回测）。
func TestLibraryReplayStepMappedAndInserted(t *testing.T) {
	typ, payload, ok := stepTask("library_replay", config.DefaultSchedulerConfig(), "20260822")
	if !ok || typ != store.TaskBacktestStrategy {
		t.Fatalf("library_replay 映射错误: ok=%v typ=%s", ok, typ)
	}
	if !strings.Contains(payload, `"kind":"all"`) {
		t.Fatalf("payload 应为 kind=all, 得 %s", payload)
	}
	steps := []string{"dataload", "sector_rebuild", "discover_factors", "discover_patterns", "list"}
	if containsStep(steps, "backtest") {
		t.Fatal("前置条件：默认步骤不含 backtest")
	}
	steps = insertAfter(steps, "discover_factors", "backtest")
	steps = insertAfter(steps, "discover_patterns", "library_replay")
	want := []string{"dataload", "sector_rebuild", "discover_factors", "backtest", "discover_patterns", "library_replay", "list"}
	if len(steps) != len(want) {
		t.Fatalf("链长度 %d != %d", len(steps), len(want))
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("链顺序错误: %v", steps)
		}
	}
}

// TestFailedTaskRequeuesAtTail §失败重排队：任务失败不落 error 终态——
// 自动回队尾（先于它入队的健康任务 B/C 先跑），error 列留失败原因，
// 冷却期内不重启；冷却过后自动重试，不设上限。
// English: a failed task re-enqueues at the queue TAIL — healthier tasks enqueued before it run
// first; the failure reason is recorded; a cooldown gates the retry (no cap).
func TestFailedTaskRequeuesAtTail(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	// 假二进制：run-task 时带 "--fail" 参数的退出 2（模拟失败），否则立即成功。
	// §flaky 根修：日志路径烘焙进脚本。
	script := filepath.Join(dir, "fakebin.sh")
	content := "#!/bin/sh\n" +
		"echo \"FAKE $@\" >> '" + logPath + "'\n" +
		"case \"$*\" in\n" +
		"  *run-task*) case \"$*\" in *\"--task-id 1\"*) exit 2 ;; *) exit 0 ;; esac ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(script, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六盘后

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()

	// 入队顺序：A(会失败) → B、C（健康）。payload 带 "fail":true 让假二进制区分。
	mk := func(ref int64, fail bool) *store.ResearchTask {
		p := `{}`
		if fail {
			p = `{"fail": true}`
		}
		return &store.ResearchTask{
			Type: store.TaskBacktestCandidate, RefID: ref,
			Priority: "high", Payload: p,
		}
	}
	failID, err := qdb.EnqueueResearchTask(mk(1, true))
	if err != nil {
		t.Fatal(err)
	}
	okB, err := qdb.EnqueueResearchTask(mk(2, false))
	if err != nil {
		t.Fatal(err)
	}
	okC, err := qdb.EnqueueResearchTask(mk(3, false))
	if err != nil {
		t.Fatal(err)
	}

	s.tick() // A 启动并失败 → 回队尾；自驱排水应继续跑完 B、C
	waitFor(t, 15*time.Second, func() bool {
		b, _ := qdb.GetResearchTask(okB)
		c, _ := qdb.GetResearchTask(okC)
		return b != nil && c != nil && b.Status == store.TaskDone && c.Status == store.TaskDone
	}, "健康任务 B、C 应先于 A 的重试完成")

	a, _ := qdb.GetResearchTask(failID)
	if a == nil || a.Status != store.TaskQueued {
		t.Fatalf("失败任务应回 queued(而非 error 终态), got %+v", a)
	}
	if !strings.Contains(a.Error, "运行失败") {
		t.Fatalf("error 列应保留失败原因, got %q", a.Error)
	}
	// A 在冷却期内：不应已重试。真实不变量由上方"a.Status==queued"承载（重试必然先置 running）；
	// 此处不再直接断言 s.busy——失败回队与 watchdog 启动是异步的，紧随其后的即时负断言
	// 在 CI 负载下天然竞态（§W3-c 引入 fsync 后写盘时序变化使其暴露），属测试自身缺陷。
	_ = s

	// 冷却过期 → A 自动重试（再次失败→再次回队；不设上限）。
	// 用单调 requeue_seq 判定重试发生过：首次失败=1，二次失败回队=2。
	a1, _ := qdb.GetResearchTask(failID)
	if a1 == nil || a1.RequeueSeq != 1 {
		t.Fatalf("首次失败后 requeue_seq 应为 1, got %+v", a1)
	}
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 10, 0, 0, loc) })
	s.tick()
	waitFor(t, 15*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(failID)
		return tk != nil && tk.RequeueSeq >= 2 && tk.Status == store.TaskQueued
	}, "冷却过期后失败任务应重试并二次回队")
	a2, _ := qdb.GetResearchTask(failID)
	if !strings.Contains(a2.Error, "运行失败") {
		t.Fatalf("error 列应保留最后一次失败原因, got %q", a2.Error)
	}
	waitIdleAndSettle(t, s)
}
