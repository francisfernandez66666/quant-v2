// notify ntfy 通道：消息头/优先级映射、双网关并投、未启用静默。
package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestNtfySend httptest 接收端断言：POST 路径/标题头/优先级映射/正文。
func TestNtfySend(t *testing.T) {
	var gotPath, gotTitle, gotPri, gotBody string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("Title")
		gotPri = r.Header.Get("Priority")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := NewNtfyGateway(srv.URL, "secret-topic")
	if err := g.Send(Message{Level: LevelHigh, Title: "清仓提醒", Content: "600000 触发止损"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/secret-topic" {
		t.Errorf("请求路径/方法错误: %s %s", gotMethod, gotPath)
	}
	if gotTitle != "清仓提醒" || gotBody != "600000 触发止损" {
		t.Errorf("标题/正文错误: %q %q", gotTitle, gotBody)
	}
	if gotPri != "4" { // LevelHigh → high(4)
		t.Errorf("优先级映射错误: want 4, got %s", gotPri)
	}
}

// TestNtfyLevelPriorityMap 低/中级别的优先级头映射。
func TestNtfyLevelPriorityMap(t *testing.T) {
	var pris []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pris = append(pris, r.Header.Get("Priority"))
	}))
	defer srv.Close()
	g := NewNtfyGateway(srv.URL, "tp")
	for _, lv := range []AlertLevel{LevelLow, LevelMedium} {
		if err := g.Send(Message{Level: lv, Title: "x", Content: "y"}); err != nil {
			t.Fatalf("Send(level=%d): %v", lv, err)
		}
	}
	if len(pris) != 2 || pris[0] != "2" || pris[1] != "3" {
		t.Errorf("级别映射错误: %v", pris)
	}
}

// TestNtfyDisabledEmptyTopic topic 为空返回 nil，调用 Send 不 panic（空安全）。
func TestNtfyDisabledEmptyTopic(t *testing.T) {
	if g := NewNtfyGateway("https://ntfy.sh", ""); g != nil {
		t.Errorf("空 topic 应返回 nil")
	}
	var g *NtfyGateway // nil 接收者
	if err := g.Send(Message{Title: "a", Content: "b"}); err != nil {
		t.Errorf("nil 网关 Send 应静默成功, got %v", err)
	}
}

// countGateway 测试用计数网关（记录收到的消息）。
type countGateway struct {
	mu   sync.Mutex
	msgs []Message
}

func (c *countGateway) Send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	return nil
}

func (c *countGateway) waitCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		c.mu.Lock()
		n := len(c.msgs)
		c.mu.Unlock()
		if n >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("want %d msgs, got %d", want, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestPushGatewayDualDispatch 主网关与 ntfy 并投：两通道各收到一条。
func TestPushGatewayDualDispatch(t *testing.T) {
	n := New()
	main, ops := &countGateway{}, &countGateway{}
	n.SetGateway(main)
	n.SetNtfy(ops)

	n.PushGateway(Message{Level: LevelHigh, Title: "t", Content: "c"})
	main.waitCount(t, 1)
	ops.waitCount(t, 1)
}

// TestPushGatewayOnlyOneChannel 只配 ntfy 不配主网关：仍投递、不 panic（§HARDENING 后新增路径）。
func TestPushGatewayOnlyOneChannel(t *testing.T) {
	n := New()
	ops := &countGateway{}
	n.SetNtfy(ops)
	n.PushGateway(Message{Level: LevelMedium, Title: "t", Content: "c"})
	ops.waitCount(t, 1)
}
