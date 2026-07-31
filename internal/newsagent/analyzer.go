package newsagent

import (
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// analyzeDeep Stage2 全量分析：调用 LLM 对筛选后的新闻深度分析，生成结构化 NewsEvent。
// 后置校正：档位归一 + 中性→0 + datetime 回退。
func (a *Agent) analyzeDeep(items []data.NewsItem) []NewsEvent {
	if len(items) == 0 {
		return nil
	}

	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = item.Title
	}

	results := a.llmClient.AnalyzeHotTopicBatch(titles)

	events := make([]NewsEvent, 0, len(results))
	for i, ht := range results {
		if ht == nil || ht.Title == "" {
			continue
		}
		postProcess(ht)
		dt := items[i].Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		// 来源自带的个股标签（如财联社 stock_list）并入归因，交 cleaner 清洗
		related := append([]string{}, ht.RelatedStocks...)
		for _, s := range items[i].Stocks {
			if s == "" || containsStr(related, s) {
				continue
			}
			related = append(related, s)
		}
		event := NewsEvent{
			Title:             items[i].Title,
			Content:           items[i].Content,
			Datetime:          dt,
			Source:            items[i].Source,
			IsMaterial:        true,
			Level:             ht.Level,
			Direction:         directionFromScore(ht.Score),
			Score:             ht.Score,
			Sectors:           ht.Sectors,
			UpstreamSectors:   ht.UpstreamSectors,
			DownstreamSectors: ht.DownstreamSectors,
			RelatedStocks:     related,
			ImpactLevel:       ht.ImpactLevel,
			EventType:         ht.EventType,
			Urgency:           ht.Urgency,
			Reason:            ht.Reason,
		}
		events = append(events, event)
	}

	log.Printf("[newsagent] Stage2全量分析: %d 个事件", len(events))
	return events
}

// containsStr 判断字符串切片中是否包含指定元素。
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// postProcess 对 LLM 分析结果做后置校正：
//   - 档位归一：|score| 归到 {0,0.25,0.5,0.75} 最近档，>0.75 截断为 0.75
//   - 中性→0：LLM 自身判定方向/情绪为"中性"时强制 score=0（消除 +0.5 中性污染）
func postProcess(ht *llm.HotTopic) {
	s := ht.Score
	if s < 0 {
		s = -s
	}
	if s > 0.75 {
		s = 0.75
	}
	tiers := []float64{0, 0.25, 0.5, 0.75}
	best := tiers[0]
	for _, t := range tiers {
		diff := s - t
		if diff < 0 {
			diff = -diff
		}
		if diff < s-best {
			best = t
		}
	}
	score := best
	if ht.Score < 0 {
		score = -score
	}
	if strings.EqualFold(ht.Sentiment, "中性") || strings.EqualFold(ht.Direction, "中性") {
		score = 0
	}
	ht.Score = score
}

// directionFromScore 由带符号 Score 推导展示用方向：>0 利好，<0 利空，==0 中性。
func directionFromScore(score float64) string {
	if score > 0 {
		return "利好"
	}
	if score < 0 {
		return "利空"
	}
	return "中性"
}
