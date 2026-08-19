package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWebhookGatewaySend 验证通用推送网关：消息以 JSON POST 到网关地址。
func TestWebhookGatewaySend(t *testing.T) {
	received := make(chan map[string]interface{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		json.Unmarshal(body, &m)
		received <- m
		w.WriteHeader(200)
	}))
	defer srv.Close()

	gw := NewWebhookGateway(srv.URL)
	if err := gw.Send(Message{Level: LevelHigh, Title: "止损", Content: "600519 触达止损"}); err != nil {
		t.Fatalf("Send 应成功: %v", err)
	}

	select {
	case m := <-received:
		if m["title"] != "止损" || m["content"] == "" {
			t.Fatalf("网关应收到 title/content, got %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("网关未收到请求")
	}
}

// TestWebhookGatewayEmptyURL 验证 URL 为空时 Send 为 no-op（不报错）。
func TestWebhookGatewayEmptyURL(t *testing.T) {
	gw := NewWebhookGateway("")
	if err := gw.Send(Message{Title: "x"}); err != nil {
		t.Fatalf("空 URL 应 no-op, got %v", err)
	}
}

// TestPushGatewayDisabled 验证未启用网关时 PushGateway 不投递（无 panic）。
func TestPushGatewayDisabled(t *testing.T) {
	n := New()
	n.PushGateway(Message{Title: "x"}) // gateway 为 nil，应直接返回
}

// TestSetGatewayEnable 验证设置网关后 PushGateway 会转发到外部服务。
func TestSetGatewayEnable(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hit <- struct{}{}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	n := New()
	n.SetGateway(NewWebhookGateway(srv.URL))
	n.PushGateway(Message{Title: "清仓", Content: "M8 组合清仓"})

	select {
	case <-hit:
		// 成功转发
	case <-time.After(2 * time.Second):
		t.Fatal("设置网关后应转发到外部服务")
	}
}
