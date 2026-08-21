// 多因子研究工具（B3/B5）：对一批股票计算 7 大类因子并输出 IC/IR/分层验证报告；
// B5 提供 optimize（权重优化产出候选）/ list（候选列表）/ approve（审批应用）；
// scan-depth 实时扫描研究池盘口，识别托单/压单并产出候选。
// 用法：research [flags] factor|optimize|scan-depth|discover-factors|discover-patterns|sector-rebuild|list|approve
//
//	flags：--db（默认 ~/.quant-trading-v2/trading.db）、--start（YYYYMMDD，默认 20200101）、
//	--end（YYYYMMDD，默认今天）、--h（前瞻天数，默认 5）、--quantiles（默认 5）、
//	--min-stocks（每日最小样本，默认 10）、--codes <文件>（研究池：每行一个 ts_code）、
//	--out（输出目录，默认 ./research_out）。
//
// 输出：report.json（全部因子验证数据）+ report.html（自包含可视化）。
// （research factor runs factor validation over a research pool; optimize/list/approve
// implement the B5 auto-research loop (weights optimization → guarded candidate → approval).）
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// defaultDB 默认研究 SQLite 库路径（~/.quant-trading-v2/trading.db）。
// （defaultDB is the default research SQLite DB path.）
var defaultDB = filepath.Join(os.Getenv("HOME"), ".quant-trading-v2", "trading.db")

// main 入口：解析全局 flags（--db/--start/--end 等）后按子命令分发；
// run-task 是任务队列的唯一执行入口（由 researchd worker 拉起）。
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dbPath := flag.String("db", defaultDB, "研究 SQLite 库路径")
	start := flag.String("start", "20200101", "起始日期 YYYYMMDD")
	end := flag.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	horizon := flag.Int("h", 5, "前瞻收益天数")
	quantiles := flag.Int("quantiles", 5, "分层数")
	minStocks := flag.Int("min-stocks", 10, "每日最小样本数")
	codesFile := flag.String("codes", "", "研究池文件（每行一个 ts_code，# 注释）")
	outDir := flag.String("out", "./research_out", "输出目录")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		log.Fatalf("用法: research [flags] factor|optimize|scan-depth|discover-factors|discover-patterns|sector-rebuild|paper-research|backtest|backtest-strategy|run-task|list|approve")
	}
	cmd := args[0]

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开研究库失败: %v", err)
	}
	defer db.Close()

	switch cmd {
	case "factor":
		runFactor(db, *start, *end, *horizon, *quantiles, *minStocks, *codesFile, *outDir)
	case "optimize":
		cmdOptimize(db, args[1:])
	case "scan-depth":
		cmdScanDepth(db, args[1:])
	case "discover-factors":
		cmdDiscoverFactors(db, args[1:])
	case "discover-patterns":
		cmdDiscoverPatterns(db, args[1:])
	case "sector-rebuild":
		cmdSectorRebuild(db, *start, *end)
	case "paper-research":
		// 模拟盘研究：读取盘后落库的模拟盘成交/净值，生成信号质量报告并落库（夜间 scheduler 调用）
		// English: paper research — reads the post-close paper fills/snapshots, produces a signal-quality
		// report and persists it (invoked by the nightly scheduler).
		cmdPaperResearch(db, args[1:])
	case "backtest":
		cmdBacktestCandidate(db, args[1:])
	case "backtest-strategy":
		// 战法/规则历史回放（二期并入，原独立二进制 bt_strategy）。
		cmdBacktestStrategy(db, *dbPath, args[1:])
	case "run-task":
		// 队列任务分发器（子系统统一改造一期）：worker 以 run-task --task-id N 拉起。
		// English: queue-task dispatcher (phase 1) — the worker spawns `run-task --task-id N`.
		cmdRunTask(db, *dbPath, args[1:])
	case "list":
		cmdList(db, args[1:])
	case "approve":
		dataDir := filepath.Dir(*dbPath)
		cmdApprove(db, args[1:], dataDir)
	default:
		log.Fatalf("未知子命令: %s", cmd)
	}
}

// cmdSectorRebuild 重建板块历史（E5）：按行业聚合每日涨停家数/平均涨跌幅/领涨股，
// 写入 sector_history 表，供形态战法回测与因子环境分组。
// 用法：research [--db ...] [--start ...] [--end ...] sector-rebuild
// English: rebuilds sector history (E5) — aggregates per-industry limit-up counts, mean change and
// top gainers into sector_history, used by pattern backtests and factor environment grouping.
func cmdSectorRebuild(db *store.DB, start, end string) {
	log.Printf("重建板块历史 %s ~ %s …", start, end)
	n, err := db.RebuildSectorHistory(start, end)
	if err != nil {
		log.Fatalf("重建板块历史失败: %v", err)
	}
	log.Printf("板块历史重建完成：写入 %d 行", n)
	// 打印行业覆盖概览（按最新交易日的涨停家数排序）
	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return
	}
	last := dates[len(dates)-1]
	lus, err := db.SectorLimitUpCounts(last)
	if err != nil {
		return
	}
	inds, err := db.SectorHistory("", last, last)
	if err != nil {
		return
	}
	log.Printf("%s 板块概览：%d 个行业", last, len(inds))
	for _, sd := range inds {
		log.Printf("  %-8s 涨停=%-3d 成员=%-3d 涨幅=%+.2f%% 领涨=%v",
			sd.Industry, sd.LimitupCnt, sd.MemberCount, sd.ChangePct, sd.TopStocks)
	}
	_ = lus
}

// runFactor 执行因子验证主流程：装配研究池面板 → 对全部已注册因子逐个体算
// IC/IR/分层单调性报告 → 输出 report.json + report.html。
// （runFactor validates every registered factor over the research pool and writes reports.）
func runFactor(db *store.DB, start, end string, horizon, quantiles, minStocks int, codesFile, outDir string) {
	codes, err := db.StockCodes()
	if codesFile != "" {
		codes, err = readCodesFile(codesFile)
	}
	if err != nil {
		log.Fatalf("读取研究池失败: %v", err)
	}
	if len(codes) == 0 {
		log.Fatalf("研究池为空（库中无股票或 --codes 文件为空）")
	}

	defs := factor.All()
	log.Printf("装配 %d 只股票（%s ~ %s）…", len(codes), start, end)
	panels, err := research.BuildPanels(db, codes, start, end, defs)
	if err != nil {
		log.Fatalf("装配面板失败: %v", err)
	}
	log.Printf("有效面板 %d 只（无行情股票已跳过）", len(panels))
	if len(panels) == 0 {
		log.Fatalf("无有效面板，检查 --db/--start/--end 或先跑 dataload")
	}

	var reports []*research.FactorReport
	for _, d := range defs {
		r := research.Summarize(panels, d, start, end, horizon, quantiles, minStocks)
		reports = append(reports, r)
		log.Printf("[%s] %s IC=%.4f IR=%.3f 有效日=%d 单调=%v",
			d.Cat.CategoryName(), d.Name, r.ICMean, r.IR, len(r.IC), r.Monotonic)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}
	if err := writeReports(outDir, reports); err != nil {
		log.Fatalf("写报告失败: %v", err)
	}
	log.Printf("完成：%s", outDir)
}

// writeReports 输出 report.json + report.html。
func writeReports(dir string, reports []*research.FactorReport) error {
	js, err := research.JSONReport(reports)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), js, 0o644); err != nil {
		return err
	}
	html, err := research.HTMLReport(reports)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.html"), html, 0o644)
}
