// Package server 提供 HTTP 服务器及其相关功能。
// SSEBroker 实现 Server-Sent Events 的发布-订阅模式，用于向前端实时推送数据。
// English: Package server provides the HTTP server and its related functionality. SSEBroker
// implements a pub-sub model for Server-Sent Events to push realtime data to the frontend.
// （Package server provides the HTTP server. SSEBroker implements a pub-sub based Server-Sent Events
// broadcast for pushing realtime data to the frontend.）
package server

import (
	"encoding/json"
	"log"
	"sync"
)

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
}

// NewSSEBroker 创建 SSEBroker 实例，初始化账号映射与历史缓冲表。
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[string]map[chan SSEEvent]struct{}),
		history: make(map[string][]SSEEvent),
	}
}

// pushToCh 向单个客户端 channel 非阻塞写入；channel 满则丢弃（慢客户端不阻塞发送方）。
func (b *SSEBroker) pushToCh(ch chan SSEEvent, ev SSEEvent) {
	select {
	case ch <- ev:
	default:
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
	}
	b.mu.Unlock()
	return ch
}

// Unsubscribe 注销一个全局 SSE 客户端（兼容旧接口，遍历所有账号分组移除）。
func (b *SSEBroker) Unsubscribe(ch chan SSEEvent) {
	b.UnsubscribeFor("", ch)
}

// UnsubscribeFor 注销指定账号下的一个 SSE 客户端，关闭并移除其 channel。
func (b *SSEBroker) UnsubscribeFor(userID string, ch chan SSEEvent) {
	b.mu.Lock()
	if set := b.clients[userID]; set != nil {
		delete(set, ch)
		if len(set) == 0 {
			delete(b.clients, userID)
		}
	}
	b.mu.Unlock()
	close(ch)
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
	b.mu.Unlock()
}

// BroadcastTo 向指定账号的所有 SSE 客户端定向推送消息（账号隔离，如止盈/止损/清仓等关键消息）。
// 同时把事件记入该账号的历史缓冲以支持断线续传。
func (b *SSEBroker) BroadcastTo(userID string, v interface{}) {
	if userID == "" {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	b.mu.Lock()
	id := b.nextID()
	ev := SSEEvent{ID: id, Data: data}
	b.record(userID, ev)
	for ch := range b.clients[userID] {
		b.pushToCh(ch, ev)
	}
	b.mu.Unlock()
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
