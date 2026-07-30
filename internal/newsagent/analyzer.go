package newsagent

import (
	"log"

	"quant-trading-v2/internal/data"
)

// analyzeDeep Stage2 全量分析：调用 LLM 对筛选后的新闻深度分析，生成结构化 NewsEvent。
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
		event := NewsEvent{
			Title:             items[i].Title,
			Content:           items[i].Content,
			Datetime:          items[i].Datetime,
			Source:            items[i].Source,
			IsMaterial:        true,
			Level:             ht.Level,
			Direction:         ht.Direction,
			Score:             ht.Score,
			Sectors:           ht.Sectors,
			UpstreamSectors:   ht.UpstreamSectors,
			DownstreamSectors: ht.DownstreamSectors,
			RelatedStocks:     ht.RelatedStocks,
			ImpactLevel:       ht.ImpactLevel,
			EventType:        ht.EventType,
			Urgency:           ht.Urgency,
			Reason:            ht.Reason,
		}
		events = append(events, event)
	}

	log.Printf("[newsagent] Stage2全量分析: %d 个事件", len(events))
	return events
}
