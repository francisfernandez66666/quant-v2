// qmt_admin_test.go — §0925EVE-W3-G（FIX_PLAN ⑫ C3）：网关 admin 客户端单元测试。
// 覆盖 server 层 HTTP 测试够不到的纯客户端分支：
//
//	① /admin/status 响应非 JSON → 报错（不返回空清单）；
//	② order-confirm 入参守卫（空 signal_id / 非法 decision 不出网）；
//	③ 网关回 200 但响应体非 JSON → fail-closed 报错，绝不默认成功。
//
// English: client-level tests for the admin gateway client — non-JSON responses fail
// closed and bad inputs never leave the process.
package trading

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestQMTAdminPendingReviewNonJSONIsError 网关回 200 + HTML/垃圾体：必须 error，
// 不得伪造空清单（空清单=「没有待核对单」的资金安全断言，只准由真 JSON 产生）。
func TestQMTAdminPendingReviewNonJSONIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>proxy swallowed the gateway</html>"))
	}))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tok", 3*time.Second, 0)
	_, err := c.PendingReview(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("非 JSON 响应应报 decode 错误, got %v", err)
	}
}

// TestQMTAdminConfirmGuards 入参守卫在出网前拦截：空锚点/非法 decision 一律 error
// （防把 curl 面手滑请求转发给特权端点）。
func TestQMTAdminConfirmGuards(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tok", 3*time.Second, 0)
	if _, err := c.ConfirmOrder(context.Background(), OrderConfirmRequest{Decision: "released"}); err == nil {
		t.Fatal("空 signal_id 应本地拒绝")
	}
	if _, err := c.ConfirmOrder(context.Background(), OrderConfirmRequest{SignalID: "s", Decision: "reissue"}); err == nil {
		t.Fatal("非法 decision 应本地拒绝")
	}
	if hits != 0 {
		t.Fatalf("守卫失败：非法请求仍出网 %d 次", hits)
	}
}

// TestQMTAdminConfirmNonJSONFailClosed 网关 200 但体不可解析：必须 error（fail-closed），
// 绝不能默认 OK——「改判成没成」只能由结构化结论回答。
func TestQMTAdminConfirmNonJSONFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tok", 3*time.Second, 0)
	_, err := c.ConfirmOrder(context.Background(), OrderConfirmRequest{SignalID: "sell:x", Decision: "released"})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("非 JSON 回包应 fail-closed 报错, got %v", err)
	}
}

// TestQMTAdminPendingReviewTokenHeader 锁「Bearer token 确实带上」——复用 QMTClient 的
// token 装配线，防未来重构悄悄把 admin 通道变成免鉴权裸奔。
func TestQMTAdminPendingReviewTokenHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true,"unresolved_orders":[],"unresolved_count":0}`))
	}))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "sekret", 3*time.Second, 0)
	res, err := c.PendingReview(context.Background())
	if err != nil {
		t.Fatalf("正常响应不应报错: %v", err)
	}
	if got != "Bearer sekret" {
		t.Fatalf("Bearer token 未透传: %q", got)
	}
	if res.Orders == nil || len(res.Orders) != 0 || res.TotalCount != 0 || res.Truncated {
		t.Fatalf("空清单解析错误: %+v", res)
	}
}
