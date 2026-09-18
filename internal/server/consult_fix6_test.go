// consult_fix6_test.go — §FIX-6/§FIX-10(20260919 批四) 服务端回归：
// ① bodyLimit 全局 64KB 请求体闸（已知 ContentLength 超限 → 413；chunked 越限 → 解码失败 400）；
// ② 咨询消息长度闸（>2000 字 → 400 中文文案；纯空白按空处理）；
// ③ 引擎侧账号隔离不可用（ErrStoreUnavailable 经 %w 链）→ 503，与上游故障 500 分流。
// English: server-side regressions for the global 64KB body cap, the 2000-rune consult
// message limit, and mapping ErrStoreUnavailable to 503 (distinct from upstream 500).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"quant-trading-v2/internal/data"
)

// fix6Ctrl 仅接管"直通"用例：固定回复，捕获收到的消息以便断言 TrimSpace 生效。
// English: a passthrough fake that returns a fixed reply and captures the incoming message.
type fix6Ctrl struct {
	EngineController
	err error // 非 nil 时 ConsultLLM 直接返回该错误
}

func (f *fix6Ctrl) ConsultLLM(ctx context.Context, userID, userMsg string, proMode bool) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "答复", nil
}

// TestBodyLimitContentLengthTooLarge §FIX-6：已知 ContentLength 超 64KB → 413 中文错误，且不读 body。
// English: known ContentLength over 64KB → 413 with a Chinese error, body never read.
func TestBodyLimitContentLengthTooLarge(t *testing.T) {
	var readBody bool
	h := bodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		readBody = true
		w.WriteHeader(200)
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(strings.Repeat("a", maxBodyBytes+1)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "64KB") {
		t.Fatalf("413 文案应说明 64KB 上限: %s", rec.Body.String())
	}
	if readBody {
		t.Fatal("413 拒绝路径不应把 body 读进内存")
	}
}

// TestBodyLimitChunkedOverflow §FIX-6：ContentLength 未知（chunked 形态）时由 MaxBytesReader
// 在读取越限处令解码失败——经 handleConsult 表现为 400 invalid request body，而非 OOM。
// English: with unknown ContentLength, MaxBytesReader fails the read past the cap; through
// handleConsult that surfaces as 400 (decode error), never a memory blow-up.
func TestBodyLimitChunkedOverflow(t *testing.T) {
	s := newConsultServer(t, &fix6Ctrl{})
	// 合法 JSON 前缀 + 超长填充：整体 > 64KB，但只有解码器读到才会发现越限。
	payload := `{"message":"` + strings.Repeat("字", 40000) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(payload))
	r.ContentLength = -1 // 模拟 chunked：bodyLimit 只能挂 MaxBytesReader 兜底
	rec := httptest.NewRecorder()
	s.chain(http.HandlerFunc(s.handleConsult)).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("chunked 越限应解码失败 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBodyLimitPassesNormalBody §FIX-6 反向护栏：64KB 以内的小 body 必须原样透传。
// English: guard the happy path—bodies within 64KB pass through untouched.
func TestBodyLimitPassesNormalBody(t *testing.T) {
	var got string
	h := bodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = string(raw)
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(`{"message":"ok"}`))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != `{"message":"ok"}` {
		t.Fatalf("正常 body 应完整透传, got %q", got)
	}
}

// consultBody 构造指定消息内容的咨询请求（经同一 Test 辅助入口走 handleConsult）。
// English: build a consult request with the given raw message JSON.
func consultBody(t *testing.T, message string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(string(raw)))
	r.RemoteAddr = "10.0.0.1:1234"
	return r
}

// TestConsultMessageLengthCap §FIX-6：2001 字 → 400 中文文案；恰好 2000 字 → 放行。
// English: 2001 runes → 400 with Chinese copy; exactly 2000 runes passes the gate.
func TestConsultMessageLengthCap(t *testing.T) {
	s := newConsultServer(t, &fix6Ctrl{})
	long := strings.Repeat("字", consultMessageMaxRunes+1)
	if utf8.RuneCountInString(long) != consultMessageMaxRunes+1 {
		t.Fatal("用例前提：payload 恰为上限+1 个 rune")
	}
	rec := doConsult(s, consultBody(t, long))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超限消息应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "上限 2000 字") {
		t.Fatalf("400 文案应含上限说明: %s", rec.Body.String())
	}
	ok := doConsult(s, consultBody(t, strings.Repeat("字", consultMessageMaxRunes)))
	if ok.Code != http.StatusOK {
		t.Fatalf("恰好 2000 字应放行, got %d body=%s", ok.Code, ok.Body.String())
	}
}

// TestConsultWhitespaceMessageRejected §FIX-6：纯空白消息 TrimSpace 后按空拒绝。
// English: whitespace-only messages are trimmed and rejected as empty.
func TestConsultWhitespaceMessageRejected(t *testing.T) {
	s := newConsultServer(t, &fix6Ctrl{})
	rec := doConsult(s, consultBody(t, "  \n\t 　 "))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "message required") {
		t.Fatalf("纯空白应 400 message required, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestConsultStoreUnavailableMapsTo503 §FIX-10：隔离存储不可用（%w 链）→ 503 可重试语义，
// 不与上游 5xx 故障（500）混淆。
// English: ErrStoreUnavailable through the %w chain maps to 503 (retryable), distinct from 500.
func TestConsultStoreUnavailableMapsTo503(t *testing.T) {
	s := newConsultServer(t, &fix6Ctrl{err: fmt.Errorf("咨询: %w", data.ErrStoreUnavailable)})
	rec := doConsult(s, consultRequest("u_iso"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("隔离未就绪应 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(body["error"], "串号") {
		t.Fatalf("503 响应应含防串号文案: %s", rec.Body.String())
	}
}
