// outbox_test.go — §GAP5.2 回归：静默时段门控（跨午夜）+ 补投队列重试与死信 + §R3-8 P1-D 持久化。
package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestQuietHoursWindow QuietHoursWindow。
// 静默时段窗口判定（含跨零点）。
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
// 静默时段只压低优先级消息、放行高优先级。
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
// outbox 对失败消息按序重试直至送达。
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
// 返回固定失败串，模拟投递失败。
func (errFake) Error() string { return "fake delivery failure" }

// forceAllDue 把队列内全部条目推到已到期（真实退避 30s 起步，测试不等钟）。
func forceAllDue(o *Outbox) {
	o.mu.Lock()
	for i := range o.items {
		o.items[i].nextAt = time.Now().Add(-time.Second)
	}
	o.mu.Unlock()
}

// TestOutboxBatchPumpSingleDelivery §H9 回归：同一轮多条到期条目应各自恰好投递一次、
// 队列清空、后续空轮零重投。旧实现快照后按数组下标做身份校验——首条成功出队使后续条目
// 在队列中整体前移、校验必然失败 → 既不删除也不退避，每秒整批重投（重投风暴）。
// English: H9 — a batch of due items must each deliver exactly once and drain the queue.
func TestOutboxBatchPumpSingleDelivery(t *testing.T) {
	o := &Outbox{}
	titles := []string{"A", "B", "C"}
	var mu sync.Mutex
	calls := map[string]int{}
	for _, title := range titles {
		title := title
		o.enqueue("webhook:x", Message{Level: LevelHigh, Title: title}, func(string, Message) error {
			mu.Lock()
			calls[title]++
			mu.Unlock()
			return nil
		})
	}
	o.Stop() // 停掉惰性启动的后台协程，测试内只由 pump 同步驱动，杜绝计时器干扰

	forceAllDue(o)
	o.pump()
	if got := o.pendingLen(); got != 0 {
		t.Fatalf("同轮 %d 条到期应一次 pump 全部出队, pending=%d", len(titles), got)
	}
	mu.Lock()
	for _, title := range titles {
		if calls[title] != 1 {
			t.Fatalf("%s 应恰好投递一次, got %d", title, calls[title])
		}
	}
	mu.Unlock()

	// 空轮补验：无到期项时不得产生任何重投
	o.pump()
	mu.Lock()
	defer mu.Unlock()
	for _, title := range titles {
		if calls[title] != 1 {
			t.Fatalf("空轮不应重投 %s, got %d", title, calls[title])
		}
	}
}

// TestOutboxBatchPumpMixedOutcome §H9 回归（成败混合形态）：同轮 3 条到期，中间一条持续失败，
// 前序出队不得使后序成功条目定位漂移——A/C 恰投一次出队，B 失败一次后退避留在队列，
// 而非旧实现下整批每秒重投。
// English: H9 — a mid-batch failure must not shift the later successes out of the queue either.
func TestOutboxBatchPumpMixedOutcome(t *testing.T) {
	o := &Outbox{}
	var mu sync.Mutex
	calls := map[string]int{}
	for _, title := range []string{"A", "B", "C"} {
		title := title
		o.enqueue("webhook:x", Message{Level: LevelHigh, Title: title}, func(string, Message) error {
			mu.Lock()
			calls[title]++
			mu.Unlock()
			if title == "B" {
				return errFake{} // 中间条持续失败
			}
			return nil
		})
	}
	o.Stop()

	forceAllDue(o)
	o.pump()
	if got := o.pendingLen(); got != 1 {
		t.Fatalf("A/C 应出队、B 应退避留队, pending=%d", got)
	}
	mu.Lock()
	gotA, gotB, gotC := calls["A"], calls["B"], calls["C"]
	mu.Unlock()
	if gotA != 1 || gotB != 1 || gotC != 1 {
		t.Fatalf("首轮每条应各投一次, A=%d B=%d C=%d", gotA, gotB, gotC)
	}
	o.mu.Lock()
	bDue := !o.items[0].nextAt.After(time.Now())
	o.mu.Unlock()
	if bDue {
		t.Fatal("B 失败后应退避到未来时间（非秒级重投）")
	}

	// 再驱动一轮：仅 B 到期重投，A/C 已出队不得复现
	forceAllDue(o)
	o.pump()
	if got := o.pendingLen(); got != 1 {
		t.Fatalf("B 再失败应仍留队退避, pending=%d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["A"] != 1 || calls["C"] != 1 {
		t.Fatalf("已出队条目不得被重投, A=%d C=%d", calls["A"], calls["C"])
	}
	if calls["B"] != 2 {
		t.Fatalf("B 应累计投递 2 次, got %d", calls["B"])
	}
}

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
	// 停止后台重试协程，释放持久化写句柄，避免退出后仍在重写 outbox.json 导致临时目录无法清理
	defer n1.outbox.Stop()
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
	defer n2.outbox.Stop()
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

// Send 测试桩：总是成功。
func (fakeGatewayOK) Send(Message) error { return nil }

// TestOutboxSingleWriterDurableOnStop §N-7（2026-09-22 PM 批）回归：高频入队后 Stop——
// 旧实现 saveLocked 每次变更各起一个写协程并发 AtomicWrite 同一文件（Windows 上多个
// rename 互相踩踏即 Access is denied → 落盘持续失败），且各协程持不同时刻快照、
// 完成顺序无保证，盘上终态可能倒回更早的更小规模队列。现收敛为单写者 +
// 最新快照胜出 + 退出前终刷：Stop() 返回即要求文件与内存队列全量一致。
// English: §N-7 — Stop must leave the file equal to the full in-memory queue: the old per-change
// writer goroutines raced on one file (Access denied on Windows) and could let an older snapshot win.
func TestOutboxSingleWriterDurableOnStop(t *testing.T) {
	path := t.TempDir() + "/outbox.json"
	n := New()
	n.SetOutboxPersistPath(path)
	for i := 0; i < 50; i++ {
		n.outbox.enqueue("gateway", Message{Level: LevelHigh, Title: fmt.Sprintf("t%02d", i)},
			func(string, Message) error { return errFake{} })
	}
	n.outbox.Stop() // 返回=唯一落盘协程已终刷（saveWG 覆盖 loop 生命周期）
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Stop 后应已落盘: %v", err)
	}
	var items []outboxPersistItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("落盘内容应为合法 JSON（并发写撕裂?）: %v", err)
	}
	if len(items) != 50 {
		t.Fatalf("Stop 返回时盘上队列应与内存全量一致（最新快照胜出），got %d/50", len(items))
	}
}

// TestParseHM 验证 "HH:MM" 解析：正常时间转分钟数、非法值解析失败。
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
