package newsagent

import (
	"log"
	"math"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// analyzeDeep Stage2 全量分析：调用 LLM 对筛选后的新闻深度分析，生成结构化 NewsEvent。
// LLM 轮询重试（最多3次、递增间隔）仍失败时该批丢弃（返回 nil，不降级关键词兜底）。
// 后置校正：档位归一 + 中性归零 + datetime 回退。
func (a *Agent) analyzeDeep(items []data.NewsItem) []NewsEvent {
	if len(items) == 0 {
		return nil
	}

	// 抽取标题列表，供 LLM 批量分析
	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = item.Title
	}

	results, err := a.llmClient.AnalyzeHotTopicBatch(titles)
	if err != nil {
		log.Printf("[newsagent] Stage2 LLM批量分析失败, 该批%d条丢弃: %v", len(titles), err)
		return nil
	}

	events := make([]NewsEvent, 0, len(results))
	for i, ht := range results {
		// 跳过空结果：LLM 未给出标题的无效项
		if ht == nil || ht.Title == "" {
			continue
		}
		// 后置校正：档位归一 + 中性强制归零
		postProcess(ht)
		// 兜底时间：新闻无发布时间时用当前时间
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
		// 组装 NewsEvent：标题/正文取原始新闻，分析字段取 LLM 结果
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
//   - 中性归零（放宽）：仅当方向/情绪为"中性" 且 强度档位为 0（fallback 无方向）
//     时才归零；对 LLM 明确给出的非零方向分数保留量化档，避免弱事件被误清空。
func postProcess(ht *llm.HotTopic) {
	// 取绝对值并截断上限，只处理强度档位
	s := ht.Score
	if s < 0 {
		s = -s
	}
	if s > 0.75 {
		s = 0.75
	}
	// 遍历四个档位找与 |score| 最近的档位（贪心最近邻）
	tiers := []float64{0, 0.25, 0.5, 0.75}
	best := tiers[0]
	bestDiff := math.Abs(s - tiers[0])
	for _, t := range tiers[1:] {
		if diff := math.Abs(s - t); diff < bestDiff {
			best = t
			bestDiff = diff
		}
	}
	// 还原原始符号（利好正分/利空负分），中性档 0 无符号
	score := best
	if ht.Score < 0 {
		score = -score
	}
	// 中性归零（放宽版）：LLM 判定方向为"中性" 且 无明确档位时归零，
	// 消除 fallback 遗留污染；有明确非中性分档的事件保留，避免误杀。
	neutral := strings.EqualFold(ht.Sentiment, "中性") || strings.EqualFold(ht.Direction, "中性")
	if neutral && best == 0 {
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
