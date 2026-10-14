// research_tasks 队列单测：入队/出队排序（优先级、preempted 置顶、链序 FIFO）、
// 认领 CAS、控制标志消费、启动恢复、backtest_jobs 一次性迁移映射。
package store

import (
	"strings"
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

// TestTaskSameCauseBreaker §M14（2026-09-22）同因连败熔断（取代 §P1-11 异因混计上限）：
//  1. 同一失败指纹连败达阈值 → failed_needs_attention 挂起终态，不再出队；
//  2. 异因失败不误判同因——指纹每次不同则连败恒为 1，但 §QUEUE-REQUEUE（2026-10-12）
//     另有一条与原因无关的累计回队闸把它们收口（旧"异因不设上限"正是 33 次回队的空子）；
//  3. 人工 Requeue 复活 → 回到 queued 且连败计数清零（重新起算）。
func TestTaskSameCauseBreaker(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// 1) 连败熔断：前 N-1 次仍回队尾可重试，第 N 次挂起。
	//    §NIGHTCAP（2026-10-14）判据归一后这条闸叫"连败"而不是"同因"——本段故意让每次
	//    失败原因都不同，只要连着没成就必须累加（旧的"换因从 1 重计"正是 #416 空烧六轮
	//    而熔断读数恒 1 的根因），由下面第二段的异因循环共同自证。
	for i := 1; i <= ConsecutiveFailLimit; i++ {
		if err := db.RequeueFailedTask(id, "boom-异因-"+itoa(i)); err != nil {
			t.Fatalf("第 %d 次重入队: %v", i, err)
		}
		tk, err := db.GetResearchTask(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if i < ConsecutiveFailLimit {
			if tk.Status != TaskQueued {
				t.Fatalf("第 %d 次应仍为 queued, 实际 %s", i, tk.Status)
			}
			if tk.FailStreak != i {
				t.Fatalf("第 %d 次 fail_streak 应为 %d（换因不再洗账）, 实际 %d", i, i, tk.FailStreak)
			}
			next, _ := db.DequeueHighestTask()
			if next == nil || next.ID != id {
				t.Fatalf("第 %d 次失败任务应能出队重试", i)
			}
		} else {
			if tk.Status != TaskNeedsAttention {
				t.Fatalf("连败 %d 次应挂起 failed_needs_attention, 实际 %s", i, tk.Status)
			}
			if !strings.Contains(tk.Error, "连败熔断") || !strings.Contains(tk.Error, "boom-异因-") {
				t.Fatalf("挂起留痕应含熔断标记+原因, got %q", tk.Error)
			}
			if got := RequeueTripReasonOf(tk.Error); got != "consecutive_fail" {
				t.Fatalf("挂起成因应判读为 consecutive_fail, 实际 %s", got)
			}
		}
	}
	// 挂起终态不再出队（同因失败任务的死亡证明）
	if next, _ := db.DequeueHighestTask(); next != nil {
		t.Fatalf("failed_needs_attention 不应出队, 取到 %+v", next)
	}
	// 3) 人工 Requeue 复活 + 计数清零
	if err := db.RequeueTask(id); err != nil {
		t.Fatalf("人工 Requeue: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk == nil || tk.Status != TaskQueued || tk.FailStreak != 0 {
		t.Fatalf("复活后应 queued 且连败清零, got %+v", tk)
	}
	// 复活后再 1 次同因失败：连败从 1 重新计（不吃挂起前旧账）
	if err := db.RequeueFailedTask(id, "boom"); err != nil {
		t.Fatalf("复活后重入队: %v", err)
	}
	tk, _ = db.GetResearchTask(id)
	if tk.FailStreak != 1 || tk.Status != TaskQueued {
		t.Fatalf("复活后同因首败应 streak=1 且回队, got %+v", tk)
	}
	// 成功清零同因连败计数（2 连败后成功，再来同因 2 连败不应触熔断）
	if err := db.UpdateTaskRunState(id, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("done: %v", err)
	}
	tk, _ = db.GetResearchTask(id)
	if tk.FailStreak != 0 || tk.FailFP != "" {
		t.Fatalf("成功后连败计数应清零, got %+v", tk)
	}
	// 2) §NIGHTCAP（2026-10-14）改判：异因循环现在由**连败闸**收口（累计闸 8 > 连败 6，
	//    失败路径上先触连败）。旧断言"换因后 streak 恒 1、只能等累计 20 圈"就是 #416 空烧
	//    六轮而熔断读数恒 1 的护身符；现在异因照样累加，第 6 次挂起、成因如实判读为
	//    consecutive_fail（不是同因也不是累计）。累计闸在失败路径上退为兜底，
	//    其独立存活面由抢占路径的 TestTaskPreemptRequeueKeepsStreakAndCounts 证明。
	id2, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue2: %v", err)
	}
	for i := 1; i < ConsecutiveFailLimit; i++ {
		if err := db.RequeueFailedTask(id2, "boom-异因-"+itoa(i)); err != nil {
			t.Fatalf("异因第 %d 次: %v", i, err)
		}
		tk2, err := db.GetResearchTask(id2)
		if err != nil {
			t.Fatalf("get2: %v", err)
		}
		// 每轮都还能出队重试，且两把尺子各计各的：streak/total 逐轮 +1；
		// unfinished_streak 不在本入口动（由 worker 终态分派统一记，见 MarkTaskUnfinished）。
		if tk2.Status != TaskQueued || tk2.FailStreak != i || tk2.RequeueTotal != i || tk2.UnfinishedStreak != 0 {
			t.Fatalf("异因第 %d 次应 queued/streak=%d/total=%d/unfinished=0, got %+v", i, i, i, tk2)
		}
		if next, _ := db.DequeueHighestTask(); next == nil || next.ID != id2 {
			t.Fatalf("异因第 %d 次应能出队重试", i)
		}
	}
	if err := db.RequeueFailedTask(id2, "boom-异因-最后一轮"); err != nil {
		t.Fatalf("异因末轮: %v", err)
	}
	tk2, _ := db.GetResearchTask(id2)
	if tk2 == nil || tk2.Status != TaskNeedsAttention {
		t.Fatalf("连败达 %d 次应挂起, got %+v", ConsecutiveFailLimit, tk2)
	}
	if !strings.HasPrefix(tk2.Error, "连败熔断") || RequeueTripReasonOf(tk2.Error) != "consecutive_fail" {
		t.Fatalf("挂起留痕必须写清是连败闸触的（不能冒充同因/累计）, got %q", tk2.Error)
	}
	if next, _ := db.DequeueHighestTask(); next != nil {
		t.Fatalf("连败闸挂起后不应出队, 取到 %+v", next)
	}
	// 人工复活把所有尺子一起清零：不清 total/顺延账的话，owner 刚复活的任务一跑就再次触闸。
	if err := db.RequeueTask(id2); err != nil {
		t.Fatalf("人工 Requeue(连败闸): %v", err)
	}
	tk2, _ = db.GetResearchTask(id2)
	if tk2.Status != TaskQueued || tk2.RequeueTotal != 0 || tk2.FailStreak != 0 || tk2.UnfinishedStreak != 0 || tk2.DeferDay != "" {
		t.Fatalf("复活后应 queued 且连败/累计/顺延账全清零, got %+v", tk2)
	}
}

// TestTaskPreemptRequeueKeepsStreakAndCounts §QUEUE-REQUEUE 抢占续跑通道：
// 旧形态走人工复活入口 RequeueTask，每次 worker 重启/让位都把同因连败账清零——
// 这是 33 次回队没被熔断拦住的第二条通道。现行为：
//  1. 抢占回队**不清** fail_fp/fail_streak（连败账跨重启有效）；
//  2. 抢占回队计入 requeue_total（反复让位也跑不完＝需要人看）；
//  3. 达上限挂起，且挂起文案是"含抢占/重启续跑"而不是伪造一条崩溃原因。
func TestTaskPreemptRequeueKeepsStreakAndCounts(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// 先攒一次同因失败（streak=1），再模拟"重启让位"——旧实现这一步就会把 streak 抹成 0。
	if err := db.RequeueFailedTask(id, "单步超时(8h0m0s)，已回队尾重试"); err != nil {
		t.Fatalf("首败: %v", err)
	}
	if err := db.UpdateTaskRunState(id, TaskPreempted, "10%", 0, "", "调度器停止，断点缓存有效"); err != nil {
		t.Fatalf("preempted: %v", err)
	}
	if err := db.RequeueAfterPreemption(id); err != nil {
		t.Fatalf("抢占回队: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.Status != TaskQueued || tk.FailStreak != 1 {
		t.Fatalf("抢占回队不得清零同因连败账, got %+v", tk)
	}
	if tk.RequeueTotal != 2 { // 1 次失败回队 + 1 次抢占回队
		t.Fatalf("抢占回队必须计入累计闸, got %+v", tk)
	}
	// 反复抢占续跑（原因每次不同，同因闸永不触）→ 由累计闸收口。
	// 每轮都先落 preempted 再回队：RequeueAfterPreemption 只认 preempted/paused（0 行会报错），
	// 这与 worker 的真实顺序一致（终态落库 → 回队翻转），不给"绕过状态闸"留合法通道。
	for i := tk.RequeueTotal + 1; i < TotalRequeueLimit; i++ {
		if err := db.UpdateTaskRunState(id, TaskPreempted, "10%", 0, "", "调度器停止，断点缓存有效"); err != nil {
			t.Fatalf("preempted 第 %d 次: %v", i, err)
		}
		if err := db.RequeueAfterPreemption(id); err != nil {
			t.Fatalf("抢占回队第 %d 次: %v", i, err)
		}
	}
	tk, _ = db.GetResearchTask(id)
	if tk.Status != TaskQueued || tk.RequeueTotal != TotalRequeueLimit-1 {
		t.Fatalf("未达上限时抢占任务应仍 queued 且计数=%d, got %+v", TotalRequeueLimit-1, tk)
	}
	if err := db.RequeueAfterPreemption(id); err != nil {
		t.Fatalf("末轮抢占: %v", err)
	}
	tk, _ = db.GetResearchTask(id)
	if tk.Status != TaskNeedsAttention || !strings.HasPrefix(tk.Error, "累计回队熔断") {
		t.Fatalf("抢占续跑达上限应挂起并留累计闸文案, got %+v", tk)
	}
	if !strings.Contains(tk.Error, "含抢占/重启续跑") {
		t.Fatalf("文案必须说明这条是抢占累计（不是崩溃）, got %q", tk.Error)
	}
	// 挂起行不再出队
	if next, _ := db.DequeueHighestTask(); next != nil {
		t.Fatalf("挂起后不应出队, 取到 %+v", next)
	}
}

// TestTaskPreemptRequeueZeroRowIsLoud §QUEUE-REQUEUE 状态闸的现形腿：queued 行调用抢占回队
// 必须报错，而不是"改了 0 行还返回 nil"——静默 0 行意味着累计闸一次都没计上，
// 任务照样无限转圈，而日志全绿（本仓锤过的静默失败族）。
func TestTaskPreemptRequeueZeroRowIsLoud(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.RequeueAfterPreemption(id); err == nil {
		t.Fatal("queued 行的抢占回队必须现形（0 行不是成功）")
	} else if !strings.Contains(err.Error(), "抢占回队未生效") {
		t.Fatalf("报错文案要指名是哪条腿没落上, got %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.RequeueTotal != 0 {
		t.Fatalf("未生效时不得留下计数, got %+v", tk)
	}
}

// TestTaskRequeueTripReasonReadsBothBreakers 触闸成因回读：两个前缀各自归因，
// 其它 error（人工写的、旧数据）不得被冒充成其中任何一种。
func TestTaskRequeueTripReasonReadsBothBreakers(t *testing.T) {
	cases := []struct{ errText, want string }{
		{"连败熔断(6连败，不看失败原因)：boom", "consecutive_fail"},
		{"同因连败熔断(10连败)：boom", "same_cause"}, // §NIGHTCAP 旧文案（发版前挂起的行）仍可判读
		{"累计回队熔断(8次回队)：boom", "total_requeue"},
		{"运行失败(exit status 1)", "requeue"},
		{"", "requeue"},
	}
	for _, c := range cases {
		if got := RequeueTripReasonOf(c.errText); got != c.want {
			t.Fatalf("成因回读 %q → %s，期望 %s", c.errText, got, c.want)
		}
	}
}

// TestTaskRequeueTotalSurvivesSuccess 累计闸跨"偶发成功"不清账（设计取舍，反例锁）：
// 中间成功一次不是免罪——跑成了还继续重排才是空烧的佐证；只有人工 Requeue 才清零。
func TestTaskRequeueTotalSurvivesSuccess(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.RequeueFailedTask(id, "网络抖动"); err != nil {
		t.Fatalf("失败回队: %v", err)
	}
	if err := db.UpdateTaskRunState(id, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("done: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.FailStreak != 0 || tk.RequeueTotal != 1 {
		t.Fatalf("成功应清同因账、不清累计账, got %+v", tk)
	}
}

// TestChainParkedCount §QUEUE-REQUEUE 链收尾的诚实位：挂起态不在 ActiveResearchTasks 集合里，
// "队列排空"必须由挂起步数单独证明，否则链会把"某步被熔断请出队列"报成"这一天全跑完"。
// 三态自证：无链日＝0；全跑完＝0；有挂起步＝该数。
func TestChainParkedCount(t *testing.T) {
	db := testDB(t)
	a, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskBacktestNightly, Payload: `{}`, ChainDay: "20261012", ChainSeq: 1})
	if err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	b, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskBacktestStrategy, Payload: `{}`, ChainDay: "20261012", ChainSeq: 2})
	if err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	// 空串链日＝无链任务，不参与统计（也不报错）
	if n, err := db.ChainParkedCount(""); err != nil || n != 0 {
		t.Fatalf("无链日应 (0,nil), got (%d,%v)", n, err)
	}
	// 两条都正常跑完 → 0
	if err := db.UpdateTaskRunState(a, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("done a: %v", err)
	}
	if err := db.UpdateTaskRunState(b, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("done b: %v", err)
	}
	if n, err := db.ChainParkedCount("20261012"); err != nil || n != 0 {
		t.Fatalf("全跑完时应 0, got (%d,%v)", n, err)
	}
	// 把 b 改成挂起态（模拟熔断闸请出队列）→ 必须数出来
	if err := db.UpdateTaskRunState(b, TaskNeedsAttention, "", 0, "", "累计回队熔断(20次回队)：boom"); err != nil {
		t.Fatalf("park b: %v", err)
	}
	n, err := db.ChainParkedCount("20261012")
	if err != nil || n != 1 {
		t.Fatalf("挂起 1 步必须现形, got (%d,%v)", n, err)
	}
	// 挂起行同时不得出现在"活跃/未终结"集合里（这条锁住的就是 finishTask 那个盲点的前提）
	active, err := db.ActiveResearchTasks()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	for _, tk := range active {
		if tk.ID == b {
			t.Fatalf("挂起行不应算活跃（正因如此链收尾必须单独数挂起）, got %+v", active)
		}
	}
	// 别的链日不受影响
	if n, err := db.ChainParkedCount("20261011"); err != nil || n != 0 {
		t.Fatalf("邻日应 0, got (%d,%v)", n, err)
	}
}
