// Package llm 提供大语言模型（LLM）客户端配置与调用功能。（Package llm provides LLM client configuration and invocation.）
package llm

import "time"

// Config LLM（大语言模型）客户端配置。
// 包含 API 密钥、请求地址、模型名称和请求超时。
// （Config is the LLM client configuration: API key, request URL, model name and per-request timeout.）
type Config struct {
	APIKey  string        // API 密钥，用于认证
	APIKeys []string      // 多 API 密钥（并发请求按 key 轮询分发，突破单 key 限流；为空时回退 APIKey）
	APIURL  string        // API 请求地址（如 https://api.openai.com/v1/chat/completions）
	Model   string        // 模型名称（如 gpt-4、deepseek-chat 等）
	Timeout time.Duration // 单次请求超时（<=0 时 New 兜底为 60s）

	// Streaming 是否启用流式（SSE）响应。默认开启；推理模型（GLM-Z1 等）在非流式下
	// 需等整段含思维链生成完毕才返回响应头，易误判"等待响应头超时"。
	// Streaming=false 时回落到非流式一次性取回，供不支持 SSE 的上游兜底。
	// （Streaming enables SSE responses, on by default; false falls back to one-shot non-streaming.）
	Streaming bool
	// StreamIdleTimeout 流式下"相邻数据分片"的空闲阈值：超过则认为模型卡死（区别于
	// 仍在思维链输出的心跳），返回错误交由上层走重试队列。<=0 时 New 兜底为 60s。
	// （StreamIdleTimeout is the idle threshold between stream chunks; exceeding it means the model is stuck.）
	StreamIdleTimeout time.Duration

	// BatchConcurrency LLM 批量分析（Stage0/Stage2 分批）的最大并发批次数量。
	// <=0 时 New 兜底为 8。API 配额充足时可调高以加快盘前新闻归因吞吐。
	// （BatchConcurrency caps how many LLM batch calls (Stage0/Stage2 chunked analysis) run concurrently;
	// <=0 defaults to 8. Raise it when API quota allows to speed premarket news attribution.）
	BatchConcurrency int

	// ClassifierModel 可选的新闻归因分类专用模型（Stage0/1 合并调用等"快速分类/初筛"场景）。
	// 配置轻量/快速模型可显著加快分类吞吐；留空则与主模型一致，行为不变。
	// （ClassifierModel is an optional dedicated model for news-attribution classification (Stage0/1
	// combined calls and other cheap classification/screening). A lighter/faster model here speeds up
	// classification throughput; when empty, the main model is used and behavior is unchanged.）
	ClassifierModel string

	// §GAP5.1 成本治理：当日调用次数 / token 总量预算（0=不设限）。任一超限后当日
	// 所有新请求被熔断拒绝（次日自动恢复），杜绝 LLM 账单失控。
	// English: §GAP5.1 cost governance — daily call/token budgets (0 = unlimited); once exceeded,
	// new requests are rejected until the next day.
	DailyCallBudget  int64
	DailyTokenBudget int64
}
