// optimizations_test.go 参数优化端点测试（§P2-f）：列表/审批写覆盖/内置拒绝/淘汰。
// English: sweep-optimizer endpoint tests — list / approve writes rule overrides / builtin rows
// are rejected / reject flow.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

func seedOptLib(t *testing.T, dir string) {
	t.Helper()
	entry := research.AppliedFactorEntry{
		ID: "fac_1", Name: "因子战法#1", Enabled: true, CandID: 1,
		Factors: []string{"mom_5"}, BuyThreshold: 70, Horizon: 5,
	}
	entry.Weights = map[string]float64{"mom_5": 1}
	entry.Directions = map[string]int{"mom_5": 1}
	// appendAppliedFactor 未导出——经 ApplyFactorRule 等价路径不可行（需候选行），
	// 测试内直接落 JSON 文件等价构造。
	b, _ := json.Marshal([]research.AppliedFactorEntry{entry})
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatalf("seed factor lib: %v", err)
	}
}

func TestOptimizationEndpoints(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfgMgr := config.NewManager("")
	s := &Server{researchDB: db, researchDir: dir, cfg: cfgMgr}
	seedOptLib(t, dir)

	if err := db.SaveOptimizationResults(990, "profitfactor", []map[string]any{
		{"rank": 1.0, "strategy": "双响炮", "strategy_kind": "",
			"params":   map[string]any{"trail_pct": 8.0, "hold_days": 15.0, "min_score": 70.0},
			"win_rate": 40.0, "profit_factor": 1.2, "avg_hold_days": 3.0, "trigger_count": 100.0},
		{"rank": 2.0, "strategy": "因子战法#1", "strategy_kind": "fac_1",
			"params":   map[string]any{"trail_pct": 12.0, "hold_days": 20.0, "min_score": 60.0},
			"win_rate": 45.0, "profit_factor": 1.1, "avg_hold_days": 5.0, "trigger_count": 90.0},
	}); err != nil {
		t.Fatal(err)
	}

	rows, _ := db.OptimizationResultsByTask(990)
	builtinID, ruleID := rows[0].ID, rows[1].ID

	// ① 内置行（双响炮）审批：写统一出场旋钮到 config（§P2 反馈升级——四内置全支持）
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/approve", nil)
	req.SetPathValue("id", itoa(builtinID))
	s.handleOptimizationApprove(rr, req)
	if rr.Code != 200 {
		t.Fatalf("内置行应用参数失败 code=%d body=%s", rr.Code, rr.Body.String())
	}
	gotCfg := cfgMgr.GetStrategyConfig()
	if gotCfg.DoubleBump.TrailingDrawbackPct != 8 || gotCfg.DoubleBump.MaxHoldDays != 15 {
		t.Fatalf("双响炮统一出场旋钮未写入: %+v", gotCfg.DoubleBump)
	}

	// ② 规则行审批 → 覆盖落 applied_factors.json + status=approved
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/approve", nil)
	req2.SetPathValue("id", itoa(ruleID))
	s.handleOptimizationApprove(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("规则行审批失败 code=%d body=%s", rr2.Code, rr2.Body.String())
	}
	entries, err := research.ListAppliedFactorRules(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("读库失败: %v", err)
	}
	e := entries[0]
	if e.BuyThreshold != 60 || e.ExitTrailPct != 12 || e.ExitMaxHoldDays != 20 {
		t.Fatalf("覆盖未写入: threshold=%v trail=%v hold=%v", e.BuyThreshold, e.ExitTrailPct, e.ExitMaxHoldDays)
	}
	got, _ := db.GetOptimization(ruleID)
	if got.Status != "approved" {
		t.Fatalf("状态应为 approved, got %s", got.Status)
	}

	// ③ 列表接口
	rr3 := httptest.NewRecorder()
	s.handleOptimizationList(rr3, httptest.NewRequest(http.MethodGet, "/list", nil))
	var resp struct {
		Optimizations []map[string]any `json:"optimizations"`
	}
	json.Unmarshal(rr3.Body.Bytes(), &resp)
	if len(resp.Optimizations) != 1 {
		t.Fatalf("列表应有 1 个任务分组")
	}

	// ④ 淘汰
	rr4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodPost, "/reject", nil)
	req4.SetPathValue("id", itoa(builtinID))
	s.handleOptimizationReject(rr4, req4)
	if rr4.Code != 200 {
		t.Fatalf("reject 失败: %s", rr4.Body.String())
	}
	got2, _ := db.GetOptimization(builtinID)
	if got2.Status != "rejected" {
		t.Fatalf("状态应为 rejected")
	}
}
