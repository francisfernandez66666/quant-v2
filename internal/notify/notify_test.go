// notify 推送服务：WS 客户端注册/注销、消息投递与按优先级分级的告警。
package notify

import (
	"testing"
	"time"

	"quant-trading-v2/internal/strategy"
)

// receive 带超时地从通道收取一条消息。
func receive(t *testing.T, ch chan Message) (Message, bool) {
	t.Helper()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(200 * time.Millisecond):
		return Message{}, false
	}
}

// TestPushDelivers 注册的 WS 客户端能收到投递的消息。
func TestPushDelivers(t *testing.T) {
	n := New()
	ch := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")

	n.Push(Message{Level: LevelMedium, Title: "t", Content: "c"})
	m, ok := receive(t, ch)
	if !ok || m.Title != "t" || m.Content != "c" || m.Level != LevelMedium {
		t.Errorf("推送消息未正确投递, got %+v ok=%v", m, ok)
	}
}

// TestUnregisterStopsDelivery 注销后客户端不再收到消息。
func TestUnregisterStopsDelivery(t *testing.T) {
	n := New()
	ch := n.RegisterWS("c1")
	n.UnregisterWS("c1")

	n.Push(Message{Title: "t"})
	if _, ok := receive(t, ch); ok {
		t.Error("注销后的客户端不应再收到消息")
	}
}

// TestPushSignalLevel 信号优先级映射到告警级别。
func TestPushSignalLevel(t *testing.T) {
	n := New()
	ch := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")

	// P1 → 高（桌面通知+消息）
	n.PushSignal(&strategy.Signal{Type: strategy.SignalDragon, Priority: strategy.P1, Code: "001", Name: "x", Reason: "r"})
	if m, ok := receive(t, ch); !ok || m.Level != LevelHigh {
		t.Errorf("P1 应 LevelHigh, got %+v ok=%v", m, ok)
	}

	// P4 → 低
	n.PushSignal(&strategy.Signal{Type: strategy.SignalNShape, Priority: strategy.P4, Code: "002"})
	if m, ok := receive(t, ch); !ok || m.Level != LevelLow {
		t.Errorf("P4 应 LevelLow, got %+v ok=%v", m, ok)
	}

	// 默认（P3）→ 中
	n.PushSignal(&strategy.Signal{Type: strategy.SignalDragonReturn, Priority: strategy.P3, Code: "003"})
	if m, ok := receive(t, ch); !ok || m.Level != LevelMedium {
		t.Errorf("P3 应 LevelMedium, got %+v ok=%v", m, ok)
	}
}

// TestPushHitAndTrade 命中/交易提醒均产出带信号的消息。
func TestPushHitAndTrade(t *testing.T) {
	n := New()
	ch := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")

	sig := &strategy.Signal{
		Code: "300750", Name: "宁德", Type: strategy.SignalNShape,
		Priority: strategy.P1, Price: 200, Meta: map[string]float64{"d1": 40, "d2": 30},
		Confidence: 0.8, Qty: 100, Amount: 20000,
	}
	n.PushHit(sig, 3.2, 1e6, map[string]string{"d1": "利好"})
	if m, ok := receive(t, ch); !ok || m.Level != LevelLow || m.Signal != sig {
		t.Errorf("命中提醒异常, got %+v ok=%v", m, ok)
	}

	n.PushTrade(sig, 3.2, 1e6)
	if m, ok := receive(t, ch); !ok || m.Level != LevelHigh || m.Signal != sig {
		t.Errorf("交易提醒异常, got %+v ok=%v", m, ok)
	}
}