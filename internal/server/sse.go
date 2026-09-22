// Package server 提供 HTTP 服务器及其相关功能。
// SSEBroker 实现 Server-Sent Events 的发布-订阅模式，用于向前端实时推送数据。
// English: Package server provides the HTTP server and its related functionality. SSEBroker
// implements a pub-sub model for Server-Sent Events to push realtime data to the frontend.
// （Package server provides the HTTP server. SSEBroker implements a pub-sub based Server-Sent Events
// broadcast for pushing realtime data to the frontend.）
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/metrics"
)

// sseTicketTTL 一次性票据有效期（60s 足够 EventSource 完成建链；过短体验差，过长放大泄漏面）。
const sseTicketTTL = 60 * time.Second

// sseTicketMaxLive 同时存活的票据上限：超过后新票挤出最旧票（防票据池内存放大）。
const sseTicketMaxLive = 1024

// sseTicket 一次有效的 SSE 建链票据：绑定签发用户，60s 后过期，消费后立即作废。
type sseTicket struct {
	userID   string
	expireAt time.Time
}

// newSSETicket 生成一个 24 字节随机 hex 票据；随机源失败时降级为时间戳+随机数拼接（仍满足
// 不可枚举，因为票据仅 60s 有效且一次性）。
// English: mintSSETicket generates a 24-byte random hex ticket (with a degraded fallback if the
// CSPRNG fails; the 60s one-time window still defeats enumeration).
func (s *Server) newSSETicket(userID string) string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("f%x-%x", time.Now().UnixNano(), b[0])
	}
	s.sseTicketsMu.Lock()
	defer s.sseTicketsMu.Unlock()
	if s.sseTickets == nil {
		s.sseTickets = make(map[string]sseTicket)
	}
	if len(s.sseTickets) >= sseTicketMaxLive {
		// 挤出最旧的一批（按时间淘汰约 1/4），保持池有界
		var oldest []string
		for k := range s.sseTickets {
			oldest = append(oldest, k)
			if len(oldest) >= sseTicketMaxLive/4 {
				break
			}
		}
		for _, k := range oldest {
			delete(s.sseTickets, k)
		}
	}
	tk := hex.EncodeToString(b[:])
	s.sseTickets[tk] = sseTicket{userID: userID, expireAt: time.Now().Add(sseTicketTTL)}
	return tk
}

// consumeSSETicket 校验并作废票据：不存在/已消费（已被 delete）→ 失败；命中即删除（一次性），
// 并返回票据绑定的账号。票据只由已认证用户签发，故命中票据即代表该账号已通过认证。
// English: consumeSSETicket validates and atomically consumes a one-time ticket (deleted on first
// use), returning the account the ticket was minted for. A valid ticket implies prior authentication.
func (s *Server) consumeSSETicket(tk string) (string, bool) {
	s.sseTicketsMu.Lock()
	defer s.sseTicketsMu.Unlock()
	v, ok := s.sseTickets[tk]
	if !ok {
		return "", false
	}
	delete(s.sseTickets, tk)
	if time.Now().After(v.expireAt) {
		return "", false
	}
	return v.userID, true
}

// sweepSSETickets 惰性清理过期票据（供建链/签发时顺带调用），防过期票滞留。
// English: sweepSSETickets lazily drops expired tickets.
func (s *Server) sweepSSETickets() {
	s.sseTicketsMu.Lock()
	defer s.sseTicketsMu.Unlock()
	for k, v := range s.sseTickets {
		if time.Now().After(v.expireAt) {
			delete(s.sseTickets, k)
		}
	}
}

// sseMaxHistory 每个账号在内存中保留的最近事件条数上限，用于断线续传（last-event-id）补发。
const sseMaxHistory = 200

// SSEEvent 单条 SSE 事件：包含全局自增序号 ID（写入 `id:` 行）与已序列化的数据。
type SSEEvent struct {
	ID   uint64 // 全局自增序号
	Data []byte // 已 JSON 序列化的事件数据
}

// SSEBroker SSE 事件广播器。
// 采用发布-订阅模式管理客户端连接，按账号分组（userID）支持定向推送与账号隔离，
// 并为每个账号保留最近事件缓冲以支持断线续传。
// 非阻塞发送：客户端消费不及时不会阻塞其他客户端。
type SSEBroker struct {
	mu      sync.RWMutex                          // 保护 clients/history/seq 的读写锁
	clients map[string]map[chan SSEEvent]struct{} // 账号 -> 该账号下订阅客户端 channel 集合
	history map[string][]SSEEvent                 // 账号 -> 最近事件环形缓冲（含 id，供断线补发）
	seq     uint64                                // 全局事件自增序号
	// §A3（AUDIT_FULLSTACK_20260918）慢客户端丢弃可见化与兜底断连：旧实现丢弃既无计数
	// 也不迫使重连，连接存活的慢客户端会永久停在陈旧数据上。现记 per-client 连续丢弃数
	// 与全局累计（进 quant_gauge_sse_dropped_total / sse_evictions_total）；连续丢弃达到
	// sseDropEvictLimit（=channel 缓冲 16 全满未消费，客户端已实质失能）即主动回收连接，
	// 迫使前端带 Last-Event-ID 重连走 history 补发路径。字段由 b.mu 保护。
	drops      map[chan SSEEvent]int // ch -> 连续丢弃计数（写成功清零）
	totalDrops int64                 // 进程生命周期累计丢弃事件数
	evictions  int64                 // 因越阈被主动回收的连接数
	// §UPDLINK（2026-09-22 H-4）：有界广播（BroadcastToWithin）因锁超预算丢推送的累计次数。
	// 独立于 b.mu——锁真被卡死时绝不能再抢同一把锁计数，否则监控本身也挂住。
	skipMu sync.Mutex
	skips  int64
	// §F-6（20260917 缺陷修复批）real_advice 最后一轮定向广播快照（按账号）：
	// GET /api/positions/advice 用它做 REST 回填——旧实现恒返空列表，断线超补发窗
	// （history 200 条）或页面重载后卖出建议无从补齐，只能等下一次 5s 循环广播。
	advMu   sync.Mutex
	lastAdv map[string]adviceSnapshot // userID -> 最近一次 real_advice 事件
}

// adviceSnapshot 一轮 real_advice 广播的原始 JSON 与时间戳（原样暂存，读取方自行解析）。
type adviceSnapshot struct {
	at   time.Time
	data []byte
}

// NewSSEBroker 创建 SSEBroker 实例，初始化账号映射与历史缓冲表。
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[string]map[chan SSEEvent]struct{}),
		history: make(map[string][]SSEEvent),
		lastAdv: make(map[string]adviceSnapshot),
		drops:   make(map[chan SSEEvent]int),
	}
}

// sseDropEvictLimit 连续丢弃阈值（=SubscribeFor 的 channel 缓冲 16）：缓冲整体积满且仍
// 追不上即判定客户端失能，主动断连换取带 Last-Event-ID 的重连补发（§A3）。
const sseDropEvictLimit = 16

// pushToCh 向单个客户端 channel 非阻塞写入；channel 满则丢弃（慢客户端不阻塞发送方）
// 并记 §A3 连续丢弃计数；写成功清零计数。调用方持 b.mu。返回是否发生丢弃。
func (b *SSEBroker) pushToCh(ch chan SSEEvent, ev SSEEvent) bool {
	select {
	case ch <- ev:
		if b.drops[ch] > 0 {
			b.drops[ch] = 0
		}
		return false
	default:
		b.totalDrops++
		b.drops[ch]++
		metrics.SetGauge("sse_dropped_total", b.totalDrops)
		return true
	}
}

// record 为指定账号追加一条事件到历史缓冲；超出上限时丢弃最旧一条（环形）。
func (b *SSEBroker) record(userID string, ev SSEEvent) {
	h := b.history[userID]
	h = append(h, ev)
	if len(h) > sseMaxHistory {
		h = h[len(h)-sseMaxHistory:]
	}
	b.history[userID] = h
}

// nextID 分配并返回下一个全局事件序号。
func (b *SSEBroker) nextID() uint64 {
	b.seq++
	return b.seq
}

// Subscribe 注册一个全局 SSE 客户端（不区分账号），返回接收数据的 channel。
// 兼容旧接口：等价于 SubscribeFor("", 0)。
func (b *SSEBroker) Subscribe() chan SSEEvent {
	return b.SubscribeFor("", 0)
}

// SubscribeFor 按账号注册一个 SSE 客户端，返回接收数据的 channel（缓冲 16）。
// userID 用于账号隔离与定向推送；空字符串表示全局分组。
// lastID 为客户端最后收到的事件序号：>0 时立即把该账号历史中序号更大的事件补发进 channel，
// 实现断线续传（SSE Last-Event-ID）。
func (b *SSEBroker) SubscribeFor(userID string, lastID uint64) chan SSEEvent {
	ch := make(chan SSEEvent, 16)
	b.mu.Lock()
	if b.clients[userID] == nil {
		b.clients[userID] = make(map[chan SSEEvent]struct{})
	}
	b.clients[userID][ch] = struct{}{}
	// 断线续传：把历史中 lastID 之后的事件按顺序补发（channel 刚创建为空，缓冲足够）
	if lastID > 0 {
		for _, ev := range b.history[userID] {
			if ev.ID > lastID {
				b.pushToCh(ch, ev)
			}
		}
		// §A3：补发窗最多 200 条 > 缓冲 16，回放期溢满是常态而非客户端失能，
		// 清掉回放产生的连丢计数，避免健康重连刚建立就被误回收。
		b.drops[ch] = 0
	}
	b.mu.Unlock()
	return ch
}

// Unsubscribe 注销一个全局 SSE 客户端（兼容旧接口，遍历所有账号分组移除）。
func (b *SSEBroker) Unsubscribe(ch chan SSEEvent) {
	b.UnsubscribeFor("", ch)
}

// UnsubscribeFor 注销指定账号下的一个 SSE 客户端，关闭并移除其 channel。
// §修复 FIX#5（2026-09-04）：close(ch) 移入锁内——旧实现先解锁再关闭，与 Broadcast/BroadcastTo
// 持锁对每个已注册 channel 非阻塞 send 并发 → `send on closed channel` panic。
// 现在先删注册再关 channel，注销与广播在锁内互斥，杜绝向已关闭 channel 发送。
// §UPDLINK（2026-09-22 · AUDIT_E2E_FULL H-4，P0）：close 从「无条件」改为「注册表命中才关」，
// 并用 defer 解锁兜底。原因是同一个客户端 channel 存在两条关闭路径——§A3 慢客户端回收
// （evict→UnsubscribeFor）与 handleFixSSE 的 defer UnsubscribeFor：FIX#5 之后先到的路径已把 ch
// 从注册表删除，后到的路径 delete 是 no-op 但 close 照关 → `close of closed channel` panic，
// 而 panic 点在持 b.mu 区间内、Unlock 语句被跳过 → **广播锁永久泄漏**，此后任何
// Broadcast/BroadcastTo 阻塞，而 POST /api/qmt/report 尾部必经 BroadcastTo → 网关上行回报
// （positions/account）全线挂死、实盘账冻结成旧照片（生产实录：10:02:43 panic → 11:46 仍无恢复）。
// English: §UPDLINK — make the close conditional on registry membership (idempotent unsubscribe)
// and defer the unlock so no panic can leak the broadcast lock. Previously the §A3 eviction path and
// the handler's deferred path could both close the same channel; the second close panicked while
// holding b.mu, permanently leaking the lock and deadlocking every uplink report.
func (b *SSEBroker) UnsubscribeFor(userID string, ch chan SSEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock() // 锁内任何一步 panic 也不会把广播锁留在持有态（H-4 根因二）
	if !b.unregisterLocked(userID, ch) {
		return // 已被另一条路径注销并关闭：幂等跳过，绝不双关
	}
	close(ch)
}

// unregisterLocked 把 ch 从注册表摘除，并返回它此前是否确实登记在案（= 本次调用拥有关闭权）。
// 先在调用方声明的分组查；未命中再遍历全分组兜底——否则 userID 传参与订阅不一致时 channel
// 既不被关闭（写循环 goroutine 永久挂起）也永不注销。调用方须持有 b.mu。
// English: unregisterLocked removes ch from the registry and reports whether it was registered
// (i.e. whether this caller owns the close); falls back to scanning all groups.
func (b *SSEBroker) unregisterLocked(userID string, ch chan SSEEvent) bool {
	found := b.detachLocked(userID, ch)
	if !found {
		for uid := range b.clients {
			if b.detachLocked(uid, ch) {
				found = true
				break
			}
		}
	}
	delete(b.drops, ch) // §A3 客户端注销即清失能计数
	return found
}

// detachLocked 从单个账号分组移除 ch（分组空则删键），返回是否命中。调用方持 b.mu。
func (b *SSEBroker) detachLocked(userID string, ch chan SSEEvent) bool {
	set := b.clients[userID]
	if set == nil {
		return false
	}
	if _, ok := set[ch]; !ok {
		return false
	}
	delete(set, ch)
	if len(set) == 0 {
		delete(b.clients, userID)
	}
	return true
}

// evictStalledLocked 在持 b.mu 的广播循环后调用：把连续丢弃越阈的客户端挑出来，
// 返回 (userID, ch) 列表；真正的移除+close 由调用方在锁外经 UnsubscribeFor 完成
// （FIX#5 语义：close 必须与广播在锁内互斥，UnsubscribeFor 自带锁，绝不可在持锁时调用）。
func (b *SSEBroker) evictStalledLocked() []sseEvictTarget {
	var out []sseEvictTarget
	for userID, set := range b.clients {
		for ch := range set {
			if b.drops[ch] >= sseDropEvictLimit {
				out = append(out, sseEvictTarget{userID: userID, ch: ch})
			}
		}
	}
	return out
}

// sseEvictTarget 一个待回收的失能客户端（账号分组 + channel）。
type sseEvictTarget struct {
	userID string
	ch     chan SSEEvent
}

// evict 在锁外回收越阈连接：UnsubscribeFor 移除并 close → handleFixSSE 写循环收到
// 关闭信号结束响应 → 前端 EventSource 自动重连并带 Last-Event-ID 走补发路径。
func (b *SSEBroker) evict(targets []sseEvictTarget) {
	if len(targets) == 0 {
		return
	}
	for _, tg := range targets {
		b.UnsubscribeFor(tg.userID, tg.ch)
	}
	b.mu.Lock()
	b.evictions += int64(len(targets))
	b.mu.Unlock()
	metrics.SetGauge("sse_evictions_total", b.evictions)
	log.Printf("[sse] §A3 回收 %d 个失能慢客户端连接（连续丢弃≥%d），等待 Last-Event-ID 重连补发",
		len(targets), sseDropEvictLimit)
}

// SSEDropStats 返回累计丢弃数与累计回收连接数（测试与运维探针读取）。
func (b *SSEBroker) SSEDropStats() (drops, evictions int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalDrops, b.evictions
}

// Broadcast 向所有账号的所有 SSE 客户端广播消息（全局事件，如 scan/score/trigger 状态事件）。
// 同时把事件记入每个账号的历史缓冲以支持断线续传。
func (b *SSEBroker) Broadcast(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	b.mu.Lock()
	id := b.nextID()
	ev := SSEEvent{ID: id, Data: data}
	for userID := range b.clients {
		b.record(userID, ev)
		for ch := range b.clients[userID] {
			b.pushToCh(ch, ev)
		}
	}
	targets := b.evictStalledLocked() // §A3 挑出连续丢弃越阈者，锁外回收
	b.mu.Unlock()
	b.evict(targets)
}

// BroadcastTo 向指定账号的所有 SSE 客户端定向推送消息（账号隔离，如止盈/止损/清仓等关键消息）。
// 同时把事件记入该账号的历史缓冲以支持断线续传。
// English: targeted push to one account's clients (blocking lock acquisition, legacy semantics).
func (b *SSEBroker) BroadcastTo(userID string, v interface{}) {
	b.broadcastTo(userID, v, 0)
}

// BroadcastToWithin §UPDLINK（2026-09-22 H-4 兜底）：有界等待版定向推送——最多等 budget 取到
// 广播锁，超预算就放弃本次推送并计数告警（返回 false）。
// 为什么需要：H-4 事故里广播锁被一次 close panic 永久占用，POST /api/qmt/report 尾部必经的
// BroadcastTo 于是把**所有上行回报**挂死（实盘账冻结 1h45m 无人知晓）。账本落库先于广播，
// 前端刷新只是尽力而为，因此上行入口绝不能被 SSE 侧的锁异常拖住——宁可丢一次推送（前端有
// 轮询/REST 回填兜底），也不能丢整条资金上报链。
// English: §UPDLINK — bounded-wait targeted push used by the gateway uplink: if the broadcast lock
// cannot be acquired within the budget the push is skipped (and counted/alerted) instead of hanging
// the report endpoint. The ledger write already happened; the frontend has REST/polling fallback.
func (b *SSEBroker) BroadcastToWithin(userID string, v interface{}, budget time.Duration) bool {
	return b.broadcastTo(userID, v, budget)
}

// broadcastTo 定向推送实现体。budget<=0 表示无界等待（旧语义）。返回值仅在"锁超预算未推送"
// 时为 false；空账号/序列化失败属"无事可做"，按 true 返回以免调用方误判为异常。
func (b *SSEBroker) broadcastTo(userID string, v interface{}, budget time.Duration) bool {
	if userID == "" {
		return true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return true
	}
	if !b.acquireForBroadcast(budget) {
		b.noteSkippedBroadcast(userID)
		return false
	}
	id := b.nextID()
	ev := SSEEvent{ID: id, Data: data}
	b.record(userID, ev)
	for ch := range b.clients[userID] {
		b.pushToCh(ch, ev)
	}
	targets := b.evictStalledLocked() // §A3 同 Broadcast：定向推送路径同样回收失能连接
	b.mu.Unlock()
	b.evict(targets)
	// §F-6：real_advice 事件额外留存一份最新快照（供 REST 回填；不影响广播主路径）。
	if m, ok := v.(map[string]interface{}); ok && m["type"] == "real_advice" {
		b.advMu.Lock()
		if b.lastAdv == nil {
			b.lastAdv = make(map[string]adviceSnapshot)
		}
		b.lastAdv[userID] = adviceSnapshot{at: time.Now(), data: append([]byte(nil), data...)}
		b.advMu.Unlock()
	}
	return true
}

// sseLockRetryStep 有界取锁的自旋步长（2ms：广播锁正常持有仅微秒级，无需更细）。
const sseLockRetryStep = 2 * time.Millisecond

// acquireForBroadcast 取广播写锁：budget<=0 时无界等待；否则 TryLock 自旋到预算耗尽。
// 调用方成功取锁后必须自行 Unlock。
// English: acquire the broadcast write lock, waiting at most budget (TryLock spin) when bounded.
func (b *SSEBroker) acquireForBroadcast(budget time.Duration) bool {
	if budget <= 0 {
		b.mu.Lock()
		return true
	}
	deadline := time.Now().Add(budget)
	for {
		if b.mu.TryLock() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(sseLockRetryStep)
	}
}

// noteSkippedBroadcast 记一次"因广播锁超预算而丢推送"：量规供 Prometheus/告警规则消费，
// 日志按首次+每 50 次节流，避免锁真死时把 stderr 刷爆。
func (b *SSEBroker) noteSkippedBroadcast(userID string) {
	b.skipMu.Lock()
	b.skips++
	n := b.skips
	b.skipMu.Unlock()
	metrics.SetGauge("sse_broadcast_skipped_total", n)
	if n == 1 || n%50 == 0 {
		log.Printf("[sse] §UPDLINK 广播锁超预算，本次定向推送放弃（用户=%s，累计 %d 次）——SSE 侧存在持锁阻塞，需排查", userID, n)
	}
}

// SSESkipStats 返回累计"锁超预算丢推送"次数（测试与运维探针读取）。
func (b *SSEBroker) SSESkipStats() int64 {
	b.skipMu.Lock()
	defer b.skipMu.Unlock()
	return b.skips
}

// LastRealAdvice 返回某账号最近一轮 real_advice 广播的原始 JSON 与时间戳（无记录返回 ok=false）。
// 供 GET /api/positions/advice 做断线/重载后的 REST 回填（§F-6）。
func (b *SSEBroker) LastRealAdvice(userID string) (data []byte, at time.Time, ok bool) {
	if b == nil || userID == "" {
		return nil, time.Time{}, false
	}
	b.advMu.Lock()
	defer b.advMu.Unlock()
	snap, found := b.lastAdv[userID]
	if !found {
		return nil, time.Time{}, false
	}
	return snap.data, snap.at, true
}

// Len 返回当前连接的 SSE 客户端总数（跨账号分组）。
func (b *SSEBroker) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, set := range b.clients {
		n += len(set)
	}
	return n
}

// Log 记录 SSE 相关日志，日志前缀为 [sse]。
func (b *SSEBroker) Log(format string, args ...interface{}) {
	log.Printf("[sse] "+format, args...)
}
