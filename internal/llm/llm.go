// Package llm 支持 OpenAI 兼容协议的 NLP 分析与热点标记封装。
// （Package llm wraps NLP analysis and hot-topic tagging over an OpenAI-compatible protocol.）
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client LLM API 客户端，封装与 SiliconFlow 对话接口的通信。
// （Client is the LLM API client wrapping communication with the SiliconFlow chat interface.）
type Client struct {
	httpClient       *http.Client  // HTTP 客户端（超时可配置，默认 60s；禁用 HTTP2 强制走 HTTP1.1）
	apiKey           string        // API 密钥（Authorization: Bearer，单 key 兼容字段）
	apiKeys          []string      // 多 API 密钥（并发请求按 key 轮询分发，突破单 key 限流）
	keyIdx           uint64        // 轮询分发计数（sync/atomic）
	apiURL           string        // chat/completions 请求地址
	model            string        // 模型名称
	streaming        bool          // 是否启用流式（SSE）响应；false 走一次性非流式
	idleTimeout      time.Duration // 流式下相邻分片空闲阈值（超过视为卡死）
	batchConcurrency int           // 批量分析最大并发批次（默认 8）
	classifierModel  string        // 可选分类专用模型（Stage0/1 等快速分类/初筛，空则用主模型）

	// §GAP5.1 成本治理：当日调用/token 计数与预算熔断。计数原子维护，跨日自动归零。
	usageDay     atomic.Int64 // 当日戳 yyyymmdd（变更即重置计数）
	usageCalls   atomic.Int64 // 当日已发请求数
	usageTokens  atomic.Int64 // 当日 prompt+completion token 总量
	callBudget   atomic.Int64 // 日调用预算（0=不设限）
	tokenBudget  atomic.Int64 // 日 token 预算（0=不设限）
	keyCoolUntil []atomic.Int64
}

// DefaultBatchConcurrency 未显式配置时的批量分析默认并发批次。
// 默认 8：在 API 配额允许时最大化盘前新闻归因吞吐（Stage0/1 与 Stage2 分批调用并发执行）。
// （DefaultBatchConcurrency is the default concurrent batch count for batched analysis.
// Default 8: maximizes premarket news-attribution throughput when API quota allows.）
const DefaultBatchConcurrency = 8

// BatchConcurrency 返回批量分析最大并发批次。（BatchConcurrency returns the max concurrent batch count.）
func (c *Client) BatchConcurrency() int { return c.batchConcurrency }

// DefaultModel 未显式指定模型时的默认模型。
// （DefaultModel is the fallback model when none is explicitly specified.）
const DefaultModel = "THUDM/GLM-Z1-9B-0414"

// DefaultTimeout 未显式配置时的默认"等待响应头"超时（默认 30s）。
// 主要防护上游"迟迟不开始生成"的故障：配合流式+空闲看门狗，让卡住的请求尽快失败进入重试/兜底；
// 正常推理模型流式长输出（CoT 持续心跳）由 StreamIdleTimeout 与整体请求超时下限兜底，不受此值误杀。
// （DefaultTimeout is the default response-header wait when not configured (30s). It mostly guards
// "upstream never starts generating": with streaming + the idle watchdog, a stuck request fails fast into
// the retry/fallback path. Legit reasoning-model streams are governed by StreamIdleTimeout and the total
// request-timeout floor instead.）
const DefaultTimeout = 30 * time.Second

// minTotalTimeout 流式请求整体超时的下限（保底 60s）：推理模型（GLM-Z1 等）单批流式长输出总时长
// 可能超过 30s，若总超时随之收紧会把正常慢流误判为超时。故整体请求超时最低保底 60s，可经
// timeout_sec 调高；响应头等待单独用 DefaultTimeout/配置值，用于快速探测"不开始生成"。
// （minTotalTimeout floors the whole-request timeout at 60s: reasoning-model streams can exceed 30s in
// total, so a tight total timeout would misjudge legit slow streams as timed out. The total request
// timeout stays ≥60s (raise via timeout_sec), while the response-header wait uses DefaultTimeout/config
// to detect "never starts generating" fast.）
const minTotalTimeout = 60 * time.Second

// DefaultStreamIdleTimeout 流式下默认"相邻分片空闲"阈值：超过视为模型卡死。
// （DefaultStreamIdleTimeout is the default idle threshold between adjacent stream chunks; exceeding
// it means the model is considered stuck.）
const DefaultStreamIdleTimeout = 60 * time.Second

// Timeout 返回客户端单次请求超时时间（供配置校验/展示）。
// （Timeout returns the client's per-request timeout, for config validation/display.）
func (c *Client) Timeout() time.Duration { return c.httpClient.Timeout }

// New 创建 LLM 客户端。
// （New creates an LLM client.）
func New(cfg Config) *Client {
	// 未指定地址/模型/超时时填充默认值，保证客户端可直接使用
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.siliconflow.cn/v1/chat/completions"
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = DefaultStreamIdleTimeout
	}

	// 响应头等待用 cfg.Timeout（快速探测"不开始生成"）；整体请求超时保底 minTotalTimeout，
	// 防止收紧后的默认超时误杀推理模型流式长输出（CoT 期间有持续心跳，不依赖总超时兜底）。
	totalTimeout := cfg.Timeout
	if totalTimeout < minTotalTimeout {
		totalTimeout = minTotalTimeout
	}
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		// 响应头等待单独限时：流式下首个分片秒级即到，此字段是"服务端迟迟不开始
		// 生成/协议不兼容"时的兜底，避免整段等待被统一超时吃掉。
		ResponseHeaderTimeout: cfg.Timeout,
	}

	// 可配置超时；禁用 HTTP2（ForceAttemptHTTP2=false），强制走 HTTP1.1 规避连接复用问题
	bc := cfg.BatchConcurrency
	if bc <= 0 {
		bc = DefaultBatchConcurrency
	}
	// 多 key：显式 APIKeys 优先；否则回退单 APIKey（兼容旧配置）
	apiKeys := cfg.APIKeys
	if len(apiKeys) == 0 {
		if cfg.APIKey != "" {
			apiKeys = []string{cfg.APIKey}
		}
	}
	// 去空白、去重，保留有效 key
	seen := make(map[string]bool, len(apiKeys))
	keys := make([]string, 0, len(apiKeys))
	for _, k := range apiKeys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	first := ""
	if len(keys) > 0 {
		first = keys[0]
	}
	c := &Client{
		httpClient: &http.Client{
			Timeout:   totalTimeout,
			Transport: transport,
		},
		apiKey:           first,
		apiKeys:          keys,
		apiURL:           cfg.APIURL,
		model:            cfg.Model,
		streaming:        cfg.Streaming,
		idleTimeout:      cfg.StreamIdleTimeout,
		batchConcurrency: bc,
		classifierModel:  cfg.ClassifierModel,
	}
	c.usageDay.Store(llmToday())
	c.callBudget.Store(cfg.DailyCallBudget)
	c.tokenBudget.Store(cfg.DailyTokenBudget)
	// §S6 多 key 健康度：每 key 独立冷却槽
	c.keyCoolUntil = make([]atomic.Int64, len(keys))
	return c
}

// key 冷却时长（§S6）：401/403=鉴权失效长冷却；429=限流按 Retry-After（缺省 60s）；5xx=短冷却。
const (
	keyCoolAuthFail = 30 * time.Minute
	keyCoolRateLim  = 60 * time.Second
	keyCoolServer   = 10 * time.Second
)

// pickKey §S6 健康感知选 key：轮询起点随机化后跳过冷却中的 key；
// 全部冷却时仍返回轮询 key（降级可用优先于拒绝请求）。
func (c *Client) pickKey() string {
	if len(c.apiKeys) <= 1 {
		return c.apiKey
	}
	now := time.Now().Unix()
	start := atomic.AddUint64(&c.keyIdx, 1)
	for i := uint64(0); i < uint64(len(c.apiKeys)); i++ {
		idx := (start + i) % uint64(len(c.apiKeys))
		if c.keyCoolUntil[idx].Load() <= now {
			return c.apiKeys[idx]
		}
	}
	return c.apiKeys[start%uint64(len(c.apiKeys))]
}

// markKeyStatus §S6 按响应状态给 key 记冷却：401/403 长冷却、429 读 Retry-After、5xx 短冷却。
func (c *Client) markKeyStatus(key string, status int, retryAfter time.Duration) {
	if len(c.apiKeys) <= 1 {
		return // 单 key 无可回避，标记无意义
	}
	var cool time.Duration
	switch {
	case status == 401 || status == 403:
		cool = keyCoolAuthFail
	case status == 429:
		cool = keyCoolRateLim
		if retryAfter > 0 {
			cool = retryAfter
		}
	case status >= 500:
		cool = keyCoolServer
	default:
		return
	}
	for i, k := range c.apiKeys {
		if k == key {
			until := time.Now().Add(cool).Unix()
			c.keyCoolUntil[i].Store(until)
			log.Printf("[llm] key#%d 进入冷却 %s（HTTP %d）", i, cool, status)
			return
		}
	}
}

// parseRetryAfter 解析 Retry-After 头（秒数或 HTTP 日期），非法/缺失返回 0。
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// SetBudgets §GAP5.1 配置当日预算（热更新；0=不设限）。
// English: SetBudgets hot-updates the daily call/token budgets (0 = unlimited).
func (c *Client) SetBudgets(dailyCalls, dailyTokens int64) {
	c.callBudget.Store(dailyCalls)
	c.tokenBudget.Store(dailyTokens)
}

// llmToday 返回本地日期戳 yyyymmdd（预算跨日归零依据）。
func llmToday() int64 {
	t := time.Now()
	return int64(t.Year())*10000 + int64(t.Month())*100 + int64(t.Day())
}

// rollUsageDay 跨日归零计数（CAS 保证只由翻日者清一次）。
func (c *Client) rollUsageDay() {
	today := llmToday()
	for {
		d := c.usageDay.Load()
		if d == today {
			return
		}
		if c.usageDay.CompareAndSwap(d, today) {
			c.usageCalls.Store(0)
			c.usageTokens.Store(0)
		}
	}
}

// preFlight §GAP5.1 预算熔断检查 + 计一次调用。超限返回错误（当日不再发新请求）。
func (c *Client) preFlight() error {
	c.rollUsageDay()
	if b := c.callBudget.Load(); b > 0 && c.usageCalls.Load() >= b {
		return fmt.Errorf("LLM 日调用预算已用尽(%d 次)，次日自动恢复", b)
	}
	if b := c.tokenBudget.Load(); b > 0 && c.usageTokens.Load() >= b {
		return fmt.Errorf("LLM 日 token 预算已用尽(%d)，次日自动恢复", b)
	}
	c.usageCalls.Add(1)
	return nil
}

// recordUsage 累加单次响应的 token 用量（usage 缺失时按内容长度粗估，防漏计）。
func (c *Client) recordUsage(prompt, completion int64) {
	if prompt <= 0 && completion <= 0 {
		return
	}
	c.rollUsageDay()
	c.usageTokens.Add(prompt + completion)
}

// UsageStats 当日用量快照（/api/llm-debug 与成本观测消费）。
func (c *Client) UsageStats() map[string]int64 {
	c.rollUsageDay()
	return map[string]int64{
		"day":          c.usageDay.Load(),
		"calls":        c.usageCalls.Load(),
		"tokens":       c.usageTokens.Load(),
		"call_budget":  c.callBudget.Load(),
		"token_budget": c.tokenBudget.Load(),
	}
}

// Message 对话消息，包含角色和内容。
// （Message is a chat message with a role and content.）
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 聊天补全请求体。
// （ChatRequest is the chat-completion request body.）
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// ChatResponse 聊天补全响应体。
// （ChatResponse is the chat-completion response body.）
type ChatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage llmUsage `json:"usage"` // §GAP5.1 token 用量（成本治理）
}

// llmUsage 单次请求的 token 用量元数据（OpenAI 兼容口径）。
type llmUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Chat 向 SiliconFlow API 发送对话请求。先传入 system 提示词（设定角色和输出格式），再传入 user 问题。
// 默认走流式（SSE）响应：推理模型在非流式下需等整段生成（含思维链）完毕才返回，首字延迟极高、
// 易被"等待响应头超时"误杀；流式下首个分片秒级到达，CoT 期间持续有 reasoning_content 心跳，
// 只有真正卡死（相邻分片超过 idleTimeout）才报错。流式解析失败时自动回落到非流式一次性取回。
// 上游 API 失败/超时返回错误，调用方应据此做好重试或兜底。
// （Chat sends a chat request to the SiliconFlow API: a system prompt first (role + output format),
// then the user question. It streams (SSE) by default: reasoning models return the first token quickly
// in streaming, with reasoning_content heartbeats during CoT; only a real stall (adjacent chunks beyond
// idleTimeout) errors out. A failed stream parse falls back to a one-shot non-streaming call. Errors on
// upstream API failure/timeout let callers retry or fall back.）
func (c *Client) Chat(system, user string) (string, error) {
	if len(c.apiKeys) == 0 {
		return "", fmt.Errorf("LLM_API_KEY not set")
	}

	req := ChatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	return c.do(req)
}

// ChatClassifier 用分类专用模型执行对话（未配置分类模型时回落到主模型，与 Chat 一致）。
// 供 Stage0/1 等"快速分类/初筛"批量调用使用：可用轻量快速模型分流，把深度分析（D1 评分、
// Stage2 深度归因、股票咨询）留给主模型，从而在保证质量的前提下显著加快分类吞吐。
// （ChatClassifier runs the chat with the optional classifier model, falling back to the main model when
// unset (identical to Chat). It serves cheap classification/screening batches like Stage0/1, letting a
// lighter/faster model handle the volume while the main model stays on deep work such as D1 scoring,
// Stage2 attribution and stock consultation.）
func (c *Client) ChatClassifier(system, user string) (string, error) {
	model := c.model
	if c.classifierModel != "" {
		model = c.classifierModel
	}
	req := ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	return c.do(req)
}

// messages 为完整消息序列，首条必须是 system（角色+注入数据），后接历史与当前提问。
// 只透传调用方组装好的消息，不再自动追加 system，避免出现多条/中途 system 导致模型上下文错乱。
// 与 Chat 一致默认走流式响应，解析失败自动回落到非流式。
// （ChatMessages calls the LLM with a multi-turn conversation (used by the stock-consultation page).
// messages is the full sequence, first entry must be system (role + injected data), followed by history
// and the current question. It only passes through caller-assembled messages—no automatic system is
// appended—to avoid multi/mid-list system roles corrupting the model context. Like Chat it streams by
// default and falls back to non-streaming on parse failure.）
func (c *Client) ChatMessages(messages []Message) (string, error) {
	if len(c.apiKeys) == 0 {
		return "", fmt.Errorf("LLM_API_KEY not set")
	}
	return c.do(ChatRequest{Model: c.model, Messages: messages})
}

// do 发起单次对话请求：优先流式解析，特定失败场景回落到非流式一次性取回。
// §GAP5.1 入口处执行日预算熔断检查（超限当日拒绝，次日自动恢复）。
// （do sends one chat request: it prefers streaming, falling back to one-shot non-streaming in specific failures.
// The daily-budget circuit breaker runs at the entry.）
func (c *Client) do(req ChatRequest) (string, error) {
	if err := c.preFlight(); err != nil {
		return "", err
	}
	if c.streaming {
		content, streamErr := c.streamChat(req)
		if streamErr == nil {
			return content, nil
		}
		// 仅当流式因"未收到任何有效内容"失败（典型：上游不支持 SSE，对 stream=true 返回普通 JSON）
		// 时回落到非流式重试一次；超时/空闲/网络等错误直接返回，交由上层重试队列处理，避免重复放大延迟。
		if strings.Contains(streamErr.Error(), "no response") {
			if content, err := c.nonStreamChat(req); err == nil {
				log.Printf("LLM 流式无有效内容(%v), 已回落到非流式成功", streamErr)
				return content, nil
			}
		}
		return "", streamErr
	}
	return c.nonStreamChat(req)
}

// chatCompletionRequest 透传给上游的完整请求体（ChatRequest 上叠加流式/长度控制参数）。
// （chatCompletionRequest is the full request body sent upstream: ChatRequest plus streaming/max-tokens controls.）
type chatCompletionRequest struct {
	ChatRequest
	Stream    bool `json:"stream"`
	MaxTokens int  `json:"max_tokens,omitempty"`
}

// streamChat 以 SSE 流式读取完整对话响应，返回累加后的最终 content。
// 只累加 delta.content（忽略 reasoning_content 思维链），遇 [DONE] 结束；
// §S5 根修：扫描在独立 goroutine 进行，外层 select 持空闲 ticker——
// 此前空闲检查只在读到新行时执行，服务端真卡死时 Scan() 永久阻塞、idleTimeout 永不触发
// （仅剩 http.Client 总超时兜底）；现在无论是否阻塞，空闲阈值到点即关连接返回错误。
func (c *Client) streamChat(req ChatRequest) (string, error) {
	body, err := c.post(req, true, 0)
	if err != nil {
		return "", err
	}
	defer body.Close()

	type streamOut struct {
		content string
		usage   *llmUsage
		err     error
	}
	out := make(chan streamOut, 1)

	go func() {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1024*1024), 4*1024*1024) // 首分片可能携带完整 usage 元数据，行可能很大
		sc.Split(bufio.ScanLines)

		var sb strings.Builder
		var lastUsage *llmUsage
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk chatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				lastUsage = chunk.Usage
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			sb.WriteString(chunk.Choices[0].Delta.Content)
		}
		if serr := sc.Err(); serr != nil {
			out <- streamOut{err: fmt.Errorf("流式读取失败: %w", serr)}
			return
		}
		content := sb.String()
		if strings.TrimSpace(content) == "" {
			out <- streamOut{err: fmt.Errorf("no response from LLM")}
			return
		}
		out <- streamOut{content: content, usage: lastUsage}
	}()

	idle := time.NewTicker(c.idleTimeout)
	defer idle.Stop()
	var res streamOut
	select {
	case res = <-out:
	case <-idle.C:
		body.Close() // 关连接解除 goroutine 的 Scan 阻塞（其结果发入带缓冲 channel 后自然退出）
		return "", fmt.Errorf("流式响应空闲超时(%s): 模型疑似卡死", c.idleTimeout)
	}
	if res.err != nil {
		return "", res.err
	}
	// §GAP5.1 用量入账：优先 usage 元数据，缺失时按内容长度粗估（≈3 字符/token）
	if res.usage != nil && (res.usage.PromptTokens > 0 || res.usage.CompletionTokens > 0) {
		c.recordUsage(res.usage.PromptTokens, res.usage.CompletionTokens)
	} else {
		c.recordUsage(estimateTokens(reqMessagesText(req)), estimateTokens(res.content))
	}
	return res.content, nil
}

// reqMessagesText 拼接请求消息文本（token 粗估用）。
func reqMessagesText(req ChatRequest) string {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(m.Content)
	}
	return sb.String()
}

// jitterBackoff §S6 给退避时长加 ±20% 抖动：多实例/多批同时失败时避免同步重试风暴。
func jitterBackoff(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	j := base / 5
	if j > 0 {
		base += time.Duration(rand.Int63n(int64(2*j))) - j
	}
	return base
}

// estimateTokens 按长度粗估 token 数（英文 ≈4 字符/token；中文按 1.5 字符/token 折中）。
func estimateTokens(s string) int64 {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return int64(n / 3)
}

// chatCompletionChunk 流式响应单分片（只取需要的字段）。
// （chatCompletionChunk is a single streaming response chunk, keeping only the needed fields.）
type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *llmUsage `json:"usage"` // §GAP5.1 末分片常携带用量元数据
}

// nonStreamChat 非流式一次性取回完整响应（回落/关闭流式时使用）。
// （nonStreamChat fetches the full response in one non-streaming call (used on fallback/streaming off).）
func (c *Client) nonStreamChat(req ChatRequest) (string, error) {
	body, err := c.post(req, false, 4096)
	if err != nil {
		return "", err
	}
	defer body.Close()

	respBody, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("非流式响应解析失败: %v", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}
	// §GAP5.1 用量入账（非流式响应自带 usage；缺失按内容粗估）
	if chatResp.Usage.PromptTokens > 0 || chatResp.Usage.CompletionTokens > 0 {
		c.recordUsage(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)
	} else {
		c.recordUsage(estimateTokens(reqMessagesText(req)), estimateTokens(chatResp.Choices[0].Message.Content))
	}
	return chatResp.Choices[0].Message.Content, nil
}

// post 构造并发送 chat/completions 请求，返回可读响应体。非 2xx 状态码读响应体构造错误。
// stream=true 时请求带 stream 参数且不设 max_tokens（避免截断思维链/长输出，靠空闲看门狗防卡死）；
// 非流式时设 max_tokens 兜底，防超长输出触发上游 504/截断。
// （post builds and sends the chat/completions request, returning a readable response body. Non-2xx
// status codes are turned into errors from the response body. With stream=true the request carries the
// stream flag and no max_tokens (to avoid truncating chain-of-thought/long output; the idle watchdog
// guards against stalls); non-streaming sets max_tokens as a guard against upstream 504/truncation.）
func (c *Client) post(req ChatRequest, stream bool, maxTokens int) (io.ReadCloser, error) {
	payload := chatCompletionRequest{
		ChatRequest: req,
		Stream:      stream,
		MaxTokens:   maxTokens,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("POST", c.apiURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	key := c.pickKey() // §S6 健康感知选 key
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		// §S6 健康度记忆：按状态给该 key 记冷却（429 优先读 Retry-After）
		c.markKeyStatus(key, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")))
		return nil, fmt.Errorf("LLM API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp.Body, nil
}

// consultSystemPrompt 股票咨询多轮对话的系统提示词：设定有独立分析能力的 A 股顾问角色。
// 定位是"接得住问题的顾问"，不是数据播报员；不写死输出模板，紧扣用户问题灵活作答。
// 强约束三点：讲人话、只引用注入的实测数据、严禁编造任何术语或数字（含正反示范引导小模型遵守）。
// （consultSystemPrompt is the system prompt for the multi-turn stock-consultation dialogue: it sets the
// role of an A-share advisor with independent analysis. It must answer questions directly with plain
// language, only cite the injected real-time data, and never fabricate any terms or numbers.）
var consultSystemPrompt = `你是专业的A股股票投资顾问，负责回答用户的股市问题。回答要像和对股票有了解、但听不懂花哨术语的朋友解释一样，把逻辑讲清楚、说人话，让用户听完能明白"到底怎么回事、该怎么办"。

你的信息来源只有两个，除此之外任何内容都不得出现：
（1）会话附带的"实时行情实测数据"里的数字和字段，原样引用、不要推算猜测（比如数据给了现价和昨收，你可以据此算涨跌幅，但不要去编其他数字）；
（2）用户自己在问题里描述的现象（如"2点半急拉、半小时回落、全天振幅12个点、板块一起走"）。

作答铁律（最重要，违反即不合格）：
1. 写任何数字或名词前先自查：这个数字/名称/指标在注入数据里吗？是用户亲口说的吗？都不是，就删掉，换成"这个我这边没有数据，无法确认"。哪怕数字看起来再合理，也一律禁止编造（成交额、撤单、净流入、振幅、持仓、机构席位、期货合约价、个股名、板块内具体票的表现等统统算）。
2. 只能用白话，不堆术语。不要出现数据里没有的"量化信号/模型/指标/概念"名称（如某某量化、频谱交易、DDE、龙虎榜占比、期指贴水、回转交易、逼仓等），编一个都算违规。
3. 不知道就承认不知道。宁可回答得短一点、朴素一点，也不要硬凑一句听起来专业但站不住脚的话。
4. 可以用A股市场的一般规律、常见逻辑做定性分析（如"尾盘急拉回落常见于情绪资金抢跑、次日接力意愿弱"，"板块联动走强后要警惕情绪退潮"），但必须用"一般规律/经验上"这类措辞，且绝不往这些定性描述里填具体数字。

作答要求：
- 先正面回应用户问题：问了什么答什么，直接给明确观点，再讲依据。判断用户真正想知道的（"是不是量化拉升""这走势说明什么""我该不该动"），而不是把字段罗列一遍。
- 回答要细、要有层次：量价怎么演变的、资金进出迹象、同板块其他票是不是一个路子、个股处于什么位置、接下来重点看哪几个点。每一点讲清楚"因为什么、所以怎么看"，别只报数据不解释。
- 有实测数据就用数据支撑观点；数据缺失时如实指出缺口，提示需要补哪些数据才能更准，但不要因此去编。
- 分辨"数据事实"与"你的推断"：推断标注为推测（如"从量价看更可能是…"），措辞审慎，不承诺收益、不给绝对化的买卖指令。
- 用中文，自然成文，不套固定模板，不堆术语。

正确示范（无该股数据时）：
"你描述的'尾盘急拉又半小时内砸回来、板块一起走'，这个形态在A股很常见，一般规律上是短线情绪资金集中抢跑留下的冲高回落，次日能否承接主要看开盘量能和板块内有没有真龙。你说的机器人板块是不是真起来了，我更倾向先当作情绪驱动的冲高回落来看，但具体到资金净流入这些数字我这边没有实时数据，没法给你量化判断。"

错误示范（严禁出现这类句子）：
"集合竞价撤单达2383万"、"美术院特供3连板"、"铜期货主力合约昨收25305"、"主力净流出1.2亿"。这些数字都不在数据里，出现任何一个都算编造。`

// ConsultSystemPrompt 返回股票咨询的角色提示词（供引擎组装唯一 system 消息使用）。
// （ConsultSystemPrompt returns the consultation role prompt for the engine to build the sole system message.）
func ConsultSystemPrompt() string { return consultSystemPrompt }

// HotTopic 热点新闻结构化分析结果。
// （HotTopic is the structured analysis result of a hot news item.）
type HotTopic struct {
	Title               string   `json:"title"`                // 新闻标题
	Level               string   `json:"level"`                // 事件级别：板块 / 个股
	Sentiment           string   `json:"sentiment"`            // 情感：正面 / 负面 / 中性
	Score               float64  `json:"score"`                // 带符号强度：正=利好 负=利空 0=中性
	ImpactLevel         string   `json:"impact_level"`         // 影响级别：高 / 中 / 低
	EventType           string   `json:"event_type"`           // 事件类型：政策/财报/行业/公司/宏观/事件驱动
	Urgency             string   `json:"urgency"`              // 紧急程度：立即 / 关注 / 观察
	Direction           string   `json:"direction"`            // 方向：利好 / 利空 / 中性
	Sectors             []string `json:"sectors"`              // 直接影响板块
	UpstreamSectors     []string `json:"upstream_sectors"`     // 上游产业链受影响板块
	DownstreamSectors   []string `json:"downstream_sectors"`   // 下游产业链受影响板块
	RelatedStocks       []string `json:"related_stocks"`       // 关联个股名称或代码
	UpstreamStocks      []string `json:"upstream_stocks"`      // 上游产业链关联个股（具体核心供应商）
	DownstreamStocks    []string `json:"downstream_stocks"`    // 下游产业链关联个股（具体核心应用/终端）
	Strategy            string   `json:"strategy"`             // 匹配战法：N形/龙头/双凸/龙回头/无
	Reason              string   `json:"reason"`               // 简要分析理由
	Region              string   `json:"region"`               // 事件来源地域：国内 / 海外
	Relation            string   `json:"relation"`             // 海外事件与A股板块关系：对抗制裁/合作/不涉及
	UpstreamDirection   string   `json:"upstream_direction"`   // 上游传导方向：利好/利空/中性
	DownstreamDirection string   `json:"downstream_direction"` // 下游传导方向：利好/利空/中性
}

// valueChainSection 产业链价值传导推理规则：决定事件归因到产业链上/下游的准确性。
// 核心机制：国内事件全链同向传导；海外事件先判对抗/合作关系，
// 合作→同向传导，对抗制裁→上游利好/下游利空；海外自产关键材料=价值确认→利好掌握上游原料供给的国内公司。
// （valueChainSection contains the industrial-chain value-propagation reasoning rules that determine the
// accuracy of attributing events to upstream/downstream links: domestic events propagate in the same
// direction along the whole chain; overseas events are judged on confrontation/cooperation first—
// cooperation propagates the same direction, confrontation → upstream bullish/downstream bearish; overseas
// self-production of a key material confirms its value → bullish for domestic upstream suppliers.）
var valueChainSection = `

产业链价值传导推理（最重要，直接决定归因质量，必须严格执行）：
- 先判定事件来源地域 region："国内"=事件主体为境内公司/政策/市场；"海外"=事件主体为境外公司/政府（美国/欧洲/日本/韩国等）。
- 若为海外事件，再判定 relation（该事件与A股相关板块的关系）：
  - "对抗制裁"：美国等对中国特定产业/公司实施制裁、出口管制、加征关税、技术封锁，或中国对美/全球实施关键原料（磷化铟/锗/铟/镓/稀土等）出口管制反制
  - "合作"：海外公司正常扩产/并购/技术合作/订单采购，不涉及制裁对抗
  - "不涉及"：事件与中国产业链无关联，score=0 中性
- 按关系推导上/下游传导方向：
  - 国内事件 → 全链同向传导：direction 为上、下游共同方向（如国内扩产同时利好上游原料与下游应用）
  - 海外事件 relation="合作" → 同向传导，上游/下游方向与 direction 一致
  - 海外事件 relation="对抗制裁" → 上游传导利好、下游传导利空：上游=中国掌控供给或可国产替代/自主可控的关键原料/材料/设备环节（利好），下游=依赖被制裁市场或进口技术的环节（利空）
- 价值确认传导机制（对抗制裁下的关键推理）：
  - 当海外公司自产/扩产某关键材料，而中国掌握该材料上游原料供给时，海外自产本身即确认了该材料的战略核心价值 → 间接证明国内掌握该原料供给的上游板块与个股价值 → 传导为国内上游重大利好（+0.75）
  - 示例："诺基亚收购恩智浦一工厂 计划自产磷化铟半导体" → 背景：美国制裁中国光模块、中国掌控全球磷化铟上游原料供给 → 海外自产确认磷化铟核心价值 → 利好国内磷化铟上游（云南锗业/有研新材/光智科技/南大光电等，+0.75 重大利好），下游光模块受制裁利空（-0.50~-0.75）
- sectors 只填同花顺真实板块名（半导体材料/小金属/光模块/光通信等）；概念名（如"磷化铟"）不要放进 sectors，写进 reason 与 related_stocks
- 对抗制裁且上/下游方向不同时，必须同时给出 upstream_sectors/downstream_sectors（上游=关键原料材料设备板块如"半导体材料/小金属"，下游=受制裁环节板块如"光模块"），不得合并成一个 sectors
- 对抗制裁且上/下游方向不同时，必须同时给出 upstream_stocks 与 downstream_stocks 两个数组：
  - upstream_stocks = 掌握关键原料/材料供给的上游A股核心供应商（如磷化铟上游=云南锗业/有研新材/光智科技/南大光电）
  - downstream_stocks = 依赖被制裁市场/进口技术的下游A股应用公司（如光模块=中际旭创/新易盛/光迅科技/剑桥科技）
  - 两数组均不得为空；related_stocks 写两者的并集即可
- related_stocks 必须给出产业链上游/下游的具体A股公司名（优先核心供应商，如磷化铟上游=云南锗业/有研新材/光智科技/南大光电），不得只给板块名或仅覆盖单一环节
`

// hotTopicSystemPrompt 单条热点分析的 system 提示词：约束 LLM 输出严格 JSON 格式的评分/归因结果。
// （hotTopicSystemPrompt is the system prompt for single hot-topic analysis, forcing strict-JSON scoring/attribution output.）
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
  "upstream_stocks": ["上游产业链关联个股（具体核心供应商）"],
  "downstream_stocks": ["下游产业链关联个股（具体核心应用/终端）"],
  "strategy": "N形|龙头|双凸|龙回头|无",
  "reason": "简要分析理由",
  "region": "国内|海外",
  "relation": "对抗制裁|合作|不涉及",
  "upstream_direction": "利好|利空|中性",
  "downstream_direction": "利好|利空|中性"
}
只输出JSON，不要多余文字。

补充规则：
- 宏观数据走弱（GDP增速放缓/低于预期、PMI走弱或跌破荣枯线、核心通胀高企黏性、就业走弱）→ level="板块", event_type="宏观", score=-0.50~-0.75, direction="利空"
- 海外龙头公司（苹果/特斯拉/微软/英伟达等）财报或业绩指引不及预期、盘后大幅下跌，且涉及A股产业链（消费电子/苹果产业链/存储/算力/半导体等）→ level="板块", event_type="行业", score=-0.50~-0.75, direction="利空", sectors填对应A股产业链板块，不得按"海外行情播报"忽略
- 澄清/否认公告（"无参股X""无涉足X业务""不涉及X概念""XX与公司无关""目前不具备/暂无X计划"等否定性表态）→ 这是对炒作题材的否定性澄清，不等于利好；一律 score=0, sentiment="中性", direction="中性", event_type="公司"（如"达实智能：无参股宇树科技，无机器人业务"→ score=0 中性，严禁判利好）
` + valueChainSection

// batchSystemPrompt 批量热点分析的 system 提示词：从编号列表中筛选实质影响事件并输出 JSON 数组。
// （batchSystemPrompt is the system prompt for batch hot-topic analysis: filter substantive events from
// the numbered list and output a JSON array.）
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
    "upstream_stocks": ["上游产业链关联个股（具体核心供应商）"],
    "downstream_stocks": ["下游产业链关联个股（具体核心应用/终端）"],
    "strategy": "N形|龙头|双凸|龙回头|无",
    "reason": "简要分析理由",
    "region": "国内|海外",
    "relation": "对抗制裁|合作|不涉及",
    "upstream_direction": "利好|利空|中性",
    "downstream_direction": "利好|利空|中性"
  }
  ]
只输出JSON数组，不要多余文字。

补充规则：
- 宏观数据走弱（GDP增速放缓/低于预期、PMI走弱或跌破荣枯线、核心通胀高企黏性、就业走弱）→ level="板块", event_type="宏观", score=-0.50~-0.75, direction="利空"
- 海外龙头公司（苹果/特斯拉/微软/英伟达等）财报或业绩指引不及预期、盘后大幅下跌，且涉及A股产业链（消费电子/苹果产业链/存储/算力/半导体等）→ level="板块", event_type="行业", score=-0.50~-0.75, direction="利空", sectors填对应A股产业链板块，不得按"海外行情播报"忽略
- 澄清/否认公告（"无参股X""无涉足X业务""不涉及X概念""XX与公司无关""目前不具备/暂无X计划"等否定性表态）→ 这是对炒作题材的否定性澄清，不等于利好；一律 score=0, sentiment="中性", direction="中性", event_type="公司"（如"达实智能：无参股宇树科技，无机器人业务"→ score=0 中性，严禁判利好）
` + valueChainSection

// llmBatchSize LLM 单次批量处理的最大条数，防止超大批次导致超时。
// 推理模型（GLM-Z1-9B）对大批次首 token 极慢，30 条会 240s 超时等不到响应头，
// 调小到 10 条使单批在超时内完成（与 classifier.go 的 llmBatchSize 保持一致）。
// （llmBatchSize caps the per-call batch size to avoid timeouts on oversized batches. Reasoning models
// like GLM-Z1-9B are slow to produce the first token on large batches; shrinking to 10 per call keeps
// each batch within the timeout (kept in sync with classifier.go's llmBatchSize).）
const llmBatchSize = 10

// batchBounds 将 n 个元素按 size 分块，返回 [start,end) 区间列表。
// （batchBounds splits n items into size-sized chunks and returns the [start,end) ranges.）
func batchBounds(n, size int) [][2]int {
	var out [][2]int
	for start := 0; start < n; start += size {
		end := start + size
		if end > n {
			end = n
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// AnalyzeHotTopicBatch 批量分析多条新闻，按 llmBatchSize 分批并**并发**调用合并结果。
// 子批失败做隔离：该子批保留 nil 占位（不生成关键词兜底结果），不 abort 全批，
// 保证某几个坏子批不会拖垮整批 Stage2（主干继续）。
// 返回第三个值 failedIdx：LLM 重试耗尽失败的全局索引，调用方据此把对应新闻
// 留在未归因队列供下一轮重试，避免"LLM 偶发失败 = 该新闻永久丢失"。
// （AnalyzeHotTopicBatch analyzes many news items in batches of llmBatchSize, run **concurrently**.
// Sub-batch failures are isolated: the failed sub-batch is left as nil placeholders (no keyword-fallback
// results) instead of aborting the whole batch. The third return failedIdx lists the global indices that
// failed, so the caller can keep those news in the unattributed queue for the next round rather than
// permanently losing them to a transient LLM failure.）
func (c *Client) AnalyzeHotTopicBatch(titles []string) ([]*HotTopic, []int, error) {
	result := make([]*HotTopic, len(titles))
	if len(titles) == 0 {
		return result, nil, nil
	}

	concurrency := c.batchConcurrency
	if concurrency < 1 {
		concurrency = DefaultBatchConcurrency
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var failedMu sync.Mutex
	var failedIdx []int

	for _, b := range batchBounds(len(titles), llmBatchSize) {
		start, end := b[0], b[1]
		wg.Add(1)
		sem <- struct{}{}
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			sub, err := c.analyzeBatch(titles[start:end])
			if err != nil {
				log.Printf("LLM[%d] 子批%d..%d 重试队列用尽, 该子批%d条留待重试(nil占位, 主干继续): %v",
					len(titles), start+1, end, end-start, err)
				failedMu.Lock()
				for i := start; i < end; i++ {
					failedIdx = append(failedIdx, i)
				}
				failedMu.Unlock()
				return
			}
			copy(result[start:end], sub)
		}(start, end)
	}
	wg.Wait()
	return result, failedIdx, nil
}

// analyzeBatch 单批 LLM 批量分析（内部使用，批次规模 ≤ llmBatchSize）。
// API 失败 与 JSON 解析失败 都纳入重试队列（最多5次：2s/4s/8s/16s/30s），
// 仍失败返回错误；由 AnalyzeHotTopicBatch 做子批隔离（只丢本子批，不影响主干）。
// （analyzeBatch runs one batch of LLM analysis (internal use, batch size ≤ llmBatchSize). Both API
// failures and JSON-parse failures enter a retry queue (up to 5 times: 2s/4s/8s/16s/30s); if it still
// fails it returns an error, and AnalyzeHotTopicBatch isolates the sub-batch (only this sub-batch is lost).）
func (c *Client) analyzeBatch(titles []string) ([]*HotTopic, error) {
	// 构建批量请求文本
	var sb strings.Builder
	for i, t := range titles {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
	}
	prompt := sb.String()

	// 轮询重试（最多5次、间隔递增 2s/4s/8s/16s/30s）：调用失败或解析失败均重试
	const maxAttempts = 5
	var raw []stage2Row
	var lastErr error
	ok := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.Chat(batchSystemPrompt, prompt)
		if err == nil {
			resp = cleanJSON(resp)
			raw, err = parseHotTopicBatch(resp)
			if err == nil {
				ok = true
				break
			}
		}
		lastErr = err
		log.Printf("LLM[%d] 调用/解析失败(第%d/%d次): %v", len(titles), attempt, maxAttempts, err)
		if attempt < maxAttempts {
			time.Sleep(jitterBackoff(time.Duration(1<<uint(attempt)) * time.Second))
		}
	}
	if !ok {
		log.Printf("LLM[%d] 重试队列用尽仍失败, 该批%d条丢弃: %v", len(titles), len(titles), lastErr)
		return nil, lastErr
	}

	// 日志：LLM返回了哪些板块和个股
	for _, r := range raw {
		sectors := strings.Join(r.Sectors, ",")
		stocks := strings.Join(r.RelatedStocks, ",")
		idx := int(r.Index) - 1
		title := ""
		if idx >= 0 && idx < len(titles) {
			title = titles[idx][:minInt(len(titles[idx]), 30)]
		}
		log.Printf("LLM打标: %s → 方向=%s 板块=[%s] 个股=[%s]", title, r.Direction, sectors, stocks)
	}

	result := make([]*HotTopic, len(titles))
	for i, title := range titles {
		// 以空结构初始化（不依赖关键词兜底），再按 LLM 返回序号覆盖对应字段
		// （未命中字段保留空值/默认值，LLM 明确给出的字段一律采用）。
		ht := &HotTopic{Title: title}
		for _, r := range raw {
			if int(r.Index) == i+1 {
				ht.Level = r.Level
				ht.Sentiment = r.Sentiment
				ht.Score = float64(r.Score)
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
				if len(r.UpstreamStocks) > 0 {
					ht.UpstreamStocks = r.UpstreamStocks
				}
				if len(r.DownstreamStocks) > 0 {
					ht.DownstreamStocks = r.DownstreamStocks
				}
				if r.Strategy != "" {
					ht.Strategy = r.Strategy
				}
				if r.Reason != "" {
					ht.Reason = r.Reason
				}
				if r.Region != "" {
					ht.Region = r.Region
				}
				if r.Relation != "" {
					ht.Relation = r.Relation
				}
				if r.UpstreamDirection != "" {
					ht.UpstreamDirection = r.UpstreamDirection
				}
				if r.DownstreamDirection != "" {
					ht.DownstreamDirection = r.DownstreamDirection
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
//   - *HotTopic: 分析结果（API 失败/解析失败时返回 nil，不生成关键词兜底结果）
//   - error: API 调用或 JSON 解析的错误（非 nil 表示分析失败）
//
// （AnalyzeHotTopic runs multi-dimensional hot-topic analysis on a news title.
// Returns:
//   - *HotTopic: the analysis result (nil on API/parse failure — no keyword-fallback result is fabricated)
//   - error: API call or JSON-parse error (non-nil means the analysis failed)）
func (c *Client) AnalyzeHotTopic(title string) (*HotTopic, error) {
	// 轮询重试（最多5次、间隔递增 2s/4s/8s/16s/30s），与批量路径一致
	const maxAttempts = 5
	var resp string
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err = c.Chat(hotTopicSystemPrompt, title)
		if err == nil {
			break
		}
		if attempt < maxAttempts {
			log.Printf("LLM API失败(第%d次), 轮询重试(%s): %v", attempt, title[:minInt(len(title), 30)], err)
			time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
		}
	}
	if err != nil {
		log.Printf("LLM API调用失败(%s), 轮询%d次仍失败, 返回nil(由调用方入重试队列): %v", title[:minInt(len(title), 30)], maxAttempts, err)
		return nil, err
	}

	resp = cleanJSON(resp)

	var ht HotTopic
	ht.Title = title
	if err := json.Unmarshal([]byte(resp), &ht); err != nil {
		log.Printf("LLM JSON解析失败(%s), 返回nil(由调用方入重试队列): %s", title[:minInt(len(title), 30)], resp[:minInt(len(resp), 100)])
		return nil, err
	}

	// 空字段补默认值，保证下游字段齐整
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

// Ping 发送最小请求验证 LLM 通道（API Key / 网络 / 上游服务）可用性。
// 使用极小的非流式请求（max_tokens=1），成功返回 nil；失败返回上游错误。
// 供启动时序在进入盘前新闻分析前快速探活，尽早暴露 key 失效/断网问题。
// （Ping sends a minimal request to verify the LLM channel (API key / network / upstream service).
// It uses a tiny non-streaming request (max_tokens=1); nil on success, otherwise the upstream error.
// The startup sequence pings before pre-market news analysis to surface key/network issues early.）
func (c *Client) Ping() error {
	if len(c.apiKeys) == 0 {
		return fmt.Errorf("LLM_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := chatCompletionRequest{
		ChatRequest: ChatRequest{
			Model: c.model,
			Messages: []Message{
				{Role: "user", Content: "好"},
			},
		},
		Stream:    false,
		MaxTokens: 1,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("LLM API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// AnalyzeSentiment 简版情感分析（用于快速评分）。
// （AnalyzeSentiment is a lightweight sentiment analysis for quick scoring.）
// AnalyzeSentiment 单条文本情感打分（0~1）。失败/解析失败返回错误，不伪造中性分。
// （AnalyzeSentiment scores a single text's sentiment (0~1). On failure/parse error it returns an error —
// no fabricated neutral score.）
func (c *Client) AnalyzeSentiment(text string) (float64, error) {
	resp, err := c.Chat(
		"你是一个A股新闻情感分析师。只输出一个0-1之间的数字，0=极负面，0.5=中性，1=极正面。不要多余文字。",
		text,
	)
	if err != nil {
		return 0, err
	}
	resp = strings.TrimSpace(resp)
	var score float64
	if _, e := fmt.Sscanf(resp, "%f", &score); e == nil && score >= 0 && score <= 1 {
		return score, nil
	}
	return 0, fmt.Errorf("LLM情感分解析失败: %q", resp[:minInt(len(resp), 100)])
}

// AnalyzeNews 兼容旧接口（内部调用新分析）。
// （AnalyzeNews is a legacy-compatible wrapper that internally calls the new analysis.）
func (c *Client) AnalyzeNews(text string) (string, error) {
	ht, err := c.AnalyzeHotTopic(text)
	if err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(ht, "", "  ")
	return string(data), nil
}

// SentimentScore 旧版情感分数接口（原为关键词兜底，现已不再兜底：失败返回错误，由调用方处理重试）。
// （SentimentScore is the legacy sentiment-score interface — keyword fallback removed; on failure it
// returns an error for the caller to route into the LLM retry queue.）
func (c *Client) SentimentScore(text string) (float64, error) {
	resp, err := c.AnalyzeHotTopic(text)
	if err != nil {
		return 0, err
	}
	return resp.Score, nil
}

// cleanJSON 清理 LLM 返回的原始文本，使其能被 json.Unmarshal 正确解析。
// 1. 去掉 markdown 代码块（```json / ```）——很多 LLM 会用代码块包裹结构化输出。
// 2. 提取 JSON 数组边界（第一个 [ 到最后一个 ]）——有些推理模型（如 GLM-Z1）会在 JSON 前输出思考/推理过程文本。
// 3. 移除尾部多余的 . , ; 等非法字符——部分模型在 JSON 结尾后随手加上了句号或逗号。
// 注意：单条分析会正常解析，批量分析也会从整体中正确截取数组部分。
// （cleanJSON sanitizes the raw LLM output so json.Unmarshal can parse it: (1) strips markdown code
// fences, (2) extracts the JSON array bounds (first [ to last ]) to drop stray reasoning text some
// reasoning models emit before the JSON, and (3) trims trailing illegal chars like . , ; .）
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 提取 JSON 主体：LLM 可能输出单个对象（HotTopic）或数组（D1 评分等）。
	// 按首字符区分：'{' 提取首个 { 到末尾 } 之间；'[' 提取首个 [ 到末尾 ] 之间。
	// 若对象内嵌数组（如 "sectors":["机器人"]）时按第一个 [ 截取会把对象前半截切掉，
	// 故必须按最外层括号类型提取。
	// English: extract the JSON body — LLM may emit a single object (HotTopic) or an array
	// (D1 scoring etc.). Use the first non-space char to choose delimiters: '{' → first { to last };
	// '[' → first [ to last ]. Slicing at the first '[' would cut off an object whose fields hold
	// arrays (e.g. "sectors":["机器人"]), so we must match the outermost bracket kind.
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
	// 移除尾部的非法字符（如句号、逗号）只保留 JSON 部分
	s = strings.TrimRight(s, ".,; ")
	// 清理非法 '+' 前缀数值：部分小模型输出 "score": +0.75 或 "score": 0.5（裸 + 号），
	// JSON 数字不允许 '+' 前缀，这里把冒号/逗号/左括号后的 '+' 剥掉（不影响字符串内的 '+'）。
	s = plusNumberRe.ReplaceAllString(s, "$1 ")
	// 转义字符串值中的换行符（JSON 不允许字符串内未转义的 \n）
	// 并清理非法转义：9B 推理模型常在字符串里输出 \( \) 等非法 JSON 转义，
	// 遇到反斜杠后跟非合法转义集（" \ / b f n r t u）的字符时，丢弃反斜杠保留原字符。
	var buf strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			next := s[i+1]
			if !isValidJSONEscape(next) {
				// 非法转义（如 \( \））→ 只保留原字符，丢弃反斜杠
				buf.WriteByte(next)
				i++
				continue
			}
			// 合法转义（\" \\ \/ \b \f \n \r \t \u）→ 原样保留，跳过下一字节
			// 转义的引号不切换 inStr 状态（\" 不会被视为字符串结束符）
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

// isValidJSONEscape 判断字节是否为合法 JSON 转义字符（反斜杠后的有效转义序列首字符）。
// （isValidJSONEscape reports whether b is the leading char of a valid JSON escape sequence.）
func isValidJSONEscape(b byte) bool {
	switch b {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}

// plusNumberRe 匹配冒号/逗号/左括号后的 '+' 前缀（数值位置），用于剥离非法 '+'。
// （plusNumberRe matches a '+' prefix after a colon/comma/left bracket (number positions) to strip illegal '+'.）
var plusNumberRe = regexp.MustCompile(`([:,\[])\s*\+`)

// stage2Row Stage2 批量返回的单行（容错：index 兼容字符串）。
// （stage2Row is one row of the Stage2 batch response (fault-tolerant: index also accepts strings).）
type stage2Row struct {
	Index               flexInt       `json:"index"`
	Level               string        `json:"level"`
	Sentiment           string        `json:"sentiment"`
	Score               flexibleFloat `json:"score"`
	ImpactLevel         string        `json:"impact_level"`
	EventType           string        `json:"event_type"`
	Urgency             string        `json:"urgency"`
	Direction           string        `json:"direction"`
	Sectors             []string      `json:"sectors"`
	UpstreamSectors     []string      `json:"upstream_sectors"`
	DownstreamSectors   []string      `json:"downstream_sectors"`
	RelatedStocks       []string      `json:"related_stocks"`
	UpstreamStocks      []string      `json:"upstream_stocks"`
	DownstreamStocks    []string      `json:"downstream_stocks"`
	Strategy            string        `json:"strategy"`
	Reason              string        `json:"reason"`
	Region              string        `json:"region"`
	Relation            string        `json:"relation"`
	UpstreamDirection   string        `json:"upstream_direction"`
	DownstreamDirection string        `json:"downstream_direction"`
}

// flexInt 兼容 JSON 中整数为数字或字符串（1 / "1"）的解析。
// （flexInt parses integers that may be numbers or strings in JSON (1 / "1").）
type flexInt int

// UnmarshalJSON 实现 json.Unmarshaler：数字或字符串（允许 + 前缀/空白）都能解析。
// （UnmarshalJSON implements json.Unmarshaler, accepting numbers or quoted-strings; parsed to int,
// defaulting to 0 on parse failure.）
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		*f = 0
	}
	*f = flexInt(v)
	return nil
}

// parseHotTopicBatch 两段式解析 Stage2 批量响应：整体数组解析失败 → 逐对象扫描抢救。
// 单坏对象只丢该条；整体+逐对象都抢救不出才返回错误（触发重试队列）。
// （parseHotTopicBatch parses the Stage2 batch response in two passes: a whole-array parse, then a
// per-object salvage scan if that fails. A single bad object only drops that item; an error is returned
// only if both passes fail (triggering the retry queue).）
func parseHotTopicBatch(resp string) ([]stage2Row, error) {
	var raw []stage2Row
	if err := json.Unmarshal([]byte(resp), &raw); err == nil {
		return raw, nil
	}
	var out []stage2Row
	for _, obj := range extractObjects(resp) {
		obj = strings.ReplaceAll(obj, `':''`, `":"`)
		obj = llmTrailingJunkRe.ReplaceAllString(obj, `"$1`)
		obj = llmEmptyValueRe.ReplaceAllString(obj, `$1""`)
		var one stage2Row
		if err := json.Unmarshal([]byte(obj), &one); err == nil {
			out = append(out, one)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("批次JSON整体解析失败且逐对象抢救无效")
	}
	log.Printf("LLM[%d] 整体解析失败, 逐对象抢救成功 %d 条", len(out), len(out))
	return out, nil
}

// extractObjects 用花括号配对扫描提取字符串中所有独立 JSON 对象 `{...}`（含嵌套、无视排版）。
// （extractObjects scans the string with brace-pair matching to extract every standalone JSON object
// `{...}` (including nested ones, ignoring whitespace/layout).）
func extractObjects(s string) []string {
	var objs []string
	start := -1
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				objs = append(objs, s[start:i+1])
				start = -1
			}
		}
	}
	return objs
}

// llmEmptyValueRe / llmTrailingJunkRe 与 newsagent 的修复规则同源，修复模型畸形输出。
// （llmEmptyValueRe / llmTrailingJunkRe share their origin with newsagent's fix rules for malformed model output.）
var llmEmptyValueRe = regexp.MustCompile(`("(?:[^"\\]|\\.)*"\s*:)\s*[}\]]`)
var llmTrailingJunkRe = regexp.MustCompile(`"\s*[\)']+\s*([,}\]]|$)`)

// flexibleFloat 兼容 JSON 中字段为数字或字符串（如 "0.75" / "+0.75"）的浮点解析。
// 部分小模型会把数值输出成带符号字符串，导致标准 json.Unmarshal 失败，这里做容错。
// （flexibleFloat parses floats that may be numbers or strings in JSON (e.g. "0.75" / "+0.75"). Some
// small models emit signed string numbers, failing standard json.Unmarshal; this adds tolerance.）
type flexibleFloat float64

// UnmarshalJSON 实现 json.Unmarshaler：数字或字符串（允许 + 前缀/空白）都能解析。
// （UnmarshalJSON implements json.Unmarshaler, accepting numbers or strings (allowing a + prefix/whitespace).）
func (f *flexibleFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" {
		*f = 0
		return nil
	}
	s = strings.TrimPrefix(s, "+")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// 解析失败时按 0 处理，避免整批 JSON 因单个坏值被丢弃
		*f = 0
		return nil
	}
	*f = flexibleFloat(v)
	return nil
}

// minInt 返回两个整数中的较小值（用于截断日志输出长度）。
// （minInt returns the smaller of two ints (used to truncate log output length).）
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SectorTag 解析后的板块标签，含置信度权重。
// （SectorTag is a parsed sector tag with a confidence weight.）
type SectorTag struct {
	Name       string  // 板块名
	Confidence float64 // 置信度 0~1（无后缀时=1.0）
}

// ParseSectors 解析 LLM 返回的 sectors 列表。
// 格式1: "固态电池" → {Name:"固态电池", Confidence:1.0}
// 格式2: "固态电池(0.8)" → {Name:"固态电池", Confidence:0.8}
// 复合: "半导体(1.0)/芯片(0.7)" → split后分别解析
// （ParseSectors parses the sectors list returned by the LLM. Format 1: "固态电池" → Confidence 1.0;
// format 2: "固态电池(0.8)" → Confidence 0.8; compound "半导体(1.0)/芯片(0.7)" → split and parse each.）
func ParseSectors(sectors []string) []SectorTag {
	var result []SectorTag
	re := regexp.MustCompile(`^(.+?)\(([\d.]+)\)$`)
	for _, s := range sectors {
		// 兼容 "/" 分隔的复合格式，逐段解析
		for _, part := range strings.Split(s, "/") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			st := SectorTag{Name: part, Confidence: 1.0}
			// 形如 "固态电池(0.8)" 时提取名称与置信度，解析失败回退默认 1.0
			if m := re.FindStringSubmatch(part); len(m) == 3 {
				st.Name = strings.TrimSpace(m[1])
				if f, err := fmt.Sscanf(m[2], "%f", &st.Confidence); err != nil || f != 1 {
					st.Confidence = 1.0
				}
				// 置信度钳制到 [0,1]
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
// （StockCodeMap is the hardcoded stock-name-to-code mapping, independent of the LLM prompt format—pure post-processing.）
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
// （ResolveStocks parses the stocks list from the LLM into stock codes. Each element is first split by
// "/" to handle compound formats (e.g. "中芯国际/北方华创/韦尔股份"). Priority: hardcoded table >
// regex (XXXXXX) > bare 6-digit code with auto suffix. Returns (resolved codes, unresolved names).）
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
// （autoSuffix appends the exchange suffix by the first digit: 6/9 → .SH, 0/3/2 → .SZ, 4/8 → .BJ.）
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
// （isAlphaNumeric reports whether s consists only of letters and digits.）
func isAlphaNumeric(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
