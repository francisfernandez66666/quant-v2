// researchd_main_smoke_test.go — §SMOKE（2026-09-22 LOW 族）researchd 主链路冒烟测试。
//
// 目标：以最小闭环锁住 researchd（cmd/researchd → scheduler.Scheduler）主链路四段——
//  1. 入队：外部（quant API/手动 high）直接 EnqueueResearchTask + 调度器夜间链自动入队；
//  2. runner 执行：tryStartNext 出队 → 认领 → spawn run-task/dataload 子进程（假二进制）；
//  3. 状态落库：research_tasks 行推进到 done（error 空、result 可读写回）；
//  4. 状态落文件：research_state.json（last_step/last_status/Done）与 scheduler_status.json、
//     task_logs/task_<id>.log 任务日志文件均真实落盘。
//
// 口径说明（勿与生产装配混淆）：researchd main.go 只做 New + SetAlertFunc + Run(ctx) 薄装配，
// 主逻辑全在 scheduler 的 tick/worker 链路，故冒烟落在本包内直接驱动 s.tick()
// （与既有 worker 测试同法，注入盘后时钟免 30s 等待）；不改任何既有源文件与既有测试。
// English: end-to-end smoke for the researchd consumption loop — enqueue -> runner exec ->
// DB terminal state -> state/status/log files on disk — using the established fake-binary pattern.
package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// TestSmokeResearchdMainChain §SMOKE 主链路冒烟：入队→runner 执行→状态落库/落文件 最小闭环。
// 反例含义：主链路任一环断裂（任务不出队、子进程不启动、终态不落库、状态文件不落盘）
// 都会使本测试失败——此前该链路只有分场景单测，无一条贯穿式冒烟（LOW 族缺口）。
func TestSmokeResearchdMainChain(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "0") // 假二进制秒完（sleep 0），冒烟只验通断不验时长
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fake.log")
	fake := fakeScript(t, logPath)
	dbPath := filepath.Join(dir, "trading.db")
	cfg := cfgSamples(fake, dbPath) // Nightly.Steps=["dataload"]：链任务 1 环
	cfgPath := mustConfig(t, cfg)
	statePath := filepath.Join(dir, "research_state.json")

	// —— 与 cmd/researchd main.go 同构的装配（New + 盘后时钟驱动）——
	s := New(dir, cfgPath, statePath)
	loc := time.FixedZone("CST", 8*3600)
	s.setNow(func() time.Time { return time.Date(2026, 8, 22, 16, 0, 0, 0, loc) }) // 周六 16:00 盘后

	// [入队-手动] 模拟 quant API 侧直接入队的高优任务（researchd 之外唯一的生产入队形态）
	qdb, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开队列库: %v", err)
	}
	defer qdb.Close()
	manualID, err := qdb.EnqueueResearchTask(&store.ResearchTask{
		Type: store.TaskBacktestCandidate, RefID: 900, Priority: "high", Payload: `{"h":5}`,
	})
	if err != nil {
		t.Fatalf("入队手动 high: %v", err)
	}

	// [runner] 单次 tick：workerTick 自动入队当日夜链（dataload low）并出队执行；
	// 任务终态后经 tryStartNext 自驱排水，不依赖 30s tick。
	s.tick()

	// [落库-手动任务] 手动 high 应推进到 done 终态
	waitFor(t, 15*time.Second, func() bool {
		tk, _ := qdb.GetResearchTask(manualID)
		return tk != nil && tk.Status == store.TaskDone
	}, "§SMOKE 手动 high 任务应经 runner 执行并落 done 终态（落库）")
	tk, _ := qdb.GetResearchTask(manualID)
	if tk.Error != "" {
		t.Fatalf("§SMOKE done 终态不应残留 error, got %q", tk.Error)
	}

	// [落库-夜链] 当日链任务由调度器入队（入队段验证）并同样落 done
	var chainID int64
	waitFor(t, 15*time.Second, func() bool {
		chain, err := qdb.LatestTaskByRef(store.TaskDataload, 0)
		if err != nil || chain == nil || chain.ChainDay != "20260822" {
			return false
		}
		chainID = chain.ID
		return chain.Status == store.TaskDone
	}, "§SMOKE 夜链 dataload 应被自动入队并执行完成（落库）")

	// [执行留痕] runner 确实 spawn 过子进程：假二进制日志两行（run-task + dataload daily）
	waitFor(t, 5*time.Second, func() bool { return callCount(t, logPath) == 2 }, "§SMOKE 子进程应各执行一次")
	bl, _ := os.ReadFile(logPath)
	if !strings.Contains(string(bl), "run-task --task-id "+itoa(manualID)) {
		t.Fatalf("§SMOKE 应记录手动任务的 run-task 调用, log=%s", bl)
	}

	// [落文件-状态] research_state.json：链排空 → Done=true + last_step/last_status 留痕
	waitFor(t, 10*time.Second, func() bool {
		raw, err := os.ReadFile(statePath)
		if err != nil {
			return false
		}
		var st struct {
			Done       bool   `json:"done"`
			Day        string `json:"day"`
			LastStep   string `json:"last_step"`
			LastStatus string `json:"last_status"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return false
		}
		return st.Done && st.Day == "20260822" && st.LastStatus == "done" && st.LastStep != ""
	}, "§SMOKE research_state.json 应落 done 终态留痕（落文件）")

	// [落文件-可见性] tick 末尾的 scheduler_status.json 快照存在且含队列可解释原因
	statusRaw, err := os.ReadFile(filepath.Join(dir, "scheduler_status.json"))
	if err != nil {
		t.Fatalf("§SMOKE scheduler_status.json 应已落盘: %v", err)
	}
	if !strings.Contains(string(statusRaw), `"reason"`) {
		t.Fatalf("§SMOKE scheduler_status.json 缺 reason 字段: %s", statusRaw)
	}

	// [落文件-任务日志] task_logs/task_<id>.log 由 runTask 建出（排障链路的一部分）
	if _, err := os.Stat(filepath.Join(dir, "task_logs", "task_"+itoa(manualID)+".log")); err != nil {
		t.Fatalf("§SMOKE 手动任务日志文件应存在: %v", err)
	}
	if chainID > 0 {
		if _, err := os.Stat(filepath.Join(dir, "task_logs", "task_"+itoa(chainID)+".log")); err != nil {
			t.Fatalf("§SMOKE 链任务日志文件应存在: %v", err)
		}
	}

	waitIdleAndSettle(t, s)
}

// itoa 任务 ID → 十进制串（拼 run-task 参数与日志文件名断言用）。
func itoa(id int64) string { return strconv.FormatInt(id, 10) }
