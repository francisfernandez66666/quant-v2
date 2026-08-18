// E6 因子规则应用/加载测试：ApplyFactorRule 落盘 → LoadAppliedFactorRule 读回。
package research

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestApplyLoadFactorRule 审批因子候选落盘后能完整读回规则。
func TestApplyLoadFactorRule(t *testing.T) {
	dir := t.TempDir()
	// 构造一条 factor 候选（Weights 为复合结构）
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
func TestLoadAppliedPatternRuleMissing(t *testing.T) {
	r, err := LoadAppliedPatternRule(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("缺失文件应无错误: %v", err)
	}
	if r != nil {
		t.Fatalf("缺失文件应返回 nil, got %+v", r)
	}
}
