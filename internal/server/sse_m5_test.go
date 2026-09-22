// ── §M5（2026-09-22 PM 批）SSE 续传契约回归 ──
// 锁死两件事：① 建链票据在 60s TTL 内可复用——旧「消费即废」语义下，浏览器原生重连
//
//	（只会原样重发同一 URL）必然 401，唯一携带 Last-Event-ID 请求头的补发通道被掐死；
//
// ② handleFixSSE 收 `?last_event_id=` query 作为补发续读位置（请求头缺席时；头优先），
//
//	手动换票重建的新 EventSource 附加不了请求头，query 是这条路径唯一可达的补发入口。
//
// English: §M5 regression — the ticket stays valid within its TTL (native reconnect replays the
// same URL), and handleFixSSE accepts the resume position as a query when the header is absent.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSETicketReusableWithinTTL(t *testing.T) {
	s := &Server{}
	tk := s.newSSETicket("u1")
	// 同一张票连用两次都必须命中同一账号（原生重连/重建不换票的语义基础）
	for i := 0; i < 2; i++ {
		uid, ok := s.useSSETicket(tk)
		if !ok || uid != "u1" {
			t.Fatalf("第 %d 次 TTL 内复用应通过并绑定 u1，got uid=%q ok=%v", i+1, uid, ok)
		}
	}
	if _, ok := s.useSSETicket("bogus-ticket"); ok {
		t.Fatal("伪造票据必须拒绝")
	}
	// 过期票：拒绝，且校验时顺手清除，不再滞留票据池
	s.sseTicketsMu.Lock()
	v := s.sseTickets[tk]
	v.expireAt = time.Now().Add(-time.Second)
	s.sseTickets[tk] = v
	s.sseTicketsMu.Unlock()
	if _, ok := s.useSSETicket(tk); ok {
		t.Fatal("过期票据必须拒绝（可复用≠永久有效）")
	}
	s.sseTicketsMu.Lock()
	_, still := s.sseTickets[tk]
	s.sseTicketsMu.Unlock()
	if still {
		t.Fatal("过期票据未在校验时被清除，票据池会无界膨胀")
	}
}

// sseStreamBodyWithResume 以 ticket(+可选 header/query 续读载体) 跑一次 handleFixSSE，
// 在超时上下文里收一段流后返回响应体（含补发的 `id:`/`data:` 帧）。
func sseStreamBodyWithResume(t *testing.T, s *Server, ticket, headerLastID, queryLastID string) string {
	t.Helper()
	target := "/api/events?ticket=" + ticket
	if queryLastID != "" {
		target += "&last_event_id=" + queryLastID
	}
	req := httptest.NewRequest("GET", target, nil)
	if headerLastID != "" {
		req.Header.Set("Last-Event-ID", headerLastID)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	s.handleFixSSE(rec, req.WithContext(ctx))
	return rec.Body.String()
}

func TestSSEQueryLastEventIDReplaysRing(t *testing.T) {
	s := &Server{sse: NewSSEBroker()}
	s.sseTickets = make(map[string]sseTicket)
	for i := 1; i <= 3; i++ {
		s.sse.BroadcastTo("u1", map[string]interface{}{"type": "signal", "n": i})
	}
	tk := s.newSSETicket("u1")

	// ① 无续读载体：只收 priming/心跳，不补发任何历史事件
	body := sseStreamBodyWithResume(t, s, tk, "", "")
	if strings.Contains(body, "id: ") {
		t.Fatalf("未带续读位置不应补发历史，body=%s", body)
	}

	// ② query 形态（手动重建路径）：last_event_id=1 → 只补发 2、3
	body = sseStreamBodyWithResume(t, s, tk, "", "1")
	for _, want := range []string{"id: 2\n", "id: 3\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("query 续传缺补发帧 %q，body=%s", want, body)
		}
	}
	if strings.Contains(body, "id: 1\n") {
		t.Errorf("已收到的 1 号事件被重复补发，body=%s", body)
	}
	if got := fmt.Sprint(sseSignalSeqs(body)); got != "[2 3]" {
		t.Errorf("query 续传的补发载荷序列应为 [2 3]，got %s（body=%s）", got, body)
	}

	// ③ 请求头优先：header=2 压过 query=1 → 只补发 3（保持 SSE 原生协议语义）
	body = sseStreamBodyWithResume(t, s, tk, "2", "1")
	if !strings.Contains(body, "id: 3\n") || strings.Contains(body, "id: 2\n") {
		t.Errorf("Last-Event-ID 头未优先于 query，body=%s", body)
	}
	if got := fmt.Sprint(sseSignalSeqs(body)); got != "[3]" {
		t.Errorf("header 优先时的补发载荷序列应为 [3]，got %s（body=%s）", got, body)
	}
}

// sseSignalSeqs 从一段 SSE 流文本里按序提取 data 帧的 n 字段（补发载荷断言用）。
func sseSignalSeqs(body string) []int {
	var out []int
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err == nil {
			if n, ok := m["n"].(float64); ok {
				out = append(out, int(n))
			}
		}
	}
	return out
}
