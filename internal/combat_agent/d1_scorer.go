// Package combat_agent 提供操盘战斗 Agent 的核心评分逻辑。
// D1Scorer 负责对个股进行 D1 级别的事件驱动评分，依赖 LLM 分析新闻事件与行情数据。
// 评分维度（含优先级）：负面过滤(blocked) > 顶级影响 > 间接影响 > 中等影响 > 低影响，
// 输出 0~40 满分制分数（对应 N 形 D1 事件维度）并附 LLM 分析理由。
// D1 分独立于"板块利好/利空事件分"（HotTopic.Score / SectorHot.Score，0~1，仅作评分上下文），
// 由 LLM 按 40 分制独立核定，避免两者混用。
//
// 评分机制：
//   - 批量评分：按 llmBatchSize 分批并发调用 LLM
//   - 失败重试：支持轮询重试（§S5 默认 2 次含首次=1 次重试），指数抖动并封顶
//   - 无事件归零：无实质事件的个股 D1 强制归 0
//   - 负面过滤：命中负面事件的个股标记 Blocked，D1=0
package combat_agent

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy_engine"
)

// D1Score 表示单只个股的 D1 事件评分结果。
// Score 范围 0~40（对应 N 形 D1 维度满分 40），Blocked 表示被负面过滤拦截，Reason 为 LLM 分析理由。
// RetryPending 表示本轮 LLM 失败（未拿到有效评分），分数 0 且待重试队列下轮重新调 LLM。
//
// 字段说明：
//   - Code: 股票代码
//   - Score: 评分值，0~40，越高越值得关注
//   - Blocked: 是否被负面过滤拦截（利空事件命中）
//   - Reason: LLM 给出的评分分析理由
//   - RetryPending: LLM 失败待重试（分数 0，入重试队列，下轮重新调 LLM）
type D1Score struct {
	Code         string  `json:"code"`          // 股票代码
	Score        float64 `json:"score"`         // 评分值，0~40，越高越值得关注
	Blocked      bool    `json:"blocked"`       // 是否被负面过滤拦截（利空事件命中）
	Reason       string  `json:"reason"`        // LLM 给出的评分分析理由
	RetryPending bool    `json:"retry_pending"` // LLM 失败待重试（分数 0，入重试队列，下轮重新调 LLM）
}

// D1Scorer 批量个股 D1 评分器。
// 收拢到 combat_agent，LLM 参考 events_leftside.yaml 规则评分。
// 非并发安全，建议由 Engine 在独立 goroutine 中单实例调用。
//
// 字段说明：
//   - llmClient: LLM 客户端，用于调用大模型进行 D1 评分
//   - yamlContent: events_leftside.yaml 原始内容，作为 LLM prompt 参考
//   - maxAttempts: D1 LLM 调用轮询重试次数（含首次），默认 2（§S5）
//   - retryBackoff: 相邻两次重试的基础间隔
//   - sectorEvents: 代码→所属板块事件标题映射
//   - maxTokens: D1 评分 LLM 单次调用的推理长度上限（§信号速度 S3），默认 2048
type D1Scorer struct {
	llmClient    *llm.Client       // LLM 客户端，用于调用大模型进行 D1 评分
	yamlContent  string            // events_leftside.yaml 原始内容，作为 LLM prompt 参考
	maxAttempts  int               // D1 LLM 调用轮询重试次数（含首次），默认 2（§S5）；0/负回退默认
	retryBackoff time.Duration     // 相邻两次重试的基础间隔
	sectorEvents map[string]string // 代码→所属板块事件标题（板块事件传导 D1：个股不在新闻点名里也能拿到板块利好作为评分上下文）
	maxTokens    int               // D1 评分 LLM 单次调用推理长度上限（§S3），默认 2048；0/负回退默认
}

// defaultD1MaxTokens D1 评分默认推理长度上限（§信号速度 S3）。
// 与 llm.defaultD1MaxTokens 保持一致；D1 输出结构化 JSON，无需超长思维链。
const defaultD1MaxTokens = 2048

// defaultMaxAttempts 默认 D1 LLM 轮询重试次数（含首次）。
// §信号速度 S5：5 → 2（含首次=1 次重试）。当轮不反复死磕，失败置 RetryPending 进下轮重试队列，
// 配合增量 D1（只评新事件/缺分股）整体轮次显著提速；max_retry_times 仍可前端热改调高。
// English: default D1 LLM total attempts including the first (§speed S5: 5 → 2, i.e. one retry). The round
// gives up fast, marking failures RetryPending into the next round's queue; combined with incremental D1
// (only new-event/missing codes re-scored) rounds are much faster. max_retry_times remains UI-adjustable.
const defaultMaxAttempts = 2

// NewD1Scorer 创建 D1Scorer 实例。
// 参数：
//   - llmClient: LLM 客户端，用于调用大模型进行评分
//   - yamlContent: events_leftside.yaml 的原始内容，作为评分规则的参考上下文
//
// 返回值：
//   - 初始化后的 D1Scorer 指针
func NewD1Scorer(llmClient *llm.Client, yamlContent string) *D1Scorer {
	return &D1Scorer{
		llmClient:    llmClient,
		yamlContent:  yamlContent,
		maxAttempts:  defaultMaxAttempts,
		retryBackoff: 2 * time.Second,
		sectorEvents: make(map[string]string),
		maxTokens:    defaultD1MaxTokens,
	}
}

// SetSectorEvents 设置"代码→所属板块事件标题"映射，用于把板块级别事件传导到个股的 D1 评分上下文。
// 个股即使不在新闻点名的 RelatedStocks 里，只要所属热点板块有正向事件，也能获得该事件标题供 LLM 合理打分。
//
// 参数：
//   - m: 代码→板块事件标题映射
func (ds *D1Scorer) SetSectorEvents(m map[string]string) {
	ds.sectorEvents = m
}

// SetMaxRetries 设置 D1 评分 LLM 调用的轮询重试次数（含首次）。
// n<=0 时回退默认 defaultMaxAttempts。
//
// 参数：
//   - n: 重试次数
//
// 返回值：
//   - 设置的生效值
func (ds *D1Scorer) SetMaxRetries(n int) int {
	if n <= 0 {
		ds.maxAttempts = defaultMaxAttempts
	} else {
		ds.maxAttempts = n
	}
	return ds.maxAttempts
}

// SetMaxTokens 设置 D1 评分 LLM 单次调用的推理长度上限（§信号速度 S3）。
// n<=0 时回退默认 defaultD1MaxTokens（2048）。
//
// 参数：
//   - n: max_tokens 上限
//
// 返回值：
//   - 设置的生效值
func (ds *D1Scorer) SetMaxTokens(n int) int {
	if n <= 0 {
		ds.maxTokens = defaultD1MaxTokens
	} else {
		ds.maxTokens = n
	}
	return ds.maxTokens
}

// d1SystemPrompt 是 D1 评分的系统级提示词，定义评分优先级规则和输出格式。
// LLM 根据该提示词对个股关联事件进行分级打分（负面过滤/顶级影响/间接影响/中等影响/低影响）。
// 打分采用 0~40 满分制（对应 N 形 D1 维度：事件驱动硬闸，满分 40，信号需 D1>0）。
//
// 评分优先级：
//  1. 负面过滤(negative_filter): score=0, blocked=true
//  2. 顶级影响(top_impact): score=16~40
//  3. 间接影响(indirect): score=12~24
//  4. 中等影响(medium_impact): score=8~20
//  5. 低影响(low_impact): score=0~8
var d1SystemPrompt = `你是一个A股个股D1事件评分专家。对每只个股基于关联事件进行D1评分。

评分采用 0~40 满分制，对应 N 形策略的 D1 事件驱动维度（满分 40，信号需 D1>0 且 总分≥60）。
分数越高代表该股的事件驱动越强、越值得关注；请给出明确的正向分值，避免一律给极低分。

按优先级：
1. 负面过滤(negative_filter): score=0, blocked=true — 立案/减持/质押/解禁/被调查等
2. 顶级影响(top_impact): score=16~40 — 政策/技术突破/并购重组/龙头大额回购等
3. 间接影响(indirect): score=12~24 — 板块情绪传导/上游下游联动等
4. 中等影响(medium_impact): score=8~20 — 业绩/回购/涨价/订单等
5. 低影响(low_impact): score=0~8 — 普通公告/机构调研等

注意：
- 若给到的"关联事件"是热点板块级别事件（光模块/算力/AI/半导体等产业链利好），
  应按该板块事件的利好强度给该股一个与其绑定的合理分值，而非 0 或极低的 0~4 分。
- 仅当确实无任何利好事件、且无板块关联时才给低分。
- 只有命中负面过滤（立案/减持/质押/解禁等明确利空）时才 blocked=true 并给 0 分。

详细规则见下方参考。

输出JSON数组：
[
  {"code":"600519","score":0~40,"blocked":true/false,"reason":"评分理由"}
]
只输出JSON数组，不要多余文字。`

// llmBatchSize D1 单次 LLM 调用承载的最大个股条数。
// 与 classifier.go 的 llmBatchSize 保持一致：超大批次会让推理模型 prompt 过长、
// 输出被截断漏项（观察：55只漏38%、49只漏16%、17只不漏），故按此大小分批调用。
const llmBatchSize = 10

// batchBounds 将 n 个元素按 size 分块，返回 [start,end) 区间列表。
// 参数：
//   - n: 元素总数
//   - size: 每块大小
//
// 返回值：
//   - [start,end) 区间列表
func batchBounds(n, size int) [][2]int {
	if size <= 0 {
		size = 1
	}
	var bounds [][2]int
	for start := 0; start < n; start += size {
		end := start + size
		if end > n {
			end = n
		}
		bounds = append(bounds, [2]int{start, end})
	}
	return bounds
}

// BatchScore 对一组个股进行批量 D1 评分。
// 按 llmBatchSize 分批 → 每批独立"构建 prompt → 调用 LLM（轮询重试）→ 解析 JSON →
// 标记失败个股" → 合并结果返回。
//
// 失败策略（全局原则：LLM 失败不靠兜底，全部走 LLM 重试队列）：
//   - 本轮 LLM 明确给出的分数一律保留
//   - 某批失败（重试全败/解析失败）或漏掉某只个股时，该批/该股不伪造分数、
//     不回退上一轮，标记 RetryPending=true 且 Score=0（Reason 注明"待重试"），
//     由调用方把这类个股并入重试队列，下一轮重新调 LLM
//
// 参数：
//   - codes: 待评分的个股代码列表
//   - events: 当前周期的新闻事件列表，用于查找个股关联事件
//   - marketData: 个股行情数据映射，key 为股票代码
//
// 返回值：
//   - map[string]D1Score: key 为股票代码，value 为 D1Score 评分结果
func (ds *D1Scorer) BatchScore(codes []string, events []newsagent.NewsEvent, marketData map[string]*strategy_engine.StockMarketData) map[string]D1Score {
	t0 := time.Now()
	result := make(map[string]D1Score, len(codes))

	// LLM 客户端未配置或没有待评分个股 → 全量默认 0 分
	// English: no LLM client or no codes → default everything to 0.
	if ds.llmClient == nil || len(codes) == 0 {
		for _, code := range codes {
			result[code] = D1Score{Code: code, Score: 0, Blocked: false, Reason: "LLM未配置，默认0分"}
		}
		log.Printf("[D1Scorer] LLM未配置，%d只个股默认0分", len(codes))
		return result
	}

	maxAttempts := ds.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	// 按 llmBatchSize 分批**并发**调用：每批独立 goroutine，用独立 map 合并，
	// 配合 LLM 客户端多 key 轮询，各批请求落到不同 API key，突破单 key 限流、显著提速。
	// 并发度取自 LLM 客户端 BatchConcurrency（前端可热改），默认 4。
	// English: chunks of llmBatchSize are scored concurrently (one goroutine per chunk, each
	// producing its own map merged afterwards). Combined with the client's multi-key round-robin,
	// concurrent chunks spread across different API keys, beating single-key rate limits.
	concurrency := 1
	if ds.llmClient != nil {
		if bc := ds.llmClient.BatchConcurrency(); bc > 0 {
			concurrency = bc
		}
	}
	bounds := batchBounds(len(codes), llmBatchSize)
	chunkResults := make([]map[string]D1Score, len(bounds))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, b := range bounds {
		chunk := codes[b[0]:b[1]]
		log.Printf("[D1Scorer] 分批评分 %d~%d / %d 只", b[0]+1, b[1], len(codes))
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, chunk []string) {
			defer wg.Done()
			defer func() { <-sem }()
			chunkResults[i] = ds.scoreChunk(chunk, events, marketData, maxAttempts)
		}(i, chunk)
	}
	wg.Wait()

	// 合并各批独立结果（无锁：每批写自己专属的 map 槽位）
	for _, m := range chunkResults {
		for code, sc := range m {
			result[code] = sc
		}
	}

	log.Printf("[D1Scorer] 批量评分完成: %d/%d只, 耗时 %v", len(result), len(codes), time.Since(t0))
	return result
}

// scoreChunk 对单批个股（≤ llmBatchSize 只）构建 prompt 并调用 LLM 评分，返回本批独立结果 map
// （不写共享 map，天然并发安全）。轮询重试（最多 maxAttempts 次、间隔按指数抖动并封顶，
// 防重要 D1 评分随调用失败丢失）。
//
// 失败策略（全局原则：LLM 失败不靠兜底，全部走 LLM 重试队列）：
// 整批失败/解析失败/漏项时不再回退上一轮评分或按理由兜底 0 分，而是把该批/该股标记为
// RetryPending=true（Score=0, Reason 注明"待重试"），由调用方并入重试队列下轮重新调 LLM。
//
// 参数：
//   - codes: 单批个股代码列表
//   - events: 新闻事件列表
//   - marketData: 行情数据映射
//   - maxAttempts: 最大重试次数
//
// 返回值：
//   - 本批独立结果 map
func (ds *D1Scorer) scoreChunk(codes []string, events []newsagent.NewsEvent, marketData map[string]*strategy_engine.StockMarketData, maxAttempts int) map[string]D1Score {
	result := make(map[string]D1Score, len(codes))
	// 构建用户prompt：列出每只个股及其关联事件、行情数据
	// English: build the user prompt listing each stock with its events and market data.
	var sb strings.Builder
	sb.WriteString("请对以下个股进行D1评分，参考events_leftside.yaml分级规则：\n\n")
	// 附上评分规则原文，让 LLM 按项目规则打分
	// English: attach the raw rules so the LLM grades per the project's rules.
	if ds.yamlContent != "" {
		sb.WriteString("参考规则:\n")
		sb.WriteString(ds.yamlContent)
		sb.WriteString("\n\n")
	}

	// 无实质事件集合：既无个股关联新闻、也无板块正向事件的个股，D1 一律归 0
	// （不允许 LLM 给"无特定事件"占位低分，否则会当作有效 D1 触发左侧买入并固化提醒）。
	// English: stocks with no substantive event (no individual news match and no bullish sector event)
	// are forced to D1=0 so a placeholder low score can't act as a valid D1 that fires/gets pinned.
	noEvent := make(map[string]bool, len(codes))
	for i, code := range codes {
		sb.WriteString(fmt.Sprintf("%d. 代码: %s\n", i+1, code))
		md := marketData[code]
		// 有行情数据则补充名称与价格/涨跌幅，辅助 LLM 判断
		// English: attach name and price/change when market data exists to aid the LLM.
		if md != nil {
			if md.Name != "" {
				sb.WriteString(fmt.Sprintf("   名称: %s\n", md.Name))
			}
			if md.Price > 0 {
				sb.WriteString(fmt.Sprintf("   价格: %.2f  涨跌幅: %.2f%%\n", md.Price, md.ChangePct))
			}
		}
		// 事件描述：先按 代码/名称 匹配关联新闻标题；未命中但该股所属热点板块有正向事件时，
		// 注入板块事件标题作为 D1 评分上下文，避免"未点名个股→无特定事件→LLM 给 0.1"。
		// 但无任何事件关联的个股标记为 noEvent，评分后强制归 0。
		// English: attach the linked event title — first by matching code/name against news events; when no
		// individual event matches but the stock belongs to a hot sector with a bullish event, inject the
		// sector event title so the LLM grades it fairly instead of "no specific event → ~0.1". Codes with
		// no event link at all are flagged noEvent and forced to 0 afterwards.
		eventDesc := findEventForCode(code, md, events)
		if eventDesc == "" {
			eventDesc = ds.sectorEvents[code]
		}
		if eventDesc != "" {
			sb.WriteString(fmt.Sprintf("   关联事件: %s\n", eventDesc))
		} else {
			noEvent[code] = true
			sb.WriteString("   关联事件: 无特定事件\n")
		}
		sb.WriteString("\n")
	}

	prompt := sb.String()
	log.Printf("[D1Scorer] 单批评分 %d只个股, prompt=%d字符", len(codes), len(prompt))

	// 调用LLM（系统提示词 d1SystemPrompt 固定，用户提示词携带个股与规则）。
	// §信号速度 S3：走 ChatD1 显式 max_tokens（默认 2048，配置 rules.llm.d1_max_tokens 可调），
	// 限制推理长度降低单股评分耗时（原走 Chat → nonStreamChat 硬编码 4096）。
	// English: §speed S3 — use ChatD1 with an explicit max_tokens cap (default 2048, adjustable via
	// rules.llm.d1_max_tokens) to cut per-stock scoring latency (previously Chat → nonStreamChat @4096).
	var resp string
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err = ds.llmClient.ChatD1(d1SystemPrompt, prompt, ds.maxTokens)
		if err == nil {
			break
		}
		if attempt < maxAttempts {
			backoff := ds.retryBackoff * time.Duration(attempt)
			if cap := 9 * time.Second; backoff > cap {
				backoff = cap
			}
			log.Printf("[D1Scorer] LLM调用失败(第%d次,还剩%d次), %v后重试: %v",
				attempt, maxAttempts-attempt, backoff, err)
			time.Sleep(backoff)
		}
	}
	if err != nil {
		log.Printf("[D1Scorer] LLM调用轮询重试%d次仍失败: %v, 本批%d只标记待重试(入重试队列)", maxAttempts, err, len(codes))
		ds.markRetryPending(result, codes, "LLM失败")
		return result
	}

	// 清洗 LLM 输出（去掉 markdown 代码块与多余文字，仅保留 JSON 数组）
	resp = cleanJSON(resp)

	var raw []D1Score
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		log.Printf("[D1Scorer] JSON解析失败→本批 %d 只标记待重试(入重试队列): %v (首300字符: %q)", len(codes), err, resp[:minInt(len(resp), 300)])
		ds.markRetryPending(result, codes, "解析失败")
		return result
	}

	for _, r := range raw {
		if r.Code != "" {
			// 分数越界防护：裁剪到 0~40
			if r.Score > 40 {
				r.Score = 40
			}
			if r.Score < 0 {
				r.Score = 0
			}
			result[r.Code] = r
			log.Printf("[D1Scorer] %s → score=%.2f blocked=%v reason=%s", r.Code, r.Score, r.Blocked, r.Reason)
		}
	}

	// 补全LLM未返回的个股：标记 RetryPending 待重试（不回退上一轮、不归 0 兜底），
	// 由调用方并入重试队列下轮重新调 LLM。
	// English: stocks the LLM didn't return are marked RetryPending for the next round's LLM call —
	// never padded with prior-round scores or a plain 0.
	var missed []string
	for _, code := range codes {
		if _, ok := result[code]; !ok {
			missed = append(missed, code)
		}
	}
	if len(missed) > 0 {
		ds.markRetryPending(result, missed, "LLM未返回")
	}

	// 无实质事件（无个股新闻、无板块正向事件）的个股 D1 强制归 0：
	// 不允许 LLM 给"无特定事件"占位低分充当有效 D1。同时清除可能残留的占位理由，
	// 避免该股凭 0 分之上的一点点占位分被当作有效 D1 触发买入/固化提醒。
	// English: force D1=0 for stocks with no substantive event, so a placeholder low score can never act
	// as a valid D1. This closes the "无特定事件 → LLM 给 0.1 → 触发左侧买入并固化" leak.
	for code := range noEvent {
		if r, ok := result[code]; ok {
			if r.Score > 0 {
				log.Printf("[D1Scorer] %s 无实质事件 → D1强制归0 (原score=%.2f reason=%s)", code, r.Score, r.Reason)
			}
			r.Score = 0
			r.Blocked = false
			r.Reason = "无特定事件，D1归0"
			result[code] = r
		}
	}
	return result
}

// markRetryPending 对 LLM 评分失败的个股标记 RetryPending（Score=0, Reason 注明"待重试"），
// 由调用方把该股并入 LLM 重试队列，下一轮重新调 LLM；不伪造分数、不回退上一轮。
//
// 参数：
//   - result: 结果 map，将被原地修改
//   - codes: 需要标记的股票代码列表
//   - reason: 失败原因说明
func (ds *D1Scorer) markRetryPending(result map[string]D1Score, codes []string, reason string) {
	for _, code := range codes {
		result[code] = D1Score{Code: code, Score: 0, Blocked: false, Reason: reason + "待重试", RetryPending: true}
		log.Printf("[D1Scorer] %s %s, 标记待重试(Score=0)", code, reason)
	}
}

// findEventForCode 从 events 中查找个股关联事件描述。
// 遍历所有事件的 RelatedStocks 与 CleanedStocks 字段，通过子串匹配找到对应事件标题。
//
// 参数：
//   - code: 股票代码
//   - md: 个股行情数据（提供股票名称，板块级新闻常只带"名称"不带代码，须用名称兜底匹配）
//   - events: 新闻事件列表
//
// 返回值：
//   - 匹配到的事件标题，未匹配则返回空字符串
func findEventForCode(code string, md *strategy_engine.StockMarketData, events []newsagent.NewsEvent) string {
	for _, ev := range events {
		for _, s := range ev.RelatedStocks {
			if stockMatch(s, code, md) {
				return ev.Title
			}
		}
		for _, s := range ev.CleanedStocks {
			if stockMatch(s, code, md) {
				return ev.Title
			}
		}
	}
	return ""
}

// EventSignature 计算个股 D1 评分上下文的稳定签名：命中该股的新闻事件（Datetime+Title 排序拼接）
// + 板块事件标题。签名未变 → 引擎复用当日已有评分，避免每轮全池重调 LLM（§信号速度 S1）。
// 与 findEventForCode 同源匹配逻辑（RelatedStocks/CleanedStocks → stockMatch），保证"事件变化"判定
// 与评分视角一致；重复事件（同时间+标题）去重，板块事件变化同样触发重评。
// English: EventSignature computes a stable signature of a stock's D1 context — the sorted
// "Datetime|Title" of news events matching the stock, plus its sector event title. When the signature
// is unchanged the engine reuses the same-day score instead of re-calling the LLM each round (§speed S1).
// Matching is the same source as findEventForCode (RelatedStocks/CleanedStocks → stockMatch), and a
// changed sector event also triggers re-scoring.
func EventSignature(code string, md *strategy_engine.StockMarketData, events []newsagent.NewsEvent, sectorEvent string) string {
	var parts []string
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.Datetime == "" {
			continue // 无时间戳的事件不参与签名（无法定位新旧）
		}
		// 事件与该股关联（RelatedStocks / CleanedStocks 任一命中）
		hit := false
		for _, s := range ev.RelatedStocks {
			if stockMatch(s, code, md) {
				hit = true
				break
			}
		}
		if !hit {
			for _, s := range ev.CleanedStocks {
				if stockMatch(s, code, md) {
					hit = true
					break
				}
			}
		}
		if !hit {
			continue
		}
		// 去重后把「时间|标题」并入签名，事件集变化即可驱动重评
		key := ev.Datetime + "|" + ev.Title
		if !seen[key] {
			seen[key] = true
			parts = append(parts, key)
		}
	}
	sort.Strings(parts) // 排序保证同事件集产生相同签名
	sig := strings.Join(parts, ";")
	if sectorEvent != "" {
		sig += "|SEC|" + sectorEvent // 板块事件变化也计入签名
	}
	return sig
}

// stockMatch 判断事件关联股票串 s 是否与目标股票命中。
// §D5 修复：精确匹配，兼容三种形态——CleanedStocks 的 "名称|代码"、RelatedStocks 的
// "名称(代码)" 与纯名称/纯代码。有代码段时代码精确比对（去交易所后缀）；否则名称全等。
//
// 参数：
//   - s: 事件关联股票字符串
//   - code: 目标股票代码
//   - md: 目标股票行情数据（提供名称）
//
// 返回值：
//   - true: 命中
//   - false: 未命中
func stockMatch(s, code string, md *strategy_engine.StockMarketData) bool {
	if s == "" {
		return false
	}
	name, scode := s, ""
	switch {
	case strings.IndexByte(s, '|') >= 0:
		i := strings.IndexByte(s, '|')
		name, scode = s[:i], s[i+1:]
	case strings.IndexByte(s, '(') > 0 && strings.HasSuffix(s, ")"):
		i := strings.IndexByte(s, '(')
		name, scode = s[:i], s[i+1:len(s)-1]
	}
	if scode != "" && code != "" && bareCode(scode) == bareCode(code) {
		return true
	}
	if name != "" && code != "" && name == code {
		return true // s 本身就是代码
	}
	if name != "" && md != nil && md.Name != "" && name == md.Name {
		return true
	}
	return false
}

// bareCode 去掉交易所后缀（600540.SH → 600540）。
// 参数：
//   - c: 股票代码（可能带交易所后缀）
//
// 返回值：
//   - 去掉后缀的纯代码
func bareCode(c string) string {
	if i := strings.IndexByte(c, '.'); i >= 0 {
		return c[:i]
	}
	return c
}

// minInt 返回两个整数中的较小值。
// 参数：
//   - a: 第一个整数
//   - b: 第二个整数
//
// 返回值：
//   - 两个整数中的较小值
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cleanJSON 清洗 LLM 返回的原始字符串，提取出纯 JSON 数组部分。
// 处理步骤：
//  1. 去除首尾空格
//  2. 全局剔除 UTF-8 BOM(U+FEFF)
//  3. 去除 markdown 代码块标记（```json / ```）
//  4. 提取第一个 '[' 到最后一个 ']' 之间的内容
//  5. 去除末尾多余标点
//  6. 清理非法 '+' 前缀数值
//  7. 转义字符串值中的换行符
//
// 参数：
//   - s: LLM 返回的原始字符串
//
// 返回值：
//   - 清洗后的纯 JSON 字符串
func cleanJSON(s string) string {
	s = strings.ReplaceAll(s, "\ufeff", "")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 提取 JSON 主体：LLM 可能输出单个对象（HotTopic）或数组（D1 评分等）。
	// 按首字符区分：'{' 提取首个 { 到末尾 } 之间；'[' 提取首个 [ 到末尾 ] 之间。
	// English: extract the JSON body — LLM may emit a single object (HotTopic) or an array
	// (D1 scoring etc.). Use the first non-space char to choose delimiters: '{' → first { to last };
	// '[' → first [ to last ].
	if start := strings.IndexAny(s, "{["); start >= 0 {
		open := s[start]
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		if end := strings.LastIndexByte(s, close); end > start {
			s = s[start : end+1]
		}
	}
	s = strings.TrimRight(s, ".,; ")
	// 清理非法 '+' 前缀数值：部分小模型输出 "score": +0.75（裸 + 号），JSON 数字不允许。
	s = d1PlusNumberRe.ReplaceAllString(s, "$1 ")
	// 转义字符串值中的换行符（JSON 不允许字符串内未转义的 \n）
	// 并清理非法转义：9B 推理模型常在字符串里输出 \( \) 等非法 JSON 转义。
	var buf strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			next := s[i+1]
			if !isValidJSONEscape(next) {
				buf.WriteByte(next)
				i++
				continue
			}
			buf.WriteByte(ch)
			buf.WriteByte(next)
			i++
			continue
		}
		if ch == '"' {
			inStr = !inStr
			buf.WriteByte(ch)
			continue
		}
		if inStr && (ch == '\n' || ch == '\r') {
			buf.WriteString("\\n")
		} else {
			buf.WriteByte(ch)
		}
	}
	return buf.String()
}

// d1PlusNumberRe 匹配冒号/逗号/左括号后的 '+' 前缀（数值位置），用于剥离非法 '+'。
var d1PlusNumberRe = regexp.MustCompile(`([:,\[])\s*\+`)

// isValidJSONEscape 判断字节是否为合法 JSON 转义字符。
// 参数：
//   - b: 待判断的字节
//
// 返回值：
//   - true: 是合法 JSON 转义字符
//   - false: 不是合法 JSON 转义字符
func isValidJSONEscape(b byte) bool {
	switch b {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}
