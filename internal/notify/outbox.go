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
	"quant-trading-v2/internal/opslog"
)

// 外发重试参数：单条最大尝试次数与退避时间上下限。
const (
	outboxMaxAttempts = 5 // 单条最大尝试次数（含首次失败后的全部重试）
	outboxBaseDelay   = 30 * time.Second
	outboxMaxDelay    = 10 * time.Minute
)

// outboxItem 运行时待补投条目：deliver 为闭包（不可序列化），持久化走 outboxPersistItem。
type outboxItem struct {
	id       int64                       // §H9 队列内唯一 ID（入队时分配）：批量投递回写定位用，不依赖易漂移的数组下标
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
	mu          sync.Mutex    // 保护队列状态的互斥锁
	items       []outboxItem  // 待补投消息条目
	nextID      int64         // §H9 条目唯一 ID 计数器（进程内单调递增；持久化行加载时重新分配）
	started     bool          // 后台重试协程是否已启动
	stop        chan struct{} // 停止后台协程的信号通道
	owner       *Notifier     // 重建持久化条目的投递函数用（New 时绑定）
	persistPath string        // 非空时启用磁盘持久化（重启续发）
	// §N-7（2026-09-22 PM 批）落盘收敛为单写者：旧实现 saveLocked 每次变更各起一个 goroutine
	// 做 AtomicWrite，多个 rename 并发砸同一目标文件——Windows 上目标正被另一 rename/读方
	// 占住即 Access is denied，通知队列落盘持续失败；且 goroutine 拿锁顺序无保证，
	// 旧快照可能后写覆盖新快照。现改为：变更只更新 pending 快照 + 给唯一落盘协程(loop)
	// 发合并信号（dirty chan，容量 1 天然合批）。
	// English: §N-7 — persistence collapsed to a single writer: mutations only stash the latest
	// snapshot and signal the one loop goroutine (cap-1 dirty chan coalesces bursts), so concurrent
	// renames of the same file (Access is denied on Windows) and old-overwrites-new snapshots are gone.
	dirty   chan struct{}       // 容量 1：合并"请落盘"信号，仅由 loop/兜底写者消费
	pending []outboxPersistItem // 待落盘最新快照（持锁读写）
	saving  bool                // pending 是否有效（区分"空队列待落盘"与"无变更"）
	saveMu  sync.Mutex          // flushPending 串行闸：loop 落盘与 Stop 兜底/无协程兜底不并发
	saveWG  sync.WaitGroup      // 追踪落盘协程（loop 与兜底写者），Stop 时等待其退出
	failSeq int                 // §N-7 连续落盘失败计数（计入告警节流，成功即清零）
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
	// 恢复上次未投递完的消息：解析持久化项并按投递器重建队列。
	n := 0
	for _, p := range persisted {
		dl, ok := outboxDelivererFor(o.owner, p.DeliverStr)
		if !ok {
			continue
		}
		o.nextID++ // §H9 ID 只在进程内有意义：持久化行加载时重新分配，避免与在途计数冲突
		o.items = append(o.items, outboxItem{
			id: o.nextID, kind: p.Kind, msg: p.Msg, attempts: p.Attempts,
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
	o.nextID++ // §H9 入队即分配进程内唯一 ID，投递回写按 ID 定位（不再依赖数组下标）
	o.items = append(o.items, outboxItem{
		id: o.nextID, kind: kind, msg: msg, attempts: 1,
		nextAt:  time.Now().Add(outboxBaseDelay),
		deliver: deliver,
	})
	if !o.started {
		o.started = true
		// §N-7 计数在启动方（持锁、go 之前）登记，而非 loop 协程内部：
		// Add 落在协程内会与 Stop() 的 saveWG.Wait() 形成"Wait 已返回后才 Add"的竞态窗口。
		// English: register the WaitGroup count in the starter (under lock, before go), not inside
		// loop — otherwise Stop()'s Wait could observe zero and return before the goroutine Adds.
		o.saveWG.Add(1)
		stopCh := make(chan struct{})
		o.stop = stopCh
		o.dirty = make(chan struct{}, 1) // §N-7 与 loop 同生命周期：唯一落盘协程的合并信号
		// 传局部变量而非 o.stop 字段：go 语句的实参在新协程内求值时会与 Stop() 对字段的
		// 写形成数据竞争（-race 实测），局部副本彻底隔离。
		// English: pass a local, not the o.stop field — evaluating the field inside the new
		// goroutine races with Stop()'s writes (caught by -race).
		go o.loop(stopCh)
	}
	o.saveLocked()
	o.mu.Unlock()
	log.Printf("[notify][outbox] 入队补投 kind=%s title=%q（第 1 次失败）", kind, msg.Title)
}

// loop 重试主循环：每秒检查到期项，指数退避（30s 起步 ×2，封顶 10min），5 次后死信。
// §N-7：loop 同时是持久化的唯一落盘协程——消费 dirty 合并信号做增量写，
// 退出前（收到 stop）最后补一次 flush，保证 Stop() 返回时已入队变更全部落盘。
// English: the loop is also the single persistence writer — it consumes coalesced dirty signals
// and performs one final flush on stop, so everything enqueued before Stop() is on disk when it returns.
func (o *Outbox) loop(stop chan struct{}) {
	defer o.saveWG.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			o.flushPending()
			return
		case <-tick.C:
			o.pump()
		case <-o.dirty:
			o.flushPending()
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
// §H9：回写定位改按条目唯一 id 而非快照数组下标——同一批内前一条成功出队会让后续条目
// 在队列中整体前移，按 idx 做的身份校验必然失败，条目既不删除也不退避，每秒整批重投（风暴）。
// English: R3-8 P1-D — due items are collected under lock but delivered outside it, so a slow
// webhook can no longer stall the main push path.
// English: H9 — write-back locates items by their unique id instead of the snapshot index; an
// earlier successful dequeue shifts the rest of the queue and broke the old index-based check.
func (o *Outbox) pump() {
	// job 待投递任务：条目唯一 ID + 对应 outboxItem 快照。
	type job struct {
		id   int64
		item outboxItem
	}
	o.mu.Lock()
	now := time.Now()
	jobs := make([]job, 0, len(o.items))
	for i := range o.items {
		if !o.items[i].nextAt.After(now) {
			jobs = append(jobs, job{id: o.items[i].id, item: o.items[i]})
		}
	}
	o.mu.Unlock()

	for _, j := range jobs {
		err := j.item.deliver(j.item.kind, j.item.msg)
		o.mu.Lock()
		// §H9 按 id 在最新队列中定位本条：deliver 期间队列可能被并发 enqueue 追加、
		// 或同批次前序条目出队而整体移位，id 是唯一稳定锚点；找不到 = 已被处置，跳过。
		idx := -1
		for i := range o.items {
			if o.items[i].id == j.id {
				idx = i
				break
			}
		}
		if idx >= 0 {
			if err == nil {
				log.Printf("[notify][outbox] 补投成功 kind=%s title=%q", j.item.kind, j.item.msg.Title)
				o.items = append(o.items[:idx], o.items[idx+1:]...) // 成功出队
			} else {
				it := o.items[idx]
				it.attempts++
				if it.attempts > outboxMaxAttempts {
					log.Printf("[notify][outbox] 死信：kind=%s title=%q 已尝试 %d 次仍失败，放弃",
						it.kind, it.msg.Title, it.attempts-1)
					o.items = append(o.items[:idx], o.items[idx+1:]...)
				} else {
					delay := outboxBaseDelay << (it.attempts - 1) // 30s/1min/2min/4min/8min
					// 指数退避要封顶，否则尝试次数一多位移出来的下次时间会漂到几天后；
					// 封顶后把条目留在队列里，等 saveLocked 落盘继续排班。
					if delay > outboxMaxDelay {
						delay = outboxMaxDelay
					}
					it.nextAt = time.Now().Add(delay)
					o.items[idx] = it
				}
			}
			o.saveLocked()
		}
		o.mu.Unlock()
	}
}

// saveLocked 登记落盘请求（调用方须持锁）。§N-7：不再每次变更各起一个写协程——
// 只把最新快照放进 pending，并给唯一落盘协程发合并信号；无在跑写者时兜底起一个
// 短命写者（saveMu 保证任何时刻至多一个写在进行）。推送路径仍不被文件 IO 阻塞。
// English: stashes the latest snapshot and signals the single writer (cap-1 coalescing); if no
// writer is running, spawns a short-lived one, serialized by saveMu. The push path never blocks on IO.
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
	o.pending = items // 最新快照胜出：旧快照永不后写覆盖新状态
	o.saving = true
	if o.started && o.dirty != nil {
		select {
		case o.dirty <- struct{}{}:
		default: // 已有待处理信号——合并，loop 消费时自会取最新 pending
		}
		return
	}
	o.saveWG.Add(1)
	go func() {
		defer o.saveWG.Done()
		o.flushPending()
	}()
}

// flushPending 取走最新快照并原子写盘（saveMu 串行化的单写者临界区）。
// 失败处理：连续失败只首报+每 10 次一报（防刷日志），同时经 opslog 计入当日告警——
// 落盘持续失败意味着重启会丢补投队列（止损/清仓提醒），必须可见。
// English: drains the pending snapshot under saveMu; failures are counted, throttled in logs and
// escalated once per day via opslog (a stalled persistence means the restart loses retry items).
func (o *Outbox) flushPending() {
	// saveMu 先行：取快照+写盘整体串行，杜绝"取旧快照者后写覆盖取新快照者"的顺序倒挂
	o.saveMu.Lock()
	defer o.saveMu.Unlock()
	o.mu.Lock()
	if !o.saving {
		o.mu.Unlock()
		return
	}
	items := o.pending
	o.pending = nil
	o.saving = false
	path := o.persistPath
	o.mu.Unlock()
	if path == "" {
		return
	}
	data, err := json.Marshal(items)
	if err != nil {
		log.Printf("[notify][outbox] 持久化序列化失败（跳过本次落盘）: %v", err)
		return
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		o.failSeq++
		if o.failSeq == 1 || o.failSeq%10 == 0 {
			log.Printf("[notify][outbox] 持久化失败（连续第 %d 次）: %v", o.failSeq, err)
		}
		opslog.DayOnce("outbox-persist-fail", func() {
			opslog.Logf("notify", "通知 outbox 落盘失败（重启将丢补投队列）: %v", err)
		})
		return
	}
	o.failSeq = 0
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
