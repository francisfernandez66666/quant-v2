// outbox_test.go — §GAP5.2 回归：静默时段门控（跨午夜）+ 补投队列重试与死信 + §R3-8 P1-D 持久化。
package notify

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestQuietHoursWindow QuietHoursWindow。
func TestQuietHoursWindow(t *testing.T) {
	n := New()
	n.SetQuietHours("22:00", "08:00")

	in := func(h, m int) bool { return n.inQuietHours(time.Date(2026, 8, 26, h, m, 0, 0, time.Local)) }
	if !in(23, 30) || !in(2, 0) || !in(7, 59) {
		t.Fatal("跨午夜窗口内的时刻应判静默")
	}
	if in(8, 0) || in(12, 0) || in(21, 59) {
		t.Fatal("窗口外时刻不应判静默")
	}
	// 非跨午夜窗口
	n.SetQuietHours("12:00", "14:00")
	if !n.inQuietHours(time.Date(2026, 8, 26, 13, 0, 0, 0, time.Local)) {
		t.Fatal("普通窗口内应判静默")
	}
	// 关闭
	n.SetQuietHours("", "")
	if n.inQuietHours(time.Date(2026, 8, 26, 13, 0, 0, 0, time.Local)) {
		t.Fatal("未配置时不应有静默窗口")
	}
}

// TestQuietHoursSuppressesLowOnly QuietHoursSuppressesLowOnly。
func TestQuietHoursSuppressesLowOnly(t *testing.T) {
	n := New()
	n.SetQuietHours("00:00", "23:59") // 全天静默
	hits := 0
	n.RegisterWS("t")
	ch := n.wsClients["t"]
	done := make(chan int)
	go func() {
		for range ch {
			hits++
			done <- hits
		}
	}()
	// 低级别被抑制
	n.Push(Message{Level: LevelLow, Title: "low"})
	// 高级别放行
	n.Push(Message{Level: LevelHigh, Title: "high"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("高级别消息应放行到达 WS")
	}
	time.Sleep(50 * time.Millisecond)
	if hits != 1 {
		t.Fatalf("全天静默下应只有高级别 1 条送达, got %d", hits)
	}
}

// TestOutboxRetriesThenDelivers OutboxRetriesThenDelivers。
func TestOutboxRetriesThenDelivers(t *testing.T) {
	o := &Outbox{}
	calls := 0
	o.enqueue("gateway", Message{Level: LevelHigh, Title: "补投测试"}, func(string, Message) error {
		calls++
		if calls >= 3 {
			return nil // 第 3 次成功
		}
		return errFake{}
	})
	// 强制到期驱动 pump（真实退避 30s 起步，测试不等钟）
	for i := 0; i < 10 && o.pendingLen() > 0; i++ {
		o.mu.Lock()
		if len(o.items) > 0 {
			o.items[0].nextAt = time.Now().Add(-time.Second)
		}
		o.mu.Unlock()
		o.pump()
	}
	if o.pendingLen() != 0 {
		t.Fatalf("第 3 次应投递成功出队, pending=%d", o.pendingLen())
	}
	if calls != 3 {
		t.Fatalf("应恰好投递 3 次, got %d", calls)
	}
}

// TestOutboxDeadLetter 超过最大尝试次数后死信出队（不无限堆积）。
func TestOutboxDeadLetter(t *testing.T) {
	o := &Outbox{}
	o.enqueue("webhook:x", Message{Level: LevelHigh, Title: "必死"}, func(string, Message) error {
		return errFake{}
	})
	for i := 0; i < outboxMaxAttempts+2 && o.pendingLen() > 0; i++ {
		o.mu.Lock()
		if len(o.items) > 0 {
			o.items[0].nextAt = time.Now().Add(-time.Second)
		}
		o.mu.Unlock()
		o.pump()
	}
	if o.pendingLen() != 0 {
		t.Fatalf("超过上限应死信出队, pending=%d", o.pendingLen())
	}
}

type errFake struct{}

// Error Error。
func (errFake) Error() string { return "fake delivery failure" }

// TestOutboxPersistsAcrossRestart §R3-8 P1-D 回归：入队即落盘、新实例加载续发——
// 此前补投队列纯内存，进程重启丢全部待补投的止损/清仓提醒。
func TestOutboxPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/outbox.json"

	// 实例 1：入队一条必失败的关键提醒（持久化启用）
	n1 := New()
	n1.SetOutboxPersistPath(path)
	n1.outbox.enqueue("gateway", Message{Level: LevelHigh, Title: "止损提醒"}, func(string, Message) error {
		return errFake{}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("入队后应落盘: %v", err)
	}

	// 实例 2（模拟重启）：从文件恢复，且 gateway 通道可重建
	n2 := New()
	n2.SetGateway(&fakeGatewayOK{})
	n2.SetOutboxPersistPath(path)
	if got := n2.outbox.pendingLen(); got != 1 {
		t.Fatalf("重启后应恢复 1 条待补投, got %d", got)
	}

	// 驱动到期重试 → 经重建的 deliverGateway 投递成功出队
	n2.outbox.mu.Lock()
	if len(n2.outbox.items) > 0 {
		n2.outbox.items[0].nextAt = time.Now().Add(-time.Second)
	}
	n2.outbox.mu.Unlock()
	n2.outbox.pump()
	if n2.outbox.pendingLen() != 0 {
		t.Fatalf("重建通道补投成功后应出队, pending=%d", n2.outbox.pendingLen())
	}

	// 未知通道标识的行应被安全丢弃（不误投）
	bad := `[{"kind":"mystery","msg":{"level":3,"title":"x"},"attempts":1,"next_at":"2026-01-01T00:00:00Z","deliver_str":"mystery"}]`
	p2 := dir + "/bad.json"
	os.WriteFile(p2, []byte(bad), 0o600)
	n3 := New()
	n3.SetOutboxPersistPath(p2)
	if n3.outbox.pendingLen() != 0 {
		t.Fatal("未知通道的持久化行应被丢弃")
	}
}

// fakeGatewayOK 恒成功的推送网关桩。
type fakeGatewayOK struct{}

func (fakeGatewayOK) Send(Message) error { return nil }

func TestParseHM(t *testing.T) {
	if v, ok := parseHM("22:05"); !ok || v != 22*60+5 {
		t.Fatalf("parseHM(22:05)=%d,%v", v, ok)
	}
	if _, ok := parseHM("25:00"); ok {
		t.Fatal("非法小时应解析失败")
	}
	if _, ok := parseHM("abc"); ok {
		t.Fatal("非时间串应解析失败")
	}
	if !strings.Contains(strings.ToLower("OK"), "ok") {
		t.Fatal("占位断言")
	}
}
