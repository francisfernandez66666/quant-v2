// 因子库测试：registry 结构 + 各因子公式单测。
package factor

import (
	"math"
	"testing"
)

// approx 校验数值近似（NaN 要求一致，否则容差 1e-9）。
func approx(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("长度不一致: got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("idx %d: 期望 NaN，得 %v", i, got[i])
			}
			continue
		}
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("idx %d: 期望 %.10f，得 %.10f", i, want[i], got[i])
		}
	}
}

// TestRegistry 校验注册表：ID 唯一、按 ID 排序、7 大类齐备、元信息完整。
func TestRegistry(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("注册表为空")
	}
	seen := make(map[string]bool)
	cats := make(map[Category]bool)
	lastID := ""
	for _, d := range all {
		if d.ID == "" || d.Name == "" || d.Compute == nil || d.Desc == "" {
			t.Fatalf("因子元信息不完整: %+v", d)
		}
		if seen[d.ID] {
			t.Fatalf("因子 ID 重复: %s", d.ID)
		}
		seen[d.ID] = true
		cats[d.Cat] = true
		if d.ID < lastID {
			t.Fatalf("因子未按 ID 排序: %s < %s", d.ID, lastID)
		}
		lastID = d.ID
	}
	if len(cats) != 7 {
		t.Fatalf("应覆盖 7 大类，实际 %d: %v", len(cats), cats)
	}
	// 每类中文名有效
	for c := range cats {
		if c.CategoryName() == "未知" {
			t.Fatalf("类别 %d 中文名缺失", c)
		}
	}
	// Get/ByCategory
	if d, ok := Get("ROE"); !ok || d.Cat != CatQuality {
		t.Fatalf("Get(ROE) 异常: %+v", d)
	}
	if _, ok := Get("不存在"); ok {
		t.Fatal("Get 不存在因子应返回 false")
	}
	if len(ByCategory(CatValue)) == 0 || len(ByCategory(CatLiquidity)) == 0 {
		t.Fatal("ByCategory 分类结果为空")
	}
}