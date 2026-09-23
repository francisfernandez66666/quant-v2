// 运维口径:（本子命令的 RUNBOOK 说明——docs 由 owner 另行转正，此处为唯一权威描述）
//
// 目的：§ADJ-BASIS 把复权口径位（|adj=<research.AdjBaselineVersion>）折进 research_ckpts 的
// resume_key 之后，改前写入的断点行永远不会再被命中，成为 GB 级死重。本命令用来先量后删。
//
// 两步走（顺序不可颠倒）：
//
//	① 只读预检（缺省即 dry-run，**不删任何行**）：
//	   ./bin/research --db <研究库 trading.db> prune-stale-checkpoints
//	   打印表总行数、含口径位行数、判定为旧口径的行数与 payload 字节数、cutoff 时刻，
//	   以及逐命名空间（df| / dp| / pfac-dedup: / other）的规模。核对规模符合预期。
//	② 确认无误后再删（必须显式带 --apply）：
//	   ./bin/research --db <研究库 trading.db> prune-stale-checkpoints --apply
//	   删除范围严格限定为：resume_key 不含当前口径位 **且** created_at 严格早于
//	   "表内最后一次新口径断点写入时刻（cutoff）"的行；含口径位的行一律不动。
//
// 四道自动拒绝门（任一命中即不删，--apply 时以非零状态退出）：
//
//	· 在跑门：research_tasks 里有未终结（queued/running/paused/preempted）的
//	  discover_factors / discover_patterns 任务 → 等夜间链跑完再执行；
//	· 无基线门：库里一条含口径位的断点都没有（新基线尚未落库）→ 无对照，不删证据；
//	· 口径位来源：--adj-marker 缺省取 research.AdjBasisMarker，不要手填历史版本，
//	  否则会把当前基线判成旧口径；确需回滚判据时先只做 ①；
//	· 命名空间外的键（other 桶）同样按上述判据处理，日志只打印前缀，绝不打印
//	  resume_key 全文（键内含策略参数载荷）。
//
// 回收口径：DELETE 只把页还给 SQLite freelist，**库文件不会收缩**。命令末尾那行给出的
// 行数与字节（dbstat 度量的 research_ckpts 实占前后差，含索引页）是"表内可用空间"的口径；
// 要把空间真正还给操作系统，需在停写窗口另行执行 VACUUM——本工具**不会**自动 VACUUM。
// 已删行不可恢复：如需留证，先备份库文件（或导出待删行）再执行 ②。
//
// English: two-step operator procedure — dry-run by default (counts only), then an explicit
// --apply; pruning is refused while discovery tasks are in flight or when no basis-tagged
// checkpoint exists yet. Rows freed by DELETE go back to the freelist only (no VACUUM here).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdPruneStaleCheckpoints 子命令入口：解析 --apply / --adj-marker 后执行 pruneStaleCheckpoints，
// 并在"运维要求删、但被在跑门/无基线门挡住"时以非零状态退出（便于夜间脚本/人工感知）。
func cmdPruneStaleCheckpoints(db *store.DB, args []string) {
	fs := flag.NewFlagSet("prune-stale-checkpoints", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	apply := fs.Bool("apply", false, "真正删除（缺省 false：只统计不删）")
	marker := fs.String("adj-marker", research.AdjBasisMarker, "断点键中的复权口径位（缺省=当前基线，勿手填历史值）")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("prune-stale-checkpoints 参数解析失败: %v", err)
	}
	rep, err := pruneStaleCheckpoints(db, *marker, *apply)
	if err != nil {
		log.Fatalf("prune-stale-checkpoints 失败: %v", err)
	}
	if rep.Apply && (len(rep.Blocked) > 0 || rep.NoBaseline) {
		os.Exit(1) // 原因已由 pruneStaleCheckpoints 打印，这里只把失败状态透出
	}
}

// pruneStaleCheckpoints 执行一次统计/删除并打印运维输出（永不返回 nil 报告，除非出错）。
// 与 cmdPruneStaleCheckpoints 分开，是为了让测试可以直接拿到报告断言，而不依赖进程退出。
func pruneStaleCheckpoints(db *store.DB, marker string, apply bool) (*store.CkptPruneReport, error) {
	rep, err := db.PruneStaleCheckpoints(marker, apply)
	if err != nil {
		return nil, err
	}
	mode := "DRY-RUN（缺省，未删除任何行）"
	if rep.Apply {
		mode = "APPLY"
	}
	fmt.Printf("prune-stale-checkpoints：口径位=%q 模式=%s\n", rep.Marker, mode)
	fmt.Printf("  表总行数=%d 含口径位（一律保留）=%d 判定旧口径=%d 旧口径 payload=%d 字节\n",
		rep.TotalRows, rep.MarkedRows, rep.StaleRows, rep.StaleBytes)
	if rep.CutoffAt != "" {
		fmt.Printf("  cutoff=%s（表内最后一次新口径断点写入；created_at 严格早于此者才可删）\n", rep.CutoffAt)
	}
	if rep.NoBaseline {
		fmt.Println("  拒绝：库中没有任何含当前口径位的断点行（新基线尚未落库），本轮不删任何行。")
	}
	// 逐命名空间 before/after 规模（只打印前缀，绝不打印 resume_key 全文）。
	for _, ns := range rep.Namespaces {
		fmt.Printf("  命名空间 %-13s 行数=%-7d 可删=%-7d payload=%d 字节\n",
			ns.Namespace, ns.Rows, ns.Stale, ns.Bytes)
	}
	if len(rep.Blocked) > 0 {
		fmt.Printf("  在跑门：%d 个未终结的写断点任务 %v —— 本轮不删除，请等其结束后再 --apply。\n",
			len(rep.Blocked), rep.Blocked)
	}
	if !rep.Apply {
		fmt.Println("  下一步：确认规模无误后加 --apply 执行删除（不可恢复，必要时先备份库文件）。")
		return rep, nil
	}
	if rep.DeletedRows == 0 {
		fmt.Println("完成：本轮未删除任何行（无可删行，或被在跑门/无基线门挡住，见上）。")
		return rep, nil
	}
	freed := int64(0)
	sizeNote := "dbstat 不可用，仅报行数"
	if rep.TableBefore >= 0 && rep.TableAfter >= 0 {
		freed = rep.TableBefore - rep.TableAfter
		sizeNote = fmt.Sprintf("research_ckpts 实占 %d → %d 字节（差 %+d，含索引页）", rep.TableBefore, rep.TableAfter, freed)
	}
	fmt.Printf("完成：实际删除 %d 行 / 释放 payload %d 字节；%s。"+
		"注意：DELETE 只把页交回 SQLite freelist，库文件未收缩；需停写窗口另行 VACUUM 才真正还盘。\n",
		rep.DeletedRows, rep.StaleBytes, sizeNote)
	return rep, nil
}
