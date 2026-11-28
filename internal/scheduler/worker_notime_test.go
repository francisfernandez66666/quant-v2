// §NO-TIME-LIMIT-BACKTEST（2026-11-28 owner 裁决）worker 侧行为锁：
//   - TestNoTimeLimitBacktestScope：非交易时段只放开**回测类**任务；发现类不放开；
//     交易时段一律不放开（避免研究任务抢盘中 CPU）。
//   - TestNoTimeLimitBacktestSkipsNightcap：回测类任务本夜预算已超上限时，队首预算闸
//     不得把它顺延到次日（原 §NIGHTCAP 行为），且子进程真被拉起并跑完；同一条件下
//     发现类任务仍按原规则顺延——两向锁证明豁免不是把整条预算闸拆掉。
//   - TestNoTimeLimitBacktestYieldsAtTradingWindow：周末启动的无时限回测跨到交易日
//     09:15 时必须被既有「交易时段开始」抢占链中止并回队，收盘后从断点续跑——
//     「不设时间限制」不等于「闯进盘中还和 quant 抢资源」。
//
// English: behavior legs for the standing rule that non-trading-window backtests have
// no execution deadline / nightly budget cap, while discovery and trading-window tasks
// keep the original guards; a run crossing into the trading session still yields.
package scheduler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

func TestNoTimeLimitBacktestScope(t *testing.T) {
	cfg := config.SchedulerConfig{}
	loc := time.FixedZone("CST", 8*3600)
	weekend := time.Date(2026, 11, 28, 16, 0, 0, 0, loc) // 周六盘后
	trading := time.Date(2026, 11, 30, 10, 0, 0, 0, loc) // 周一盘中

	for _, kind := range []string{
		store.TaskBacktestCandidate, store.TaskBacktestNightly, store.TaskBacktestStrategy,
	} {
		if !noTimeLimitBacktest(kind, weekend, cfg) {
			t.Errorf("%s 非交易时段应无时间上限", kind)
		}
		if noTimeLimitBacktest(kind, trading, cfg) {
			t.Errorf("%s 交易时段必须保留时间上限（不能抢盘中 CPU）", kind)
		}
	}
	if noTimeLimitBacktest(store.TaskDiscoverFactors, weekend, cfg) {
		t.Error("发现类任务不在豁免清单里（长任务会独占唯一消费槽）")
	}
}

func TestNoTimeLimitBacktestSkipsNightcap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	fake := fakeScript(t, logPath)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath)
	cfg.PerTaskNightMaxMin = store.NightCapDefaultMin
	cfg.StepTimeoutMin = 90
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))

	// 真实今日 16:00：任何一天都落在盘后窗口，日期维度现场派生（同 §NIGHTCAP 测试纪律）。
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
	btID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 301, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	disID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskDiscoverFactors, RefID: 302, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 两条都把本夜预算记到「超过上限」：回测应被豁免直接跑，发现仍应顺延。
	over := store.NightCapDefaultMin + 60
	if err := qdb.AddTaskNightUsed(btID, today, over); err != nil {
		t.Fatal(err)
	}
	if err := qdb.AddTaskNightUsed(disID, today, over); err != nil {
		t.Fatal(err)
	}

	s.tick()
	waitFor(t, 10*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(btID)
		return tk != nil && tk.Status == store.TaskDone
	}, "超预算回测任务应照常拉起并跑完（不受 nightcap 顺延）")
	bt, _ := qdb.GetResearchTask(btID)
	if bt.DeferDay != "" {
		t.Fatalf("回测任务不得被写 defer_day, got %q", bt.DeferDay)
	}
	if nightcapCallsFor(t, logPath, btID) == 0 {
		t.Fatal("回测任务必须真实拉起子进程（不是被别的任务顶替落 done）")
	}

	s.tick() // 队首换人：发现类没有豁免，应落原让位路径
	waitFor(t, 10*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(disID)
		return tk != nil && tk.DeferDay == tomorrow && tk.Status == store.TaskQueued
	}, "发现类任务仍应按 §NIGHTCAP 顺延到次日")
	if nightcapCallsFor(t, logPath, disID) != 0 {
		t.Fatal("被顺延的发现任务本夜不得拉起子进程")
	}
	waitIdleAndSettle(t, s)
}

// TestNoTimeLimitBacktestYieldsAtTradingWindow 跨交易时段让位腿（2026-11-28 owner 追加）：
// 无时限回测在周末启动 → 注入时钟拨到交易日 10:00 → tickInner 的「交易时段开始」抢占
// 必须中止子进程、任务回 queued（requeue_total+1、error 写抢占原因）→ 收盘后再 tick
// 从断点续跑并跑完。锁的是「时段切换仍优先于免时限豁免」，不是新增一套看门狗。
func TestNoTimeLimitBacktestYieldsAtTradingWindow(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "30") // 子进程长睡，确保盘中抢占时还在跑
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	fake := fakeScript(t, logPath)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath)
	cfg.PerTaskNightMaxMin = store.NightCapDefaultMin
	cfg.StepTimeoutMin = 720
	gateOff := false // 测试夹具不跑真实资金流采集，关掉 §MONEYFLOW-GATE 的等标记闸
	cfg.MoneyflowGateEnabled = &gateOff
	cfgPath := mustConfig(t, cfg)
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)

	weekend := time.Date(2026, 11, 28, 16, 0, 0, 0, loc) // 周六盘后
	trading := time.Date(2026, 11, 30, 10, 0, 0, 0, loc) // 周一 10:00 交易段
	afterClose := time.Date(2026, 11, 30, 15, 30, 0, 0, loc)
	s.setNow(func() time.Time { return weekend })

	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列: %v", err)
	}
	defer qdb.Close()
	id, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 401, Priority: "low", Payload: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	s.tick() // 周末启动，无时限回测进入运行态
	waitFor(t, 10*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(id)
		return tk != nil && tk.Status == store.TaskRunning && nightcapCallsFor(t, logPath, id) > 0
	}, "无时限回测应在周末正常拉起")

	s.setNow(func() time.Time { return trading })
	s.tick() // tickInner 见交易窗口 → preemptCurrent("交易时段开始")
	waitFor(t, 10*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(id)
		return tk != nil && tk.Status == store.TaskQueued
	}, "跨入交易时段的回测应被抢占并回队（不是继续无时限跑）")
	tk, _ := qdb.GetResearchTask(id)
	if !strings.Contains(tk.Error, "被抢占") {
		t.Fatalf("抢占原因应写进 error 列, got %q", tk.Error)
	}
	if tk.RequeueTotal < 1 {
		t.Fatalf("抢占续跑应计入 requeue_total, got %d", tk.RequeueTotal)
	}

	// 收盘后从断点续跑：撤掉长睡让第二次执行立刻结束。
	t.Setenv("FAKE_SLEEP", "")
	s.setNow(func() time.Time { return afterClose })
	s.tick()
	waitFor(t, 10*time.Second, func() bool {
		cur, _ := qdb.GetResearchTask(id)
		return cur != nil && cur.Status == store.TaskDone
	}, "下一个可用时段应把被抢占的回测续跑完")
	if nightcapCallsFor(t, logPath, id) < 2 {
		t.Fatal("被抢占任务必须真实拉起过两次子进程（初跑 + 续跑）")
	}
	waitIdleAndSettle(t, s)
}
