// §PROD-LLM2（2026-09-18 生产实录）：咨询/D1/复盘全停于裸错误 "no response from LLM"。
// 本文件锁定两项修复的行为：①content 全空时以 reasoning_content 兜底（流式与非流式两路）；
// ②空响应错误必须携带诊断证据（分片数/choices 数、finish_reason、usage、原始响应摘录）。
// English: §PROD-LLM2 locks the two fixes: reasoning_content fallback when content is empty
// (stream and non-stream), and empty-response errors carrying finish_reason/usage/raw excerpts.
package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamChatReasoningOnlyFallback 流式只有 reasoning_content、content 全空 → 用思维链正文兜底成功。
func TestStreamChatReasoningOnlyFallback(t *testing.T) {
	var hits int
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"","reasoning_content":"这只票"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"","reasoning_content":"还有空间"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	got, err := c.Chat("system", "user")
	if err != nil {
		t.Fatalf("仅思维链应答应兜底成功: %v", err)
	}
	if got != "这只票还有空间" {
		t.Fatalf("兜底正文应为 reasoning_content 拼接, 实际 %q", got)
	}
	if hits != 1 {
		t.Fatalf("兜底成功不应再回落非流式, 请求次数=%d", hits)
	}
}

// TestStreamChatEmptyCarriesEvidence content 与 reasoning 皆空 → 错误保留 "no response from LLM"
// 前缀（§FIX-0921 回落判定依赖），并携带 finish_reason/分片数等诊断证据。
func TestStreamChatEmptyCarriesEvidence(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	_, err := c.Chat("system", "user")
	if err == nil {
		t.Fatal("全空流式应答应报错")
	}
	msg := err.Error()
	for _, want := range []string{"no response from LLM", "finish_reason", `"stop"`, "data分片=1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误应含诊断证据 %q, 实际: %s", want, msg)
		}
	}
}

// TestNonStreamEmptyChoicesEvidence choices 为空的 200 响应 → 错误携带 usage 与原始响应摘录。
func TestNonStreamEmptyChoicesEvidence(t *testing.T) {
	srv := newJSONServer(t, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":0,"total_tokens":100},"vendor_note":"quota window exceeded"}`)
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	_, err := c.Chat("system", "user")
	if err == nil {
		t.Fatal("空 choices 应报错")
	}
	msg := err.Error()
	for _, want := range []string{"no response from LLM", "choices=0", "prompt=100", "quota window exceeded"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误应含证据 %q, 实际: %s", want, msg)
		}
	}
}

// TestNonStreamReasoningFallback 非流式 message.content 空、reasoning_content 有正文 → 兜底成功。
func TestNonStreamReasoningFallback(t *testing.T) {
	srv := newJSONServer(t, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"结论：等企稳"},"finish_reason":"stop"}]}`)
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	got, err := c.Chat("system", "user")
	if err != nil {
		t.Fatalf("仅 reasoning 正文应兜底成功: %v", err)
	}
	if got != "结论：等企稳" {
		t.Fatalf("应返回 reasoning 正文, 实际 %q", got)
	}
}

// TestNonStreamEmptyContentEvidence content 与 reasoning 皆空 → 报错带 finish_reason 证据。
func TestNonStreamEmptyContentEvidence(t *testing.T) {
	srv := newJSONServer(t, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`)
	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: false, Timeout: 5 * time.Second})
	_, err := c.Chat("system", "user")
	if err == nil {
		t.Fatal("空正文应报错")
	}
	msg := err.Error()
	for _, want := range []string{"no response from LLM", "content_filter", "content为空"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误应含证据 %q, 实际: %s", want, msg)
		}
	}
}

// newJSONServer 固定 JSON 200 应答的测试服务器（非流式路径用）。
func newJSONServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
