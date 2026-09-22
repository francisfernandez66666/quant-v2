// sse_uplink_test.go — §UPDLINK（2026-09-22 · AUDIT_E2E_FULL H-4，P0）回归用例。
// 职责：锁死两件事——① 同一个客户端 channel 被两条路径（§A3 evict 与 handler defer）先后注销时
// 不得 double-close、更不得把 SSE 广播锁留在持有态；② 广播锁真被卡住时，上行回报入口必须靠
// 有界预算自保（宁可丢推送也要 200 返回），让 H-4 那 1h45m 的静默期不可能重演。
// English: §UPDLINK regression tests — idempotent unsubscribe must never double-close or leak the
// broadcast lock, and the gateway uplink endpoint must survive a jammed broadcast lock by giving up
// the push within its budget instead of hanging forever.
package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
)

// unlockDone 用一次无界 Broadcast 探锁：能按时返回即锁未被泄漏（锁死时会永久阻塞）。
// English: probe the broadcast lock with a bounded Broadcast — returning in time proves no leak.
func unlockDone(t *testing.T, b *SSEBroker, where string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		b.Broadcast(map[string]string{"type": "probe"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s：广播锁被泄漏（Broadcast 永久阻塞）——H-4 的原始形态", where)
	}
}

// TestUnsubscribeForDoubleCloseIsIdempotent 同一 channel 连续注销两次不得 panic，
// 且第二次必须是幂等空操作（旧实现在锁内无条件 close → close of closed channel panic，
// panic 跳过 Unlock → 广播锁永久泄漏）。
func TestUnsubscribeForDoubleCloseIsIdempotent(t *testing.T) {
	b := NewSSEBroker()
	ch := b.SubscribeFor("u1", 0)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("重复注销不得 panic（双关回归）: %v", r)
		}
	}()
	b.UnsubscribeFor("u1", ch)
	b.UnsubscribeFor("u1", ch) // 模拟 handler defer 撞上已被 evict 注销的 channel
	unlockDone(t, b, "双关之后")
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("注销后 channel 应已关闭")
		}
	default:
		t.Fatalf("首次注销应关闭 channel")
	}
}

// TestUnsubscribeForScansAllGroupsOnUserIDMismatch userID 传参与订阅分组不一致时仍须注销并关闭：
// 否则该 channel 既不被关闭（写循环 goroutine 永不退出）也永不注销（注册表泄漏）。
func TestUnsubscribeForScansAllGroupsOnUserIDMismatch(t *testing.T) {
	b := NewSSEBroker()
	ch := b.SubscribeFor("u1", 0)
	b.UnsubscribeFor("u2", ch) // 声明的分组里没有它
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("跨分组注销应关闭 channel")
		}
	default:
		t.Fatalf("userID 不一致时应回退遍历全分组注销，实际 channel 仍开放")
	}
	if _, still := b.clients["u1"]; still {
		t.Fatalf("回退遍历后 u1 分组应已被清空")
	}
}

// TestEvictAndHandlerConcurrentUnsubscribe §A3 回收路径与 handler defer 注销路径并发竞争同一
// channel（H-4 生产形态）：反复跑不得 panic，也不得泄漏广播锁。
func TestEvictAndHandlerConcurrentUnsubscribe(t *testing.T) {
	for round := 0; round < 50; round++ {
		b := NewSSEBroker()
		ch := b.SubscribeFor("u1", 0)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { // 广播方：持续推送并可能触发 §A3 回收
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Broadcast(map[string]string{"type": "tick"})
			}
		}()
		go func() { // evict 腿（模拟 §A3 越阈回收）
			defer wg.Done()
			b.evict([]sseEvictTarget{{userID: "u1", ch: ch}})
		}()
		go func() { // handler defer 腿
			defer wg.Done()
			b.UnsubscribeFor("u1", ch)
		}()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("第 %d 轮并发注销 panic（双关回归）: %v", round, r)
				}
			}()
			wg.Wait()
		}()
		if t.Failed() {
			return
		}
		unlockDone(t, b, fmt.Sprintf("第 %d 轮并发注销之后", round))
	}
}

// TestBroadcastToWithinGivesUpOnStuckLock 锁被长期持有时，有界广播必须放弃并计数
// （返回 false），而不是像无界 BroadcastTo 那样把调用方拖死。
func TestBroadcastToWithinGivesUpOnStuckLock(t *testing.T) {
	b := NewSSEBroker()
	b.SubscribeFor("u1", 0)
	b.mu.Lock() // 模拟 H-4 里被 panic 泄漏的广播锁
	done := make(chan bool, 1)
	go func() {
		done <- b.BroadcastToWithin("u1", map[string]string{"type": "qmt_report"}, 50*time.Millisecond)
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatalf("锁被占住时有界广播应返回 false")
		}
	case <-time.After(2 * time.Second):
		b.mu.Unlock()
		t.Fatalf("有界广播仍被卡住（预算未生效）")
	}
	b.mu.Unlock()
	if got := b.SSESkipStats(); got != 1 {
		t.Fatalf("锁超预算丢推送应计数，got %d", got)
	}
	// 计数走独立锁：广播锁卡死期间自监控本身不能被拖住
	unlockDone(t, b, "放弃一次推送之后")
}

// TestQMTReportSurvivesStuckSSELock 端到端口径：广播锁被卡死时，POST /api/qmt/report 仍须在
// 预算内返回 200（账本已落库、前端有轮询兜底）。这条用例就是 H-4 那 1h45m 的否定式回归。
func TestQMTReportSurvivesStuckSSELock(t *testing.T) {
	s, _, _ := newTestResearchServer(t)
	s.sse = NewSSEBroker()
	s.sse.SubscribeFor("u_1", 0)
	s.sse.mu.Lock() // 模拟 panic 泄漏后的广播锁
	defer s.sse.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/qmt/report",
		bytes.NewBufferString(`{"type":"heartbeat"}`))
	// 必须带上下文用户：uid 为空时广播本身就是空操作，测不到"锁超预算"这条腿。
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey{}, &auth.User{ID: "u_1"}))

	start := time.Now()
	res := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		s.handleQMTReport(rr, req)
		res <- rr.Code
	}()
	select {
	case code := <-res:
		if code != http.StatusOK {
			t.Fatalf("上行心跳应 200，got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("广播锁卡住时回报端点被拖死（H-4 复现），耗时 %s", time.Since(start))
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("回报应在广播预算内返回，实际耗时 %s", elapsed)
	}
	if got := s.sse.SSESkipStats(); got != 1 {
		t.Fatalf("本次应记一次「锁超预算丢推送」自监控计数，got %d", got)
	}
}
