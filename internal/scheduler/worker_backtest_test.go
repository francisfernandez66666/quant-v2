// worker_backtest_test.go — §回测自动增强 A0/C 管线测试：payload 注入回退链
// （无记录/停用=原样；enabled=注入并解析名义额）、SWEEP_JSON pareto 段捎带进
// 冠军行 grid_json（零 schema 迁移）、旧任务无 pareto 键解析容错。
// English: nightly payload injection + SWEEP_JSON pareto carry-through tests for the worker.
package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// writeRulesConfig 写一份仅含 rules.paper 的 config.json，返回路径。
func writeRulesConfig(t *testing.T, dir, fixedAmount string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	raw := `{"rules":{"paper":{"fixed_amount":` + fixedAmount + `}}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInjectBacktestPayloadFallbacks 注入端回退链：无记录 / enabled=false 原样返回；
// enabled=true 时注入并把真实配置的 paper.fixed_amount 解析为显式名义额。
func TestInjectBacktestPayloadFallbacks(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfgPath := writeRulesConfig(t, dir, "20000")
	s := New(dir, cfgPath, filepath.Join(dir, "research_state.json"))
	const base = `{"kind":"optimize","top_n":20}`

	// 1) 无记录 → 原样
	if got := s.injectBacktestPayload(db, base); got != base {
		t.Fatalf("无记录不应改动 payload: %s", got)
	}
	// 2) enabled=false → 原样
	if err := db.SetBacktestSettings(`{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if got := s.injectBacktestPayload(db, base); got != base {
		t.Fatalf("停用不应改动 payload: %s", got)
	}
	// 3) enabled=true → 注入且名义额取 fixed_amount=20000
	if err := db.SetBacktestSettings(`{"enabled":true,"slippage":{"auto_calibrate":true}}`); err != nil {
		t.Fatal(err)
	}
	got := s.injectBacktestPayload(db, base)
	if got == base {
		t.Fatal("enabled 时应注入 backtest 段")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	bt, ok := p["backtest"].(map[string]any)
	if !ok || bt["enabled"] != true {
		t.Fatalf("backtest 段异常: %v", p["backtest"])
	}
	if bt["order_value_yuan"] != 20000.0 {
		t.Fatalf("名义额未解析: %v", bt["order_value_yuan"])
	}
	// 原有字段保持不变
	if p["kind"] != "optimize" || p["top_n"] != 20.0 {
		t.Fatalf("原字段被破坏: %v", p)
	}
}

// TestSaveSweepResultsParetoCarry pareto/slippage_calib 顶层键随 grid/batches 捎带进
// 冠军行 grid_json；旧任务（无这些键）grid_json 亦无该键（前端按缺键降级为 champion 展示）。
func TestSaveSweepResultsParetoCarry(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(dir, writeRulesConfig(t, dir, "10000"), filepath.Join(dir, "research_state.json"))

	// 新任务：带 pareto + slippage_calib（单行——SWEEP_JSON regex 按整行匹配）
	out := "SWEEP_JSON:" + `{"strategy":"双响炮","objective":"profitfactor","grid":[{"tp":8,"sl":5,"expectancy":1.2,"triggers":30}],"batches":[{"batch":1,"tp":8,"sl":5,"objective":2.1}],"results":[{"rank":1,"strategy":"双响炮","params":{"take_profit_pct":8,"stop_loss_pct":5,"hold_days":10,"min_score":60}}],"pareto":{"gates":{"min_win_rate":30},"front":[{"win_rate":45,"profit_factor":2.1,"sharpe":1.3,"calmar":1.8,"trigger_count":120}],"recommended":null},"slippage_calib":{"source":"paper_median","buy_bps":4.1,"sell_bps":2.8,"n_buy":96,"n_sell":74}}`
	s.saveSweepResults(db, 501, strings.Join([]string{"noise line", out, "tail"}, "\n"))

	tasks, err := db.ListOptimizations(10)
	if err != nil {
		t.Fatal(err)
	}
	grid := championGrid(t, tasks, 501)
	if !strings.Contains(grid, `"pareto"`) || !strings.Contains(grid, `"slippage_calib"`) {
		t.Fatalf("pareto/校准段未捎带进 grid_json: %s", grid)
	}
	// pareto 段可二次解析（RawMessage 嵌入保持原样 JSON）
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(grid), &extra); err != nil {
		t.Fatal(err)
	}
	var pareto map[string]any
	if err := json.Unmarshal(extra["pareto"], &pareto); err != nil {
		t.Fatalf("pareto 段非法: %v", err)
	}
	if fr, ok := pareto["front"].([]any); !ok || len(fr) != 1 {
		t.Fatalf("front 点集异常: %v", pareto["front"])
	}

	// 旧任务：无 pareto 键 → grid_json 亦无该键
	outOld := `SWEEP_JSON:{"strategy":"龙头","objective":"winrate","grid":[{"tp":5,"sl":4,"expectancy":0.5,"triggers":25}],"results":[{"rank":1,"strategy":"龙头","params":{"take_profit_pct":5,"stop_loss_pct":4,"hold_days":8}}]}`
	s.saveSweepResults(db, 502, outOld)
	tasks2, _ := db.ListOptimizations(10)
	grid2 := championGrid(t, tasks2, 502)
	if strings.Contains(grid2, `"pareto"`) {
		t.Fatalf("旧任务不应出现 pareto 键: %s", grid2)
	}
}

// championGrid 从列表结果取指定任务的冠军行 grid_json。
func championGrid(t *testing.T, tasks []map[string]any, taskID int64) string {
	t.Helper()
	for _, task := range tasks {
		if tid, _ := task["task_id"].(int64); tid != taskID {
			continue
		}
		rows, _ := task["results"].([]*store.OptimizationResult)
		if len(rows) > 0 {
			return rows[0].GridJSON
		}
	}
	t.Fatalf("未找到任务 #%d 的排名行", taskID)
	return ""
}
