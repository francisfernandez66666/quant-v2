// qmt_client_20260925eve_test.go — §0925EVE-W3-E 两条接线修复的单测：
// ① C4：BrokerStatus 的 failover_enable/dispatch 必须从 GET /admin/status 读
//
//	（网关只在 _do_admin_status 发这两键），/health 基础字段解析保留原语义；
//	/admin/status 不可达时返回「读不到」标记（AdminStatusOK=false + 原因留痕），
//	两键指针保持 nil——绝不用零值冒充「自动翻转=关」。
//
// ② C8：order() 对网关确定性 4xx 直败不重试（400 空转二次请求是旧缺陷本体），
//
//	408/429 与网络错误/5xx 维持既有重试语义。
package trading

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// eveGatewayStub §0925EVE-W3-E 假网关：/health 只发基础字段（与真实 gateway.py 一致，
// 它从不在这条端点上发 failover_enable/dispatch），/admin/status 可控开关成 404。
// 同时记录每个请求的 Authorization 头，钉「带既有 token 机制」这一要求。
type eveGatewayStub struct {
	t           *testing.T
	adminOK     bool // false → /admin/status 回 404（模拟旧版/不可达）
	authSeen    map[string]string
	orderHits   int32
	orderStatus int
}

func (s *eveGatewayStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.authSeen == nil {
		s.authSeen = map[string]string{}
	}
	s.authSeen[r.URL.Path] = r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/health":
		// 真实网关 /health 的字段面：没有 failover_enable/dispatch。
		w.Write([]byte(`{"ok":true,"ts":"t","broker":"queued","broker_connected":true,
			"xt_connected":false,"queued_connected":true}`))
	case r.URL.Path == "/admin/status":
		if !s.adminOK {
			http.NotFound(w, r)
			return
		}
		// gateway.py _do_admin_status 形态（queued 通道在线时才有 dispatch）。
		w.Write([]byte(`{"ok":true,"ts":"t","active":"queued","failover_enable":false,
			"brokers":{"xt":false,"queued":true},
			"dispatch":{"pending":2,"inflight":0,"done":37}}`))
	case r.URL.Path == "/order" && r.Method == http.MethodPost:
		atomic.AddInt32(&s.orderHits, 1)
		w.WriteHeader(s.orderStatus)
		if s.orderStatus == http.StatusOK {
			w.Write([]byte(`{"ok":true,"order_id":"GW-1","err":""}`))
			return
		}
		w.Write([]byte(`{"ok":false,"err":"deterministic rejection"}`))
	default:
		http.NotFound(w, r)
	}
}

// TestBrokerStatusReadsAdminStatusEndpoint C4 正路：两键从 /admin/status 取回，
// /health 基础字段原样保留，且第二腿携带 Bearer token。
func TestBrokerStatusReadsAdminStatusEndpoint(t *testing.T) {
	stub := &eveGatewayStub{t: t, adminOK: true}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk-secret", 2e9, 0)

	st, err := c.BrokerStatus()
	if err != nil {
		t.Fatalf("BrokerStatus: %v", err)
	}
	// /health 腿（原语义保留）
	if st.Broker != "queued" || !st.QueuedConnected || st.XTConnected || !st.BrokerConnected {
		t.Fatalf("/health 基础字段解析回归: %+v", st)
	}
	// /admin/status 腿（新接线）
	if !st.AdminStatusOK {
		t.Fatalf("admin/status 可达时 AdminStatusOK 必须为 true: %+v", st)
	}
	if st.FailoverEnable == nil {
		t.Fatal("failover_enable 必须读到指针（哪怕是 false），nil=读不到，两者语义不同")
	}
	if *st.FailoverEnable {
		t.Fatalf("网关报 failover_enable=false，不得翻成 true")
	}
	disp, ok := st.Dispatch.(map[string]any)
	if !ok || disp["pending"].(float64) != 2 {
		t.Fatalf("dispatch 未从 /admin/status 透传: %#v", st.Dispatch)
	}
	// token 机制：第二腿必须走既有 Bearer 注入（网关对非 /health 端点强制鉴权）
	if got := stub.authSeen["/admin/status"]; got != "Bearer tk-secret" {
		t.Fatalf("/admin/status 请求未携带 Bearer token: %q", got)
	}
}

// TestBrokerStatusAdminUnreachableNotFake C4 反路：/admin/status 不可达（404/断连）时，
// 查询整体不报错（/health 读数仍可用），但两键必须停留在「读不到」形态：
// FailoverEnable=nil、Dispatch=nil、AdminStatusOK=false 且留原因——不许空值冒充。
func TestBrokerStatusAdminUnreachableNotFake(t *testing.T) {
	stub := &eveGatewayStub{t: t, adminOK: false}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk-secret", 2e9, 0)

	st, err := c.BrokerStatus()
	if err != nil {
		t.Fatalf("/admin/status 不可达不应拖垮 /health 读数: %v", err)
	}
	if st.AdminStatusOK {
		t.Fatal("AdminStatusOK 必须为 false（读不到这件事本身要可见）")
	}
	if st.AdminStatusErr == "" {
		t.Fatal("AdminStatusErr 必须留痕失败原因，供前端区分「没这功能」与「暂时读不到」")
	}
	if st.FailoverEnable != nil || st.Dispatch != nil {
		t.Fatalf("读不到时两键必须保持空（nil），零值即冒充: %+v", st)
	}
	if st.Broker != "queued" {
		t.Fatalf("/health 腿读数不应受影响: %+v", st)
	}
}

// TestOrderFailsFastOn400 C8：网关 400（确定性拒绝）只打一次请求。
// 旧实现对一切非 200 重试 retries 次——同一条非法请求在网关口径下重试一万次也是 400，
// 只会空转二次/三次投递。
func TestOrderFailsFastOn400(t *testing.T) {
	stub := &eveGatewayStub{t: t, adminOK: true, orderStatus: http.StatusBadRequest}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk", 2e9, 2) // 配置 retries=2：旧行为会打 3 次

	_, err := c.order(OrderRequest{SignalID: "SIG-400", Code: "600000.SH", Side: SideBuy, Price: 10, Qty: 100})
	if err == nil {
		t.Fatal("400 必须判败")
	}
	if got := atomic.LoadInt32(&stub.orderHits); got != 1 {
		t.Fatalf("400 确定性拒绝应 1 次直败（不重试）, got %d hits", got)
	}
}

// TestOrderStillRetries5xxAnd429 C8 反证：5xx 与 429 属瞬态，重试语义原样维持
// （把 4xx 一刀切禁重试会把限流/网关抖动的可救场景误杀）。
func TestOrderStillRetries5xxAnd429(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusRequestTimeout} {
		stub := &eveGatewayStub{t: t, adminOK: true, orderStatus: status}
		srv := httptest.NewServer(stub)
		c := NewQMTClient(srv.URL, "tk", 2e9, 2)
		if _, err := c.order(OrderRequest{SignalID: "SIG-R", Code: "600000.SH", Side: SideBuy, Price: 10, Qty: 100}); err == nil {
			t.Fatalf("HTTP %d 全失败后应返回错误", status)
		}
		if got := atomic.LoadInt32(&stub.orderHits); got != 3 {
			t.Fatalf("HTTP %d 应维持 1+retries=3 次尝试, got %d", status, got)
		}
		srv.Close()
	}
}

// TestIsDeterministicGatewayRejection C8 分类器本身的钉桩：
// 409（§CLAIMRELEASE 同信号在途）与 400 同为确定性拒绝；非 HTTP 错误走重试。
func TestIsDeterministicGatewayRejection(t *testing.T) {
	mk := func(code int) error { return &gatewayHTTPError{Method: "POST", Path: "/order", Status: code} }
	cases := []struct {
		code int
		want bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusConflict, true},
		{http.StatusNotFound, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	}
	for _, c := range cases {
		if got := isDeterministicGatewayRejection(mk(c.code)); got != c.want {
			t.Errorf("HTTP %d 分类错误: got %v want %v", c.code, got, c.want)
		}
	}
	if isDeterministicGatewayRejection(errors.New("dial tcp 10.0.0.2:8789: connect: connection refused")) {
		t.Error("非网关 HTTP 状态错误（网络层）不得判为确定性拒绝——须维持重试语义")
	}
	// 被包装过的 4xx（上层 fmt.Errorf %w 透传）仍要能分类：errors.As 逐层解链。
	wrapped := fmt.Errorf("place order: %w", mk(http.StatusBadRequest))
	if !isDeterministicGatewayRejection(wrapped) {
		t.Error("包装链上的 400 仍应识别为确定性拒绝")
	}
}
