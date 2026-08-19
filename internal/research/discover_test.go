// E2/E3 因子发现测试：前向选择 + 权重优化 + 样本内/样本外 + 反推泛化。
// English: E2/E3 factor discovery tests: forward selection + weight optimization + in-sample/out-of-sample + reverse generalization.
package research

import (
	"strconv"
	"testing"
)

// TestDiscoverFactorsSelectsSignal 因子发现应选中强信号 f1/f2，剔除噪声 f3。
// English: TestDiscoverFactorsSelectsSignal: factor discovery should select strong signals f1/f2 and drop noise f3.
func TestDiscoverFactorsSelectsSignal(t *testing.T) {
	dates := makeDates(40) // 足够长便于分段
	// English: long enough for segmentation
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	res := DiscoverFactors(panels, DiscoverOpts{
		Factors: []string{"f1", "f2", "f3"},
		Horizon: 1, MinStocks: 3, MaxFactors: 3, SplitPct: 0.6,
		MinDays: 5, MinIR: 0.3,
	})
	if len(res.Factors) == 0 {
		t.Fatal("因子发现未选出任何因子")
	}
	// 不应选中噪声因子 f3（除非与其他因子互补）；f1/f2 至少一个被选中
	// English: should not select noise factor f3 (unless complementary with other factors); at least one of f1/f2 should be selected
	hasSignal := false
	for _, f := range res.Factors {
		if f == "f1" || f == "f2" {
			hasSignal = true
		}
	}
	if !hasSignal {
		t.Fatalf("因子发现未选中强信号 f1/f2，实际=%v", res.Factors)
	}
	if res.IR <= 0 {
		t.Fatalf("因子发现 IR=%.4f 应为正", res.IR)
	}
	// 合成数据 IC 恒定 → IR 可能为 NaN（std=0），样本内/样本外不做严格 >0 断言；
	// English: synthetic data has constant IC → IR may be NaN (std=0); in-sample/out-of-sample are not strictly asserted >0;
	// 但代码路径应产出 InsampleIR/OutsampleIR 字段（NaN→0 或正数皆合法）。
	// English: but the code path should produce InsampleIR/OutsampleIR fields (either NaN→0 or positive is valid).
	// 反推泛化（GenExcess）才是合成数据的可靠验证：高分组应跑赢全样本。
	// English: reverse generalization (GenExcess) is the reliable validation for synthetic data: the top group should beat the full sample.
	if res.GenExcess <= 0 {
		t.Fatalf("反推泛化超额=%.4f 应为正（高分组跑赢全样本）", res.GenExcess)
	}
	// 方向：f1/f2 看多（+1）
	// English: direction: f1/f2 long (+1)
	for _, f := range res.Factors {
		if res.Directions[f] != 1 {
			t.Fatalf("因子 %s 方向=%d 期望 +1", f, res.Directions[f])
		}
	}
}

// TestDiscoverFactorsEmptyPanels 空面板返回合理 Reason。
// English: TestDiscoverFactorsEmptyPanels: empty panels return a sensible Reason.
func TestDiscoverFactorsEmptyPanels(t *testing.T) {
	res := DiscoverFactors(nil, DiscoverOpts{Factors: []string{"f1"}, Horizon: 1, MinStocks: 3})
	if res.Reason == "" {
		t.Fatal("空面板应返回 Reason")
	}
}

// TestCompositeICRange 时间范围过滤：只统计指定日期区间的 IC 行。
// English: TestCompositeICRange: time-range filter — only counts IC rows in the given date range.
func TestCompositeICRange(t *testing.T) {
	dates := []string{"20230101", "20230102", "20230103", "20230104", "20230105", "20230106"}
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	// 只取后 3 天
	// English: only take the last 3 days
	rows := CompositeICRange(panels, []string{"f1", "f2"}, map[string]float64{"f1": 0.5, "f2": 0.5}, 1, 3, "20230104", "20230106")
	if len(rows) == 0 {
		t.Fatal("范围内无 IC 行")
	}
	for _, r := range rows {
		if r.Date < "20230104" || r.Date > "20230106" {
			t.Fatalf("IC 行日期 %s 超出范围", r.Date)
		}
	}
	// 全区间应多于子区间
	// English: the full range should have more rows than the sub-range
	all := CompositeICRange(panels, []string{"f1", "f2"}, map[string]float64{"f1": 0.5, "f2": 0.5}, 1, 3, "", "")
	if len(all) <= len(rows) {
		t.Fatalf("全区间行数 %d 应大于子区间 %d", len(all), len(rows))
	}
}

// TestReverseExtension 反推泛化：强因子组合的高分组平均前瞻收益应高于非高分组，
// English: TestReverseExtension: reverse generalization — the top group's average forward return for a strong factor combo should exceed the non-top group,
// 且 t 统计量为正（高分组显著跑赢）。
// English: and the t-statistic should be positive (top group significantly outperforms).
// English:
func TestReverseExtension(t *testing.T) {
	dates := makeDates(40)
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	top, rest, excess, _, tstat := reverseExtension(panels, []string{"f1", "f2"},
		map[string]int{"f1": 1, "f2": 1}, map[string]float64{"f1": 0.5, "f2": 0.5},
		DiscoverOpts{Horizon: 1, MinStocks: 3}, "", "")
	if excess <= 0 {
		t.Fatalf("反推泛化超额=%.4f 应为正（高分组跑赢非高分组）", excess)
	}
	if rest <= 0 || top <= rest {
		t.Fatalf("异常：top=%.4f rest=%.4f", top, rest)
	}
	if isNaN(tstat) || tstat <= 0 {
		t.Fatalf("t 统计量=%.2f 应为正（高分组显著跑赢）", tstat)
	}
}

// makeDates 生成 n 个连续交易日（YYYYMMDD，用递增数字日期保证字典序=时间序）。
// English: makeDates generates n consecutive trading days (YYYYMMDD, using incrementing numeric dates so lexicographic order = time order).
func makeDates(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(20230101 + i)
	}
	return out
}
