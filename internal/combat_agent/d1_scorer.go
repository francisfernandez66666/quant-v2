// Package combat_agent 提供操盘战斗 Agent 的核心评分逻辑。
// D1Scorer 负责对个股进行 D1 级别的事件驱动评分，依赖 LLM 分析新闻事件与行情数据。
// 评分维度（含优先级）：负面过滤(blocked) > 顶级影响 > 间接影响 > 中等影响 > 低影响，
// 输出 0.0~1.0 的归一化分数并附 LLM 分析理由。
// English: provides D1 event-driven scoring per stock, using the LLM to analyze news events and market
// data. Scoring dimensions by priority: negative filter (blocked) > top impact > indirect > medium >
// low; outputs a normalized 0.0~1.0 score with an LLM reason.
package combat_agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy_engine"
)

// D1Score 表示单只个股的 D1 事件评分结果。
// Score 范围 0.0~1.0，Blocked 表示被负面过滤拦截，Reason 为 LLM 分析理由。
// English: D1 event-scoring result for a single stock — Score in 0.0~1.0, Blocked means the negative
// filter tripped, Reason is the LLM analysis.
type D1Score struct {
	Code    string  `json:"code"`    // 股票代码
	Score   float64 `json:"score"`   // 评分值，0.0~1.0，越高越值得关注
	Blocked bool    `json:"blocked"` // 是否被负面过滤拦截（利空事件命中）
	Reason  string  `json:"reason"`  // LLM 给出的评分分析理由
}

// D1Scorer 批量个股 D1 评分器。
// 收拢到 combat_agent，LLM 参考 events_leftside.yaml 规则评分。
// 非并发安全，建议由 Engine 在独立 goroutine 中单实例调用。
// English: batch D1 scorer for stocks, scoring with the LLM referencing events_leftside.yaml rules.
// Not concurrency-safe; the Engine should call it from a single goroutine.
type D1Scorer struct {
	llmClient    *llm.Client   // LLM 客户端，用于调用大模型进行 D1 评分
	yamlContent  string        // events_leftside.yaml 原始内容，作为 LLM prompt 参考
	maxAttempts  int           // D1 LLM 调用轮询重试次数（含首次），默认 5；0/负回退默认
	retryBackoff time.Duration // 相邻两次重试的基础间隔
}

// defaultMaxAttempts 默认 D1 LLM 轮询重试次数（含首次）。
// 加重次数以抗 LLM 偶发超时/限流，避免重要 D1 评分随调用失败而丢失。
// English: default number of D1 LLM retries (including the first attempt), raised to survive occasional
// LLM timeouts/rate-limits so important D1 scores are not lost to failures.
const defaultMaxAttempts = 5

// NewD1Scorer 创建 D1Scorer 实例。
// llmClient: LLM 客户端，用于调用大模型进行评分。
// yamlContent: events_leftside.yaml 的原始内容，作为评分规则的参考上下文。
// English: creates a D1Scorer with the LLM client and the raw events_leftside.yaml as scoring-context.
func NewD1Scorer(llmClient *llm.Client, yamlContent string) *D1Scorer {
	return &D1Scorer{
		llmClient:    llmClient,
		yamlContent:  yamlContent,
		maxAttempts:  defaultMaxAttempts,
		retryBackoff: 2 * time.Second,
	}
}

// SetMaxRetries 设置 D1 评分 LLM 调用的轮询重试次数（含首次）。
// n<=0 时回退默认 defaultMaxAttempts。返回设置的生效值。
// English: sets the D1 LLM retry count (including the first attempt); n<=0 reverts to the default and
// the effective value is returned.
func (ds *D1Scorer) SetMaxRetries(n int) int {
	if n <= 0 {
		ds.maxAttempts = defaultMaxAttempts
	} else {
		ds.maxAttempts = n
	}
	return ds.maxAttempts
}

// d1SystemPrompt 是 D1 评分的系统级提示词，定义评分优先级规则和输出格式。
// LLM 根据该提示词对个股关联事件进行分级打分（负面过滤/顶级影响/间接影响/中等影响/低影响）。
// English: the system prompt for D1 scoring, defining priority rules and the JSON output format that the
// LLM uses to grade linked events of each stock.
var d1SystemPrompt = `你是一个A股个股D1事件评分专家。对每只个股基于关联事件进行D1评分。

按优先级：
1. 负面过滤(negative_filter): score=0, blocked=true — 立案/减持/质押/解禁等
2. 顶级影响(top_impact): score=0.4~1.0 — 政策/技术突破/并购重组等
3. 间接影响(indirect): score=0.3~0.6 — 板块情绪传导等
4. 中等影响(medium_impact): score=0.2~0.5 — 业绩/回购/涨价等
5. 低影响(low_impact): score=0.0~0.2 — 普通公告/调研等

详细规则见下方参考。

输出JSON数组：
[
  {"code":"600519","score":0.0~1.0,"blocked":true/false,"reason":"评分理由"}
]
只输出JSON数组，不要多余文字。`

// BatchScore 对一组个股进行批量 D1 评分。
// codes: 待评分的个股代码列表。
// events: 当前周期的新闻事件列表，用于查找个股关联事件。
// marketData: 个股行情数据映射，key 为股票代码。
// fallback: 上一轮成功评分结果（可 nil）。本轮 LLM 明确给出的分数一律保留；
// 仅当 LLM 整批失败（重试全败/解析失败）或漏掉某只个股时，才回退上一轮分数，无则归 0。
// 返回 map[string]D1Score，key 为股票代码，value 为 D1Score 评分结果。
// 逻辑：构建 prompt → 调用 LLM（3 次递增轮询）→ 解析 JSON 响应 → 补全未返回的个股 → 返回结果。
// English: batch-scores a list of stocks. Scores explicitly returned by the LLM this round are always
// kept; the prior-round fallback is used only when the whole batch fails (all retries / parse error) or
// a specific stock is missed, otherwise it defaults to 0. Pipeline: build prompt → call LLM with
// increasing retries → parse JSON → fill missing stocks → return the score map.
func (ds *D1Scorer) BatchScore(codes []string, events []newsagent.NewsEvent, marketData map[string]*strategy_engine.StockMarketData, fallback map[string]D1Score) map[string]D1Score {
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
		// 事件描述来自 events：按 代码 或 名称 匹配关联新闻标题（板块级新闻只带个股名称，须名称兜底）
		eventDesc := findEventForCode(code, md, events)
		if eventDesc != "" {
			sb.WriteString(fmt.Sprintf("   关联事件: %s\n", eventDesc))
		} else {
			sb.WriteString("   关联事件: 无特定事件\n")
		}
		sb.WriteString("\n")
	}

	prompt := sb.String()
	log.Printf("[D1Scorer] 批量评分 %d只个股, prompt=%d字符", len(codes), len(prompt))

	// 调用LLM（系统提示词 d1SystemPrompt 固定，用户提示词携带个股与规则）。
	// 轮询重试（最多 maxAttempts 次、间隔按指数抖动并封顶，防重要 D1 评分随调用失败丢失），
	// 仍失败则回退上一轮评分（fallback 里有值）或按理由兜底 0 分。
	maxAttempts := ds.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	var resp string
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err = ds.llmClient.Chat(d1SystemPrompt, prompt)
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
		log.Printf("[D1Scorer] LLM调用轮询重试%d次仍失败: %v, 回退上一轮评分", maxAttempts, err)
		ds.fillFallback(result, codes, fallback, "LLM失败")
		return result
	}

	// 清洗 LLM 输出（去掉 markdown 代码块与多余文字，仅保留 JSON 数组）
	resp = cleanJSON(resp)

	var raw []D1Score
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		ds.fillFallback(result, codes, fallback, "解析失败")
		log.Printf("[D1Scorer] JSON解析失败→整批 %d 只个股归0/回退: %v (首300字符: %q)", len(codes), err, resp[:minInt(len(resp), 300)])
		return result
	}

	for _, r := range raw {
		if r.Code != "" {
			// 分数越界防护：裁剪到 0.0~1.0
			if r.Score > 1.0 {
				r.Score = 1.0
			}
			if r.Score < 0 {
				r.Score = 0
			}
			result[r.Code] = r
			log.Printf("[D1Scorer] %s → score=%.2f blocked=%v reason=%s", r.Code, r.Score, r.Blocked, r.Reason)
		}
	}

	// 补全LLM未返回的个股：优先回退上一轮评分，无则兜底 0 分，保证结果完整
	for _, code := range codes {
		if _, ok := result[code]; !ok {
			ds.fillFallback(result, []string{code}, fallback, "LLM未返回")
		}
	}

	log.Printf("[D1Scorer] 批量评分完成: %d/%d只, 耗时 %v", len(raw), len(codes), time.Since(t0))
	return result
}

// fillFallback 对缺失评分的个股回退上一轮评分（fallback 有值则复用，无则按 reason 归 0）。
func (ds *D1Scorer) fillFallback(result map[string]D1Score, codes []string, fallback map[string]D1Score, reason string) {
	for _, code := range codes {
		if f, ok := fallback[code]; ok {
			result[code] = f
			log.Printf("[D1Scorer] %s %s, 回退上一轮 score=%.2f blocked=%v", code, reason, f.Score, f.Blocked)
			continue
		}
		result[code] = D1Score{Code: code, Score: 0, Blocked: false, Reason: reason}
		log.Printf("[D1Scorer] %s %s, 无上一轮评分, 默认0分", code, reason)
	}
}

// findEventForCode 从 events 中查找个股关联事件描述。
// 遍历所有事件的 RelatedStocks 与 CleanedStocks 字段，通过子串匹配找到对应事件标题。
// code: 股票代码。
// md: 个股行情数据（提供股票名称，板块级新闻常只带"名称"不带代码，须用名称兜底匹配）。
// events: 新闻事件列表。
// 返回匹配到的事件标题，未匹配则返回空字符串。
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

// stockMatch 判断事件关联股票串 s 是否与 代码/名称 命中。
// s 可能形态：纯名称("招金黄金")、名称(代码)("招金黄金(600540)")、名称|代码("招金黄金|600540")。
func stockMatch(s, code string, md *strategy_engine.StockMarketData) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, code) || strings.Contains(code, s) {
		return true
	}
	if md != nil && md.Name != "" {
		if strings.Contains(s, md.Name) || strings.Contains(md.Name, s) {
			return true
		}
	}
	return false
}

// minInt 返回两个整数中的较小值。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cleanJSON 清洗 LLM 返回的原始字符串，提取出纯 JSON 数组部分。
// 处理步骤：去除首尾空格 → 全局剔除 UTF-8 BOM(U+FEFF)（LLM 输出可能在数组内部夹 BOM，
// 仅剥首尾会漏掉中间字符导致 json.Unmarshal 整批失败）→ 去除 markdown 代码块标记
// （ ```json / ``` ）→ 提取第一个 '[' 到最后一个 ']' 之间的内容 → 去除末尾多余标点。
func cleanJSON(s string) string {
	s = strings.ReplaceAll(s, "\ufeff", "")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if start := strings.IndexByte(s, '['); start >= 0 {
		if end := strings.LastIndexByte(s, ']'); end > start {
			s = s[start : end+1]
		}
	}
	s = strings.TrimRight(s, ".,; ")
	return s
}
