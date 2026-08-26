// outbox.go — §GAP5.2 推送补投队列（outbox）：投递失败的消息按指数退避重试，
// 超过上限记死信日志后放弃。对照 qmt_gateway G9 的回报 outbox，Go 通知侧的对等物——
// 此前 JPush/Webhook 失败仅记日志即丢，关键提醒（清仓/止损/交易信号）可能静默丢失。
// English: push-retry outbox — failed deliveries are retried with exponential backoff and
// dead-lettered (logged) past the attempt cap; the Go-side counterpart of the gateway report outbox.
package notify

import (
	"log"
	"sync"
	"time"
)

const (
	outboxMaxAttempts = 5 // 单条最大尝试次数（含首次失败后的全部重试）
	outboxBaseDelay   = 30 * time.Second
	outboxMaxDelay    = 10 * time.Minute
)

// outboxItem 一条待补投消息。
type outboxItem struct {
	kind     string    // 目标标识："webhook:<url>" | "gateway"
	msg      Message   // 原始消息体
	attempts int       // 已尝试次数
	nextAt   time.Time // 下次尝试时间
}

// Outbox 补投队列：惰性启动后台重试协程（首条入队时拉起），进程生命周期内有效。
type Outbox struct {
	mu      sync.Mutex
	items   []outboxItem
	started bool
	stop    chan struct{}
	deliver func(kind string, msg Message) error // 投递函数（由 Notifier 注入）
}

// enqueue 失败消息入队并惰性启动重试协程。
func (o *Outbox) enqueue(kind string, msg Message, deliver func(string, Message) error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.deliver == nil {
		o.deliver = deliver
	}
	o.items = append(o.items, outboxItem{kind: kind, msg: msg, attempts: 1, nextAt: time.Now().Add(outboxBaseDelay)})
	if !o.started {
		o.started = true
		o.stop = make(chan struct{})
		go o.loop(o.stop)
	}
	o.mu.Unlock()
	log.Printf("[notify][outbox] 入队补投 kind=%s title=%q（第 1 次失败）", kind, msg.Title)
}

// loop 重试主循环：每秒检查队首到期项，指数退避（30s 起步 ×2，封顶 10min），5 次后死信。
func (o *Outbox) loop(stop chan struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			o.pump()
		}
	}
}

// pump 扫描到期项逐条重试。
func (o *Outbox) pump() {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	kept := o.items[:0]
	for _, it := range o.items {
		if it.nextAt.After(now) {
			kept = append(kept, it)
			continue
		}
		if err := o.deliver(it.kind, it.msg); err == nil {
			log.Printf("[notify][outbox] 补投成功 kind=%s title=%q", it.kind, it.msg.Title)
			continue
		}
		it.attempts++
		if it.attempts > outboxMaxAttempts {
			log.Printf("[notify][outbox] 死信：kind=%s title=%q 已尝试 %d 次仍失败，放弃", it.kind, it.msg.Title, it.attempts-1)
			continue
		}
		delay := outboxBaseDelay << (it.attempts - 1) // 30s/1min/2min/4min/8min
		if delay > outboxMaxDelay {
			delay = outboxMaxDelay
		}
		it.nextAt = time.Now().Add(delay)
		kept = append(kept, it)
	}
	o.items = kept
}

// pendingLen 待补投数量（诊断用）。
func (o *Outbox) pendingLen() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.items)
}
