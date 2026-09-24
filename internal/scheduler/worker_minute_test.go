// §MINUTE-K（2026-09-24）分钟 K 夜间日增环的调度侧守护测试：
//  1. 步骤映射：minute_sync → TaskMinuteSync，payload 带 scale/count/incremental（回放侧与装载侧
//     必须同一周期口径，所以 scale 是下发的、不是子命令猜的）；
//  2. 出厂步骤序：minute_sync 紧跟 dataload（先有日线再有分钟，回放读的是当天刷新的表）；
//  3. 命令组装：走 dataload 专用二进制、参数逐字对齐 cmd/dataload 的 minute-sync 子命令；
//  4. 空表门控：分钟表从未回填过时该环**不入队**（否则每晚必然 0 行判失败、淹没真告警），
//     回填过一次之后自动恢复入队——门控只看"有没有行"，不看任务类型。
//
// English: guards for the nightly minute-bar step — mapping, ordering, argv assembly, and the
// empty-table gate that keeps a never-backfilled minute table from failing every single night.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestMinuteSyncStepMapped 步骤映射与默认链序位。
func TestMinuteSyncStepMapped(t *testing.T) {
	typ, payload, ok := stepTask("minute_sync", config.DefaultSchedulerConfig(), "20260924")
	if !ok || typ != store.TaskMinuteSync {
		t.Fatalf("minute_sync 映射错误: ok=%v typ=%s", ok, typ)
	}
	for _, s := range []string{`"scale":5`, `"count":60`, `"incremental":true`} {
		if !strings.Contains(payload, s) {
			t.Fatalf("payload 应含 %s, 得 %s", s, payload)
		}
	}
	steps := config.DefaultSchedulerConfig().Nightly.Steps
	if len(steps) < 2 || steps[0] != "dataload" || steps[1] != "minute_sync" {
		t.Fatalf("默认夜间链应为 dataload→minute_sync 开头, 得 %v", steps)
	}
}

// TestMinuteSyncTaskCommand 命令组装：dataload 二进制 + minute-sync 子命令参数。
// payload 的 scale/count 必须透传（两侧口径不一致时动量判断会静默退回日线 MACD）。
func TestMinuteSyncTaskCommand(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "dataload")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写假二进制: %v", err)
	}
	cfg := cfgSamples(bin, filepath.Join(tmp, "trading.db"))
	s := New(tmp, filepath.Join(tmp, "config.json"), filepath.Join(tmp, "research_state.json"))

	binOut, args, err := s.taskCommand(cfg, &store.ResearchTask{Type: store.TaskMinuteSync, Payload: `{"scale":5,"count":60,"incremental":true}`})
	if err != nil {
		t.Fatalf("taskCommand: %v", err)
	}
	if binOut != bin {
		t.Fatalf("应使用 dataload 二进制, 得 %s", binOut)
	}
	want := "--db " + cfg.DB + " minute-sync --scale 5 --count 60 --incremental"
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("参数错位:\n got=%s\nwant=%s", got, want)
	}

	// payload 缺键/为 0 时回落夜间缺省口径（5 分钟 / 60 根），绝不把 0 传给子命令。
	_, args2, err := s.taskCommand(cfg, &store.ResearchTask{Type: store.TaskMinuteSync, Payload: `{}`})
	if err != nil {
		t.Fatalf("taskCommand(空 payload): %v", err)
	}
	got2 := strings.Join(args2, " ")
	if !strings.Contains(got2, "--scale 5 --count 60") {
		t.Fatalf("空 payload 应回落缺省口径, 得 %s", got2)
	}
	if strings.Contains(got2, "--scale 0") || strings.Contains(got2, "--count 0") {
		t.Fatalf("空 payload 应回落缺省口径, 得 %s", got2)
	}
	// 显式 0 也不能原样下发：0 周期/0 根数在子命令里都是"什么都别拉"，
	// 静默跑一轮 0 行比报错更糟（payload 来自旧版本或人工改写时最容易写成 0）。
	_, args0, _ := s.taskCommand(cfg, &store.ResearchTask{Type: store.TaskMinuteSync, Payload: `{"scale":0,"count":0}`})
	got0 := strings.Join(args0, " ")
	if !strings.Contains(got0, "--scale 5 --count 60") {
		t.Fatalf("0 值应回落缺省口径, 得 %s", got0)
	}

	// 回填型任务（显式 incremental=false）不能被打上 --incremental 标记——
	// 那会把它变成"只补库里已有票"，清单语义整个反过来。
	_, args3, _ := s.taskCommand(cfg, &store.ResearchTask{Type: store.TaskMinuteSync, Payload: `{"scale":5,"count":5025,"incremental":false,"max-fail-pct":10}`})
	got3 := strings.Join(args3, " ")
	if strings.Contains(got3, "--incremental") {
		t.Fatalf("incremental=false 不应带 --incremental, 得 %s", got3)
	}
	if !strings.Contains(got3, "--count 5025") || !strings.Contains(got3, "--max-fail-pct 10") {
		t.Fatalf("回填参数未透传, 得 %s", got3)
	}
}

// TestMinuteSyncGateOnEmptyTable 空表门控 + 回填后自动恢复。
func TestMinuteSyncGateOnEmptyTable(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "trading.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cfg := cfgSamples("unused-fake-bin", dbPath)
	cfg.Nightly.Steps = []string{"dataload", "minute_sync", "list"}
	cfg.Nightly.BacktestEnabled = false
	cfg.OptimizeEnabled = false
	s := New(tmp, filepath.Join(tmp, "config.json"), filepath.Join(tmp, "research_state.json"))
	loc := time.FixedZone("CST", 8*3600)

	// —— 第 1 夜：分钟表空（一次性回填从没跑过）→ 该环不入队 ——
	day1 := time.Date(2026, 9, 24, 16, 0, 0, 0, loc)
	s.setNow(func() time.Time { return day1 })
	s.ensureNightlyEnqueue(db, cfg, day1)
	if n := countTasksOfDay(t, db, "20260924"); n != 2 {
		t.Fatalf("分钟表为空时夜间链应只有 dataload+list 两环, 实际 %d", n)
	}
	if hasTaskType(t, db, "20260924", store.TaskMinuteSync) {
		t.Fatal("分钟表为空时不得入队 minute_sync（否则每晚 0 行判失败）")
	}

	// —— 人工回填跑过一次（表里有行）→ 次夜该环自动回到链里，序位按当夜计划重排 ——
	bars := make([]store.MinuteBar, 0, 48)
	for i := 0; i < 48; i++ {
		bars = append(bars, store.MinuteBar{
			TsCode: "600000.SH", Scale: 5,
			Ts: "2026-09-24 " + minuteClock(i), Open: 10, High: 10.1, Low: 9.9, Close: 10, Vol: 100, Amount: 1000,
		})
	}
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("落分钟库: %v", err)
	}
	day2 := day1.AddDate(0, 0, 1)
	s.setNow(func() time.Time { return day2 })
	s.ensureNightlyEnqueue(db, cfg, day2)
	tasks, err := db.ListResearchTasks()
	if err != nil {
		t.Fatalf("列任务: %v", err)
	}
	seq := map[string]int{}
	for _, tk := range tasks {
		if tk.ChainDay == "20260925" {
			seq[tk.Type] = tk.ChainSeq
		}
	}
	ms, ok := seq[store.TaskMinuteSync]
	if !ok {
		t.Fatalf("回填过一夜后 minute_sync 应入队, 实际任务 %v", seq)
	}
	if ms != 1 || seq[store.TaskDataload] != 0 {
		t.Fatalf("minute_sync 必须紧跟 dataload（dataload=0, minute_sync=1），得 dataload=%d minute_sync=%d", seq[store.TaskDataload], ms)
	}
	// payload 的 scale 必须与门控用的 scale 同源，否则「有 15 分钟数据却没有 5 分钟数据」时
	// 门控放行、装载却拉另一个周期。
	tk, err := db.LatestTaskByRef(store.TaskMinuteSync, 0)
	if err != nil || tk == nil {
		t.Fatalf("取 minute_sync 任务: %v", err)
	}
	if !strings.Contains(tk.Payload, `"scale":5`) {
		t.Fatalf("入队 payload scale 应为 5（与门控同口径）, 得 %s", tk.Payload)
	}
}

// minuteClock 第 i 根 5 分钟根的 HH:MM:SS（09:35 起，跨过 11:30~13:00 午休顺延，
// 保证 48 根都落在同一天且互不重复——门控只看行数，但 ts 撞了主键就少行）。
func minuteClock(i int) string {
	mins := 9*60 + 35 + i*5
	if mins >= 11*60+30 && mins < 13*60 {
		mins += 90
	}
	return fmt.Sprintf("%02d:%02d:00", mins/60, mins%60)
}

// countTasksOfDay 当日夜链任务数。
func countTasksOfDay(t *testing.T, db *store.DB, day string) int {
	t.Helper()
	tasks, err := db.ListResearchTasks()
	if err != nil {
		t.Fatalf("列任务: %v", err)
	}
	n := 0
	for _, tk := range tasks {
		if tk.ChainDay == day {
			n++
		}
	}
	return n
}

// hasTaskType 当日是否存在某类型任务。
func hasTaskType(t *testing.T, db *store.DB, day, typ string) bool {
	t.Helper()
	tasks, err := db.ListResearchTasks()
	if err != nil {
		t.Fatalf("列任务: %v", err)
	}
	for _, tk := range tasks {
		if tk.ChainDay == day && tk.Type == typ {
			return true
		}
	}
	return false
}
