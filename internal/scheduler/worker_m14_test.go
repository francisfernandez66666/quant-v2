// §M14（2026-09-22）连败熔断 worker 侧守护测试（反例锁）：
//   - 确定性失败连败达阈值 → failed_needs_attention 挂起终态，此后绝不再被出队执行，
//     高优告警恰好一次，挂起全程留痕（error 列 / opslog / state 文件）；
//   - §NIGHTCAP（2026-10-14）口径归一：连败计数**不再比对错误指纹**——超时与被抢占两条
//     文案天然交替时旧实现每轮都把 streak 洗回 1（现网 #416 六夜空烧的成因之一），
//     所以「换着原因失败」同样累加连败，达 ConsecutiveFailLimit(6) 一并挂起，
//     挂起文案写「连败熔断」而不是旧口径的「同因连败」（照旧文案找一个不存在的共同原因）。
//
// §QUEUE-REQUEUE（2026-10-12）累计回队闸的 worker 侧说明：失败路径上 streak 与 total 同步
// 递增，而连败阈值(6) < 累计阈值(8)，失败-only 路径数学上必先触连败闸——累计闸的独占射程
// 是「抢占/重启续跑」（RequeueAfterPreemption 只记 total 不记 streak），该路径的阈值闸在
// store 层 TestTaskPreemptRequeueKeepsStreakAndCounts 钉死，worker 接线由
// TestWorkerPreemptsLowByHigh 覆盖，此处不再重复一根同形腿（旧 TestMixedCauseRequeueCapParksTask
// 的前提「异因逃过同因闸」已随口径归一作废，用例删除）。
//   - 夜间链收尾：同链有步被挂起时 done 位绝不置起（排空 ≠ 跑完）。
//
// §NIGHTCAP 本批的让位/占用行为腿在 worker_nightcap_test.go（队首预算闸、单夜占用到点让位）。
//
// English: M14 guard tests — the failure streak breaker no longer compares error fingerprints
// (the timeout/preemption text alternation used to reset it forever), so alternating causes also
// park at ConsecutiveFailLimit with the new「连败熔断」wording; the cause-independent requeue cap
// is only reachable via preemption requeues and is pinned at store level; a parked chain step
// keeps the nightly done flag false.
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

// roundSettleWait 轮内等待预算（§flake 根修 2026-10-13）。
// 为什么不是 15 秒：门禁 -full 把整包压在同一时刻跑，本机单跑本组用例 1.80s 全绿，
// 第 5 遍整轮回归里 TestSameCauseBreakerParksTask 第 2 轮 15.61s 超时判红——
// 红的是"子进程/排水 goroutine 刚好没排上"，不是判据本身（§并发测试分相同族）。
// 预算抬到 60s 只是不让调度延迟冒充失败；真正的确定性来自 beginRound 先等排水停手。
const roundSettleWait = 60 * time.Second

// beginRound 开一轮：先等后台排水停手（busy 期 tick 是空转，见 waitIdleAndSettle 注释），
// 再把注入时钟拨过 5min 失败冷却窗，最后 tick 一次。轮次边界由"空闲"这一显式状态钉住，
// 而不是交给"上一轮的异步尾巴来得及结束"。
func beginRound(t *testing.T, s *Scheduler, clk *manualClock) {
	t.Helper()
	waitIdleAndSettle(t, s)
	clk.advance(10 * time.Minute)
	s.tick()
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

// TestConsecutiveFailBreakerParksTask §M14 反例锁（§NIGHTCAP 口径归一：连败 6 次后不再重排）：
// 一条恒定失败的 high 任务被反复回队重试，触阈值后必须挂起为 failed_needs_attention、
// 告警一次、留痕可审计，且后续 tick 绝不再拉起子进程（不再烧算力）。
func TestConsecutiveFailBreakerParksTask(t *testing.T) {
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
	// §NIGHTCAP 阈值 6 < 累计 8：本用例恒定失败，若挂起来自累计闸（文案带「累计回队」）
	// 说明连败闸被绕过，由下方文案断言反向锁死——先触的必须是更可诊断的那条。
	for round := 0; round < store.ConsecutiveFailLimit+3; round++ {
		beginRound(t, s, clk) // 越过 5min 失败冷却窗，模拟夜间逐轮慢速重试
		waitFor(t, roundSettleWait, func() bool {
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
		t.Fatalf("连败 %d 次后应挂起 failed_needs_attention", store.ConsecutiveFailLimit)
	}
	tk, _ := qdb.GetResearchTask(id)
	if tk.FailStreak != store.ConsecutiveFailLimit {
		t.Fatalf("连败计数应为 %d, 实际 %d", store.ConsecutiveFailLimit, tk.FailStreak)
	}
	if !strings.Contains(tk.Error, "连败熔断") || !strings.Contains(tk.Error, "数据缺失") {
		t.Fatalf("挂起留痕应含熔断标记+原始原因, got %q", tk.Error)
	}
	if strings.Contains(tk.Error, "累计回队") {
		t.Fatalf("失败-only 路径必先触连败闸（6<8），挂起不应来自累计闸, got %q", tk.Error)
	}
	if got := runTaskCalls(t, logPath); got != store.ConsecutiveFailLimit {
		t.Fatalf("子进程应恰好被执行 %d 次（触阈值即停）, 实际 %d", store.ConsecutiveFailLimit, got)
	}
	// §M14 高优告警恰好一次（挂起是瞬时跃迁，天然去重）
	// §flake 根修（2026-09-22 收尾核验）：告警在 runner 协程里于「DB 挂起落库」之后才异步
	// 触发——上方 waitFor 观察到 NeedsAttention 即放行，高负载下断言可能抢在 fn() 执行前
	// 读到 0（本包 waitIdleAndSettle 注释所述同款异步尾巴）。改轮询等待告警落地再断言恰好 1。
	waitFor(t, 15*time.Second, func() bool { return alerts.count() >= 1 }, "熔断告警应送达")
	if got := alerts.count(); got != 1 {
		t.Fatalf("熔断告警应恰好 1 次, 实际 %d", got)
	}
	// §NIGHTCAP 成因分岔锁：连败闸触的告警必须报「连败 N 次」——报成「累计回队」就是
	// 告警撒谎（运维照假成因排查一个不存在的转圈史）。notifyRequeueBreaker 的分岔没跟上
	// 新前缀「连败熔断」时正是这个形态（本批实锤后修）。
	alerts.mu.Lock()
	streakBody := strings.Join(alerts.calls, "\n")
	alerts.mu.Unlock()
	if !strings.Contains(streakBody, "连败") || strings.Contains(streakBody, "累计回队") {
		t.Fatalf("连败闸告警正文应报连败次数（不得报累计回队）, got %q", streakBody)
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
	if got := runTaskCalls(t, logPath); got != store.ConsecutiveFailLimit {
		t.Fatalf("挂起后不得再拉起子进程（不再重排队烧算力）, 实际执行 %d 次", got)
	}
	if got := alerts.count(); got != 1 {
		t.Fatalf("挂起后重复 tick 不应再告警, 实际 %d 次", got)
	}
	waitIdleAndSettle(t, s)
}

// TestDifferentCauseFailuresStillPark §NIGHTCAP（2026-10-14）口径归一的正面腿（旧
// TestDifferentCauseFailuresNeverBreaker 的反演）：连败判据不再比对错误指纹——每次失败
// 原因各不相同（rotate 假二进制逐轮换文案）**同样累加**连败，达 ConsecutiveFailLimit(6)
// 一并挂起，挂起文案是「连败熔断」而不是「累计回队」（6<8，失败-only 路径先触连败闸）。
// 旧实现里这正是 #416 形态的漏洞：超时与被抢占文案交替，streak 每轮洗回 1，熔断读数恒 1
// 而任务空烧六夜。
// English: the inverted guard — alternating failure reasons no longer reset the streak;
// the task still parks at ConsecutiveFailLimit via the (more diagnostic) streak breaker.
func TestDifferentCauseFailuresStillPark(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	bin := failRunTaskScript(t, dir, logPath, true) // 每次末行原因带自增轮次（指纹各不相同）
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
	tripped := false
	// 上界留 +3 轮余量：若换因洗账的旧病复发（streak 每轮归 1），累计闸 8 也要 8 轮才触，
	// 循环 9 轮仍挂起则文案断言会判出「挂起来自累计闸」＝口径归一失效，同样红。
	for round := 0; round < store.ConsecutiveFailLimit+3; round++ {
		beginRound(t, s, clk)
		waitFor(t, roundSettleWait, func() bool {
			tk, _ := qdb.GetResearchTask(id)
			return tk != nil && (tk.RetryCount > prevRetry || tk.Status == store.TaskNeedsAttention)
		}, fmt.Sprintf("第 %d 轮异因失败应回队重试或挂起", round))
		tk, _ := qdb.GetResearchTask(id)
		prevRetry = tk.RetryCount
		if tk.Status == store.TaskNeedsAttention {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatalf("换因失败 %d 轮后必须挂起（口径归一：连败不看原因）", store.ConsecutiveFailLimit)
	}
	tk, _ := qdb.GetResearchTask(id)
	if tk.FailStreak != store.ConsecutiveFailLimit {
		t.Fatalf("换因失败连败账应逐轮累加到 %d（不再每轮洗回 1）, 实际 %d", store.ConsecutiveFailLimit, tk.FailStreak)
	}
	if tk.RequeueTotal != store.ConsecutiveFailLimit {
		t.Fatalf("累计回队账应与连败同步为 %d, 实际 %d", store.ConsecutiveFailLimit, tk.RequeueTotal)
	}
	if !strings.Contains(tk.Error, "连败熔断") || strings.Contains(tk.Error, "累计回队") {
		t.Fatalf("挂起必须来自连败闸（先触、更可诊断）, got %q", tk.Error)
	}
	if got := store.RequeueTripReasonOf(tk.Error); got != "consecutive_fail" {
		t.Fatalf("挂起成因应判读为 consecutive_fail, 实际 %s（文案=%q）", got, tk.Error)
	}
	if got := runTaskCalls(t, logPath); got != store.ConsecutiveFailLimit {
		t.Fatalf("子进程应恰好执行 %d 次（达阈值即停）, 实际 %d", store.ConsecutiveFailLimit, got)
	}
	waitFor(t, 15*time.Second, func() bool { return alerts.count() >= 1 }, "熔断告警应送达")
	if got := alerts.count(); got != 1 {
		t.Fatalf("熔断告警应恰好 1 次, 实际 %d", got)
	}
	// 挂起后继续 tick：绝不复活、绝不再出队执行
	for round := 0; round < 3; round++ {
		clk.advance(10 * time.Minute)
		s.tick()
	}
	time.Sleep(200 * time.Millisecond)
	if tk2, _ := qdb.GetResearchTask(id); tk2.Status != store.TaskNeedsAttention {
		t.Fatalf("挂起终态不应被后续 tick 改动, 实际 %s", tk2.Status)
	}
	waitIdleAndSettle(t, s)
}

// （旧 TestMixedCauseRequeueCapParksTask 已删：其前提「异因逃过同因闸、只有累计闸能拦」
// 随 §NIGHTCAP 口径归一作废——失败-only 路径 streak==total 同步递增而 6<8，必先触连败闸，
// 该形态已由上方 TestDifferentCauseFailuresStillPark 正面钉死；累计闸的独占射程只剩
// 抢占/重启续跑，其阈值闸在 store 层 TestTaskPreemptRequeueKeepsStreakAndCounts 钉死。）

// TestFinishTaskParkedChainKeepsDoneFalse §QUEUE-REQUEUE 收尾账本（排空 ≠ 跑完）：
// 夜间链某步被熔断闸挂起后，它既不在活跃集合（queued/running/paused/preempted）也不再重试，
// 旧实现只看「同链没有活跃行」就把这一天的 done 位置起——前端与心跳都读这个位，
// 于是「有步根本没跑成」在展示面变成「这一天全跑完了」（本仓锤过的降级报成功族）。
// 判据是 ChainParkedCount：挂起>0 ⇒ done 保持 false；把挂起步改成成功完成后 done 才允许置起
// （后置腿同时证明这条闸不是「恒不置位」的死判断，避免用保守挡住所有成功）。
func TestFinishTaskParkedChainKeepsDoneFalse(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "trading.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	cfg := cfgSamples("unused-fake-bin", dbPath)
	cfg.Nightly.Steps = []string{"dataload", "sector_rebuild", "list"}
	cfg.Nightly.AbortOnError = false
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, loc)
	const day = "20260822"

	// 半条链的终态快照：序位 0 被累计闸挂起，序位 1 已完成，序位 2 正在收尾。
	parkedID, err := db.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskDataload, Priority: "low", Status: store.TaskNeedsAttention,
		Payload: "{}", ChainDay: day, ChainSeq: 0,
		Error: "累计回队熔断(20次回队，原因每次不同未被同因闸捕获)：网络抖动",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskSectorRebuild, Priority: "low", Status: store.TaskDone,
		Payload: "{}", ChainDay: day, ChainSeq: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// 尾步按「已落库为 done」入库：真实顺序是 runner 先把终态写进行、再调 finishTask 收尾
	// （finishTask 不写这一行）。若这里留 running，活跃集合会先命中尾步自身，
	// done=false 就成了「链没排空」的假证据而不是「有步挂起」（判据自伤形态）。
	tailID, err := db.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskList, Priority: "low", Status: store.TaskDone,
		Payload: "{}", ChainDay: day, ChainSeq: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{statePath: filepath.Join(tmp, "research_state.json")}
	s.setNow(func() time.Time { return now })
	s.mu.Lock()
	s.state.Day = day // done 位只对「今天这条链」生效，Day 不同步属正常短路
	s.mu.Unlock()

	tail, _ := db.GetResearchTask(tailID)
	if tail == nil {
		t.Fatal("尾步任务读取失败")
	}
	s.finishTask(db, cfg, tail, store.TaskDone, "")

	s.mu.Lock()
	done := s.state.Done
	s.mu.Unlock()
	if done {
		t.Fatal("同链存在挂起步时 done 位必须保持 false（排空不等于跑完）")
	}
	// 挂起读数自证：本用例的前置状态确实是「活跃集合为空 + 挂起 1 条」，
	// 否则 done=false 可能来自别的原因（判据自伤形态）。
	n, err := db.ChainParkedCount(day)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("前置应为 1 条挂起步, 实际 %d", n)
	}
	if act, _ := db.ActiveResearchTasks(); len(act) != 0 {
		t.Fatalf("前置应为队列已排空, 实际活跃 %d 条", len(act))
	}

	// —— 后置腿：把挂起步改成成功完成后再收尾一次，done 必须置起（闸不是恒 false）——
	if err := db.UpdateTaskRunState(parkedID, store.TaskDone, "", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	s2 := &Scheduler{statePath: filepath.Join(tmp, "research_state2.json")}
	s2.setNow(func() time.Time { return now })
	s2.mu.Lock()
	s2.state.Day = day
	s2.mu.Unlock()
	s2.finishTask(db, cfg, tail, store.TaskDone, "")
	s2.mu.Lock()
	done2 := s2.state.Done
	s2.mu.Unlock()
	if !done2 {
		t.Fatal("挂起步被处置成完成后 done 位应置起（否则夜间链永远报未完成）")
	}
}
