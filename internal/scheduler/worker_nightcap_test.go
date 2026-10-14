// §NIGHTCAP（2026-10-14）单夜占用上限 worker 侧行为腿（两条，与熔断口径腿分文件）：
//   - TestNightcapHeadBudgetDefersAndReleasesQueue：队首侧预算闸——低优任务本夜剩余预算
//     不足 MinNightStartMin 分钟时不再认领，写 defer_day 让位；**解堵**的证据是队首后面
//     的任务当夜照常被消费（旧实现里出队查询每 30s 把同一条取回来，后面的任务一条也
//     轮不上，现网 #416 连续六夜独占队首的另一半成因）；
//   - TestNightcapBudgetCapYieldsTask：运行侧预算截断——低优任务实际可跑时长被夹到
//     本夜剩余预算，到点子进程被杀，终态走「让位」（queued+defer_day=下一夜、
//     progress 保留、连败/累计两把熔断账都不动）而不是「失败」。
//
// 测试时钟说明：两腿都用注入时钟（周六盘后），但让位腿的 1 分钟预算截断发生在
// context.WithTimeout 的**真实时间**上，故该腿实测 ~65s（预算按整数分钟，无更短档）；
// 夜账入账用的 started_at 是真实墙钟，与注入时钟跨日不齐属测试工件，断言刻意不碰
// 夜账列（夜账翻页语义已由 store 层 TestNightBudgetLedgerPageFlip 钉死）。
//
// English: §NIGHTCAP worker-level behavior legs — the head-side budget gate defers an
// over-budget low task (proving head-of-line release by the next task running in its
// place), and the run-side cap turns budget expiry into a planned yield that leaves both
// breaker ledgers untouched.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// nightcapCallsFor 统计假二进制日志里「指定任务 ID 被 run-task 拉起」的次数
// （taskCommand 对 run-task 固定带 --task-id <id>；dataload 直连调用不带 ID 不计）。
// 队首让位腿用它证明：被让位的任务子进程一次都没起过。
func nightcapCallsFor(t *testing.T, logPath string, id int64) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	needle := "--task-id " + fmt.Sprintf("%d", id)
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "FAKE ") && strings.Contains(l, needle) {
			n++
		}
	}
	return n
}

// TestNightcapHeadBudgetDefersAndReleasesQueue §NIGHTCAP 队首侧预算闸（解堵腿）：
// 队首低优任务本夜已占 300 分钟（上限 240，余量夹 0）→ tick 应把它顺延到下一夜
// （defer_day=明天、行留 queued、error 写让位原因），且**同一次收尾后队首后面的任务
// 能照常入跑**——第二次 tick 里 B 执行完成，而被让位的 A 子进程一次都没起过。
// English: the head-of-line budget gate — an over-budget low task is deferred to tomorrow
// without running, and the next queued task runs in its place (the anti-head-of-line-blocking proof).
func TestNightcapHeadBudgetDefersAndReleasesQueue(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	fake := fakeScript(t, logPath) // run-task 立即成功（FAKE_SLEEP 未设，不睡）
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath) // 默认链 ["dataload"]：让位后链任务照常可排
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	// 时钟纪律（§DET-TIME，本文件首拨实测踩中）：DequeueHighestTask 的顺延过滤用
	// cntime.Now()（真实今日），defer_day 必须与它同源才判得出来——注入时钟若取过去
	// 的固定日期（周六盘后老配方），让位写下的「明天」对真实今天是过去式，过滤恒放行，
	// 队首每 30s 被重复让位、后面的任务永远轮不上（用例自己红出来的就是这个）。
	// 故取「真实今日 16:00」：盘后窗口任何一天都 eligible，日期维度全部现场派生。
	base := cntime.Now()
	clk := time.Date(base.Year(), base.Month(), base.Day(), 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s.setNow(func() time.Time { return clk })
	today := cntime.DayCompactOf(clk)
	tomorrow := cntime.DayCompactOf(clk.AddDate(0, 0, 1))

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	aID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 201, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	bID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 202, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 预记账：A 本夜（起跑日键=今天）已占 300 分钟 > 上限 240 → 剩余预算夹 0 < 起跑下限 15。
	// 走生产写入口 AddTaskNightUsed 记账，不走夹具裸写（判据与来源同源，§派生状态不是来源）。
	if err := qdb.AddTaskNightUsed(aID, today, 300); err != nil {
		t.Fatal(err)
	}

	s.tick() // 队首 A 撞预算闸 → 让位；本 tick 不再认领任何任务
	waitFor(t, 10*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(aID)
		return tk != nil && tk.DeferDay == tomorrow && tk.Status == store.TaskQueued
	}, "队首超预算任务应被顺延至下一夜且留在队列")
	a, _ := qdb.GetResearchTask(aID)
	if !strings.Contains(a.Error, "单夜占用") {
		t.Fatalf("让位原因应写进 error 列（运维只读这一列归因）, got %q", a.Error)
	}
	if nightcapCallsFor(t, logPath, aID) != 0 {
		t.Fatal("被让位的队首任务本夜不得拉起子进程")
	}

	s.tick() // 第二次 tick：出队查询跳过 defer_day>今天 的 A → 队首换人，B 照常消费
	waitFor(t, 15*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(bID)
		return tk != nil && tk.Status == store.TaskDone
	}, "让位后队首后面的任务应照常被执行（解堵的正面证据）")
	if nightcapCallsFor(t, logPath, bID) == 0 {
		t.Fatal("B 应真实拉起过子进程（不是被链任务顶替跑完）")
	}
	if b, _ := qdb.GetResearchTask(bID); b.NightUsedMin != 0 || b.DeferDay != "" {
		t.Fatalf("健康任务不应被记账/顺延误伤, got night_used=%d defer=%q", b.NightUsedMin, b.DeferDay)
	}
	a2, _ := qdb.GetResearchTask(aID)
	if a2.Status != store.TaskQueued || a2.DeferDay != tomorrow {
		t.Fatalf("A 应保持 queued+顺延到明天等下一夜, got status=%s defer=%q", a2.Status, a2.DeferDay)
	}
	waitIdleAndSettle(t, s)
}

// TestNightcapBudgetCapYieldsTask §NIGHTCAP 运行侧预算截断（让位腿，实测 ~65s）：
// 低优任务本夜已占 239 分钟（余 1 分钟 < 90 分钟单步超时）→ 实际可跑时长被夹到 1 分钟；
// 长睡子进程到点被杀，终态必须是「让位」而不是「失败」：
//   - status=yielded、行回 queued、defer_day=下一夜、error 写让位原因、progress 保留；
//   - 连败账 fail_streak 与累计回队账 requeue_total 都不得动（让位是设计内行为，
//     进熔断账=下一夜它带着旧账跑，两次让位就被误挂起）；
//   - 未跑完账 unfinished_streak 记 1（连着两夜没跑完才由 store 侧自动再顺延一夜）。
//
// 直接驱动 runTask（不经 tryStartNext）：队首侧起跑闸要求剩余预算 ≥15 分钟，而让位腿
// 需要 1 分钟档的截断——两把闸各测各的，这正是分两条腿的原因。
// English: the run-side budget cap — a low task with 1 minute of night budget left is killed
// at the cap and *yields* (queued + defer_day tomorrow, progress kept, both breaker ledgers
// untouched), driven via runTask directly because the head gate's MinNightStartMin floor
// intentionally prevents sub-15-minute starts through the normal path.
func TestNightcapBudgetCapYieldsTask(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	// 本腿专用假二进制：run-task 长睡 300s（必被 1 分钟预算截断击杀），其余调用立即成功。
	content := "#!/bin/sh\n" +
		"echo \"FAKE $@\" >> " + shQuote(logPath) + "\n" +
		"case \"$*\" in\n" +
		"  *run-task*) exec sleep 300 ;;\n" +
		"  *) ;;\n" +
		"esac\n"
	scriptPath := filepath.Join(dir, "sleepbin.sh")
	if err := os.WriteFile(scriptPath, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(scriptPath, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	// 时钟纪律同上腿：真实今日 16:00，预算日键 defer_day 全部现场派生（不取过去固定日期）。
	base := cntime.Now()
	clk := time.Date(base.Year(), base.Month(), base.Day(), 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s.setNow(func() time.Time { return clk })
	today := cntime.DayCompactOf(clk)
	tomorrow := cntime.DayCompactOf(clk.AddDate(0, 0, 1))

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 301, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := qdb.AddTaskNightUsed(id, today, 239); err != nil { // 余 1 分钟
		t.Fatal(err)
	}
	if ok, err := qdb.ClaimResearchTask(id); err != nil || !ok {
		t.Fatalf("认领任务失败: ok=%v err=%v", ok, err)
	}
	// tryStartNext 认领后的两步走：Claim 置 running+started_at，UpdateTaskClaimed 预写 1% 基线。
	// 让位腿要断言「progress 保留」，所以这里把两步都拨齐（只 Claim 的话 progress 为空，
	// 保留断言就没有对照真值）。
	if err := qdb.UpdateTaskClaimed(id); err != nil {
		t.Fatal(err)
	}
	tk, err := qdb.GetResearchTask(id)
	if err != nil || tk == nil {
		t.Fatalf("读取已认领任务失败: %v", err)
	}
	go s.runTask(qdb, cfg, *tk)

	// 预算 1 分钟真实到点 → 子进程被杀 → 终态走让位。waitFor 上限 90s 覆盖截断+收尾落库。
	waitFor(t, 90*time.Second, func() bool {
		cur, _ := qdb.GetResearchTask(id)
		return cur != nil && cur.Status == store.TaskQueued && cur.DeferDay == tomorrow
	}, "预算到点后任务应让位（回 queued+defer_day=下一夜）而不是失败/挂起")
	cur, _ := qdb.GetResearchTask(id)
	if !strings.Contains(cur.Error, "单夜占用达上限") {
		t.Fatalf("让位原因应写「单夜占用达上限」, got %q", cur.Error)
	}
	if cur.FailStreak != 0 || cur.RequeueTotal != 0 {
		t.Fatalf("让位不得动熔断账（fail_streak=%d requeue_total=%d，应为 0/0）",
			cur.FailStreak, cur.RequeueTotal)
	}
	if cur.UnfinishedStreak != 1 {
		t.Fatalf("未跑完账应记 1（连续两夜没跑完才自动再顺延）, 实际 %d", cur.UnfinishedStreak)
	}
	if cur.Progress != "1%" {
		t.Fatalf("让位应保留 progress（跟失败回队清 progress 的口径刻意不同）, got %q", cur.Progress)
	}
	if cur.Status != store.TaskQueued {
		t.Fatalf("让位终态应回 queued 等下一夜, 实际 %s", cur.Status)
	}
	waitIdleAndSettle(t, s)
}
