// analyzer.go — Stage2 深度分析（analyzeDeep）与产业链事件展开、档位归一及中性归零等后置校正。
package newsagent

import (
	"fmt"
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
// （nil 占位，不做关键词兜底），调用方应把它们留在未归因队列供下一轮重试。
// （analyzeDeep is the Stage2 full analysis: LLM deep-analysis of screened news producing structured NewsEvents.
// On retry exhaustion the batch is dropped (nil, no keyword fallback), with post-processing for tier
// normalization, neutral zeroing and datetime fallback. It returns events for successes and failedItems for
// news whose LLM analysis failed (nil placeholder, no keyword fallback), which callers should keep in the
// unattributed queue for the next round.）
// English: analyzeDeep is the Stage2 full analysis: LLM deep-analysis of screened news producing structured NewsEvents. On retry exhaustion the batch is dropped (nil, no keyword fallback), with post-processing for tier normalization, neutral zeroing and datetime fallback. It returns events for successes and failedItems for news whose LLM analysis failed (nil placeholder, no keyword fallback), which callers should keep in the unattributed queue for the next round.
func (a *Agent) analyzeDeep(items []data.NewsItem) (events []NewsEvent, failedItems []data.NewsItem) {
	if len(items) == 0 {
		return nil, nil
	}

	// 抽取标题列表（附正文摘要），供 LLM 批量分析。
	// 摘要（≤80 字）给 LLM 提供"制裁/管制"等背景，避免仅凭标题误判而漏掉产业链归因。
	// English: extract the title list (with a body digest) for batch LLM analysis. The digest (<=80 runes) gives the LLM background such as sanctions/controls, avoiding misjudging on title alone and missing supply-chain attribution.
	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = titleWithDigest(item.Title, item.Content)
	}

	results, failedIdx, err := a.llmClient.AnalyzeHotTopicBatch(titles)
	if err != nil {
		// LLM 不可用/限流/耗尽：不再整批丢弃（丢信号），改为「规则兜底 + 低置信度中性」降级，
		// 保证流水线继续运转；同时打出明确降级告警（含原因与影响条数），便于运维发现 LLM 异常。
		// 降级事件照常返回给调用方参与后续流转，原批次仍作为 failedItems 留队，下轮用真实 LLM 重试。
		log.Printf("[newsagent][降级告警] Stage2 LLM批量分析失败, 整批 %d 条降级为低置信度中性(规则兜底)而非丢弃: %v", len(titles), err)
		degraded := a.ruleBasedDegrade(items, err)
		return degraded, items
	}

	// LLM 重试耗尽失败的新闻（nil 占位）：视为未归因，交调用方留队下一轮重试
	// English: news whose LLM retries were exhausted (nil placeholder) is treated as unattributed and handed to the caller to queue for the next round
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
		// English: skip empty results: invalid items for which the LLM gave no title
		if ht == nil || ht.Title == "" {
			continue
		}
		// 后置校正：档位归一 + 中性强制归零
		// English: post-processing: tier normalization + forced zeroing of neutral cases
		postProcess(ht)
		events = append(events, buildChainEvents(ht, items[i])...)
	}

	log.Printf("[newsagent] Stage2全量分析: %d 个事件, %d 条未归因留队重试", len(events), len(failedItems))
	return events, failedItems
}

// buildChainEvents 把一个 HotTopic 展开为 1~2 个 NewsEvent。
// English: buildChainEvents expands a HotTopic into 1-2 NewsEvents: differential upstream/downstream directions (e.g. sanction type: upstream bull / downstream bear) split into two directional events so each enters the watch pool and N-shape D1 scoring with the right sign; same direction merges into a single event.
// 差分（上游方向 ≠ 下游方向，如对抗制裁型上游利好/下游利空）拆为两个方向事件，
// 使上游/下游各自以正确方向进入监测池与 N 形 D1 评分；同向则合并为单事件。
// （buildChainEvents expands a HotTopic into 1-2 NewsEvents: differential upstream/downstream directions
// (e.g. sanction type: upstream bull / downstream bear) split into two events, same direction merges into one.）
func buildChainEvents(ht *llm.HotTopic, item data.NewsItem) []NewsEvent {
	// 兜底时间：新闻无发布时间时用当前时间
	// English: fallback time: use the current time when the news has no publish time
	dt := item.Datetime
	if dt == "" {
		dt = time.Now().Format("2006-01-02 15:04:05")
	}
	// 来源自带的个股标签（如财联社 stock_list）并入归因，交 cleaner 清洗
	// English: the source's own stock tags (e.g. Cailian Press stock_list) are merged into the attribution and cleaned by the cleaner
	sourceStocks := mergeStr(ht.RelatedStocks, item.Stocks)

	// 组装基础新闻事件：字段缺省补默认（Region=国内、Relation=不涉及）。
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
		// English: divergence: upstream and downstream each become an independent directional event. The upstream event prefers upstream_stocks, falling back to the full related_stocks when missing (models often fold upstream contractors into it); the downstream event uses only downstream_stocks, left empty when missing (sector propagation injects constituents), avoiding upstream-bull stocks polluting the downstream-bear event.
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
	// English: same direction: merge into a single event (upstream/downstream sectors and stocks merged in)
	allStocks := mergeStr(sourceStocks, mergeStr(ht.UpstreamStocks, ht.DownstreamStocks))
	allSectors := mergeStr(ht.Sectors, mergeStr(ht.UpstreamSectors, ht.DownstreamSectors))
	return []NewsEvent{buildChainEvent(base, ht, ht.Sectors, allStocks, allSectors, chainDirection(ht), "全链")}
}

// titleWithDigest 把正文摘要（≤80 字）拼进标题，供 LLM 获取制裁/管制等背景。
// English: titleWithDigest appends a <=80-run digest of the body into the title for LLM context like sanctions/controls.
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
// English: buildChainEvent assembles a single NewsEvent per supply-chain stage (upstream/downstream/full). chainSectors are the stage sectors, falling back to primarySectors/ht.Sectors when empty.
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
// English: chainDirection returns the merged event's direction, preferring ht.Direction then deriving from Score.
// （chainDirection returns the merged event's direction, preferring ht.Direction then deriving from Score.）
func chainDirection(ht *llm.HotTopic) string {
	if ht.Direction != "" && ht.Direction != "中性" {
		return ht.Direction
	}
	return directionFromScore(ht.Score)
}

// chainScore 按环节方向给事件赋带符号强度分（绝对值取自 ht.Score 归一卷）。
// English: chainScore assigns a signed strength score per stage direction from the normalized |ht.Score|.
// （chainScore assigns a signed strength score per stage direction from the normalized |ht.Score|.）
func chainScore(score float64, dir string) float64 {
	abs := score
	if abs < 0 {
		abs = -abs
	}
	// §R4-2 修复（前轮 P1-F）：删除"弱分<0.25 硬抬到 0.5"的保底分支——
	// 该抬零恰好越过引擎 filterThreshold(0.50) 的有效事件线，使 LLM 判弱的新闻
	// 照样进板块验真/打分池/占用 D1 配额，击穿自己的漏斗。弱分保持原值自然落漏斗外。
	// English: §R4-2 fix (round-3 P1-F) — drop the floor that bumped weak scores (<0.25) up to 0.5,
	// which breached the engine's 0.50 validity threshold; weak events now fall out of the funnel naturally.
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
// English: mergeStr merges and dedupes string slices.
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
// English: containsStr reports whether the slice contains s.
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
//   - 中性归零（放宽）：仅当方向/情绪为"中性" 且 强度档位为 0 时才归零；
//     对 LLM 明确给出的非零方向分数保留量化档，避免弱事件被误清空。
//
// （postProcess normalizes scores to the nearest tier in {0,0.25,0.5,0.75} and zeroes neutral cases only when
// the tier is 0, preserving explicit non-zero directional scores.）
// English: postProcess normalizes scores to the nearest tier in {0,0.25,0.5,0.75} and zeroes neutral cases only when the tier is 0, preserving explicit non-zero directional scores.
func postProcess(ht *llm.HotTopic) {
	// 取绝对值并截断上限，只处理强度档位
	// English: take the absolute value and cap the ceiling; only the strength tier is processed
	s := ht.Score
	if s < 0 {
		s = -s
	}
	if s > 0.75 {
		s = 0.75
	}
	// 遍历四个档位找与 |score| 最近的档位（贪心最近邻）
	// English: scan the four tiers for the one nearest |score| (greedy nearest-neighbor)
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
	// English: restore the original sign (bullish positive / bearish negative); the neutral tier 0 has no sign
	score := best
	if ht.Score < 0 {
		score = -score
	}
	// 中性归零（放宽版）：LLM 判定方向为"中性" 且 无明确档位时归零，
	// English: neutral zeroing (relaxed): zero the score only when the LLM judged the direction as "neutral" and there is no explicit tier, avoiding weak/directionless events carrying a placeholder score; events with an explicit non-neutral tier are kept to avoid false kills.
	// 避免弱/无方向事件带占位分；有明确非中性分档的事件保留，避免误杀。
	neutral := strings.EqualFold(ht.Sentiment, "中性") || strings.EqualFold(ht.Direction, "中性")
	if neutral && best == 0 {
		score = 0
	}
	ht.Score = score
}

// directionFromScore 由带符号 Score 推导展示用方向：>0 利好，<0 利空，==0 中性。
// English: directionFromScore derives a display direction from the signed Score: >0 bull, <0 bear, ==0 neutral.
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

// ruleBasedDegrade LLM 不可用时的规则兜底：基于标题/正文关键词做简易利好/利空判定，
// 统一标记为「低置信度中性」事件（Direction=中性、Score=0），保证流水线不中断、事件不整批丢失。
// 降级事件仅作为占位保留在 /api/news 可见（中性不触发信号），真实归因交由下轮 LLM 重试。
// （ruleBasedDegrade is the rule-based fallback when the LLM is unavailable: a simple keyword check
// yields a low-confidence NEUTRAL event (Direction=neutral, Score=0) so the pipeline keeps running
// and no events are dropped wholesale; real attribution is retried next round via the LLM.）
func (a *Agent) ruleBasedDegrade(items []data.NewsItem, cause error) []NewsEvent {
	// 利好/利空关键词（命中其一即给出方向提示，但事件本身仍统一标记中性，避免 LLM 缺失时给出错误方向信号）
	bullKW := []string{"利好", "增长", "中标", "签约", "增持", "回购", "扩产", "涨价", "突破", "获批", "订单", "业绩预增", "上调", "合作", "超预期"}
	bearKW := []string{"利空", "减持", "下跌", "亏损", "处罚", "退市", "调查", "暴雷", "下滑", "业绩预减", "诉讼", "查封", "风险警示", "下修"}

	events := make([]NewsEvent, 0, len(items))
	for _, item := range items {
		text := item.Title + " " + item.Content
		keywordDir := "中性"
		var hit []string
		for _, k := range bullKW {
			if strings.Contains(text, k) {
				hit = append(hit, "利好:"+k)
				keywordDir = "利好"
				break
			}
		}
		if keywordDir == "中性" {
			for _, k := range bearKW {
				if strings.Contains(text, k) {
					hit = append(hit, "利空:"+k)
					keywordDir = "利空"
					break
				}
			}
		}

		dt := item.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		// 降级：统一低置信度中性，不触发信号，仅保留可见性；关键词命中情况写入 Reason 供排查。
		ev := NewsEvent{
			Title:      item.Title,
			Content:    item.Content,
			Datetime:   dt,
			Source:     item.Source,
			IsMaterial: true,
			Level:      "一般", // 降级事件无可靠级别，标为一般，避免错误扩散到个股/板块候选
			Direction:  "中性", // 降级：低置信度中性，杜绝 LLM 缺失时给出错误方向
			Score:      0,    // 中性占位，不触发任何信号，仅保留在 /api/news 可见
			EventType:  "降级兜底",
			Reason:     fmt.Sprintf("[降级·规则兜底] LLM不可用(%v); 关键词命中=%v; 原判定方向=%s", cause, hit, keywordDir),
		}
		events = append(events, ev)
	}
	return events
}
