// ntfy_breaker_test.go §C9（2026-09-22 PM 批清扫）通道级短路 + 自监控告警回归：
// 连续失败开闸 → 窗内快速失败（不发 HTTP、不入补投队列）→ 半开探测成功即恢复；
// 开闸瞬间经 alertChannelDown 从站内通道（WS/Webhook）播报「报丧鸟哑了」。
package notify

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// flakyServer 可控成败的假 ntfy 服务端：fail=true 时全部 500。
func flakyServer(t *testing.T) (url string, setFail func(bool)) {
	t.Helper()
	var mu sync.Mutex
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(v bool) { mu.Lock(); fail = v; mu.Unlock() }
}

func TestNtfyBreakerTripsAndHalfOpenRecovers(t *testing.T) {
	url, setFail := flakyServer(t)
	g := NewNtfyGateway(url, "topic-x")
	var tripCount int
	var mu sync.Mutex
	g.onTrip = func(string) { mu.Lock(); tripCount++; mu.Unlock() }
	msg := Message{Level: LevelHigh, Title: "t", Content: "c"}

	// 前 ntfyTripThreshold 次真实失败（服务端 500），第 3 次开闸并播报一次。
	for i := 0; i < ntfyTripThreshold; i++ {
		if err := g.Send(msg); err == nil {
			t.Fatalf("第 %d 次发送应失败（服务端 500）", i+1)
		}
	}
	mu.Lock()
	trips := tripCount
	mu.Unlock()
	if trips != 1 {
		t.Fatalf("开闸应恰好播报一次, got %d", trips)
	}
	// 短路窗内：即使服务端已恢复，Send 也必须快速失败且不再发 HTTP（哨兵错误）。
	setFail(false)
	if err := g.Send(msg); !errors.Is(err, ErrNtfyShortCircuit) {
		t.Fatalf("短路窗内应返回 ErrNtfyShortCircuit, got %v", err)
	}
	// 模拟冷却到期 → 半开探测成功 → 闭合，后续发送恢复正常。
	g.bmu.Lock()
	g.openUntil = time.Now().Add(-time.Second)
	g.bmu.Unlock()
	if err := g.Send(msg); err != nil {
		t.Fatalf("冷却后半开探测应成功, got %v", err)
	}
	if err := g.Send(msg); err != nil {
		t.Fatalf("恢复后常规发送应成功, got %v", err)
	}
	// 恢复后再宕机：重新计满阈值再次开闸（第二次播报）。
	setFail(true)
	for i := 0; i < ntfyTripThreshold; i++ {
		_ = g.Send(msg)
	}
	mu.Lock()
	trips = tripCount
	mu.Unlock()
	if trips != 2 {
		t.Fatalf("二次宕机重新计满阈值应再播报一次, got %d", trips)
	}
}

// TestPushGatewayShortCircuitSkipsEnqueue §C9 调用方语义：短路窗内的 ntfy 失败
// 不进 outbox 补投队列（旧行为会把「通道宕机 × 每条新告警」放大成补投风暴）。
func TestPushGatewayShortCircuitSkipsEnqueue(t *testing.T) {
	url, _ := flakyServer(t)
	n := New()
	g := NewNtfyGateway(url, "topic-y")
	n.SetNtfy(g)
	// 手动开闸（等价于连续失败触发），避免测试等真实退避。
	g.bmu.Lock()
	g.openUntil = time.Now().Add(ntfyTripCooldown)
	g.bmu.Unlock()
	n.PushGateway(Message{Level: LevelHigh, Title: "t", Content: "c"})
	time.Sleep(150 * time.Millisecond) // PushGateway 的发送在独立 goroutine
	if got := n.outbox.pendingLen(); got != 0 {
		t.Fatalf("短路窗内不应入补投队列, pending=%d", got)
	}
}

// TestAlertChannelDownAnnouncesLocally §C9 自监控播报走站内两路：WS 客户端必收，
// 且不经网关扇出（不成环）。
func TestAlertChannelDownAnnouncesLocally(t *testing.T) {
	n := New()
	ch := n.RegisterWS("w1")
	n.alertChannelDown("ntfy 运维告警通道短路", "连续 3 次发送失败")
	select {
	case m := <-ch:
		if m.Level != LevelHigh || m.Title == "" {
			t.Fatalf("自监控播报必须 LevelHigh 且带标题, got %+v", m)
		}
	default:
		t.Fatal("WS 客户端应收到通道短路播报")
	}
	n.UnregisterWS("w1")
}
