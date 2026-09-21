// §M15（2026-09-22）夜链半截自愈守护测试（反例锁）：
// 夜间链入队中途某环入库失败时——旧实现立即 return（缺额永不补投、Day 也不推进，
// 次轮 ChainHasTasks 判真整链短路，当晚链「半截且不再补」）。
// 新语义：失败环显式记录后继续投其余环；state 记 chain_issued/chain_total 并照常推进 Day；
// 缺额序位由后续 tick 自动补投且只补缺口（稳态幂等，不覆盖 Done）。
package scheduler

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// TestNightlyChainHalfEnqueueBackfill mock 第 2 序位入队失败：
// 其余环照常入库、半截留痕、Day 照常推进；修复后下一轮只补缺额且不重复投已投环。
func TestNightlyChainHalfEnqueueBackfill(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "trading.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cfg := cfgSamples("unused-fake-bin", dbPath)
	cfg.Nightly.Steps = []string{"dataload", "sector_rebuild", "list"}
	cfg.Nightly.BacktestEnabled = false
	cfg.OptimizeEnabled = false

	statePath := filepath.Join(tmp, "research_state.json")
	s := New(tmp, filepath.Join(tmp, "config.json"), statePath)
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, loc) // 周六盘后
	s.setNow(func() time.Time { return now })

	// —— 第 1 轮：sector_rebuild（序位 1）注入入库失败 ——
	s.mu.Lock()
	s.nightlyEnqueueOverride = func(t *store.ResearchTask) (int64, error) {
		if t.ChainSeq == 1 {
			return 0, errors.New("mock: 队列库瞬时写入失败")
		}
		return db.EnqueueResearchTask(t)
	}
	s.mu.Unlock()
	s.ensureNightlyEnqueue(db, cfg, now)

	tasks, _ := db.ListResearchTasks()
	bySeq := map[int]store.ResearchTask{}
	for _, tk := range tasks {
		bySeq[tk.ChainSeq] = tk
	}
	if len(tasks) != 2 {
		t.Fatalf("第 1 轮应投 2/3（失败环不阻断其余环）, 实际 %d: %+v", len(tasks), tasks)
	}
	if bySeq[0].Type != store.TaskDataload || bySeq[2].Type != store.TaskList {
		t.Fatalf("已成功环不得重复入队/错位: seq0=%s seq2=%s", bySeq[0].Type, bySeq[2].Type)
	}
	if _, ok := bySeq[1]; ok {
		t.Fatal("失败环不应留下占位任务")
	}
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	// §M15 核心反例锁：半截也照常推进 Day（旧版 return 在 Day 赋值之前）+ 缺额留痕
	if st.Day != "20260822" || st.Done {
		t.Fatalf("半截链应照常推进 Day 且 Done=false, got day=%s done=%v", st.Day, st.Done)
	}
	if st.ChainIssued != 2 || st.ChainTotal != 3 {
		t.Fatalf("state 应留痕 chain_issued=2/chain_total=3, got %d/%d", st.ChainIssued, st.ChainTotal)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("状态文件应已落盘: %v", err)
	}
	if !strings.Contains(string(raw), `"chain_issued": 2`) || !strings.Contains(string(raw), `"chain_total": 3`) {
		t.Fatalf("状态文件缺 chain_issued/chain_total 留痕: %s", raw)
	}

	// —— 第 2 轮（模拟后续 30s tick，故障恢复）：只补缺额序位 ——
	s.mu.Lock()
	s.nightlyEnqueueOverride = nil
	s.mu.Unlock()
	s.ensureNightlyEnqueue(db, cfg, now)
	tasks, _ = db.ListResearchTasks()
	if len(tasks) != 3 {
		t.Fatalf("缺额应被自动补投（半截不永久）, 实际 %d: %+v", len(tasks), tasks)
	}
	bySeq = map[int]store.ResearchTask{}
	for _, tk := range tasks {
		bySeq[tk.ChainSeq] = tk
	}
	if bySeq[1].Type != store.TaskSectorRebuild {
		t.Fatalf("补投应落在缺口序位 1(sector_rebuild), 实际 seq1=%s", bySeq[1].Type)
	}
	for seq, want := range map[int]string{
		0: store.TaskDataload, 1: store.TaskSectorRebuild, 2: store.TaskList,
	} {
		if bySeq[seq].Type != want {
			t.Fatalf("序位 %d 类型应为 %s, 实际 %+v", seq, want, bySeq[seq])
		}
	}
	s.mu.Lock()
	st = s.state
	s.mu.Unlock()
	if st.ChainIssued != 3 || st.ChainTotal != 3 {
		t.Fatalf("补齐后应留痕 3/3, got %d/%d", st.ChainIssued, st.ChainTotal)
	}

	// —— 第 3 轮：稳态幂等——全序位就位后不再入队，也不得覆盖已完成标记 Done ——
	s.mu.Lock()
	s.state.Done = true
	s.mu.Unlock()
	s.ensureNightlyEnqueue(db, cfg, now)
	tasks, _ = db.ListResearchTasks()
	if len(tasks) != 3 {
		t.Fatalf("稳态 tick 不应重复入队, 实际 %d", len(tasks))
	}
	s.mu.Lock()
	done := s.state.Done
	s.mu.Unlock()
	if !done {
		t.Fatal("稳态 tick 不得清掉链完成标记 Done")
	}
}

// TestNightlyChainMissingSeqBackfilledAcrossRestart 模拟「半截当晚重启」：
// 新实例（state 从盘恢复）对既有链不做任何破坏，仅对缺口序位补投——
// 序位集合以队列表为准（ChainTaskSeqs），不依赖内存态。
func TestNightlyChainMissingSeqBackfilledAcrossRestart(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "trading.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	cfg := cfgSamples("unused-fake-bin", dbPath)
	cfg.Nightly.Steps = []string{"dataload", "sector_rebuild", "list"}
	cfg.Nightly.BacktestEnabled = false
	cfg.OptimizeEnabled = false
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 8, 22, 16, 0, 0, 0, loc)

	// 手工构造旧版缺陷落下的半截链：只有序位 0、1（序位 2 从未入库）。
	for seq, typ := range map[int]string{0: store.TaskDataload, 1: store.TaskSectorRebuild} {
		if _, err := db.EnqueueResearchTask(&store.ResearchTask{
			Type: typ, Priority: "low", Status: store.TaskQueued, Payload: "{}",
			ChainDay: "20260822", ChainSeq: seq,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 全新实例（相当于 researchd 重启，无任何内存态）：当轮即补投缺口，而非 ChainHasTasks 短路
	s := &Scheduler{statePath: filepath.Join(tmp, "research_state.json")}
	s.setNow(func() time.Time { return now })
	s.ensureNightlyEnqueue(db, cfg, now)
	tasks, _ := db.ListResearchTasks()
	if len(tasks) != 3 {
		t.Fatalf("重启后半截链应被补投缺口, 实际 %d: %+v", len(tasks), tasks)
	}
	var tail store.ResearchTask
	for _, tk := range tasks {
		if tk.ChainSeq == 2 {
			tail = tk
		}
	}
	if tail.Type != store.TaskList {
		t.Fatalf("缺口应补投 list(seq=2), 实际 %+v", tail)
	}
	// 状态文件字段经 json 反解校验（chain_issued/chain_total 展示兼容字段）
	s.saveState()
	raw, _ := os.ReadFile(s.statePath)
	var sf map[string]any
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatalf("state json: %v", err)
	}
	if sf["chain_issued"] != float64(3) || sf["chain_total"] != float64(3) {
		t.Fatalf("state 应含 chain_issued/total=3/3, got %s", raw)
	}
}
