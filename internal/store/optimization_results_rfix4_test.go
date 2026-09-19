// §RFIX-4 回归测试：寻优 pending 过期状态机（expired 终态）与 backtest_jobs
// running 僵尸行的启动恢复（MarkRunningInterrupted 复活）。
package store

import (
	"path/filepath"
	"testing"
)

// TestExpireStalePendingOptimizations 过期边界：29 天 pending 保留、31 天转 expired；
// approved/rejected/expired 行不受影响（幂等重跑安全）。
func TestExpireStalePendingOptimizations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	results := func(ranks ...int) []map[string]any {
		out := make([]map[string]any, 0, len(ranks))
		for _, r := range ranks {
			out = append(out, map[string]any{
				"rank": float64(r), "strategy": "N形", "params": map[string]any{"hold_days": r},
				"win_rate": 50.0, "trigger_count": float64(r),
			})
		}
		return out
	}
	if err := db.SaveOptimizationResults(11, "winrate", results(1, 2)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveOptimizationResults(12, "winrate", results(1)); err != nil {
		t.Fatal(err)
	}
	// 手工回拨 created_at：task 11 → 29 天前（保留），task 12 → 31 天前（过期）
	if _, err := db.db.Exec(`UPDATE optimization_results SET created_at = datetime('now','localtime','-29 days') WHERE task_id = 11`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE optimization_results SET created_at = datetime('now','localtime','-31 days') WHERE task_id = 12`); err != nil {
		t.Fatal(err)
	}
	// 已审批行即使超期也不动（approved 非 pending）
	rows, err := db.OptimizationResultsByTask(11)
	if err != nil || len(rows) == 0 {
		t.Fatal("预置行缺失")
	}
	if err := db.UpdateOptimizationStatus(rows[0].ID, "approved"); err != nil {
		t.Fatal(err)
	}

	n, err := db.ExpireStalePendingOptimizations(30)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应过期 1 条（31天 pending），实际 %d", n)
	}
	// 幂等重跑：不再有新过期行
	if n2, _ := db.ExpireStalePendingOptimizations(30); n2 != 0 {
		t.Fatalf("重跑应 0 条，实际 %d", n2)
	}
	if rows, _ := db.OptimizationResultsByTask(11); rows[0].Status != "approved" {
		t.Fatalf("approved 行不应被过期: %+v", rows[0])
	}
	if rows, _ := db.OptimizationResultsByTask(12); rows[0].Status != "expired" {
		t.Fatalf("31 天 pending 应转 expired: %+v", rows[0])
	}
	// 29 天内的 pending 保留（task 11 第 2 行仍是 pending 且未到期）
	if rows, _ := db.OptimizationResultsByTask(11); rows[1].Status != "pending" {
		t.Fatalf("29 天 pending 应保留: %+v", rows[1])
	}
}

// TestMarkRunningInterruptedRevived 启动恢复：残留 running 回放作业（含生产 id=3 同型
// nightly 僵尸行）被标 interrupted 并补齐 finished_at；done 行不动。
func TestMarkRunningInterruptedRevived(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertBacktestJob(&BacktestJob{Kind: "nightly", CandidateID: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertBacktestJob(&BacktestJob{Kind: "library", CandidateID: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertBacktestJob(&BacktestJob{Kind: "candidate", CandidateID: 5, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	n, err := db.MarkRunningInterrupted()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应恢复 2 个 running 行，实际 %d", n)
	}
	j, err := db.GetBacktestJob("nightly", 0)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	if j.Status != "interrupted" || j.FinishedAt == "" {
		t.Fatalf("nightly 僵尸行应变 interrupted 并补 finished_at: %+v", j)
	}
	if c, _ := db.GetBacktestJob("candidate", 5); c.Status != "done" {
		t.Fatalf("done 行不得被改动: %+v", c)
	}
	// 幂等：二次调用无行可改
	if n2, _ := db.MarkRunningInterrupted(); n2 != 0 {
		t.Fatalf("二次恢复应 0，实际 %d", n2)
	}
}
