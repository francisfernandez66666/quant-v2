package server

// §A3（AUDIT_FULLSTACK_20260918）SSE 慢客户端丢弃计数与越阈回收单测。
// 场景：客户端订阅后完全不消费 → channel 缓冲 16 写满后每条广播都触发丢弃；
// 连续丢弃达到 sseDropEvictLimit 后连接必须被主动回收（close），
// 丢弃/回收两计数进 metrics gauge，供 /api/metrics/prometheus 观测。
// English: slow-consumer eviction test for §A3 — a never-draining client must be
// force-closed once consecutive drops reach the limit, with counters exported.

import (
	"testing"

	"quant-trading-v2/internal/metrics"
)

// TestSSEEvictsStalledClient 验证失能客户端回收路径与计数可见。
func TestSSEEvictsStalledClient(t *testing.T) {
	b := NewSSEBroker()
	ch := b.SubscribeFor("u1", 0)

	// 前 16 条填满缓冲（不丢弃），再 16 条连续丢弃到阈 → 第 32 条后回收
	for i := 0; i < sseDropEvictLimit*2; i++ {
		b.BroadcastTo("u1", map[string]interface{}{"type": "message", "n": i})
	}

	drops, evictions := b.SSEDropStats()
	if drops < int64(sseDropEvictLimit) {
		t.Fatalf("丢弃计数应≥%d，实为 %d", sseDropEvictLimit, drops)
	}
	if evictions != 1 {
		t.Fatalf("应恰好回收 1 个失能连接，实为 %d", evictions)
	}

	// 被回收的 channel 必须已 close（消费干净后读不到数据且 ok=false）
	seen := 0
	for range ch {
		seen++
		if seen > sseDropEvictLimit+1 {
			t.Fatal("缓冲外不应有更多事件")
		}
	}
	if _, alive := <-ch; alive {
		t.Fatal("越阈后连接应已被 close，收到数据？")
	}

	// 回收后再广播不得 panic（不再持有任何 u1 客户端）
	b.Broadcast(map[string]interface{}{"type": "scan"})

	// metrics 面：两个计数都进了 gauge
	if v, ok := metrics.GetGauge("sse_dropped_total"); !ok || v < int64(sseDropEvictLimit) {
		t.Fatalf("sse_dropped_total gauge 未反映丢弃，得 %d ok=%v", v, ok)
	}
	if v, ok := metrics.GetGauge("sse_evictions_total"); !ok || v < 1 {
		t.Fatalf("sse_evictions_total gauge 未反映回收，得 %d ok=%v", v, ok)
	}
}

// TestSSEDropCounterResetsOnDrain 验证健康客户端（边发边消费）不落失能计数：
// 每次广播后消费一条 → 即便偶发一次丢弃，成功写入会把连续丢弃清零，永不触发回收。
func TestSSEDropCounterResetsOnDrain(t *testing.T) {
	b := NewSSEBroker()
	ch := b.SubscribeFor("u2", 0)
	for i := 0; i < sseDropEvictLimit*3; i++ {
		b.BroadcastTo("u2", map[string]interface{}{"type": "message", "n": i})
		<-ch // 及时消费，保持缓冲不满
	}
	if _, ev := b.SSEDropStats(); ev != 0 {
		t.Fatalf("健康客户端不应被回收，evictions=%d", ev)
	}
}
