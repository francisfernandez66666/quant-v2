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
// 返回值：events 为成功产出的事件；failedItems 为 LLM 重试耗尽、未完成归因的新闻
// （被兜底占位），调用方应把它们留在未归因队列供下一轮重试。
// （analyzeDeep is the Stage2 full analysis: LLM deep-analysis of screened news producing structured NewsEvents.
// On retry exhaustion the batch is dropped (nil, no keyword fallback), with post-processing for tier
// normalization, neutral zeroing and datetime fallback. It returns events for successes and failedItems for
// news whose LLM analysis failed (padded by fallback), which callers should keep in the unattributed queue.）
func (a *Agent) analyzeDeep(items []data.NewsItem) (events []NewsEvent, failedItems []data.NewsItem) {
	if len(items) == 0 {
		return nil, nil
	}

	// 抽取标题列表（附正文摘要），供 LLM 批量分析。
	// 摘要（≤80 字）给 LLM 提供"制裁/管制"等背景，避免仅凭标题误判而漏掉产业链归因。
	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = titleWithDigest(item.Title, item.Content)
	}

	results, failedIdx, err := a.llmClient.AnalyzeHotTopicBatch(titles)
	if err != nil {
		log.Printf("[newsagent] Stage2 LLM批量分析失败, 该批%d条丢弃: %v", len(titles), err)
		return nil, items
	}

	// LLM 重试耗尽被兜底占位的新闻：视为未归因，交调用方留队下一轮重试
	failedSet := make(map[int]bool, len(failedIdx))
	for _, f := range failedIdx {
		failedSet[f] = true
	}

	events = make([]NewsEvent, 0, len(results))
	for i, ht := range results {
		if failedSet[i] {
			failedItems = append(failedItems, items[i])
			continue
		}
		// 跳过空结果：LLM 未给出标题的无效项
		if ht == nil || ht.Title == "" {
			continue
		}
		// 后置校正：档位归一 + 中性强制归零
		postProcess(ht)
		events = append(events, buildChainEvents(ht, items[i])...)
	}

	log.Printf("[newsagent] Stage2全量分析: %d 个事件, %d 条未归因留队重试", len(events), len(failedItems))
	return events, failedItems
}

// buildChainEvents 把一个 HotTopic 展开为 1~2 个 NewsEvent。
// 差分（上游方向 ≠ 下游方向，如对抗制裁型上游利好/下游利空）拆为两个方向事件，
// 使上游/下游各自以正确方向进入监测池与 N 形 D1 评分；同向则合并为单事件。
// （buildChainEvents expands a HotTopic into 1-2 NewsEvents: differential upstream/downstream directions
// (e.g. sanction type: upstream bull / downstream bear) split into two events, same direction merges into one.）
func buildChainEvents(ht *llm.HotTopic, item data.NewsItem) []NewsEvent {
	// 兜底时间：新闻无发布时间时用当前时间
	dt := item.Datetime
	if dt == "" {
		dt = time.Now().Format("2006-01-02 15:04:05")
	}
	// 来源自带的个股标签（如财联社 stock_list）并入归因，交 cleaner 清洗
	sourceStocks := mergeStr(ht.RelatedStocks, item.Stocks)

	base := NewsEvent{
		Title:       item.Title,
		Content:     item.Content,
		Datetime:    dt,
		Source:      item.Source,
		IsMaterial:  true,
		Level:       ht.Level,
		ImpactLevel: ht.ImpactLevel,
		EventType:   ht.EventType,
		Urgency:     ht.Urgency,
		Reason:      ht.Reason,
		Region:      ht.Region,
		Relation:    ht.Relation,
	}
	if base.Region == "" {
		base.Region = "国内"
	}
	if base.Relation == "" {
		base.Relation = "不涉及"
	}

	if ht.UpstreamDirection != "" && ht.DownstreamDirection != "" && ht.UpstreamDirection != ht.DownstreamDirection {
		// 分化：上游与下游各自独立方向事件。
		// 上游事件优先用 upstream_stocks，缺失时回落全量 related_stocks（模型常把上游承包商并入其中）；
		// 下游事件只用 downstream_stocks，缺失时留空（交给板块传播注入成分股），
		// 避免把上游利好个股污染进下游利空事件。
		upStocks := ht.UpstreamStocks
		if len(upStocks) == 0 {
			upStocks = sourceStocks
		}
		dnStocks := ht.DownstreamStocks
		return []NewsEvent{
			buildChainEvent(base, ht, ht.Sectors, upStocks, ht.UpstreamSectors, ht.UpstreamDirection, "上游"),
			buildChainEvent(base, ht, ht.Sectors, dnStocks, ht.DownstreamSectors, ht.DownstreamDirection, "下游"),
		}
	}
	// 同向：合并为单事件（上/下游板块与个股并入）
	allStocks := mergeStr(sourceStocks, mergeStr(ht.UpstreamStocks, ht.DownstreamStocks))
	allSectors := mergeStr(ht.Sectors, mergeStr(ht.UpstreamSectors, ht.DownstreamSectors))
	return []NewsEvent{buildChainEvent(base, ht, ht.Sectors, allStocks, allSectors, chainDirection(ht), "全链")}
}

// titleWithDigest 把正文摘要（≤80 字）拼进标题，供 LLM 获取制裁/管制等背景。
// （titleWithDigest appends a ≤80-run digest of the body into the title for LLM context like sanctions/controls.）
func titleWithDigest(title, content string) string {
	if content == "" {
		return title
	}
	r := []rune(content)
	if len(r) > 80 {
		r = r[:80]
	}
	return title + "\n【摘要】" + string(r)
}

// buildChainEvent 按产业链环节组装单个 NewsEvent（上游/下游/全链环节）。
// chainSectors 为环节板块（上游/下游），为空时回退 primarySectors/ht.Sectors。
// （buildChainEvent assembles a single NewsEvent per supply-chain stage (upstream/downstream/full).
// chainSectors override primarySectors/ht.Sectors when non-empty.）
func buildChainEvent(base NewsEvent, ht *llm.HotTopic, primarySectors, related, chainSectors []string, direction, chain string) NewsEvent {
	ev := base
	switch {
	case len(chainSectors) > 0:
		ev.Sectors = chainSectors
	case len(primarySectors) > 0:
		ev.Sectors = primarySectors
	default:
		ev.Sectors = ht.Sectors
	}
	if len(related) > 0 {
		ev.RelatedStocks = related
	}
	ev.Direction = direction
	ev.Score = chainScore(ht.Score, direction)
	if chain != "" && ht.Reason != "" {
		ev.Reason = ht.Reason
		if !strings.Contains(ev.Reason, chain) {
			ev.Reason = "[" + chain + "传导] " + ev.Reason
		}
	}
	return ev
}

// chainDirection 同向合并事件的统一方向：优先用 ht.Direction，否则由 Score 推导。
// （chainDirection returns the merged event's direction, preferring ht.Direction then deriving from Score.）
func chainDirection(ht *llm.HotTopic) string {
	if ht.Direction != "" && ht.Direction != "中性" {
		return ht.Direction
	}
	return directionFromScore(ht.Score)
}

// chainScore 按环节方向给事件赋带符号强度分（绝对值取自 ht.Score 归一卷）。
// （chainScore assigns a signed strength score per stage direction from the normalized |ht.Score|.）
func chainScore(score float64, dir string) float64 {
	abs := score
	if abs < 0 {
		abs = -abs
	}
	if abs < 0.25 {
		abs = 0.5
	}
	switch dir {
	case "利好":
		return abs
	case "利空":
		return -abs
	default:
		return 0
	}
}

// mergeStr 合并去重字符串切片。（mergeStr merges and dedupes string slices.）
func mergeStr(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// containsStr 判断字符串切片中是否包含指定元素。（containsStr reports whether the slice contains s.）
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
//
// （postProcess normalizes scores to the nearest tier in {0,0.25,0.5,0.75} and zeroes neutral cases only when
// the tier is 0, preserving explicit non-zero directional scores.）
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
// （directionFromScore derives a display direction from the signed Score: positive bull, negative bear, zero neutral.）
func directionFromScore(score float64) string {
	if score > 0 {
		return "利好"
	}
	if score < 0 {
		return "利空"
	}
	return "中性"
}