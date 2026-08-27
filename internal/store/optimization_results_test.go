// Package store 数据持久化层：封装 SQLite 读写，提供策略候选、回测、持仓、订单、研究任务等表的 CRUD。
package store

import (
	"path/filepath"
	"testing"
)

func seedSweepResults() []map[string]any {
	return []map[string]any{
		{"rank": 1.0, "strategy": "双响炮", "strategy_kind": "",
			"params":   map[string]any{"take_profit_pct": 5.0, "stop_loss_pct": 8.0, "hold_days": 20.0, "min_score": 80.0},
			"win_rate": 39.5, "profit_factor": 1.16, "avg_hold_days": 10.0, "trigger_count": 1238.0},
		{"rank": 2.0, "strategy": "因子战法#1", "strategy_kind": "fac_1",
			"params":   map[string]any{"take_profit_pct": 15.0, "stop_loss_pct": 10.0, "hold_days": 20.0, "min_score": 70.0},
			"win_rate": 45.9, "profit_factor": 1.14, "avg_hold_days": 17.8, "trigger_count": 3765.0},
	}
}

// TestOptimizationResultsCRUD OptimizationResultsCRUD。
func TestOptimizationResultsCRUD(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.SaveOptimizationResults(27, "profitfactor", seedSweepResults()); err != nil {
		t.Fatal(err)
	}
	// 幂等：重跑不重复
	if err := db.SaveOptimizationResults(27, "profitfactor", seedSweepResults()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.OptimizationResultsByTask(27)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Rank != 1 || rows[0].Strategy != "双响炮" {
		t.Fatalf("rows=%+v", rows[0])
	}
	if rows[0].Params.TakeProfitPct != 5 || rows[0].Params.StopLossPct != 8 ||
		rows[0].Params.HoldDays != 20 || rows[0].Params.MinScore != 80 {
		t.Fatalf("params 解析错误: %+v", rows[0].Params)
	}
	if rows[0].Status != "pending" || rows[0].Objective != "profitfactor" {
		t.Fatalf("status/objective: %+v", rows[0])
	}

	// 状态流转 + 单条读取
	if err := db.UpdateOptimizationStatus(rows[1].ID, "approved"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetOptimization(rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "approved" || got.StrategyKind != "fac_1" {
		t.Fatalf("approve 后异常: %+v", got)
	}

	// 列表按任务倒序
	if err := db.SaveOptimizationResults(28, "winrate", seedSweepResults()[:1]); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListOptimizations(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0]["task_id"].(int64) != 28 {
		t.Fatalf("列表排序异常: %+v", list)
	}
	if list[0]["objective"].(string) != "winrate" {
		t.Fatalf("objective 缺失")
	}
}
