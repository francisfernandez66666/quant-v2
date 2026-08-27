// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"strings"
	"testing"
)

// TestSSEBrokerAccountIsolation 验证账号隔离：定向推送只到达对应账号的订阅者。
func TestSSEBrokerAccountIsolation(t *testing.T) {
	b := NewSSEBroker()

	ch1 := b.SubscribeFor("u1", 0)
	defer b.UnsubscribeFor("u1", ch1)
	ch2 := b.SubscribeFor("u2", 0)
	defer b.UnsubscribeFor("u2", ch2)

	// 只向 u1 定向推送，u2 不应收到
	b.BroadcastTo("u1", map[string]string{"type": "message", "level": "止损"})

	select {
	case ev := <-ch1:
		if !strings.Contains(string(ev.Data), "止损") {
			t.Fatalf("u1 应收到止损消息, got %s", ev.Data)
		}
	default:
		t.Fatal("u1 应收到定向推送")
	}
	select {
	case ev := <-ch2:
		t.Fatalf("u2 不应收到 u1 的定向消息, got %s", ev.Data)
	default:
		// 正确：u2 未收到
	}
}

// TestSSEBrokerBroadcastGlobal 验证全局广播会推送给所有账号分组。
func TestSSEBrokerBroadcastGlobal(t *testing.T) {
	b := NewSSEBroker()
	ch1 := b.SubscribeFor("u1", 0)
	defer b.UnsubscribeFor("u1", ch1)
	ch2 := b.SubscribeFor("u2", 0)
	defer b.UnsubscribeFor("u2", ch2)

	b.Broadcast(map[string]string{"type": "scan"})

	for _, ch := range []chan SSEEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if !strings.Contains(string(ev.Data), "scan") {
				t.Fatalf("全局广播应含 scan, got %s", ev.Data)
			}
		default:
			t.Fatal("全局广播应推送给每个账号")
		}
	}
}

// TestSSEBrokerReplayOnSubscribe 验证断线续传：带 lastID 订阅时补发历史中更新的事件。
func TestSSEBrokerReplayOnSubscribe(t *testing.T) {
	b := NewSSEBroker()
	first := b.SubscribeFor("u1", 0)
	b.BroadcastTo("u1", map[string]string{"type": "message", "seq": "1"})
	b.BroadcastTo("u1", map[string]string{"type": "message", "seq": "2"})
	// 消费第一条，记录其 ID
	ev1 := <-first
	b.UnsubscribeFor("u1", first)

	// 以 ev1.ID 为 lastID 重连，应补发第二条
	second := b.SubscribeFor("u1", ev1.ID)
	defer b.UnsubscribeFor("u1", second)

	select {
	case ev := <-second:
		if !strings.Contains(string(ev.Data), `"seq":"2"`) {
			t.Fatalf("续传应补发 seq=2 的事件, got %s", ev.Data)
		}
	default:
		t.Fatal("带 lastID 重连应补发历史事件")
	}
}

// TestSSEBrokerNoReplayForStaleID 验证 lastID 等于最新序号时不补发（无遗漏）。
func TestSSEBrokerNoReplayForStaleID(t *testing.T) {
	b := NewSSEBroker()
	ch := b.SubscribeFor("u1", 0)
	b.BroadcastTo("u1", map[string]string{"type": "message", "seq": "1"})
	ev := <-ch
	latestID := ev.ID
	b.UnsubscribeFor("u1", ch)

	// 用最新 ID 重连，不应补发任何事件
	re := b.SubscribeFor("u1", latestID)
	defer b.UnsubscribeFor("u1", re)
	select {
	case ev := <-re:
		t.Fatalf("lastID 已最新时不应补发, got %s", ev.Data)
	default:
		// 正确：无补发
	}
}
