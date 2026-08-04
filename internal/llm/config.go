// Package llm 提供大语言模型（LLM）客户端配置与调用功能。
package llm

import "time"

// Config LLM（大语言模型）客户端配置。
// 包含 API 密钥、请求地址、模型名称和请求超时。
type Config struct {
	APIKey string        // API 密钥，用于认证
	APIURL string        // API 请求地址（如 https://api.openai.com/v1/chat/completions）
	Model  string        // 模型名称（如 gpt-4、deepseek-chat 等）
	Timeout time.Duration // 单次请求超时（<=0 时 New 兜底为 60s）
}
