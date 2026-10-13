// §M14（2026-09-22）同因连败熔断 worker 侧守护测试（反例锁）：
//   - 确定性失败（同因）连败达阈值 → failed_needs_attention 挂起终态，此后绝不再被出队执行，
//     高优告警恰好一次，挂起全程留痕（error 列 / opslog / state 文件）；
//   - 异因失败不误伤：每次失败指纹不同则连败恒从 1 重计，维持回队重试主策略。
//
// §QUEUE-REQUEUE（2026-10-12）本文件再加两条 worker 侧守护：
//   - 异因循环不再是「无限循环」：不管原因怎么换，累计回队达 TotalRequeueLimit 也必须挂起，
//     且挂起文案/告警必须写清是「累计回队」而不是「同因连败」（现网 33 次回队的成因就在这条漏网闸上）；
//   - 夜间链收尾：同链有步被挂起时 done 位绝不置起（排空 ≠ 跑完）。
//
// English: M14 guard tests — identical-cause streak parks the task (one-shot alert, no more
// requeue); mixed-cause failures never trip the *streak* breaker but are capped by the
// cause-independent requeue limit; a parked chain step keeps the nightly done flag false.
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
		beginRound(t, s, clk)
		waitFor(t, roundSettleWait, func() bool {
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

// TestMixedCauseRequeueCapParksTask §QUEUE-REQUEUE（2026-10-12）累计回队闸的 worker 侧证据：
// 现网实录是「同一个 high 任务被 8 小时单步超时反复送回队尾 33 次」——每轮失败原因都不同
// （网络/上游/分区缺失轮换），指纹一变 §M14 的同因连败就归零，于是旧实现里这条任务可以
// 永远排在队列里无限烧算力，而夜间验收只看得到「队列 31 条 > 30」这一个数、归不了因。
// 本用例把这条漏网闸钉成行为腿：异因循环必须达 TotalRequeueLimit 挂起，且告警/库内文案
// 必须写成「累计回队」（写成「同因连败」会把真实成因在运维面上撒谎，正是本批要修的病根）。
func TestMixedCauseRequeueCapParksTask(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	bin := failRunTaskScript(t, dir, logPath, true) // rotate=true：每轮末行原因各不相同
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(bin, dbPath)
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	alerts := &alertCapture{}
	s.SetAlertFunc(alerts.fn)
	loc := time.FixedZone("CST", 8*3600)
	clk := &manualClock{t: time.Date(2026, 8, 22, 16, 0, 0, 0, loc)} // 周六盘后
	s.setNow(clk.now)

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 99, Priority: "high", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	prevRetry := 0
	tripped := false
	// 上界留 +3 轮余量：若累计闸失效，这里会一直 queued 转圈，最终由下面的 tripped 断言判红
	// （而不是让循环本身悄悄跑不完）。
	for round := 0; round < store.TotalRequeueLimit+3; round++ {
		beginRound(t, s, clk) // 越过 5min 失败冷却窗
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
		t.Fatalf("异因循环 %d 次后应被累计回队闸挂起，实际仍在回队", store.TotalRequeueLimit)
	}
	tk, _ := qdb.GetResearchTask(id)
	if tk.RequeueTotal != store.TotalRequeueLimit {
		t.Fatalf("累计回队计数应为 %d, 实际 %d", store.TotalRequeueLimit, tk.RequeueTotal)
	}
	// 成因自证：异因每次从 1 重计 ⇒ 同因闸确实没被触发，挂起只能来自累计闸。
	if tk.FailStreak != 1 {
		t.Fatalf("异因循环 fail_streak 应恒为 1（同因闸未触发）, 实际 %d", tk.FailStreak)
	}
	if !strings.Contains(tk.Error, "累计回队熔断") {
		t.Fatalf("挂起文案应含累计回队标记, got %q", tk.Error)
	}
	if got := store.RequeueTripReasonOf(tk.Error); got != "total_requeue" {
		t.Fatalf("挂起成因应判读为 total_requeue, 实际 %s（文案=%q）", got, tk.Error)
	}
	if got := runTaskCalls(t, logPath); got != store.TotalRequeueLimit {
		t.Fatalf("子进程应恰好执行 %d 次（达上限即停）, 实际 %d", store.TotalRequeueLimit, got)
	}
	waitFor(t, 15*time.Second, func() bool { return alerts.count() >= 1 }, "累计回队熔断告警应送达")
	if got := alerts.count(); got != 1 {
		t.Fatalf("熔断告警应恰好 1 次, 实际 %d", got)
	}
	// 告警正文必须带上真实成因（运维只读这一段话，写「同因连败」会让人去找一个不存在的原因）。
	alerts.mu.Lock()
	body := strings.Join(alerts.calls, "\n")
	alerts.mu.Unlock()
	if !strings.Contains(body, "累计回队") {
		t.Fatalf("告警正文应含「累计回队」成因口径, got %q", body)
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
	if got := runTaskCalls(t, logPath); got != store.TotalRequeueLimit {
		t.Fatalf("挂起后不得再拉起子进程, 实际执行 %d 次", got)
	}
	waitIdleAndSettle(t, s)
}

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
