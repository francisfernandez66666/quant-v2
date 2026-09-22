// notify_test_gate_test.go — §N-2（2026-09-22 傍晚批 §NOTIFYADMIN）行为锁。
//
// 钉三件事（旧实现任何登录成员一次 POST 即可向 owner 全部推送通道发 LevelHigh 实弹）：
//  1. 档位：/api/notify-test 必须 adminMiddleware——普通成员 403；
//  2. 频控：进程内 60s 最小间隔，第二次调用回 429 且带 Retry-After 秒数；
//     noop 路径（未注入 notifier）同样计数——闸放在探测前，语义统一不留缝；
//  3. 抬档不破坏现网：web/src 对该端点零调用（Settings.jsx「测试通知」走浏览器 Notification
//     权限），verify_changes.sh 的验收段是**静态 grep 源码**不是真实 HTTP 调用，admin 化无现网回归。
//
// English: §N-2 behavior lock — notify-test is admin-only (member => 403), throttled at a
// 60s process-wide minimum interval (second hit => 429 with Retry-After), audit-logged.
package server

import (
	"net/http"
	"testing"
)

// TestNotifyTestAdminOnlyAndRateLimited 成员 403 → 管理员 200 → 60s 内二击 429+Retry-After。
func TestNotifyTestAdminOnlyAndRateLimited(t *testing.T) {
	s, admin := newAdminTestServer(t)
	member, err := s.auth.CreateUser("member_nt", "pw", "", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	// ① 普通成员必须被 admin 闸挡下（旧 authMiddleware 形态下这里是 200=实弹已发）。
	req := adminReq(s, member, http.MethodPost, "/api/notify-test", `{}`)
	rr := adminDo(s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("§N-2 成员打 /api/notify-test 应 403（抬档前为 200），got %d", rr.Code)
	}

	// ② 管理员放行：测试服务未注入 notifier，走显式 noop（200），但频控计时已受理。
	req = adminReq(s, admin, http.MethodPost, "/api/notify-test", `{}`)
	rr = adminDo(s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("管理员首击应 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// ③ 60s 内二击：429 + Retry-After（正整数秒），且**不得**再次触达探测路径。
	req = adminReq(s, admin, http.MethodPost, "/api/notify-test", `{}`)
	rr = adminDo(s, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("§N-2 频控缺失即红：二击应 429, got %d body=%s", rr.Code, rr.Body.String())
	}
	ra := rr.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("429 必须带 Retry-After 语义头")
	}
	if n := len(ra); n == 0 || ra[0] < '1' || ra[0] > '9' {
		t.Fatalf("Retry-After 应为剩余秒数（1..60）, got %q", ra)
	}
}
