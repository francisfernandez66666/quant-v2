// event_layers.go — §ENH-4 子命令 event-layers：事件因子（news_score@stock/@sector）的
// IC/分层/单调性检验。依赖 §ENH-3 的 news_event_history.jsonl 归档（批 B 起自动生成）。
// 用法：research [--db --start --end --codes --quantiles --min-stocks] event-layers [--history <jsonl>] [--h 1,5,10]
// 输出：dataDir/event_factor_report.json（GET /api/research/event-factor 消费）+ 控制台摘要。
// English: event-layers subcommand — IC/layer/monotonic validation of the news-score event
// factors; writes event_factor_report.json next to the DB for the Research UI card.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdEventLayers 事件因子检验主流程（见文件头）。
func cmdEventLayers(db *store.DB, dbPath, start, end string, quantiles, minStocks int, codesFile string, args []string) {
	fs := flag.NewFlagSet("event-layers", flag.ExitOnError)
	history := fs.String("history", defaultEventHistoryPath(dbPath), "事件归档 jsonl 路径")
	hList := fs.String("h", "1,5,10", "前瞻天数列表（逗号分隔）")
	_ = fs.Parse(args)

	rows, err := research.LoadEventHistory(*history)
	if err != nil {
		log.Fatalf("读取事件归档失败: %v（需批 B 起有换日归档，或用 --history 指定）", err)
	}
	if len(rows) == 0 {
		log.Fatalf("事件归档为空: %s", *history)
	}
	industryByCode, err := db.StockIndustries()
	if err != nil {
		log.Fatalf("读取行业映射失败: %v", err)
	}
	stockScores, sectorScores := research.BuildEventScores(rows, industryByCode)
	log.Printf("事件归档 %d 行 → 个股档覆盖 %d 个事件日、板块档覆盖 %d 个事件日", len(rows), len(stockScores), len(sectorScores))

	codes, err := db.StockCodes()
	if codesFile != "" {
		codes, err = readCodesFile(codesFile)
	}
	if err != nil || len(codes) == 0 {
		log.Fatalf("研究池为空（检查 --codes 或 dataload）: %v", err)
	}
	// 面板不需要常规因子列，事件因子列由 ApplyEventScores 事后写入（defs 传空）。
	panels, err := research.BuildPanels(db, codes, start, end, nil)
	if err != nil || len(panels) == 0 {
		log.Fatalf("无有效面板（%s~%s）: %v", start, end, err)
	}
	research.ApplyEventScores(panels, stockScores, research.EventFactorStock)
	research.ApplyEventScores(panels, sectorScores, research.EventFactorSector)

	var horizons []int
	for _, s := range strings.Split(*hList, ",") {
		if h, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && h > 0 {
			horizons = append(horizons, h)
		}
	}
	defs := []factor.Def{
		{ID: research.EventFactorStock, Name: "新闻事件分(个股)", Cat: factor.CatSentiment, Desc: "LLM 冲击分，同日同票取 |score| 最大"},
		{ID: research.EventFactorSector, Name: "新闻事件分(板块)", Cat: factor.CatSentiment, Desc: "板块级事件经行业名互相包含匹配传导"},
	}
	var reports []*research.FactorReport
	for _, h := range horizons {
		for _, d := range defs {
			r := research.Summarize(panels, d, start, end, h, quantiles, minStocks)
			reports = append(reports, r)
			fmt.Printf("%-20s h=%2d IC均值 %8.4f IC_std %6.4f IR %7.3f 有效日 %3d 单调 %v(%+d)\n",
				r.ID, h, r.ICMean, r.ICStd, r.IR, len(r.IC), r.Monotonic, r.MonotonicDir)
		}
	}
	// 落盘供前端事件因子卡读取（与 DB 同目录 = 数据目录约定）。
	outPath := filepath.Join(filepath.Dir(dbPath), "event_factor_report.json")
	body, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		log.Fatalf("报告序列化失败: %v", err)
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		log.Fatalf("写报告失败: %v", err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		log.Fatalf("发布报告失败: %v", err)
	}
	log.Printf("事件因子报告已写入 %s（%d 份）", outPath, len(reports))
}

// defaultEventHistoryPath 事件归档默认路径：优先 QUANT_DATA_DIR，其次与 DB 同目录（两者同源）。
func defaultEventHistoryPath(dbPath string) string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return filepath.Join(d, "news_event_history.jsonl")
	}
	return filepath.Join(filepath.Dir(dbPath), "news_event_history.jsonl")
}
