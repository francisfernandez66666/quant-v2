package server

// w2b_sse_query_token_deadline_test.go —— §0926E2E-W2B（2026-09-26 二波）：
// SSE `?token=` 遗留通道退场闸的行为用例 + 截止常量等值锁。
//
// 缺陷背景（全量审计缺陷 9）：票据通道自 §WS-F C4a 起已是主路，但 handleFixSSE 的
// query token 回退分支永不过期——长期凭证一旦经 access log / 浏览器历史泄漏即可无限期
// 订阅账号事件流。本批给它装上截止闸（2026-10-15 00:00 CST），并在登录响应里下发退役日
// 给旧客户端留一个版本周期的升级提示。
//
// 判据按运行时真实取值链（§探针纪律）：截止闸只动"回退分支的放行窗口"，
// 票据主路、缺参 401、无效 token 401 三类既有语义都必须原样保持（反证用例逐条钉住）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
)

// w2bSSEServer 构造能跑 handleFixSSE 的最小服务器：真实 auth（建一个带长期 token 的用户）
// + SSE broker + 票据池。返回服务器与合法长期 token。
func w2bSSEServer(t *testing.T) (*Server, string) {
	t.Helper()
	mgr := auth.NewManager(t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth.Init: %v", err)
	}
	u, err := mgr.CreateUser("w2b", "pw12345678", auth.RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	s := &Server{auth: mgr, sse: NewSSEBroker(), sseTickets: make(map[string]sseTicket)}
	return s, u.Token
}

// w2bStream 带超时上下文跑一次 GET /api/events，返回 recorder（流会被 ctx 到期截断）。
func w2bStream(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	s.handleFixSSE(rec, req.WithContext(ctx))
	return rec
}

// w2bSetDeadline 临时改截止常量并在用例结束后恢复（包级 var 就是为时钟注入留的缝）。
func w2bSetDeadline(t *testing.T, d time.Time) {
	t.Helper()
	old := sseQueryTokenDeadline
	sseQueryTokenDeadline = d
	t.Cleanup(func() { sseQueryTokenDeadline = old })
}

// TestW2bQueryTokenPassesBeforeDeadline：截止前，有效长期 token 的 query 回退照常建流（200）。
func TestW2bQueryTokenPassesBeforeDeadline(t *testing.T) {
	s, tok := w2bSSEServer(t)
	w2bSetDeadline(t, time.Now().Add(time.Hour))
	rec := w2bStream(t, s, "/api/events?token="+tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("截止前 query token 回退应放行，实得 %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestW2bQueryTokenRejectedAfterDeadline：截止后**手里是有效长期凭证也 401**，
// 且文案指向票据通道（旧客户端拿到的报错必须可自助处置）。
func TestW2bQueryTokenRejectedAfterDeadline(t *testing.T) {
	s, tok := w2bSSEServer(t)
	w2bSetDeadline(t, time.Now().Add(-time.Minute))
	rec := w2bStream(t, s, "/api/events?token="+tok)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("截止后 query token 必须 401，实得 %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "retired") {
		t.Fatalf("401 文案应指向退役（含 retired 并提示票据通道），实得 %s", rec.Body.String())
	}
}

// TestW2bTicketUnaffectedAfterDeadline：退场闸只管回退分支——截止后票据主路照常 200。
func TestW2bTicketUnaffectedAfterDeadline(t *testing.T) {
	s, _ := w2bSSEServer(t)
	w2bSetDeadline(t, time.Now().Add(-time.Minute))
	tk := s.newSSETicket("any-user")
	rec := w2bStream(t, s, "/api/events?ticket="+tk)
	if rec.Code != http.StatusOK {
		t.Fatalf("票据通道不受退役闸影响，应 200，实得 %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestW2bLegacySemanticsKept（反证：闸不得改变既有拒因）：截止前无效 token→401 invalid token；
// 什么凭证都没有→401 missing ticket or token。
func TestW2bLegacySemanticsKept(t *testing.T) {
	s, _ := w2bSSEServer(t)
	w2bSetDeadline(t, time.Now().Add(time.Hour))
	if rec := w2bStream(t, s, "/api/events?token=bogus"); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "invalid token") {
		t.Fatalf("截止前无效 token 应保持旧语义 401 invalid token，实得 %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := w2bStream(t, s, "/api/events"); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "missing ticket or token") {
		t.Fatalf("无凭证应保持旧语义 401 missing，实得 %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestW2bShippedDeadlineIsLocked：出厂截止日等值锁（防"退场日期被人顺手改成明天/上周年"）——
// 真实出厂值必须是 2026-10-15T00:00:00+08:00（固定时区，不随服务器 TZ 漂移）。
// 本用例必须在任何 w2bSetDeadline 恢复之后跑（Go 用例串行、Cleanup 已还原，安全）。
func TestW2bShippedDeadlineIsLocked(t *testing.T) {
	want := time.Date(2026, 10, 15, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if !sseQueryTokenDeadline.Equal(want) {
		t.Fatalf("退役截止日被改动：期望 %s，实得 %s", want.Format(time.RFC3339), sseQueryTokenDeadline.Format(time.RFC3339))
	}
	if sseQueryTokenDeadline.IsZero() {
		t.Fatal("截止常量为零值＝query token 通道当场全停（APK 未升级前不可零值）")
	}
}

// TestW2bLoginCarriesRetirementHint：登录响应带 sse_query_token_expires_at（旧客户端的
// 升级提示数据源），值等于出厂截止日的 RFC3339 表示；其余既有键一字未动。
func TestW2bLoginCarriesRetirementHint(t *testing.T) {
	s, _ := newAdminTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"adminpw"}`))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应 200，实得 %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("登录响应非 JSON: %v", err)
	}
	got, ok := out["sse_query_token_expires_at"]
	if !ok || got != "2026-10-15T00:00:00+08:00" {
		t.Fatalf("退役提示字段缺失或值漂移：got=%v", got)
	}
	for _, k := range []string{"token", "id", "account", "role", "perms"} {
		if _, has := out[k]; !has {
			t.Fatalf("登录响应既有键 %s 丢失（加字段不得动旧契约）", k)
		}
	}
}
