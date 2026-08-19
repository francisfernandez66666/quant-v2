// E6 因子规则应用/加载测试：ApplyFactorRule 落盘 → LoadAppliedFactorRule 读回。
// English: E6 factor rule apply/load test: ApplyFactorRule persists to disk → LoadAppliedFactorRule reads back.
package research

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestApplyLoadFactorRule 审批因子候选落盘后能完整读回规则。
// English: TestApplyLoadFactorRule verifies an approved factor candidate can be fully read back after being persisted.
func TestApplyLoadFactorRule(t *testing.T) {
	dir := t.TempDir()
	// 构造一条 factor 候选（Weights 为复合结构）
	// English: Build a factor candidate (Weights is a composite structure)
	c := &store.Candidate{
		Kind: "factor", Factors: `["Mom20","STO20"]`,
		Weights: `{"weights":{"Mom20":0.6,"STO20":0.4},"directions":{"Mom20":1,"STO20":-1},"buy_threshold":65}`,
		Horizon: 5, IR: 0.4,
	}
	if err := ApplyFactorRule(dir, c); err != nil {
		t.Fatalf("ApplyFactorRule 失败: %v", err)
	}
	r, err := LoadAppliedFactorRule(dir)
	if err != nil {
		t.Fatalf("LoadAppliedFactorRule 失败: %v", err)
	}
	if r == nil {
		t.Fatal("读回规则为 nil")
	}
	if len(r.Factors) != 2 || r.Factors[0] != "Mom20" {
		t.Fatalf("因子列表异常: %v", r.Factors)
	}
	if r.Weights["Mom20"] != 0.6 || r.Directions["STO20"] != -1 {
		t.Fatalf("权重/方向读回异常: %+v %+v", r.Weights, r.Directions)
	}
	if r.BuyThreshold != 65 {
		t.Fatalf("阈值=%v 期望 65", r.BuyThreshold)
	}
}

// TestLoadAppliedFactorRuleMissing 文件不存在 → 返回 nil（未启用）。
// English: TestLoadAppliedFactorRuleMissing: file absent → returns nil (not enabled).
func TestLoadAppliedFactorRuleMissing(t *testing.T) {
	r, err := LoadAppliedFactorRule(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("缺失文件应无错误: %v", err)
	}
	if r != nil {
		t.Fatalf("缺失文件应返回 nil, got %+v", r)
	}
}

// TestApplyLoadPatternRule 审批形态候选落盘后能完整读回模板条件。
// English: TestApplyLoadPatternRule verifies an approved pattern candidate can be fully read back after being persisted.
func TestApplyLoadPatternRule(t *testing.T) {
	dir := t.TempDir()
	c := &store.Candidate{
		Kind: "pattern", Factors: `[{"factor":"Drawdown20","min":0.1,"max":0.3},{"factor":"VolShrink","min":0,"max":0.6}]`,
		Weights: "{}", Metric: 0.02,
	}
	if err := ApplyPatternRule(dir, c); err != nil {
		t.Fatalf("ApplyPatternRule 失败: %v", err)
	}
	r, err := LoadAppliedPatternRule(dir)
	if err != nil {
		t.Fatalf("LoadAppliedPatternRule 失败: %v", err)
	}
	if r == nil || len(r.Conds) != 2 {
		t.Fatalf("读回规则异常: %+v", r)
	}
	if r.Conds[0].Factor != "Drawdown20" || r.Conds[0].Max != 0.3 {
		t.Fatalf("条件读回异常: %+v", r.Conds[0])
	}
	if r.Conds[1].Factor != "VolShrink" || r.Conds[1].Min != 0 {
		t.Fatalf("条件读回异常: %+v", r.Conds[1])
	}
}

// TestLoadAppliedPatternRuleMissing 文件不存在 → nil。
// English: TestLoadAppliedPatternRuleMissing: file absent → nil.
func TestLoadAppliedPatternRuleMissing(t *testing.T) {
	r, err := LoadAppliedPatternRule(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("缺失文件应无错误: %v", err)
	}
	if r != nil {
		t.Fatalf("缺失文件应返回 nil, got %+v", r)
	}
}

// TestFactorLibraryMultiRule 战法库：多候选追加共存 + 启用/禁用/删除。
// English: TestFactorLibraryMultiRule: strategy library — multiple candidates coexist after appending + enable/disable/delete.
func TestFactorLibraryMultiRule(t *testing.T) {
	dir := t.TempDir()
	c1 := &store.Candidate{ID: 1, Kind: "factor", Factors: `["Mom20"]`,
		Weights: `{"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70}`, Horizon: 5, IR: 0.3}
	c2 := &store.Candidate{ID: 2, Kind: "factor", Factors: `["STO20"]`,
		Weights: `{"weights":{"STO20":1},"directions":{"STO20":-1},"buy_threshold":65}`, Horizon: 5, IR: 0.4}
	if err := ApplyFactorRule(dir, c1); err != nil {
		t.Fatalf("应用候选1失败: %v", err)
	}
	if err := ApplyFactorRule(dir, c2); err != nil {
		t.Fatalf("应用候选2失败: %v", err)
	}
	entries, err := ListAppliedFactorRules(dir)
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应有 2 条战法, got %d", len(entries))
	}
	// 禁用第一条后，LoadEnabledFactorRules 只返回第二条
	// English: After disabling the first entry, LoadEnabledFactorRules returns only the second
	if err := SetAppliedFactorEnabled(dir, entries[0].ID, false); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	rules, err := LoadEnabledFactorRules(dir)
	if err != nil {
		t.Fatalf("加载启用规则失败: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "因子战法#2" {
		t.Fatalf("禁用后应只剩 1 条启用规则, got %d: %+v", len(rules), rules)
	}
	// 删除第一条
	// English: Delete the first entry
	if err := RemoveAppliedFactorRule(dir, entries[0].ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	entries2, _ := ListAppliedFactorRules(dir)
	if len(entries2) != 1 || entries2[0].ID != entries[1].ID {
		t.Fatalf("删除后应剩 1 条, got %d", len(entries2))
	}
}

// TestFactorLibraryLegacyMigration 旧版单对象 applied_factors.json → 自动迁移为列表。
// English: TestFactorLibraryLegacyMigration: legacy single-object applied_factors.json → auto-migrated to a list.
func TestFactorLibraryLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"factors":["Mom20"],"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,"horizon":5,"ir":0.3,"excess":0}`
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := ListAppliedFactorRules(dir)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if len(entries) != 1 || !entries[0].Enabled || len(entries[0].Factors) != 1 {
		t.Fatalf("迁移后应 1 条启用, got %+v", entries)
	}
	// 迁移后文件应为列表格式
	// English: After migration the file should be in list format
	raw, _ := os.ReadFile(filepath.Join(dir, "applied_factors.json"))
	if len(raw) == 0 || raw[0] != '[' {
		t.Fatalf("迁移后文件应为 JSON 数组")
	}
}

// TestUpdateAppliedFactorStats 运行统计回写。
// English: TestUpdateAppliedFactorStats: running statistics write-back.
func TestUpdateAppliedFactorStats(t *testing.T) {
	dir := t.TempDir()
	c := &store.Candidate{ID: 7, Kind: "factor", Factors: `["Mom20"]`,
		Weights: `{"weights":{"Mom20":1},"directions":{"Mom20":1}}`, Horizon: 5}
	if err := ApplyFactorRule(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAppliedFactorStats(dir, "fac_7", 3, 2, 1, 0.12); err != nil {
		t.Fatalf("更新统计失败: %v", err)
	}
	entries, _ := ListAppliedFactorRules(dir)
	if entries[0].SignalCount != 3 || entries[0].Win != 2 || entries[0].Loss != 1 || entries[0].CumReturn != 0.12 {
		t.Fatalf("统计回写异常: %+v", entries[0])
	}
}
