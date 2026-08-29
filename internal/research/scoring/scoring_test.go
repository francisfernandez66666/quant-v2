// scoring_test.go — 统一打分（时序分位）模块的单元测试。
// 验证 ScoreSeries / ScoreValue 的分位语义、MinLookback 边界与单值便捷封装一致性。
package scoring

import (
	"math"
	"testing"
)

// TestScoreSeriesPercentile 验证时序分位归一化的基本语义（修复研究↔实盘打分断层后唯一口径）。
func TestScoreSeriesPercentile(t *testing.T) {
	// 单调递增序列：末值最高，分位应接近 1；首值最低，分位应接近 0。
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	out := ScoreSeries(vals)
	if math.IsNaN(out[0]) || math.IsNaN(out[len(out)-1]) {
		t.Fatalf("端点不应为 NaN：out[0]=%v out[9]=%v", out[0], out[9])
	}
	if out[0] != 0 {
		t.Errorf("最小值分位应为 0，got %v", out[0])
	}
	if out[len(out)-1] != 1 {
		t.Errorf("最大值分位应为 1，got %v", out[len(out)-1])
	}
	// 中段值（5）应处于分位 0.444...（其余有效值 9 个，严格小于它的有 {1,2,3,4} 共 4 个 → 4/9）。
	if got := out[4]; math.Abs(got-4.0/9.0) > 1e-9 {
		t.Errorf("中位值分位应≈4/9，got %v", got)
	}
}

// TestScoreSeriesMinLookback 验证样本不足 MinLookback 时返回 NaN（防止短序列误判）。
func TestScoreSeriesMinLookback(t *testing.T) {
	out := ScoreSeries([]float64{1, 2, 3}) // 长度 < MinLookback(5)
	for i, v := range out {
		if !math.IsNaN(v) {
			t.Errorf("样本不足应全为 NaN，out[%d]=%v", i, v)
		}
	}
}

// TestScoreValue 验证单值分位便捷函数与 ScoreSeries 末位一致。
func TestScoreValue(t *testing.T) {
	hist := []float64{3, 1, 4, 1, 5, 9, 2, 6}
	cur := hist[len(hist)-1] // 6
	got := ScoreValue(hist, cur)
	// 严格小于 6 的有 {3,1,4,1,5,2} 共 6 个，其余有效值 7 个（total=8 排除自身）→ 6/7≈0.857。
	if math.Abs(got-6.0/7.0) > 1e-9 {
		t.Errorf("ScoreValue 应=6/7，got %v", got)
	}
	if !math.IsNaN(ScoreValue([]float64{1, 2}, 2)) {
		t.Errorf("样本不足应返回 NaN，got %v", ScoreValue([]float64{1, 2}, 2))
	}
}
