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
	"strconv"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// seedOptLib 构造战法库种子数据。
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

// TestOptimizationEndpoints OptimizationEndpoints。
// 端到端串一遍寻优审批链：内置策略行审批写统一出场旋钮、规则行审批覆盖规则库并把状态推到
// approved，再看列表分组与 reject 淘汰；数据落在临时 SQLite + 临时规则库目录里。
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

// TestOptRefIDForSlots §W6：objective→ref_id 固定槽位（990~994），未知目标哈希进
// 1000~1099 且确定性稳定——锁死"同 objective 幂等、不同 objective 不互覆盖"的契约。
// English: objective-slot mapping contract for parallel optimize tasks.
func TestOptRefIDForSlots(t *testing.T) {
	want := map[string]int64{"": 990, "profitfactor": 990, "winRate": 991, "AVGWIN": 992, " expectancy ": 993, "calmar": 994}
	for obj, id := range want {
		if got := optRefIDFor(obj); got != id {
			t.Fatalf("optRefIDFor(%q)=%d want %d", obj, got, id)
		}
	}
	// 未知目标：确定性哈希且落在 1000~1099 区间，不与固定槽位冲突
	a, b := optRefIDFor("sharpe"), optRefIDFor("sharpe")
	if a != b || a < 1000 || a > 1099 {
		t.Fatalf("未知目标哈希漂移: %d vs %d", a, b)
	}
	if optRefIDFor("sharpe") == optRefIDFor("mdd") {
		t.Fatalf("不同未知目标不应同槽")
	}
}

// §RFIX-3 阈值覆盖守卫：候选 reason 含「预期触发=0」且审批把阈值覆盖上调 →
// 返回非阻断 warning；未上调/无特征 token/非 fac 行均不告警。
func TestThresholdOverrideWarning(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cid, err := db.SaveCandidate(&store.Candidate{
		Kind: "factor", Status: "proposed",
		Factors: `["Mom20"]`, Weights: `{"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70}`,
		Reason: "通过护栏 | 样本内IR=0.5 预期触发=0（阈值70，样本内3日均未触发，应用前请校准阈值）",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "fac_" + strconv.FormatInt(cid, 10)
	if w := thresholdOverrideWarning(db, key, 95); w == "" {
		t.Fatal("95>70 且预期触发=0 应告警")
	}
	if w := thresholdOverrideWarning(db, key, 60); w != "" {
		t.Fatalf("阈值未上调不应告警: %q", w)
	}
	if w := thresholdOverrideWarning(db, "pat_1", 95); w != "" {
		t.Fatalf("非因子行不应告警: %q", w)
	}
	if w := thresholdOverrideWarning(db, "fac_999", 95); w != "" {
		t.Fatalf("候选不存在不应告警: %q", w)
	}
}
