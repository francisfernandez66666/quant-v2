// §NO-TIME-LIMIT-BACKTEST（2026-11-28 owner 裁决）worker 侧行为锁：
//   - TestNoTimeLimitBacktestScope：非交易时段只放开**回测类**任务；发现类不放开；
//     交易时段一律不放开（避免研究任务抢盘中 CPU）。
//   - TestNoTimeLimitBacktestSkipsNightcap：回测类任务本夜预算已超上限时，队首预算闸
//     不得把它顺延到次日（原 §NIGHTCAP 行为），且子进程真被拉起并跑完；同一条件下
//     发现类任务仍按原规则顺延——两向锁证明豁免不是把整条预算闸拆掉。
//
// English: behavior legs for the standing rule that non-trading-window backtests have
// no execution deadline / nightly budget cap, while discovery and trading-window tasks
// keep the original guards.
package scheduler

import (
	"path/filepath"
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
