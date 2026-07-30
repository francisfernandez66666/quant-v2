// Package notify 推送通知服务，支持 WebSocket 实时推送和 Webhook HTTP 回调两种方式。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"quant-trading-v2/internal/strategy"
)

// AlertLevel 告警级别枚举。
type AlertLevel int

const (
	LevelLow    AlertLevel = iota
	LevelMedium AlertLevel = 1
	LevelHigh   AlertLevel = 2
)

// Message 推送消息体，包含级别、标题、正文和可选的信号对象。
type Message struct {
	Level   AlertLevel       `json:"level"`
	Title   string           `json:"title"`
	Content string           `json:"content"`
	Signal  *strategy.Signal `json:"signal,omitempty"`
}

// Notifier 推送器，管理 WebSocket 客户端和 Webhook URL 列表。
type Notifier struct {
	mu          sync.RWMutex
	wsClients   map[string]chan Message
	webhookURLs []string
}

// New 创建推送器实例。
func New() *Notifier {
	return &Notifier{
		wsClients: make(map[string]chan Message),
	}
}

// Push 向所有 WS 客户端和 Webhook 地址推送消息（非阻塞）。
func (n *Notifier) Push(msg Message) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for id, ch := range n.wsClients {
		select {
		case ch <- msg:
		default:
			log.Printf("ws client %s buffer full, dropping message", id)
		}
	}

	for _, url := range n.webhookURLs {
		go n.postWebhook(url, msg)
	}
}

// PushSignal 根据信号优先级自动选择告警级别并推送。
func (n *Notifier) PushSignal(sig *strategy.Signal) {
	level := LevelMedium
	switch sig.Priority {
	case strategy.P1, strategy.P2:
		level = LevelHigh
	case strategy.P4:
		level = LevelLow
	}

	title := string(sig.Type) + "信号"
	body := fmt.Sprintf("%s %s — %s", sig.Code, sig.Name, sig.Reason)
	if level == LevelHigh {
		PushDesktop(title, body)
	}

	n.Push(Message{
		Level:   level,
		Title:   title,
		Content: sig.Reason,
		Signal:  sig,
	})
}

// PushHit 命中提醒：有策略评分的股票，低级别提醒（不弹桌面通知）。
func (n *Notifier) PushHit(sig *strategy.Signal, chgPct, volume float64, reasons map[string]string) {
	title := "⚡" + string(sig.Type) + "命中"
	d1r := reasons["d1"]
	d2r := reasons["d2"]
	d3r := reasons["d3"]
	d4r := reasons["d4"]
	body := fmt.Sprintf("%s %s — %.0f分 D1=%.0f(%s) D2=%.0f(%s) D3=%.0f(%s) D4=%.0f(%s) 现价%.2f %.2f%% 量%.0f",
		sig.Code, sig.Name, sig.Confidence*100,
		sig.Meta["d1"], d1r,
		sig.Meta["d2"], d2r,
		sig.Meta["d3"], d3r,
		sig.Meta["d4"], d4r,
		sig.Price, chgPct, volume)
	n.Push(Message{
		Level:   LevelLow,
		Title:   title,
		Content: body,
		Signal:  sig,
	})
}

// PushTrade 交易提醒：符合策略规则的股票，强提醒（含量价+仓位+桌面通知）。
func (n *Notifier) PushTrade(sig *strategy.Signal, changePct, volume float64) {
	title := "🚀" + string(sig.Type) + "交易"
	body := fmt.Sprintf("%s %s — %.0f分 现价%.2f %.2f%% 量%.0f 建议%d股/%.0f元",
		sig.Code, sig.Name, sig.Confidence*100, sig.Price, changePct, volume, sig.Qty, sig.Amount)
	PushDesktop(title, body)
	n.Push(Message{
		Level:   LevelHigh,
		Title:   title,
		Content: body,
		Signal:  sig,
	})
}

// RegisterWS 注册一个新的 WebSocket 客户端，返回消息通道。
func (n *Notifier) RegisterWS(id string) chan Message {
	ch := make(chan Message, 100)
	n.mu.Lock()
	n.wsClients[id] = ch
	n.mu.Unlock()
	return ch
}

// UnregisterWS 注销 WebSocket 客户端。
func (n *Notifier) UnregisterWS(id string) {
	n.mu.Lock()
	delete(n.wsClients, id)
	n.mu.Unlock()
}

// postWebhook 向单个 Webhook 地址发送 POST 请求（goroutine 内调用）。
func (n *Notifier) postWebhook(url string, msg Message) {
	data, _ := json.Marshal(msg)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("webhook post error: %v", err)
		return
	}
	resp.Body.Close()
}
