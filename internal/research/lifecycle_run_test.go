// lifecycle_run_test.go §GAP-P1 20260915：衰退降级夜间链执行器测试
// （paper.json 逐日聚合 + EvaluateDemote 落库禁用 + dry-run/无观测保守分支）。
package research

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writePaperJSON 生成规则池成交文件：pool 买卖配对，dayN 各 trades 笔。
func writePaperJSON(t *testing.T, dir string, records []map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "paper.json")
	b, err := json.Marshal(map[string]any{"trades": records})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sellRec 造一笔当日买、当日卖的成对成交记录（同池同代码），
// 供降级链按「交易日 × 规则池」聚合成观测样本使用。
func sellRec(pool, code string, buyPx, sellPx float64, day string) []map[string]any {
	return []map[string]any{
		{"code": code, "strategy_type": pool, "side": "buy", "price": buyPx, "qty": 100,
			"time": day + "T09:35:00+08:00"},
		{"code": code, "strategy_type": pool, "side": "sell", "price": sellPx, "qty": 100,
			"time": day + "T14:30:00+08:00"},
	}
}

func TestPoolDailyStatsBuckets(t *testing.T) {
	dir := t.TempDir()
	var recs []map[string]any
	// fac_7：3 个交易日，每日 3 笔全亏且日均逐日走低（滚动 IR 为负）
	for i, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03"} {
		for j := 0; j < 3; j++ {
			recs = append(recs, sellRec("fac_7", "60000"+string(rune('1'+i))+string(rune('1'+j)), 10.0, 9.5-float64(i)*0.3, day)...)
		}
	}
	// pat_2：一日 2 笔有盈
	recs = append(recs, sellRec("pat_2", "000001", 10.0, 10.8, "2026-09-01")...)
	recs = append(recs, sellRec("pat_2", "000002", 10.0, 9.9, "2026-09-01")...)
	// 无池归属成交（手动）：不得混入任何规则池
	recs = append(recs, map[string]any{"code": "600519", "side": "sell", "price": 9.0, "qty": 100, "time": "2026-09-01T15:00:00+08:00"})

	stats := PoolDailyStats(writePaperJSON(t, dir, recs))
	if _, ok := stats["other"]; ok {
		t.Fatal("无池归属成交不应产生 other 池")
	}
	if len(stats["fac_7"]) != 3 {
		t.Fatalf("fac_7 应聚合 3 个观测日, got %d", len(stats["fac_7"]))
	}
	for i, d := range stats["fac_7"] {
		if d.Trades != 3 {
			t.Fatalf("day%d Trades=%d, 应 3", i, d.Trades)
		}
		if d.WinRate != 0 {
			t.Fatalf("day%d 全亏应胜率0: %+v", i, d)
		}
		if i >= 1 && d.IR >= 0 {
			// 滚动 IR：首日样本 <2 记 0，两日起日均走低 IR 必为负
			t.Fatalf("day%d 连续亏损滚动 IR 应<0: %+v", i, d)
		}
	}
	d0 := stats["pat_2"][0]
	if d0.Trades != 2 || d0.WinRate != 50 {
		t.Fatalf("pat_2 日统计错误: %+v", d0)
	}
}

// TestDemoteAppliedRulesDisables 走完整降级链：连续衰退的 fac_7 被判定 disable 并落库禁用，
// 健康的 fac_8 保持启用，完全没有观测的 fac_9 保守跳过（不误杀新规则）。
func TestDemoteAppliedRulesDisables(t *testing.T) {
	dir := t.TempDir()
	var recs []map[string]any
	for _, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04"} {
		for j := 0; j < 4; j++ {
			recs = append(recs, sellRec("fac_7", "600001"+string(rune('a'+j)), 10.0, 9.4, day)...)
		}
	}
	// fac_8 健康：每日 4 笔全盈
	for _, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04"} {
		for j := 0; j < 4; j++ {
			recs = append(recs, sellRec("fac_8", "00000"+string(rune('1'+j)), 10.0, 10.6, day)...)
		}
	}
	writePaperJSON(t, dir, recs)
	entries := []AppliedFactorEntry{
		{ID: "fac_7", Name: "因子战法#7", Enabled: true, CandID: 7},
		{ID: "fac_8", Name: "因子战法#8", Enabled: true, CandID: 8},
		{ID: "fac_9", Name: "因子战法#9", Enabled: true, CandID: 9}, // 无观测：保守 keep
	}
	b, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// 正式执行（dryRun=false）：返回的 actions 按规则 ID 索引，便于逐条断言判定结果。
	actions, err := DemoteAppliedRules(dir, "", DemoteOpts{}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DemoteAction{}
	for _, a := range actions {
		got[a.Verdict.RuleID] = a
	}
	if got["fac_7"].Verdict.Verdict != "disable" || !got["fac_7"].Disabled {
		t.Fatalf("fac_7 连续衰退应降级落库: %+v", got["fac_7"])
	}
	if got["fac_8"].Verdict.Verdict != "keep" {
		t.Fatalf("fac_8 健康应保持: %+v", got["fac_8"])
	}
	if _, ok := got["fac_9"]; ok {
		t.Fatal("fac_9 无观测不应产出判定（保守不误杀）")
	}
	// 落库校验：applied_factors.json 中 fac_7 Enabled=false
	after, err := ListAppliedFactorRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if e.ID == "fac_7" && e.Enabled {
			t.Fatal("fac_7 应已被禁用（Enabled=false）")
		}
		if e.ID == "fac_8" && !e.Enabled {
			t.Fatal("fac_8 不应被禁用")
		}
	}
}

// TestDemoteAppliedRulesDryRunKeepsEnabled 验证 dry-run 分支：
// 判定结论照样产出（便于先看报表），但 applied_patterns.json 的 Enabled 不能被改动。
func TestDemoteAppliedRulesDryRunKeepsEnabled(t *testing.T) {
	dir := t.TempDir()
	var recs []map[string]any
	for _, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03"} {
		for j := 0; j < 3; j++ {
			recs = append(recs, sellRec("pat_3", "00001"+string(rune('1'+j)), 10.0, 9.2, day)...)
		}
	}
	writePaperJSON(t, dir, recs)
	pb, _ := json.Marshal([]AppliedPatternEntry{{ID: "pat_3", Name: "形态战法#3", Enabled: true, CandID: 3}})
	if err := os.WriteFile(filepath.Join(dir, "applied_patterns.json"), pb, 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err := DemoteAppliedRules(dir, "", DemoteOpts{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Verdict.Verdict != "disable" || actions[0].Disabled {
		t.Fatalf("dry-run 应判降级但不落库: %+v", actions)
	}
	after, _ := ListAppliedPatternRules(dir)
	if !after[0].Enabled {
		t.Fatal("dry-run 不得禁用规则")
	}
}
