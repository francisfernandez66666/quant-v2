// Package server 提供 HTTP 服务器及其相关功能。
// SSEBroker 实现 Server-Sent Events 的发布-订阅模式，用于向前端实时推送数据。
// （Package server provides the HTTP server. SSEBroker implements a pub-sub based Server-Sent Events
// broadcast for pushing realtime data to the frontend.）
package server

import (
	"encoding/json"
	"log"
	"sync"
)

// SSEBroker SSE 事件广播器。
// 采用发布-订阅模式管理客户端连接，支持广播消息到所有订阅者。
// 非阻塞发送：客户端消费不及时不会阻塞其他客户端。
// （SSEBroker is the SSE event broadcaster. It manages client connections via pub-sub and broadcasts
// messages to all subscribers with non-blocking sends so slow clients never block others.）
type SSEBroker struct {
	mu      sync.RWMutex             // 保护 clients 映射的读写锁
	clients map[chan []byte]struct{} // 订阅客户端集合，channel 为每个客户端的数据通道
}

// NewSSEBroker 创建 SSEBroker 实例，初始化客户端映射表。（Creates an SSEBroker with an initialized client map.）
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{clients: make(map[chan []byte]struct{})}
}

// Subscribe 注册一个新的 SSE 客户端，返回接收数据的 channel。
// channel 缓冲区大小为 16，防止慢客户端阻塞。
// （Subscribe registers a new SSE client and returns its receive channel; a 16-buffer prevents slow clients from blocking.）
func (b *SSEBroker) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe 注销一个 SSE 客户端，关闭并移除对应的 channel。（Unsubscribes and removes a client's channel.）
func (b *SSEBroker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// Broadcast 向所有已订阅的 SSE 客户端广播消息。
// v: 任意类型，会被 JSON 序列化后发送。
// 如果客户端 channel 已满（数据消费不及时），消息会被丢弃，不会阻塞。
// （Broadcast sends a JSON-encoded message to all subscribed clients; messages are dropped when a client's channel is full.）
func (b *SSEBroker) Broadcast(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

// Len 返回当前连接的 SSE 客户端数量。（Returns the number of currently connected SSE clients.）
func (b *SSEBroker) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// Log 记录 SSE 相关日志，日志前缀为 [sse]。（Logs SSE-related messages with the [sse] prefix.）
func (b *SSEBroker) Log(format string, args ...interface{}) {
	log.Printf("[sse] "+format, args...)
}
