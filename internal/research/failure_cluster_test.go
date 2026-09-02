// 失败战法聚类单元测试（§Phase2 失败战法聚类）。
// English: unit tests for failed-strategy clustering (Phase 2).
package research

import (
	"testing"

	"quant-trading-v2/internal/store"
)

func TestClusterFailures(t *testing.T) {
	results := []*store.OptimizationResult{
		// 正常战法：不应进入失败聚类
		{ID: 1, Strategy: "双响炮", Expectancy: 1.5, ProfitFactor: 1.8, WinRate: 55, TriggerCount: 120},
		// 样本不足：期望负但触发太少
		{ID: 2, Strategy: "因子战法#1", Expectancy: -0.5, ProfitFactor: 0.9, WinRate: 45, TriggerCount: 8},
		// 盈亏比倒挂：PF<1，触发足够
		{ID: 3, Strategy: "波动突破#2", Expectancy: -1.2, ProfitFactor: 0.6, WinRate: 50, TriggerCount: 60},
		// 低胜率：PF>=1 但胜率过低，期望负
		{ID: 4, Strategy: "低胜率型", Expectancy: -0.3, ProfitFactor: 1.1, WinRate: 30, TriggerCount: 90},
		// 回撤过大：期望负，胜率 PF 均尚可但回撤超限
		{ID: 5, Strategy: "高回撤型", Expectancy: -0.1, ProfitFactor: 1.0, WinRate: 48, TriggerCount: 200, MaxDrawdownPct: 35},
		// 综合失败：期望负但无典型成因命中
		{ID: 6, Strategy: "综合型", Expectancy: -0.2, ProfitFactor: 1.0, WinRate: 45, TriggerCount: 100, MaxDrawdownPct: 12},
	}

	stats, items := ClusterFailures(results)
	if len(items) != 5 {
		t.Fatalf("应聚出 5 条失败，实际 %d", len(items))
	}
	got := map[int64]FailureClusterID{}
	for _, it := range items {
		got[it.OptID] = it.Cluster
	}
	want := map[int64]FailureClusterID{
		2: ClusterInsufficient,
		3: ClusterAdversePayoff,
		4: ClusterLowWinRate,
		5: ClusterHighDrawdown,
		6: ClusterGeneral,
	}
	for id, cl := range want {
		if got[id] != cl {
			t.Fatalf("候选 #%d 预期簇 %s，实际 %s", id, cl, got[id])
		}
	}
	// 聚合统计：5 条失败，全部簇覆盖
	if len(stats) != 5 {
		t.Fatalf("应输出 5 个簇，实际 %d", len(stats))
	}
	var total int
	for _, s := range stats {
		total += s.Count
	}
	if total != 5 {
		t.Fatalf("簇总数应 5，实际 %d", total)
	}
	// 中文名/建议非空
	for _, cl := range want {
		if cl.ClusterName() == "" || cl.ClusterAdvice() == "" {
			t.Fatalf("簇 %s 缺中文名或建议", cl)
		}
	}
}

func TestClassifyFailureNonFailure(t *testing.T) {
	r := &store.OptimizationResult{ID: 9, Expectancy: 0.8, ProfitFactor: 1.5, WinRate: 52, TriggerCount: 80}
	if cl := classifyFailure(r); cl != "" {
		t.Fatalf("非失败战法不应被聚类，实际 %s", cl)
	}
}
