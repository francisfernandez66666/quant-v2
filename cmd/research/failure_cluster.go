// 失败战法聚类子命令（§Phase2 失败战法聚类）：
// 读取扫参优化结果（optimization_results），对表现失败的战法按失败原因规则聚类并输出统计与明细。
// 用法：research [--db ...] cluster-failures [--task N] [--limit N] [--json]
// English: failed-strategy clustering subcommand — reads sweep optimization results and clusters
// failing strategies by failure cause, printing cluster stats and item detail.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdClusterFailures 失败战法聚类入口。
// English: entry point for failed-strategy clustering.
func cmdClusterFailures(db *store.DB, args []string) {
	fs := flag.NewFlagSet("cluster-failures", flag.ExitOnError)
	taskID := fs.Int64("task", 0, "仅分析指定任务（0=全部任务）")
	limit := fs.Int("limit", 20, "参与分析的任务数上限（全部任务时）")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)

	var results []*store.OptimizationResult
	if *taskID > 0 {
		rs, err := db.OptimizationResultsByTask(*taskID)
		if err != nil {
			log.Fatalf("读取任务 %d 结果失败: %v", *taskID, err)
		}
		results = rs
	} else {
		tasks, err := db.ListOptimizations(*limit)
		if err != nil {
			log.Fatalf("读取扫参任务失败: %v", err)
		}
		for _, t := range tasks {
			if items, ok := t["results"].([]*store.OptimizationResult); ok {
				results = append(results, items...)
			}
		}
	}
	if len(results) == 0 {
		log.Fatalf("无优化结果可聚类（先运行 optimize/sweep 生成扫参结果）")
	}

	stats, items := research.ClusterFailures(results)
	if *asJSON {
		b, err := json.MarshalIndent(map[string]any{
			"analyzed":     len(results),
			"clusters":     stats,
			"failures":     items,
			"cluster_meta": clusterMetaMap(),
		}, "", "  ")
		if err != nil {
			log.Fatalf("序列化失败: %v", err)
		}
		fmt.Println(string(b))
		return
	}

	log.Printf("分析 %d 条优化结果，失败 %d 条", len(results), len(items))
	if len(stats) == 0 {
		log.Printf("无失败战法")
		return
	}
	fmt.Println("失败原因簇统计：")
	fmt.Printf("%-12s %6s %8s %8s %10s %8s\n", "簇", "数量", "平均胜率%", "平均PF", "平均期望%", "平均回撤%")
	for _, s := range stats {
		fmt.Printf("%-12s %6d %8.1f %8.2f %10.2f %8.1f\n",
			s.Cluster.ClusterName(), s.Count, s.AvgWinRate, s.AvgPF, s.AvgExpectancy, s.AvgDrawdown)
		fmt.Printf("  %s\n", s.Cluster.ClusterAdvice())
	}
	fmt.Println()
	fmt.Println("失败明细（前 20 条）：")
	for i, it := range items {
		if i >= 20 {
			break
		}
		fmt.Printf("#%-4d [%-14s] %-24s 胜率=%.1f%% PF=%.2f 期望=%.2f%% 触发=%d 回撤=%.1f%%\n",
			it.OptID, it.Cluster.ClusterName(), it.Strategy, it.WinRate,
			it.ProfitFactor, it.Expectancy, it.TriggerCount, it.MaxDrawdownPct)
	}
}

// clusterMetaMap 供 --json 输出的簇元信息（名称+建议）。
// English: cluster metadata (name + advice) for the --json output.
func clusterMetaMap() map[string]any {
	ids := []research.FailureClusterID{
		research.ClusterInsufficient, research.ClusterAdversePayoff,
		research.ClusterLowWinRate, research.ClusterHighDrawdown, research.ClusterGeneral,
	}
	out := map[string]any{}
	for _, id := range ids {
		out[string(id)] = map[string]string{
			"name":   id.ClusterName(),
			"advice": id.ClusterAdvice(),
		}
	}
	return out
}
