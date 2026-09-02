// 模拟盘研究消费：夜间 scheduler 的研究步骤，读取盘后落库的模拟盘成交与每日快照
// （paper_trades / paper_daily），生成信号质量与绩效报告，并落库 paper_research_reports。
// 用法：research [--db ...] paper-research
// English: paper-research consumption — the scheduler's nightly research step reads the post-close
// paper fills and daily snapshots (paper_trades / paper_daily) exported from the live book, prints a
// signal-quality and performance report, and saves it to paper_research_reports.
package main

import (
	"encoding/json"
	"log"
	"time"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// cmdPaperResearch 生成模拟盘研究摘要（自动研究消费端）。
// English: builds the paper research summary (the auto-research consumer).
func cmdPaperResearch(db *store.DB, args []string) {
	summaries, err := db.PaperTradeSummaries()
	if err != nil {
		log.Fatalf("读取模拟盘成交汇总失败: %v", err)
	}
	daily, err := db.PaperDailyAll()
	if err != nil {
		log.Fatalf("读取模拟盘净值快照失败: %v", err)
	}
	if len(summaries) == 0 && len(daily) == 0 {
		log.Printf("[paper-research] 研究库暂无模拟盘数据（交易时段模拟盘运行 + 盘后落库后才有）")
		return
	}

	// §Phase4 归因喂回：按 用户+战法 的信号→成交质量（滑点/延迟/笔数），
	// 输给研究侧判断"哪个战法的信号兑现好"，可喂回失败聚类与寻优排序。
	// English: Phase-4 attribution — signal-to-fill quality by user+strategy (slippage/latency/count),
	// feeding "which strategy's signals fill well" back to clustering and optimization ranking.
	attrib, _ := db.PaperAttributions()

	// §Phase4 情绪相位上下文：最近一个月的历史情绪分布（配合每日复盘，判断当日情绪环境）
	// English: Phase-4 sentiment context — the emotion-phase histogram over the last ~30 trading days.
	phaseHist := research.EmotionPhaseHist(nil, nil)
	if rangeFrom := time.Now().AddDate(0, 0, -45).Format("20060102"); true {
		if emo, eerr := db.EmotionStatsRange(rangeFrom, time.Now().Format("20060102")); eerr == nil && len(emo) > 0 {
			phaseHist = research.EmotionPhaseHist(emo, nil)
		}
	}

	report := map[string]interface{}{
		"generated_at": time.Now().Format("2006-01-02 15:04:05"),
		"trades":       summaries,
		"daily":        daily,
		"attribution":  attrib,
		"emotion":      phaseHist,
	}
	// 战法池标签映射（与 paper.StrategyPools 展示一致）
	labels := map[string]string{"dragon": "龙头", "double_bump": "双板", "n_shape": "N形", "dragon_return": "龙回头", "factor": "波动突破", "pattern": "形态"}
	log.Printf("[paper-research] 模拟盘信号质量报告：%d 条成交分组 / %d 个净值快照", len(summaries), len(daily))
	// 逐组打印成交聚合：按池类型+方向输出笔数/金额/均价/滑点/延迟（研究日志可读性）
	// English: prints each pool+side aggregate — count/amount/avg price/slippage/latency (report readability).
	for _, s := range summaries {
		label := labels[s.StrategyType]
		if label == "" {
			label = "其他/手动"
		}
		log.Printf("  %-8s %s 笔数=%-4d 金额=%.0f 均价=%.2f 平均滑点=%+.3f%% 平均延迟=%.1fs",
			label, sideZh(s.Side), s.Count, s.TotalAmount, s.AvgPrice, s.AvgSlippage, s.AvgLatency)
	}
	// 归因喂回：打印前 5 个 用户+战法 的承接质量
	// English: attribution — print the top 5 user+strategy fill-quality rows.
	for i, a := range attrib {
		if i >= 5 {
			break
		}
		log.Printf("  归因 [%s/%s] 笔数=%-4d 成交额=%.0f 滑点=%+.3f%% 延迟=%.1fs (买%d/卖%d)",
			a.UserID, a.Strategy, a.Count, a.TotalAmount, a.AvgSlippage, a.AvgLatency, a.BuyCount, a.SellCount)
	}
	// 情绪相位：打印分布与最近阶段（每日复盘的环境锚点）
	// English: sentiment-phase histogram and latest phase (the environment anchor for the daily review).
	if phaseHist.Days > 0 {
		log.Printf("  情绪相位（近 %d 交易日）：最近=%s",
			phaseHist.Days, phaseHist.Last)
		for p, n := range phaseHist.PhaseDays {
			log.Printf("    %-6s %d 天", p, n)
		}
	}
	// 每个用户最新的净值/收益
	lastByUser := map[string]store.PaperDailyRecord{}
	for _, d := range daily {
		lastByUser[d.UserID] = d
	}
	for uid, d := range lastByUser {
		log.Printf("  [%s] 最新快照 %s 净值=%.2f 现金=%.2f 市值=%.2f 已实现=%.2f 持仓=%d",
			uid, d.Date, d.TotalValue, d.Cash, d.MarketValue, d.Realized, d.Positions)
	}

	// 落库 paper_research_reports（每天每用户一条，UPSERT 覆盖）
	// English: persist to paper_research_reports (one per user per day, UPSERT).
	js, err := json.Marshal(report)
	if err != nil {
		log.Fatalf("序列化报告失败: %v", err)
	}
	date := time.Now().Format("2006-01-02")
	for uid := range lastByUser {
		if err := db.SavePaperResearchReport(date, uid, string(js)); err != nil {
			log.Printf("[paper-research] 落库报告失败 user=%s: %v", uid, err)
		}
	}
	log.Printf("[paper-research] 报告已落库 paper_research_reports（date=%s）", date)
}

// sideZh 买卖方向中文名（研究日志可读性）。
// English: Chinese name for a fill side (report readability).
func sideZh(side string) string {
	if side == "buy" {
		return "买入"
	}
	if side == "sell" {
		return "卖出"
	}
	return side
}
