// m8_fanout_test.go — §M8（2026-09-22 修复批）反例锁：Push 三路内聚。
//
// 缺陷原貌：notify.go Push 只走 WS/Webhook 两路，PushGateway（JPush/ntfy 手机通道）
// 全生产仅 2 个直调点——普通信号/成交（经 PushSignal/PushTrade/Push 的消息）不触达手机。
// 本文件锁死修复形态：
//  1. Push 一次，三路（WS/Webhook/网关）各恰好收到一次——多一发算回归、少一发算缺陷复发；
//  2. 网关未配置（关闭）时 WS/Webhook 不受影响（门控只关第三路，不倒灌前 two 路）；
//  3. 级别/配置门控：低于 gatewayMinLevel 不触手机，SetGatewayMinLevel 可调；
//  4. PushTrade/PushSignal（M8 缺陷主体）经内聚后确实触达网关。
//
// English: regression locks for M8 — Push now fans out to WS + Webhook + mobile push gateway
// exactly once per call; an unconfigured gateway never suppresses WS/Webhook; the level gate holds.
package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/strategy"
)

// mockGateway 计数型推送网关桩（三路内聚测试的手机通道接收器）。
type mockGateway struct {
	mu    sync.Mutex
	sends []Message
}

// Send 记录一条投递并成功。
func (g *mockGateway) Send(msg Message) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sends = append(g.sends, msg)
	return nil
}

// count 返回已收到的投递条数。
func (g *mockGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sends)
}

// newWebhookSink 启动一个记录请求条数的 httptest 服务（Webhook 通道接收器）。
func newWebhookSink(t *testing.T) (string, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		n++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// settle 沉降窗口：等待 Push 的异步分支（Webhook goroutine / 网关 goroutine）全部落地，
// 之后的负断言（"没有第二条"）才有意义。
func settle() { time.Sleep(300 * time.Millisecond) }

// TestM8PushFanOutThreeChannelsOnce §M8 核心反例锁：Push 一次，三路各恰好收到一次。
// 缺陷形态（修复前）网关路收到 0 条；若调用方违规双调（Push 后再直调 PushGateway）则收 2 条。
// English: one Push must deliver exactly one message to each of WS / Webhook / gateway.
func TestM8PushFanOutThreeChannelsOnce(t *testing.T) {
	n := New()
	ws := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")
	url, webhookCount := newWebhookSink(t)
	n.SetWebhooks([]string{url})
	gw := &mockGateway{}
	n.SetGateway(gw)

	n.Push(Message{Level: LevelHigh, Title: "🚀交易", Content: "300750 宁德"})

	// WS：同步通道，Push 返回即应可见
	select {
	case m := <-ws:
		if m.Title != "🚀交易" {
			t.Fatalf("WS 收到错误消息 %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("§M8 反例：WS 未收到推送")
	}

	// Webhook / 网关：Push 内异步，等待到达
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (webhookCount() < 1 || gw.count() < 1) {
		time.Sleep(20 * time.Millisecond)
	}
	if webhookCount() != 1 {
		t.Fatalf("§M8 Webhook 应恰好 1 条, got %d", webhookCount())
	}
	if gw.count() != 1 {
		t.Fatalf("§M8 手机网关应恰好 1 条（0=缺陷复发：Push 不达手机；>1=双发回归）, got %d", gw.count())
	}
	settle()
	if webhookCount() != 1 || gw.count() != 1 {
		t.Fatalf("§M8 沉降后仍应各 1 条（双发/重投回归）, webhook=%d gateway=%d", webhookCount(), gw.count())
	}
}

// TestM8GatewayDisabledKeepsWSAndWebhook §M8 门控反例锁：关闭网关配置（未 SetGateway/SetNtfy）
// 时 Push 的 WS/Webhook 两路完全不受影响——网关扇出对 nil 网关自然落空。
// English: with no push gateway configured, WS and Webhook delivery must be untouched.
func TestM8GatewayDisabledKeepsWSAndWebhook(t *testing.T) {
	n := New() // gateway/ntfy 均 nil
	ws := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")
	url, webhookCount := newWebhookSink(t)
	n.SetWebhooks([]string{url})

	n.Push(Message{Level: LevelHigh, Title: "止损", Content: "600519"})

	select {
	case <-ws:
	case <-time.After(2 * time.Second):
		t.Fatal("网关关闭时 WS 仍应收到推送")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && webhookCount() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if webhookCount() != 1 {
		t.Fatalf("网关关闭时 Webhook 应正常收到 1 条, got %d", webhookCount())
	}
}

// TestM8GatewayLevelGate §M8 级别门控：默认中级（含）以上触手机，低级别命中提醒不轰炸网关；
// SetGatewayMinLevel 提到高级后中级不再触达——门控受 level/config 双重控制。
// English: the folded fan-out is level-gated (default >= Medium; tunable via SetGatewayMinLevel).
func TestM8GatewayLevelGate(t *testing.T) {
	n := New()
	gw := &mockGateway{}
	n.SetGateway(gw)

	n.Push(Message{Level: LevelLow, Title: "低"}) // 默认门槛=中：低级别不发手机
	settle()
	if gw.count() != 0 {
		t.Fatalf("LevelLow 默认不应触达手机网关, got %d", gw.count())
	}
	n.Push(Message{Level: LevelMedium, Title: "中"}) // 普通信号：M8 修复主体，必达
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gw.count() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if gw.count() != 1 {
		t.Fatalf("LevelMedium 应触达手机网关 1 条, got %d", gw.count())
	}

	// 门槛收紧到高级：中级不再触达，高级仍触达
	n.SetGatewayMinLevel(LevelHigh)
	n.Push(Message{Level: LevelMedium, Title: "中2"})
	settle()
	if gw.count() != 1 {
		t.Fatalf("门槛=高级时 LevelMedium 不应触达, got %d", gw.count())
	}
	n.Push(Message{Level: LevelHigh, Title: "高"})
	for time.Now().Before(deadline) && gw.count() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if gw.count() != 2 {
		t.Fatalf("门槛=高级时 LevelHigh 应触达, got %d", gw.count())
	}
}

// TestM8SignalAndTradeReachGateway §M8 缺陷主体反例锁：PushSignal(P1) 与 PushTrade
// 修复前只走 WS/Webhook，手机零触达；现经 Push 内聚三路，网关各收一条。
// English: PushSignal/PushTrade — the messages that never reached the phone before M8 —
// must now land on the gateway exactly once each.
func TestM8SignalAndTradeReachGateway(t *testing.T) {
	n := New()
	gw := &mockGateway{}
	n.SetGateway(gw)
	ws := n.RegisterWS("c1")
	defer n.UnregisterWS("c1")

	sig := &strategy.Signal{
		Code: "300750", Name: "宁德", Type: strategy.SignalNShape,
		Priority: strategy.P1, Price: 200, Confidence: 0.8, Qty: 100, Amount: 20000,
	}
	n.PushSignal(sig)
	n.PushTrade(sig, 3.2, 1e6)
	// 两条 WS 消息照旧可达
	for i := 0; i < 2; i++ {
		select {
		case <-ws:
		case <-time.After(2 * time.Second):
			t.Fatalf("WS 第 %d 条信号消息未送达", i+1)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gw.count() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	settle()
	if gw.count() != 2 {
		t.Fatalf("§M8 PushSignal+PushTrade 应各触达手机网关一次（共 2 条）, got %d", gw.count())
	}
}

// TestM8QuietHoursStillSuppressesAllChannels §M8 与 §GAP5.2 的交叉锁：静默时段内低/中级别
// 在三路（含新内聚的网关路）全部抑制，不得因内聚而绕开静默门。
// English: quiet hours must suppress the newly folded gateway path too — no bypass.
func TestM8QuietHoursStillSuppressesAllChannels(t *testing.T) {
	n := New()
	gw := &mockGateway{}
	n.SetGateway(gw)
	n.SetQuietHours("00:00", "23:59") // 全天静默

	n.Push(Message{Level: LevelMedium, Title: "盘中信号"})
	settle()
	if gw.count() != 0 {
		t.Fatalf("静默时段中级别不得触达网关, got %d", gw.count())
	}
	n.Push(Message{Level: LevelHigh, Title: "清仓"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gw.count() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if gw.count() != 1 {
		t.Fatalf("静默时段高级别仍应放行触达手机, got %d", gw.count())
	}
}
