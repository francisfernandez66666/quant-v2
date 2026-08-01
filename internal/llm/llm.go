// Package llm 支持 OpenAI 兼容协议的 NLP 分析与热点标记封装。
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Client LLM API 客户端，封装与 SiliconFlow 对话接口的通信。
type Client struct {
	httpClient *http.Client // HTTP 客户端（30s 超时，禁用 HTTP2 强制走 HTTP1.1）
	apiKey     string       // API 密钥（Authorization: Bearer）
	apiURL     string       // chat/completions 请求地址
	model      string       // 模型名称
}

// DefaultModel 未显式指定模型时的默认模型。
const DefaultModel = "THUDM/GLM-Z1-9B-0414"

// New 创建 LLM 客户端。
func New(cfg Config) *Client {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.siliconflow.cn/v1/chat/completions"
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2: false,
			},
		},
		apiKey: cfg.APIKey,
		apiURL: cfg.APIURL,
		model:  cfg.Model,
	}
}

// Message 对话消息，包含角色和内容。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 聊天补全请求体。
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// ChatResponse 聊天补全响应体。
type ChatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Chat 向 SiliconFlow API 发送对话请求。先传入 system 提示词（设定角色和输出格式），再传入 user 问题。
// HTTP 客户端超时时间 30s，超时后会返回错误，调用方应据此做好重试或兜底。
func (c *Client) Chat(system, user string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("LLM_API_KEY not set")
	}

	req := ChatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}

	data, _ := json.Marshal(req)
	httpReq, err := http.NewRequest("POST", c.apiURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", err
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// HotTopic 热点新闻结构化分析结果。
type HotTopic struct {
	Title             string   `json:"title"`              // 新闻标题
	Level             string   `json:"level"`              // 事件级别：板块 / 个股
	Sentiment         string   `json:"sentiment"`          // 情感：正面 / 负面 / 中性
	Score             float64  `json:"score"`              // 带符号强度：正=利好 负=利空 0=中性
	ImpactLevel       string   `json:"impact_level"`       // 影响级别：高 / 中 / 低
	EventType         string   `json:"event_type"`         // 事件类型：政策/财报/行业/公司/宏观/事件驱动
	Urgency           string   `json:"urgency"`            // 紧急程度：立即 / 关注 / 观察
	Direction         string   `json:"direction"`          // 方向：利好 / 利空 / 中性
	Sectors           []string `json:"sectors"`            // 直接影响板块
	UpstreamSectors   []string `json:"upstream_sectors"`   // 上游产业链受影响板块
	DownstreamSectors []string `json:"downstream_sectors"` // 下游产业链受影响板块
	RelatedStocks     []string `json:"related_stocks"`     // 关联个股名称或代码
	Strategy          string   `json:"strategy"`           // 匹配战法：N形/龙头/双凸/龙回头/无
	Reason            string   `json:"reason"`             // 简要分析理由
}

// hotTopicSystemPrompt 单条热点分析的 system 提示词：约束 LLM 输出严格 JSON 格式的评分/归因结果。
var hotTopicSystemPrompt = `你是一个A股多维度热点分析专家。对提供的新闻标题进行全方位分析，严格按JSON格式返回。

首先判断事件级别：
- 个股级别：股东增持/减持/回购/质押/公司公告/个股经营变动等仅影响单一公司的事件 → level="个股", sectors/upstream_sectors/downstream_sectors全部置为[]空数组
- 板块级别：政策/行业景气/宏观数据/技术突破等影响整个产业链的事件 → level="板块", 正常填写sectors

评分规则（score 为带符号数值，正=利好 负=利空，表示事件强度）：
- +0.75 强利好：业绩翻倍/扭亏为盈、龙头大额回购/增持、重磅新药获批、重大政策利好、重组获批
- +0.50 中利好：业绩小幅增长、普通中标/订单、一般增持、行业景气上行
- -0.50 中利空：业绩小幅下滑/不及预期、一般减持、行业景气下行、质押
- -0.75 强利空：业绩巨亏/预亏、被立案调查/处罚、大股东大幅减持、退市风险警示、重大政策利空
- 0    中性：海外指数波动、常规公告（董事会决议/人事变动）、无个股/板块归因的行情播报、无实质影响的行业新闻
- 注意：尽量使用 ±0.50 / ±0.75，避免使用 ±0.25 弱档；无明确方向一律输出 0

方向判定（与 score 符号一致）：
- 利好：业绩增长、中标/订单、增持/回购、新药获批、政策利好、重组/收购
- 利空：业绩下滑/亏损、减持/质押、被调查/处罚、退市/ST、政策利空
- 中性：海外指数波动、常规公告、行情播报、无归因的一般新闻

板块/个股归因要求：
- 必须从标题中识别具体板块名和股票名填入 sectors / related_stocks
- 例："凯莱英拟增资10.5亿元" → sectors=["医药"], related_stocks=["凯莱英"]
- 例："SpaceX美股盘前涨超2%" → 无A股板块/个股归因 → score=0 中性

字段说明：
{
  "level": "板块|个股",
  "sentiment": "正面|负面|中性",
  "score": 带符号数值(正利好/负利空/0中性),
  "impact_level": "高|中|低",
  "event_type": "政策|财报|行业|公司|宏观|事件驱动",
  "urgency": "立即|关注|观察",
  "direction": "利好|利空|中性",
  "sectors": ["直接影响板块"],
  "upstream_sectors": ["上游产业链受影响板块"],
  "downstream_sectors": ["下游产业链受影响板块"],
  "related_stocks": ["关联个股名称或代码"],
  "strategy": "N形|龙头|双凸|龙回头|无",
  "reason": "简要分析理由"
}
只输出JSON，不要多余文字。

补充规则：
- 宏观数据走弱（GDP增速放缓/低于预期、PMI走弱或跌破荣枯线、核心通胀高企黏性、就业走弱）→ level="板块", event_type="宏观", score=-0.50~-0.75, direction="利空"
- 海外龙头公司（苹果/特斯拉/微软/英伟达等）财报或业绩指引不及预期、盘后大幅下跌，且涉及A股产业链（消费电子/苹果产业链/存储/算力/半导体等）→ level="板块", event_type="行业", score=-0.50~-0.75, direction="利空", sectors填对应A股产业链板块，不得按"海外行情播报"忽略
`

// batchSystemPrompt 批量热点分析的 system 提示词：从编号列表中筛选实质影响事件并输出 JSON 数组。
var batchSystemPrompt = `你是一个A股多维度热点分析专家。从以下新闻中筛选出对A股有实质性影响的重大事件（如政策、行业景气、公司重大利好/利空、宏观数据、技术突破等），忽略娱乐、社会、体育、影视、名人八卦、灾难事故等无关新闻。

必须忽略以下噪音类型（score一律输出0）：
- 机构观点/专家评论/券商研报/分析师看市（如"机构观点""专家看市""某券商认为"）
- 股吧/互动问答/投资者关系/董秘回复
- 海外市场行情播报（美股/港股/欧股/外汇/黄金/原油盘面，无A股板块/个股归因）

对筛选出的每条事件按JSON格式输出，整体为一个JSON数组。如果无重大事件，只输出[]。

首先判断事件级别：
- 个股级别：股东增持/减持/回购/质押/公司公告/个股经营变动等仅影响单一公司的事件 → level="个股", sectors/upstream_sectors/downstream_sectors全部置为[]空数组
- 板块级别：政策/行业景气/宏观数据/技术突破等影响整个产业链的事件 → level="板块", 正常填写sectors

评分规则（score 为带符号数值，正=利好 负=利空，表示事件强度）：
- +0.75 强利好：业绩翻倍/扭亏为盈、龙头大额回购/增持、重磅新药获批、重大政策利好、重组获批
- +0.50 中利好：业绩小幅增长、普通中标/订单、一般增持、行业景气上行
- -0.50 中利空：业绩小幅下滑/不及预期、一般减持、行业景气下行、质押
- -0.75 强利空：业绩巨亏/预亏、被立案调查/处罚、大股东大幅减持、退市风险警示、重大政策利空
- 0    中性：海外指数波动、常规公告（董事会决议/人事变动）、无个股/板块归因的行情播报、无实质影响的行业新闻
- 注意：尽量使用 ±0.50 / ±0.75，避免使用 ±0.25 弱档；无明确方向一律输出 0

方向判定（与 score 符号一致）：
- 利好：业绩增长、中标/订单、增持/回购、新药获批、政策利好、重组/收购
- 利空：业绩下滑/亏损、减持/质押、被调查/处罚、退市/ST、政策利空
- 中性：海外指数波动、常规公告、行情播报、无归因的一般新闻

板块/个股归因要求：
- 必须从标题中识别具体板块名和股票名填入 sectors / related_stocks
- 例："凯莱英拟增资10.5亿元" → sectors=["医药"], related_stocks=["凯莱英"]
- 例："SpaceX美股盘前涨超2%" → 无A股板块/个股归因 → score=0 中性

每条新闻的格式: "序号. 标题"
返回格式:
[
  {
    "index": 序号,
    "level": "板块|个股",
    "sentiment": "正面|负面|中性",
    "score": 带符号数值(正利好/负利空/0中性),
    "impact_level": "高|中|低",
    "event_type": "政策|财报|行业|公司|宏观|事件驱动",
    "urgency": "立即|关注|观察",
    "direction": "利好|利空|中性",
    "sectors": ["直接影响板块"],
    "upstream_sectors": ["上游产业链受影响板块"],
    "downstream_sectors": ["下游产业链受影响板块"],
    "related_stocks": ["关联个股名称或代码"],
    "strategy": "N形|龙头|双凸|龙回头|无",
    "reason": "简要分析理由"
  }
  ]
 只输出JSON数组，不要多余文字。

补充规则：
- 宏观数据走弱（GDP增速放缓/低于预期、PMI走弱或跌破荣枯线、核心通胀高企黏性、就业走弱）→ level="板块", event_type="宏观", score=-0.50~-0.75, direction="利空"
- 海外龙头公司（苹果/特斯拉/微软/英伟达等）财报或业绩指引不及预期、盘后大幅下跌，且涉及A股产业链（消费电子/苹果产业链/存储/算力/半导体等）→ level="板块", event_type="行业", score=-0.50~-0.75, direction="利空", sectors填对应A股产业链板块，不得按"海外行情播报"忽略
`

// llmBatchSize LLM 单次批量处理的最大条数，防止超大批次导致超时。
const llmBatchSize = 30

// AnalyzeHotTopicBatch 批量分析多条新闻，按 llmBatchSize 分批调用并合并结果。
// 任一 批 LLM 轮询重试（最多3次、递增间隔）仍失败时返回错误，不降级关键词兜底，
// 由调用方决定如何处置（归一般仅展示），避免错误情绪打分进入事件流。
func (c *Client) AnalyzeHotTopicBatch(titles []string) ([]*HotTopic, error) {
	result := make([]*HotTopic, len(titles))
	if len(titles) == 0 {
		return result, nil
	}
	for start := 0; start < len(titles); start += llmBatchSize {
		end := start + llmBatchSize
		if end > len(titles) {
			end = len(titles)
		}
		sub, err := c.analyzeBatch(titles[start:end])
		if err != nil {
			return nil, err
		}
		copy(result[start:end], sub)
	}
	return result, nil
}

// analyzeBatch 单批 LLM 批量分析（内部使用，批次规模 ≤ llmBatchSize）。
// 失败时按递增间隔轮询重试（最多3次：2s/4s/6s），仍失败返回错误——不降级关键词兜底。
func (c *Client) analyzeBatch(titles []string) ([]*HotTopic, error) {
	// 构建批量请求文本
	var sb strings.Builder
	for i, t := range titles {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
	}
	prompt := sb.String()

	// 轮询重试（最多3次、间隔递增）
	var resp string
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err = c.Chat(batchSystemPrompt, prompt)
		if err == nil {
			break
		}
		if attempt < 3 {
			log.Printf("LLM[%d/%d] API失败(第%d次), 轮询重试: %v", len(titles), attempt, attempt, err)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	if err != nil {
		log.Printf("LLM[%d] API轮询重试3次仍失败, 该批归一般(不降级): %v", len(titles), err)
		return nil, err
	}
	resp = cleanJSON(resp)

	var raw []struct {
		Index             int      `json:"index"`
		Level             string   `json:"level"`
		Sentiment         string   `json:"sentiment"`
		Score             float64  `json:"score"`
		ImpactLevel       string   `json:"impact_level"`
		EventType         string   `json:"event_type"`
		Urgency           string   `json:"urgency"`
		Direction         string   `json:"direction"`
		Sectors           []string `json:"sectors"`
		UpstreamSectors   []string `json:"upstream_sectors"`
		DownstreamSectors []string `json:"downstream_sectors"`
		RelatedStocks     []string `json:"related_stocks"`
		Strategy          string   `json:"strategy"`
		Reason            string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		log.Printf("LLM[%d] JSON解析失败, 该批归一般(不降级): raw[:%d]=%q: %v", len(titles), minInt(len(resp), 300), resp[:minInt(len(resp), 300)], err)
		return nil, err
	}

	// 日志：LLM返回了哪些板块和个股
	for _, r := range raw {
		sectors := strings.Join(r.Sectors, ",")
		stocks := strings.Join(r.RelatedStocks, ",")
		idx := r.Index - 1
		title := ""
		if idx >= 0 && idx < len(titles) {
			title = titles[idx][:minInt(len(titles[idx]), 30)]
		}
		log.Printf("LLM打标: %s → 方向=%s 板块=[%s] 个股=[%s]", title, r.Direction, sectors, stocks)
	}

	result := make([]*HotTopic, len(titles))
	for i, title := range titles {
		ht := fallbackAnalysis(title)
		for _, r := range raw {
			if r.Index == i+1 {
				ht.Level = r.Level
				ht.Sentiment = r.Sentiment
				ht.Score = r.Score
				ht.ImpactLevel = r.ImpactLevel
				ht.EventType = r.EventType
				ht.Urgency = r.Urgency
				ht.Direction = r.Direction
				if len(r.Sectors) > 0 {
					ht.Sectors = r.Sectors
				}
				if len(r.UpstreamSectors) > 0 {
					ht.UpstreamSectors = r.UpstreamSectors
				}
				if len(r.DownstreamSectors) > 0 {
					ht.DownstreamSectors = r.DownstreamSectors
				}
				if len(r.RelatedStocks) > 0 {
					ht.RelatedStocks = r.RelatedStocks
				}
				if r.Strategy != "" {
					ht.Strategy = r.Strategy
				}
				if r.Reason != "" {
					ht.Reason = r.Reason
				}
				break
			}
		}
		if ht.Level == "" {
			ht.Level = "板块"
		}
		if ht.Strategy == "" {
			ht.Strategy = "无"
		}
		if ht.Urgency == "" {
			ht.Urgency = "观察"
		}
		result[i] = ht
	}
	log.Printf("LLM批量分析完成: %d/%d条", len(raw), len(titles))
	return result, nil
}

// AnalyzeHotTopic 对新闻标题进行多维度热点分析。
// 返回:
//   - *HotTopic: 分析结果（API失败时返回关键词兜底结果，不返回 nil）
//   - error: API 调用或 JSON 解析的错误（非 nil 表示结果来自 fallback）
func (c *Client) AnalyzeHotTopic(title string) (*HotTopic, error) {
	resp, err := c.Chat(hotTopicSystemPrompt, title)
	if err != nil {
		log.Printf("LLM API调用失败(%s), 使用关键词兜底: %v", title[:minInt(len(title), 30)], err)
		return fallbackAnalysis(title), err
	}

	resp = cleanJSON(resp)

	var ht HotTopic
	ht.Title = title
	if err := json.Unmarshal([]byte(resp), &ht); err != nil {
		log.Printf("LLM JSON解析失败(%s), 使用关键词兜底: %s", title[:minInt(len(title), 30)], resp[:minInt(len(resp), 100)])
		return fallbackAnalysis(title), err
	}

	if ht.Level == "" {
		ht.Level = "板块"
	}
	if ht.Strategy == "" {
		ht.Strategy = "无"
	}
	if ht.Urgency == "" {
		ht.Urgency = "观察"
	}
	return &ht, nil
}

// AnalyzeSentiment 简版情感分析（用于快速评分）。
func (c *Client) AnalyzeSentiment(text string) (float64, error) {
	resp, err := c.Chat(
		"你是一个A股新闻情感分析师。只输出一个0-1之间的数字，0=极负面，0.5=中性，1=极正面。不要多余文字。",
		text,
	)
	if err != nil {
		return 0.5, err
	}
	resp = strings.TrimSpace(resp)
	var score float64
	if _, e := fmt.Sscanf(resp, "%f", &score); e == nil && score >= 0 && score <= 1 {
		return score, nil
	}
	return 0.5, nil
}

// AnalyzeNews 兼容旧接口（内部调用新分析）。
func (c *Client) AnalyzeNews(text string) (string, error) {
	ht, err := c.AnalyzeHotTopic(text)
	if err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(ht, "", "  ")
	return string(data), nil
}

// SentimentScore 旧版情感分数接口（关键词兜底）。
func (c *Client) SentimentScore(text string) (float64, error) {
	resp, err := c.AnalyzeHotTopic(text)
	if err != nil {
		return 0.5, err
	}
	return resp.Score, nil
}

// cleanJSON 清理 LLM 返回的原始文本，使其能被 json.Unmarshal 正确解析。
// 1. 去掉 markdown 代码块（```json / ```）——很多 LLM 会用代码块包裹结构化输出。
// 2. 提取 JSON 数组边界（第一个 [ 到最后一个 ]）——有些推理模型（如 GLM-Z1）会在 JSON 前输出思考/推理过程文本。
// 3. 移除尾部多余的 . , ; 等非法字符——部分模型在 JSON 结尾后随手加上了句号或逗号。
// 注意：单条分析会正常解析，批量分析也会从整体中正确截取数组部分。
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 提取 JSON 数组：找到第一个 [ 和最后一个 ]
	if start := strings.IndexByte(s, '['); start >= 0 {
		if end := strings.LastIndexByte(s, ']'); end > start {
			s = s[start : end+1]
		}
	}
	// 移除尾部的非法字符（如句号、逗号）只保留 JSON 部分
	s = strings.TrimRight(s, ".,; ")
	// 转义字符串值中的换行符（JSON 不允许字符串内未转义的 \n）
	var buf strings.Builder
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			buf.WriteByte(ch)
			escaped = true
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

// fallbackAnalysis 关键词兜底分析（LLM 解析失败时使用）。
func fallbackAnalysis(title string) *HotTopic {
	ht := &HotTopic{
		Title:       title,
		Level:       "板块",
		Sentiment:   "中性",
		Score:       0.5,
		ImpactLevel: "中",
		EventType:   "行业",
		Urgency:     "关注",
		Direction:   "中性",
		Strategy:    "无",
	}

	// 板块关键词
	sectorKeywords := map[string]string{
		"人工智能": "人工智能", "AI": "人工智能", "芯片": "半导体", "半导体": "半导体",
		"新能源": "新能源", "光伏": "光伏", "锂电": "锂电池", "汽车": "汽车", "特斯拉": "汽车",
		"医药": "医药", "医疗": "医药", "白酒": "白酒", "消费": "消费",
		"金融": "金融", "银行": "银行", "券商": "券商", "地产": "房地产",
		"军工": "军工", "通信": "通信", "软件": "软件", "传媒": "传媒",
		"化工": "化工", "有色": "有色金属", "煤炭": "煤炭", "电力": "电力",
		"机器人": "机器人", "人形": "机器人", "机器": "机器人",
		"减速器": "机器人", "伺服": "机器人", "电机": "机器人",
		"算力": "算力", "数据中心": "算力",
	}
	for kw, sec := range sectorKeywords {
		if strings.Contains(title, kw) {
			ht.Sectors = append(ht.Sectors, sec)
		}
	}

	// 去重 Sectors
	if len(ht.Sectors) > 1 {
		seen := make(map[string]bool)
		uniq := make([]string, 0, len(ht.Sectors))
		for _, s := range ht.Sectors {
			if !seen[s] {
				seen[s] = true
				uniq = append(uniq, s)
			}
		}
		ht.Sectors = uniq
	}
	if len(ht.Sectors) == 0 {
		ht.Level = "个股"
	}

	// 板块→个股：通过 StockCodeMap 查找
	for _, sec := range ht.Sectors {
		for name, code := range StockCodeMap {
			if strings.Contains(name, sec) || strings.Contains(sec, name) {
				ht.RelatedStocks = append(ht.RelatedStocks, code)
			}
		}
	}

	// 情感关键词（带符号评分，分强/中两档；利空优先于利好判定）
	strongBull := []string{"涨停", "大涨", "暴涨", "走强", "突破", "飙升", "翻倍", "创新高"}
	medBull := []string{"反弹", "利好", "回暖", "回升", "增持", "预增", "扭亏", "上调"}
	strongBear := []string{"跌停", "大跌", "暴跌", "重挫", "下挫", "跳水", "崩盘", "闪崩", "抛售", "暴雷", "腰斩", "跌超", "立案", "调查", "处罚", "退市"}
	medBear := []string{"走弱", "利空", "减持", "放缓", "下滑", "走低", "回落", "不及预期", "承压", "疲软", "萎缩", "高企", "预警", "连跌", "转弱", "低于预期"}
	switch {
	case containsAny(title, strongBear):
		ht.Sentiment = "负面"
		ht.Score = -0.75
		ht.Direction = "利空"
	case containsAny(title, medBear):
		ht.Sentiment = "负面"
		ht.Score = -0.5
		ht.Direction = "利空"
	case containsAny(title, strongBull):
		ht.Sentiment = "正面"
		ht.Score = 0.75
		ht.Direction = "利好"
	case containsAny(title, medBull):
		ht.Sentiment = "正面"
		ht.Score = 0.5
		ht.Direction = "利好"
	}

	// 事件类型
	if containsAny(title, []string{"政策", "国务院", "央行", "证监会", "监管", "法规"}) {
		ht.EventType = "政策"
		ht.ImpactLevel = "高"
	}
	if containsAny(title, []string{"财报", "业绩", "营收", "利润", "亏损"}) {
		ht.EventType = "财报"
	}
	if containsAny(title, []string{"公告", "重组", "收购", "减持", "增持", "分红"}) {
		ht.EventType = "公司"
	}
	if containsAny(title, []string{"CPI", "GDP", "PMI", "利率", "降息", "加息", "通胀"}) {
		ht.EventType = "宏观"
	}

	// 策略匹配
	if containsAny(title, []string{"涨停", "连板", "龙头", "机器人"}) {
		ht.Strategy = "龙头"
	} else if containsAny(title, []string{"反弹", "反转", "突破"}) {
		ht.Strategy = "双凸"
	} else if containsAny(title, []string{"回调", "回踩", "低吸"}) {
		ht.Strategy = "N形"
	}

	// 紧急程度
	if ht.Score >= 0.75 && ht.ImpactLevel == "高" {
		ht.Urgency = "立即"
	} else if ht.Score >= 0.6 || ht.ImpactLevel == "高" {
		ht.Urgency = "关注"
	}

	return ht
}

// minInt 返回两个整数中的较小值（用于截断日志输出长度）。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// containsAny 判断字符串 s 是否包含 keywords 中的任意一个关键词。
func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// SectorTag 解析后的板块标签，含置信度权重。
type SectorTag struct {
	Name       string  // 板块名
	Confidence float64 // 置信度 0~1（无后缀时=1.0）
}

// ParseSectors 解析 LLM 返回的 sectors 列表。
// 格式1: "固态电池" → {Name:"固态电池", Confidence:1.0}
// 格式2: "固态电池(0.8)" → {Name:"固态电池", Confidence:0.8}
// 复合: "半导体(1.0)/芯片(0.7)" → split后分别解析
func ParseSectors(sectors []string) []SectorTag {
	var result []SectorTag
	re := regexp.MustCompile(`^(.+?)\(([\d.]+)\)$`)
	for _, s := range sectors {
		for _, part := range strings.Split(s, "/") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			st := SectorTag{Name: part, Confidence: 1.0}
			if m := re.FindStringSubmatch(part); len(m) == 3 {
				st.Name = strings.TrimSpace(m[1])
				if f, err := fmt.Sscanf(m[2], "%f", &st.Confidence); err != nil || f != 1 {
					st.Confidence = 1.0
				}
				if st.Confidence > 1 {
					st.Confidence = 1
				}
				if st.Confidence < 0 {
					st.Confidence = 0
				}
			}
			result = append(result, st)
		}
	}
	return result
}

// StockCodeMap 股票名称→代码硬编码映射。
// 不依赖 LLM prompt 格式，纯后处理。
var StockCodeMap = map[string]string{
	// 半导体/芯片
	"中芯国际": "688981.SH", "北方华创": "002371.SZ", "韦尔股份": "603501.SH",
	"南大光电": "300346.SZ", "中微公司": "688012.SH", "中微半导体": "688012.SH",
	"华大九天": "301269.SZ", "长电科技": "600584.SH", "兆易创新": "603986.SH",
	"卓胜微": "300782.SZ", "紫光国微": "002049.SZ", "三安光电": "600703.SH",
	"士兰微": "600460.SH", "华虹公司": "688347.SH",
	// AI/算力
	"科大讯飞": "002230.SZ", "寒武纪": "688256.SH", "浪潮信息": "000977.SZ",
	"中科曙光": "603019.SH", "海光信息": "688041.SH", "中际旭创": "300308.SZ",
	"新易盛": "300502.SZ", "天孚通信": "300394.SZ", "工业富联": "601138.SH",
	// 消费电子
	"歌尔股份": "002241.SZ", "立讯精密": "002475.SZ", "京东方A": "000725.SZ",
	"TCL科技": "000100.SZ", "传音控股": "688036.SH",
	// 金融
	"平安银行": "000001.SZ", "工商银行": "601398.SH", "建设银行": "601939.SH",
	"招商银行": "600036.SH", "中国平安": "601318.SH", "中国人寿": "601628.SH",
	"东方财富": "300059.SZ", "中信证券": "600030.SH", "华泰证券": "601688.SH",
	"同花顺": "300033.SZ", "中国银行": "601988.SH", "农业银行": "601288.SH",
	// 新能源/汽车
	"宁德时代": "300750.SZ", "比亚迪": "002594.SZ", "阳光电源": "300274.SZ",
	"隆基绿能": "601012.SH", "赣锋锂业": "002460.SZ", "天齐锂业": "002466.SZ",
	"华友钴业": "600516.SH", "容百科技": "688005.SH", "亿纬锂能": "300014.SZ",
	"恩捷股份": "002812.SZ", "先导智能": "300450.SZ", "长城汽车": "601633.SH",
	"上汽集团": "600104.SH", "赛力斯": "601127.SH", "江淮汽车": "600418.SH",
	"国轩高科": "002074.SZ", "华域汽车": "600741.SH", "宁波华翔": "002048.SZ",
	// 军工
	"航发动力": "600893.SH", "中航沈飞": "600760.SH", "中航西飞": "000768.SZ",
	"中国船舶": "600150.SH", "中航光电": "002179.SZ",
	// 医药
	"恒瑞医药": "600276.SH", "药明康德": "603259.SH", "迈瑞医疗": "300760.SZ",
	"智飞生物": "300122.SZ", "长春高新": "000661.SZ",
	// 白酒/消费
	"贵州茅台": "600519.SH", "五粮液": "000858.SZ", "泸州老窖": "000568.SZ",
	"山西汾酒": "600809.SH", "伊利股份": "600887.SH", "海天味业": "603288.SH",
	"中国中免": "601888.SH",
	// 基建/地产
	"中国建筑": "601668.SH", "中国中铁": "601390.SH", "中国交建": "601800.SH",
	"保利发展": "600048.SH", "万科A": "000002.SZ",
	// 软件/互联网
	"用友网络": "600588.SH", "金山办公": "688111.SH", "恒生电子": "600570.SH",
	"广联达": "002410.SZ",
	// 机器人/自动化/工业母机/人形机器人
	"埃斯顿": "002747.SZ", "汇川技术": "300124.SZ", "绿的谐波": "688017.SH",
	"华工科技": "000988.SZ",
	"三花智控": "002050.SZ", "拓普集团": "601689.SH", "双环传动": "002472.SZ",
	"鸣志电器": "603728.SH", "禾川科技": "688320.SH", "昊志机电": "300503.SZ",
	"中大力德": "002896.SZ", "丰立智能": "301368.SZ", "步科股份": "688160.SH",
	"秦川机床": "000837.SZ", "五洲新春": "603667.SH", "长盛轴承": "300718.SZ",
	// 特斯拉供应链
	"旭升集团": "603305.SH", "岱美股份": "603730.SH", "爱柯迪": "600933.SH",
	// 通信/5G
	"中兴通讯": "000063.SZ", "烽火通信": "600498.SH",
	// 有色/化工
	"紫金矿业": "601899.SH", "洛阳钼业": "603993.SH", "万华化学": "600309.SH",
	"宝钢股份": "600019.SH",
	// 电力/能源
	"长江电力": "600900.SH", "中国核电": "601985.SH", "中国石油": "601857.SH",
	"中国海油": "600938.SH", "中国神华": "601088.SH",
}

// ResolveStocks 将 LLM 返回的 stocks 列表解析为股票代码列表。
// 每元素先按 "/" split 处理复合格式（LLM 常用 "中芯国际/北方华创/韦尔股份"）。
// 解析优先级：硬编码表 > 正则提取 (XXXXXX) > 纯6位数字自动补后缀。
// 返回 (已解析代码列表, 未解析的名称列表)。
func ResolveStocks(stocks []string) (codes []string, unresolved []string) {
	re := regexp.MustCompile(`[（(]([A-Za-z0-9]{6})[）)]`)
	seen := make(map[string]bool)
	for _, s := range stocks {
		for _, part := range strings.Split(s, "/") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// 跳过 LLM 占位符
			if strings.Contains(part, "×") || strings.Contains(part, "×") || strings.Contains(part, "待") || strings.Contains(part, "无") {
				unresolved = append(unresolved, part)
				continue
			}
			// 正则提取 (XXXXXX)
			if m := re.FindStringSubmatch(part); len(m) == 2 {
				code := autoSuffix(m[1])
				if !seen[code] {
					codes = append(codes, code)
					seen[code] = true
				}
				continue
			}
			// 查硬编码表
			if code, ok := StockCodeMap[part]; ok {
				if !seen[code] {
					codes = append(codes, code)
					seen[code] = true
				}
				continue
			}
			// 纯6位数字代码
			if len(part) == 6 && isAlphaNumeric(part) {
				code := autoSuffix(part)
				if !seen[code] {
					codes = append(codes, code)
					seen[code] = true
				}
				continue
			}
			unresolved = append(unresolved, part)
		}
	}
	return
}

// autoSuffix 根据 A 股代码首位数字自动补交易所后缀：
// 6/9 开头→上海(.SH)，0/3/2 开头→深圳(.SZ)，4/8 开头→北交所(.BJ)。
func autoSuffix(code string) string {
	if len(code) != 6 {
		return code
	}
	switch code[0] {
	case '6', '9':
		return code + ".SH"
	case '0', '3', '2':
		return code + ".SZ"
	case '4', '8':
		return code + ".BJ"
	}
	return code
}

// isAlphaNumeric 判断字符串是否全部由字母或数字组成。
func isAlphaNumeric(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
