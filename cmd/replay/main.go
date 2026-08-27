// 历史全链路回测工具（B4）：合成事件 → 板块 → 多因子信号 → 前瞻验证。
// 用法：replay [flags] chain
//
//	flags：--db（默认 ~/.quant-trading-v2/trading.db）、--start（YYYYMMDD，默认 20200101）、
//	--end（YYYYMMDD，默认今天）、--horizon（逗号分隔，默认 1,5,10）、--min-limit-ups（默认 3）、
//	--max-per-day（默认 3）、--benchmark（默认 000300.SH）、--top-k（默认 5）、
//	--min-stocks（默认 10）、--factors（逗号分隔因子 ID，默认 7 大类精选）、--out（默认 ./bt_out）。
//
// 输出：report.json（全量数据）+ report.html（可视化）。
// （replay chain runs the offline full-chain backtest over synthesized sector limit-up events.）
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/backtest"
	"quant-trading-v2/internal/store"
)

// defaultDB 默认研究 SQLite 库路径（~/.quant-trading-v2/trading.db）。
var defaultDB = filepath.Join(os.Getenv("HOME"), ".quant-trading-v2", "trading.db")

// main 解析命令行参数、打开研究库、组装回测选项后执行 B4 全链路回测，
// 并把结果输出为 report.json + report.html（落盘到 --out 目录）。
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dbPath := flag.String("db", defaultDB, "研究 SQLite 库路径")
	start := flag.String("start", "20200101", "起始日期 YYYYMMDD")
	end := flag.String("end", time.Now().Format("20060102"), "结束日期 YYYYMMDD")
	horizon := flag.String("horizon", "1,5,10", "前瞻天数，逗号分隔")
	minLimitUps := flag.Int("min-limit-ups", 3, "触发事件的行业涨停家数下限")
	maxPerDay := flag.Int("max-per-day", 3, "每日最多事件数")
	benchmark := flag.String("benchmark", "000300.SH", "基准指数 ts_code")
	topK := flag.Int("top-k", 5, "每事件选股数")
	minStocks := flag.Int("min-stocks", 10, "当日有效样本下限")
	factors := flag.String("factors", "EP_ttm,BP,ROE,YoyNetProfit,SUE,Mom20,STO20", "因子 ID，逗号分隔")
	outDir := flag.String("out", "./bt_out", "输出目录")
	flag.Parse()

	if len(flag.Args()) < 1 || flag.Args()[0] != "chain" {
		log.Fatalf("用法: replay [flags] chain")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开研究库失败: %v", err)
	}
	defer db.Close()

	opts := backtest.DefaultOptions()
	opts.Start, opts.End = *start, *end
	opts.MinLimitUps, opts.MaxPerDay = *minLimitUps, *maxPerDay
	opts.Benchmark = *benchmark
	opts.Rule.TopK, opts.Rule.MinStocks = *topK, *minStocks
	if *factors != "" {
		opts.Rule.Factors = splitCSV(*factors)
	}
	var hs []int
	for _, s := range splitCSV(*horizon) {
		h, err := strconv.Atoi(s)
		if err != nil {
			log.Fatalf("无效 horizon: %s", s)
		}
		hs = append(hs, h)
	}
	opts.Horizons = hs

	log.Printf("回测 %s ~ %s（事件 ≥%d 涨停，TopK=%d，前瞻 %v）…", opts.Start, opts.End, opts.MinLimitUps, opts.Rule.TopK, opts.Horizons)
	rep, err := backtest.Run(db, opts)
	if err != nil {
		log.Fatalf("回测失败: %v", err)
	}
	log.Printf("事件 %d，入选 %d，事件级平均超额 %v，命中率 %v",
		rep.TotalEvents, rep.TotalPicks, rep.AvgExcess, rep.OverallHit)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}
	js, err := rep.JSONReport()
	if err != nil {
		log.Fatalf("JSON 序列化失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "report.json"), js, 0o644); err != nil {
		log.Fatalf("写 report.json 失败: %v", err)
	}
	html, err := rep.HTMLReport()
	if err != nil {
		log.Fatalf("HTML 渲染失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "report.html"), html, 0o644); err != nil {
		log.Fatalf("写 report.html 失败: %v", err)
	}
	log.Printf("完成：%s", *outDir)
}

// splitCSV 把逗号分隔字符串拆成去空白非空的切片。
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
