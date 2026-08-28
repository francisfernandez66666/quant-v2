// outbox.go — §GAP5.2 推送补投队列（outbox）：投递失败的消息按指数退避重试，
// 超过上限记死信日志后放弃。对照 qmt_gateway G9 的回报 outbox，Go 通知侧的对等物——
// 此前 JPush/Webhook 失败仅记日志即丢，关键提醒（清仓/止损/交易信号）可能静默丢失。
//
// §R3-8 P1-D 三处重构：
//  1. 投递函数随 item 携带——此前首条消息的 deliver 被永久固定为队列级函数，
//     后续 "gateway" 类失败会被重投到首个 webhook URL（kind 记录了却从不用于分发）；
//  2. 投递在锁外执行——此前 pump 全程持锁做 HTTP（最长 10s），故障期会拖死 Push/enqueue 主路径；
//  3. 可选磁盘持久化——SetPersistPath 后变更即落盘、重启续发，兑现"qmt_gateway G9 对等物"
//     最关键的持久化语义（此前纯内存，重启丢全部待补投的止损/清仓提醒）。
//
// English: push-retry outbox — failed deliveries retry with exponential backoff and dead-letter past
// the cap. R3-8 P1-D: per-item deliverers (no more first-writer-wins kind mixing), delivery happens
// outside the lock, and an optional persist file survives restarts.
package notify

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/fileutil"
)

const (
	outboxMaxAttempts = 5 // 单条最大尝试次数（含首次失败后的全部重试）
	outboxBaseDelay   = 30 * time.Second
	outboxMaxDelay    = 10 * time.Minute
)

// outboxItem 运行时待补投条目：deliver 为闭包（不可序列化），持久化走 outboxPersistItem。
type outboxItem struct {
	kind     string                      // 目标标识："webhook:<url>" | "gateway"（同时是投递通道稳定键）
	msg      Message                     // 原始消息体
	attempts int                         // 已尝试次数
	nextAt   time.Time                   // 下次尝试时间
	deliver  func(string, Message) error // 投递函数（随 item 携带；§R3-8 P1-D 不再队列级固定）
}

// outboxPersistItem 磁盘序列化形态（无函数字段）。deliverStr 可经 owner 重建投递函数。
type outboxPersistItem struct {
	// 消息类型
	Kind string `json:"kind"`
	// 消息内容
	Msg Message `json:"msg"`
	// 已投递尝试次数
	Attempts int `json:"attempts"`
	// 下次重试时间
	NextAt time.Time `json:"next_at"`
	// 投递策略字符串
	DeliverStr string `json:"deliver_str"`
}

// Outbox 补投队列：惰性启动后台重试协程（首条入队时拉起），进程生命周期内有效。
type Outbox struct {
	mu          sync.Mutex     // 保护队列状态的互斥锁
	items       []outboxItem   // 待补投消息条目
	started     bool           // 后台重试协程是否已启动
	stop        chan struct{}  // 停止后台协程的信号通道
	owner       *Notifier      // 重建持久化条目的投递函数用（New 时绑定）
	persistPath string         // 非空时启用磁盘持久化（重启续发）
	saveWG      sync.WaitGroup // 追踪在途持久化写，Stop 时等待其完成以免退出后仍在重写文件
}

// SetPersistPath 启用磁盘持久化并加载既有队列（须在首次 enqueue 前调用；文件不存在/损坏则从空队开始）。
// 无法重建投递通道的行（未知 deliverStr）直接丢弃——安全侧宁可少补投，不误投错通道。
// English: enables persistence and loads any existing queue; rows whose channel cannot be rebuilt
// are dropped (never mis-routed).
func (o *Outbox) SetPersistPath(path string) {
	if o == nil || path == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.persistPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return // 首次运行：空队
	}
	var persisted []outboxPersistItem
	if err := json.Unmarshal(data, &persisted); err != nil {
		log.Printf("[notify][outbox] 持久化文件损坏，丢弃重建: %v", err)
		return
	}
	n := 0
	for _, p := range persisted {
		dl, ok := outboxDelivererFor(o.owner, p.DeliverStr)
		if !ok {
			continue
		}
		o.items = append(o.items, outboxItem{
			kind: p.Kind, msg: p.Msg, attempts: p.Attempts,
			nextAt: p.NextAt, deliver: dl,
		})
		n++
	}
	if n > 0 {
		log.Printf("[notify][outbox] 已恢复 %d 条待补投（%s）", n, filepath.Base(path))
	}
}

// bindOwner 绑定所属 Notifier（New 时调用；重建持久化条目投递函数用）。
func (o *Outbox) bindOwner(n *Notifier) { o.owner = n }

// outboxDelivererFor 从稳定通道标识重建投递函数；未知标识返回 false。
// English: rebuilds a deliverer from its stable channel key; unknown keys fail.
func outboxDelivererFor(n *Notifier, deliverStr string) (func(string, Message) error, bool) {
	if n == nil {
		return nil, false
	}
	switch {
	case deliverStr == "gateway":
		return deliverGateway(n), true
	case strings.HasPrefix(deliverStr, "webhook:"):
		return deliverWebhook(n, strings.TrimPrefix(deliverStr, "webhook:")), true
	}
	return nil, false
}

// enqueue 失败消息入队并惰性启动重试协程。
func (o *Outbox) enqueue(kind string, msg Message, deliver func(string, Message) error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.items = append(o.items, outboxItem{
		kind: kind, msg: msg, attempts: 1,
		nextAt:  time.Now().Add(outboxBaseDelay),
		deliver: deliver,
	})
	if !o.started {
		o.started = true
		o.stop = make(chan struct{})
		go o.loop(o.stop)
	}
	o.saveLocked()
	o.mu.Unlock()
	log.Printf("[notify][outbox] 入队补投 kind=%s title=%q（第 1 次失败）", kind, msg.Title)
}

// loop 重试主循环：每秒检查到期项，指数退避（30s 起步 ×2，封顶 10min），5 次后死信。
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

// Stop 停止后台重试协程并释放其持有的持久化写句柄，避免进程/测试退出后
// 仍有 goroutine 持续重写 outbox.json 导致资源泄漏或临时目录无法清理。
// 幂等：未启动时直接返回；已停止后重复调用安全。
// English: stops the background retry goroutine and releases its file handle so it no
// longer rewrites outbox.json after the owner is gone. Idempotent and safe to call once.
func (o *Outbox) Stop() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.started {
		o.started = false
		close(o.stop) // 触发 loop 退出（enqueue 中仅创建一次，不会重复 close）
	}
	o.mu.Unlock()
	// 无论后台协程是否启动，都等待在途持久化写完成，确保退出后不再重写 outbox.json
	o.saveWG.Wait()
}

// pump 扫描到期项逐条重试。§R3-8 P1-D：收集与状态回写持锁、HTTP 投递放锁外——
// 此前全程持锁，单条 10s 超时会阻塞 Push()/enqueue() 主路径。
// English: R3-8 P1-D — due items are collected under lock but delivered outside it, so a slow
// webhook can no longer stall the main push path.
func (o *Outbox) pump() {
	type job struct {
		idx  int
		item outboxItem
	}
	o.mu.Lock()
	now := time.Now()
	jobs := make([]job, 0, len(o.items))
	for i := range o.items {
		if !o.items[i].nextAt.After(now) {
			jobs = append(jobs, job{idx: i, item: o.items[i]})
		}
	}
	o.mu.Unlock()

	for _, j := range jobs {
		err := j.item.deliver(j.item.kind, j.item.msg)
		o.mu.Lock()
		// 队列可能已被并发 enqueue 改动：按 idx 定位并校验身份（kind+title+nextAt）后再改写。
		if j.idx < len(o.items) && o.items[j.idx].nextAt.Equal(j.item.nextAt) &&
			o.items[j.idx].kind == j.item.kind && o.items[j.idx].msg.Title == j.item.msg.Title {
			if err == nil {
				log.Printf("[notify][outbox] 补投成功 kind=%s title=%q", j.item.kind, j.item.msg.Title)
				o.items = append(o.items[:j.idx], o.items[j.idx+1:]...) // 成功出队
			} else {
				it := o.items[j.idx]
				it.attempts++
				if it.attempts > outboxMaxAttempts {
					log.Printf("[notify][outbox] 死信：kind=%s title=%q 已尝试 %d 次仍失败，放弃",
						it.kind, it.msg.Title, it.attempts-1)
					o.items = append(o.items[:j.idx], o.items[j.idx+1:]...)
				} else {
					delay := outboxBaseDelay << (it.attempts - 1) // 30s/1min/2min/4min/8min
					if delay > outboxMaxDelay {
						delay = outboxMaxDelay
					}
					it.nextAt = time.Now().Add(delay)
					o.items[j.idx] = it
				}
			}
			o.saveLocked()
		}
		o.mu.Unlock()
	}
}

// saveLocked 变更落盘（调用方须持锁；异步原子写不阻塞推送路径）。
// English: persists the queue asynchronously after mutations; caller holds the lock.
func (o *Outbox) saveLocked() {
	if o.persistPath == "" {
		return
	}
	items := make([]outboxPersistItem, len(o.items))
	for i, it := range o.items {
		items[i] = outboxPersistItem{
			Kind: it.kind, Msg: it.msg, Attempts: it.attempts,
			NextAt: it.nextAt, DeliverStr: it.kind,
		}
	}
	o.saveWG.Add(1)
	go func(path string, items []outboxPersistItem) {
		defer o.saveWG.Done()
		data, err := json.Marshal(items)
		if err != nil {
			return
		}
		if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
			log.Printf("[notify][outbox] 持久化失败: %v", err)
		}
	}(o.persistPath, items)
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
