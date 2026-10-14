// §NIGHTCAP（2026-10-14）顺延闸与单夜占用台账的 store 侧守护测试：
//   - defer_day 晚于今日的行整条不进出队候选（到点那夜自动恢复）——这是让出队首的那把闸；
//   - 单夜占用台账按北京日翻页累计（跨日归零），NightBudgetLeftMin 纯函数口径与出队侧一致；
//   - MarkTaskUnfinished：连续没跑完达 DeferralStreakLimit 次自动顺延，挂起/终态行跳过不误报；
//   - 成功/取消清顺延账、人工复活清全部账。
//
// 日期维度一律按 cntime 的今日派生（今日/明日/昨日），绝不写死日历日期——
// 否则夹具隔日必红（本仓锤过的夹具写死日期反例）。
// English: store-side guard tests for the yield gate and per-night ledger; every date in the
// fixtures is derived from today, never hardcoded.
package store

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// TestDeferDayBlocksDequeueUntilDue 顺延闸行为腿：
// defer_day=明日 ⇒ 出队取不到（本夜让位）；defer_day=今日/昨日 ⇒ 恢复出队；
// defer_day 只挡低优先级排队行，不挡 high（high 排在查询第一序位，条件只过滤 defer 未到点）。
func TestDeferDayBlocksDequeueUntilDue(t *testing.T) {
	db := testDB(t)
	today := cntime.DayCompactOf(cntime.Now())
	tomorrow := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, 1))
	yesterday := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, -1))

	deferred, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`, ChainDay: "20261014", ChainSeq: 1})
	if err != nil {
		t.Fatalf("enqueue deferred: %v", err)
	}
	if err := db.DeferQueuedTaskToNight(deferred, tomorrow, "单夜占用已达上限，顺延"); err != nil {
		t.Fatalf("让位落库: %v", err)
	}
	// 让位后不出队：全库唯一一行被 defer_day 挡住，查询应返回空
	if next, _ := db.DequeueHighestTask(); next != nil {
		t.Fatalf("顺延到明日的行不应出队, 取到 id=%d defer_day=%s", next.ID, next.DeferDay)
	}
	// 行本身仍是 queued（让位不是失败也不是挂起），progress/error 各归各位
	tk, _ := db.GetResearchTask(deferred)
	if tk == nil || tk.Status != TaskQueued || tk.DeferDay != tomorrow {
		t.Fatalf("让位行应保持 queued 且 defer_day=%s, got %+v", tomorrow, tk)
	}
	// 顺延期到（把 defer_day 拨回昨日）即恢复出队——字符串比较语义自证（昨日<=今日）
	if _, err := db.db.Exec(`UPDATE research_tasks SET defer_day=? WHERE id=?`, yesterday, deferred); err != nil {
		t.Fatalf("拨快顺延日: %v", err)
	}
	next, _ := db.DequeueHighestTask()
	if next == nil || next.ID != deferred {
		t.Fatalf("顺延日已到的行应恢复出队, got %+v", next)
	}
	// 空串 defer_day 恒可出队（绝大多数行的常态）
	if _, err := db.db.Exec(`UPDATE research_tasks SET defer_day='' WHERE id=?`, deferred); err != nil {
		t.Fatalf("清空顺延日: %v", err)
	}
	if next, _ = db.DequeueHighestTask(); next == nil || next.ID != deferred {
		t.Fatalf("空 defer_day 应恒可出队, got %+v", next)
	}
	_ = today
}

// TestDeferQueuedTaskToNightZeroRowIsLoud 让位落库的现形腿：running 行调用队首让位必须报错，
// 静默 0 行意味着顺延根本没落上、队首照旧独占（本仓锤过的静默失败族）。
func TestDeferQueuedTaskToNightZeroRowIsLoud(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.UpdateTaskRunState(id, TaskRunning, "1%", 0, "", ""); err != nil {
		t.Fatalf("running: %v", err)
	}
	tomorrow := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, 1))
	if err := db.DeferQueuedTaskToNight(id, tomorrow, "x"); err == nil {
		t.Fatal("running 行的队首让位必须现形（0 行不是成功）")
	} else if want := "队首让位未生效"; !contains(err.Error(), want) {
		t.Fatalf("报错文案要指名是哪条腿没落上, got %v", err)
	}
}

// TestYieldTaskToNightRunningOnly YieldTaskToNight 只认 running 行（0 行现形），
// 让位保留 progress、写 error 原因、status 回 queued、defer_day 落明日。
func TestYieldTaskToNightRunningOnly(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDiscoverFactors, Payload: `{}`, ChainDay: "20261014", ChainSeq: 1})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.UpdateTaskRunState(id, TaskRunning, "62%", 0, "", ""); err != nil {
		t.Fatalf("running: %v", err)
	}
	tomorrow := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, 1))
	if err := db.YieldTaskToNight(id, tomorrow, "单夜占用达上限(4h0m0s)，让位至下一夜续跑（进度/断点缓存保留）"); err != nil {
		t.Fatalf("让位: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk == nil || tk.Status != TaskQueued || tk.DeferDay != tomorrow {
		t.Fatalf("让位后应 queued+defer_day=%s, got %+v", tomorrow, tk)
	}
	if tk.Progress != "62%" {
		t.Fatalf("让位必须保留 progress（与失败回队清 progress 刻意不同）, got %q", tk.Progress)
	}
	if !contains(tk.Error, "让位") {
		t.Fatalf("error 列应写让位原因供读数分成因, got %q", tk.Error)
	}
	// queued 行再让位必须报错（0 行现形）
	if err := db.YieldTaskToNight(id, tomorrow, "x"); err == nil {
		t.Fatal("非 running 行的让位必须现形")
	}
}

// TestNightBudgetLedgerPageFlip 单夜台账：同日累加、跨日翻页归零、纯函数口径与出队侧一致。
func TestNightBudgetLedgerPageFlip(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	today := cntime.DayCompactOf(cntime.Now())
	yesterday := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, -1))
	// 昨夜记账 200 分钟 → 今日首跑（不同键）翻页，今日记 30 → 累计 30
	if err := db.AddTaskNightUsed(id, yesterday, 200); err != nil {
		t.Fatalf("昨夜入账: %v", err)
	}
	if err := db.AddTaskNightUsed(id, today, 30); err != nil {
		t.Fatalf("今日入账: %v", err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.NightDay != today || tk.NightUsedMin != 30 {
		t.Fatalf("跨日应翻页(used=30), got day=%s used=%d", tk.NightDay, tk.NightUsedMin)
	}
	if err := db.AddTaskNightUsed(id, today, 12); err != nil {
		t.Fatalf("今日二笔: %v", err)
	}
	tk, _ = db.GetResearchTask(id)
	if tk.NightUsedMin != 42 {
		t.Fatalf("同日应累加(42), got %d", tk.NightUsedMin)
	}
	// 预算纯函数：昨日账不算今日；今日账参与扣减；负上限=不限(-1)
	if got := NightBudgetLeftMin(42, today, today, 240); got != 198 {
		t.Fatalf("今日预算应为 198, got %d", got)
	}
	if got := NightBudgetLeftMin(200, yesterday, today, 240); got != 240 {
		t.Fatalf("昨夜账不应占用今日预算, got %d", got)
	}
	if got := NightBudgetLeftMin(999, today, today, 240); got != 0 {
		t.Fatalf("超支应夹到 0, got %d", got)
	}
	if got := NightBudgetLeftMin(42, today, today, -1); got != -1 {
		t.Fatalf("负上限=不限(-1), got %d", got)
	}
	if got := NightBudgetLeftMin(0, "", today, 240); got != 240 {
		t.Fatalf("无账=全额, got %d", got)
	}
}

// TestMarkTaskUnfinishedDefersAtStreakLimit 连续没跑完记账：前 DeferralStreakLimit-1 次只累计，
// 第 DeferralStreakLimit 次自动顺延（defer_day=下一夜）；挂起/终态行跳过不误报；
// 成功清零顺延账。
func TestMarkTaskUnfinishedDefersAtStreakLimit(t *testing.T) {
	db := testDB(t)
	id, err := db.EnqueueResearchTask(&ResearchTask{Type: TaskDataload, Payload: `{}`})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	tomorrow := cntime.DayCompactOf(cntime.Now().AddDate(0, 0, 1))
	// 第一次没跑完：只累计，不顺延
	streak, deferred, err := db.MarkTaskUnfinished(id, tomorrow)
	if err != nil || streak != 1 || deferred {
		t.Fatalf("首次应 (1,false,nil), got (%d,%v,%v)", streak, deferred, err)
	}
	// 第二次：达到 DeferralStreakLimit，顺延到下一夜
	streak, deferred, err = db.MarkTaskUnfinished(id, tomorrow)
	if err != nil || streak != DeferralStreakLimit || !deferred {
		t.Fatalf("第二次应顺延 (%d,true,nil), got (%d,%v,%v)", DeferralStreakLimit, streak, deferred, err)
	}
	tk, _ := db.GetResearchTask(id)
	if tk.DeferDay != tomorrow || tk.UnfinishedStreak != DeferralStreakLimit {
		t.Fatalf("顺延应落 defer_day=%s streak=%d, got %+v", tomorrow, DeferralStreakLimit, tk)
	}
	// 空下一夜键：不写 defer_day，只累计（防御腿）
	if _, err := db.db.Exec(`UPDATE research_tasks SET defer_day='' WHERE id=?`, id); err != nil {
		t.Fatalf("清 defer_day: %v", err)
	}
	if _, deferred, err = db.MarkTaskUnfinished(id, ""); err != nil || deferred {
		t.Fatalf("空键不应顺延, got deferred=%v err=%v", deferred, err)
	}
	// 成功清零顺延账
	if err := db.UpdateTaskRunState(id, TaskDone, "100%", 0, "", ""); err != nil {
		t.Fatalf("done: %v", err)
	}
	tk, _ = db.GetResearchTask(id)
	if tk.UnfinishedStreak != 0 || tk.DeferDay != "" {
		t.Fatalf("成功应清顺延账, got %+v", tk)
	}
	// 挂起行跳过：不累计、不报错（避免把"已挂起"再误报成"又没跑完"）
	if err := db.UpdateTaskRunState(id, TaskNeedsAttention, "", 0, "", "连败熔断"); err != nil {
		t.Fatalf("park: %v", err)
	}
	if streak, deferred, err := db.MarkTaskUnfinished(id, tomorrow); err != nil || streak != 0 || deferred {
		t.Fatalf("挂起行应跳过 (0,false,nil), got (%d,%v,%v)", streak, deferred, err)
	}
	_ = time.Now
}

// contains 小工具（测试文件内避免为单处断言引入 strings）。
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
