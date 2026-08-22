// research_tasks 队列单测：入队/出队排序（优先级、preempted 置顶、链序 FIFO）、
// 认领 CAS、控制标志消费、启动恢复、backtest_jobs 一次性迁移映射。
package store

import (
	"testing"
)

// TestTaskDequeueOrdering 验证出队排序：high 先于 low；同级内 preempted 置顶；
// 链任务按 chain_day → chain_seq → id FIFO。
func TestTaskDequeueOrdering(t *testing.T) {
	db := testDB(t)
	mk := func(typ, prio, status, chain string, seq int) int64 {
		id, err := db.EnqueueResearchTask(&ResearchTask{
			Type: typ, Priority: prio, Status: status, ChainDay: chain, ChainSeq: seq, Payload: "{}",
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", typ, err)
		}
		return id
	}
	mk(TaskDiscoverFactors, "low", TaskQueued, "20260821", 2)   // low 链第2步
	low1 := mk(TaskBacktestCandidate, "low", TaskQueued, "", 0) // low 手动
	high := mk(TaskBacktestCandidate, "high", TaskQueued, "", 0)
	preempted := mk(TaskSectorRebuild, "low", TaskPreempted, "20260821", 3)
	mk(TaskDiscoverFactors, "low", TaskQueued, "20260820", 9) // 昨日链

	want := []int64{high, preempted, low1 /*chain_day='' 排链前*/, 0, 0}
	// 逐个出队校验顺序
	got := []int64{}
	for i := 0; i < 5; i++ {
		tk, err := db.DequeueHighestTask()
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if tk == nil {
			t.Fatalf("第 %d 次出队为空，期望仍有任务", i)
		}
		ok, err := db.ClaimResearchTask(tk.ID)
		if !ok || err != nil {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		got = append(got, tk.ID)
	}
	// high 第一；low 类内 preempted 置顶；其余 chain_day 升序（'' 在前）、seq、id
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != 5 || got[4] != 1 {
		t.Fatalf("顺序错误: got=%v 期望=[%d %d %d 5 1]", got, want[0], want[1], want[2])
	}
	// 清空后无任务可取
	tk, err := db.DequeueHighestTask()
	if err != nil || tk != nil {
		t.Fatalf("队列应为空: %v %v", tk, err)
	}
}

// TestTaskControlAndStates 控制标志消费 + 终态补 finished_at + 启动恢复。
func TestTaskControlAndStates(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskBacktestCandidate, RefID: 100, Payload: `{"h":5}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if ok, _ := db.ClaimResearchTask(id); !ok {
		t.Fatal("claim 失败")
	}
	// control 写入→消费→清空
	if err := db.SetTaskControl(id, ControlPause); err != nil {
		t.Fatalf("set control: %v", err)
	}
	if c, _ := db.ConsumeTaskControl(id); c != ControlPause {
		t.Fatalf("消费到 %q，期望 pause", c)
	}
	if c, _ := db.ConsumeTaskControl(id); c != "" {
		t.Fatalf("二次消费应为空，得到 %q", c)
	}
	// HasActiveTaskByRef：running 命中
	has, _ := db.HasActiveTaskByRef(TaskBacktestCandidate, 100)
	if !has {
		t.Fatal("running 任务应命中 active")
	}
	// done 后 finished_at 落上、active 不再命中
	if err := db.UpdateTaskRunState(id, TaskDone, "100%", 0.1234, "", ""); err != nil {
		t.Fatalf("done: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.FinishedAt == "" || tk.ResultNum != 0.1234 {
		t.Fatalf("终态字段缺失: %+v", tk)
	}
	has, _ = db.HasActiveTaskByRef(TaskBacktestCandidate, 100)
	if has {
		t.Fatal("done 后不应命中 active")
	}
	// 启动恢复：running/paused → preempted
	id2, _ := db.EnqueueResearchTask(&ResearchTask{Type: TaskList, Payload: "{}"})
	db.ClaimResearchTask(id2)
	if n, _ := db.ResetStaleRunningTasks(); n != 1 {
		t.Fatalf("恢复行数=%d，期望 1", n)
	}
	tk2, _ := db.GetResearchTask(id2)
	if tk2.Status != TaskPreempted {
		t.Fatalf("状态=%s，期望 preempted", tk2.Status)
	}
}

// TestMigrateBacktestJobsToTasks 一次性迁移：终态平移 / interrupted→cancelled /
// running candidate→preempted / library 缺类型→cancelled / 幂等不回填。
func TestMigrateBacktestJobsToTasks(t *testing.T) {
	db := testDB(t)
	legacy := []*BacktestJob{
		{Kind: "candidate", CandidateID: 7, Status: "done", AvgExcess: 0.05, Progress: "100%"},
		{Kind: "nightly", CandidateID: 0, Status: "interrupted"},
		{Kind: "candidate", CandidateID: 9, Status: "running"},
		{Kind: "library", CandidateID: 3, Status: "paused"},
	}
	for _, j := range legacy {
		if err := db.UpsertBacktestJob(j); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
	}
	// Open 时队列为空、旧表无数据 → 迁移空跑；种子后手动触发迁移（生产中发生在
	// 「旧库已有 backtest_jobs 数据 + 新代码首次打开」的时点）
	if err := db.migrateBacktestJobsToTasks(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tasks, err := db.ListResearchTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != len(legacy) {
		t.Fatalf("迁移行数=%d，期望 %d", len(tasks), len(legacy))
	}
	byRef := map[int64]ResearchTask{}
	for _, tk := range tasks {
		byRef[tk.RefID] = tk
		if tk.Priority != "low" {
			t.Fatalf("迁移行 priority 应为 low: %+v", tk)
		}
	}
	if tk := byRef[7]; tk.Type != TaskBacktestCandidate || tk.Status != TaskDone || tk.ResultNum != 0.05 {
		t.Fatalf("candidate done 行迁移错误: %+v", byRef[7])
	}
	if tk := byRef[0]; tk.Type != TaskBacktestNightly || tk.Status != TaskCancelled {
		t.Fatalf("nightly interrupted 应映射 cancelled: %+v", tk)
	}
	if tk := byRef[9]; tk.Status != TaskPreempted {
		t.Fatalf("candidate running 应映射 preempted(自动续跑): %+v", tk)
	}
	if tk := byRef[3]; tk.Type != TaskBacktestStrategy || tk.Status != TaskCancelled || tk.Error == "" {
		t.Fatalf("library 缺规则类型应 cancelled+原因: %+v", tk)
	}
	// 幂等：队列已有行后再次触发迁移不得回填/重复
	if err := db.migrateBacktestJobsToTasks(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	tasks2, _ := db.ListResearchTasks()
	if len(tasks2) != len(tasks) {
		t.Fatalf("二次迁移产生回填: %d → %d", len(tasks), len(tasks2))
	}
}

// TestD1ScoresUpsertAndQuery D1 评分历史：幂等覆盖 + 按日期读取。
func TestD1ScoresUpsertAndQuery(t *testing.T) {
	db := testDB(t)
	rows := []D1ScoreRow{
		{Code: "300750", Score: 32.5, Blocked: false, Reason: "中标利好"},
		{Code: "600519", Score: 8.0, Blocked: true, Reason: "业绩利空"},
	}
	if err := db.UpsertD1Scores("2026-08-22", rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 同键覆盖（保留最新一轮）
	if err := db.UpsertD1Scores("2026-08-22", []D1ScoreRow{{Code: "300750", Score: 35}}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := db.D1ScoresByDate("2026-08-22")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 || got["300750"] != 35 || got["600519"] != 8 {
		t.Fatalf("结果错误: %+v", got)
	}
	if other, _ := db.D1ScoresByDate("2026-08-21"); len(other) != 0 {
		t.Fatalf("日期隔离失败: %+v", other)
	}
}
