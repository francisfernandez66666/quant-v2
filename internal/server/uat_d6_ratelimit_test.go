// uat_d6_ratelimit_test.go — §UAT-D6（2026-09-16）高成本认证端点按用户频控回归：
// 锁三件事：①同用户在窗口内超 max 次被拒、不同用户互不影响；②429 响应带 Retry-After；
// ③未登录回落 IP 维度。旧实现这些端点零频控（ipLimiter 只挂 login/setup），单账号
// 可无限刷 LLM 咨询/寻优/补推/复盘——成本放大 + 饿死他人。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
)

// ctxRequest 构造带登录用户（userID 空=未登录）与固定来源 IP 的请求。
func ctxRequest(userID, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/consult", nil)
	r.RemoteAddr = ip + ":12345"
	if userID != "" {
		r = r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: userID}))
	}
	return r
}

// 限流按用户独立计数、互不影响。
func TestUserRateLimitPerUser(t *testing.T) {
	s := &Server{}
	// 用户 u_a：窗口内 3 次放行、第 4 次拒绝
	for i := 0; i < 3; i++ {
		if !s.userRateLimit(ctxRequest("u_a", "10.0.0.1"), "consult", 3, time.Minute) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if s.userRateLimit(ctxRequest("u_a", "10.0.0.1"), "consult", 3, time.Minute) {
		t.Fatal("超配额应拒绝（同用户）")
	}
	// 不同用户不受影响（即便同 IP）
	if !s.userRateLimit(ctxRequest("u_b", "10.0.0.1"), "consult", 3, time.Minute) {
		t.Fatal("另一用户同 IP 应独立计数")
	}
	// 不同桶互不影响
	if !s.userRateLimit(ctxRequest("u_a", "10.0.0.1"), "pos-review", 3, time.Minute) {
		t.Fatal("不同端点桶应独立计数")
	}
	// 未登录 → 回落 IP 维度：同 IP 共享配额
	if !s.userRateLimit(ctxRequest("", "10.0.0.9"), "consult", 2, time.Minute) ||
		!s.userRateLimit(ctxRequest("", "10.0.0.9"), "consult", 2, time.Minute) {
		t.Fatal("匿名前两次要放行")
	}
	if s.userRateLimit(ctxRequest("", "10.0.0.9"), "consult", 2, time.Minute) {
		t.Fatal("匿名同 IP 超配额要拒绝")
	}
	if !s.userRateLimit(ctxRequest("", "10.0.0.10"), "consult", 2, time.Minute) {
		t.Fatal("匿名不同 IP 应独立")
	}
}

// 限流拒绝响应为 429 且结构统一。
func TestRejectRateLimitResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	rejectRateLimit(rec, 5*time.Minute)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("应 429, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "300" {
		t.Fatalf("Retry-After 应 300, got %q", ra)
	}
}
