// Package notify 推送通知服务，支持 WebSocket 实时推送和 Webhook HTTP 回调两种方式。
// （Package notify provides a push notification service with WebSocket realtime push and Webhook HTTP callbacks.）
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"quant-trading-v2/internal/strategy"
)

// AlertLevel 告警级别枚举。（AlertLevel is the alert severity enumeration.）
type AlertLevel int

const (
	LevelLow    AlertLevel = iota // 低级别（如命中提醒、观察信号）
	LevelMedium AlertLevel = 1    // 中级别（默认告警）
	LevelHigh   AlertLevel = 2    // 高级别（交易提醒，弹桌面通知）
)

// Message 推送消息体，包含级别、标题、正文和可选的信号对象。
// （Message is a push payload with level, title, content and an optional signal object.）
type Message struct {
	// 告警级别
	Level AlertLevel `json:"level"`
	// 消息标题
	Title string `json:"title"`
	// 消息正文
	Content string `json:"content"`
	// 关联的策略信号（可选）
	Signal *strategy.Signal `json:"signal,omitempty"`
	// §GAP2-W2 目标设备别名覆盖（空=网关默认别名；私有告警按归属账号路由）
	Alias string `json:"alias,omitempty"`
}

// Notifier 推送器，管理 WebSocket 客户端和 Webhook URL 列表。
// （Notifier manages WebSocket clients and the Webhook URL list.）
type Notifier struct {
	mu          sync.RWMutex            // 保护 wsClients/webhookURLs 的读写锁
	wsClients   map[string]chan Message // WS 客户端 ID → 消息通道
	webhookURLs []string                // Webhook HTTP 回调地址列表
	gateway     PushGateway             // 外部推送网关（极光/个推/通用 REST，APK 后台/离线触达）

	// §GAP5.2 静默时段（"HH:MM" 分钟数，<0 = 未启用；可跨午夜）：窗口内仅 LevelHigh 放行，
	// 低/中级别本地留痕不推送。English: quiet-hours gate — only LevelHigh passes inside the window.
	quietStart int // 静默开始分钟数（<0=未启用）
	quietEnd   int // 静默结束分钟数（<0=未启用）

	outbox *Outbox // §GAP5.2 失败补投队列
}

// New 创建推送器实例。（Creates a Notifier instance.）
func New() *Notifier {
	n := &Notifier{
		wsClients:  make(map[string]chan Message),
		quietStart: -1,
		quietEnd:   -1,
		outbox:     &Outbox{},
	}
	n.outbox.bindOwner(n) // §R3-8 P1-D：持久化条目重启后重建投递函数用
	return n
}

// SetOutboxPersistPath 启用补投队列磁盘持久化（dataDir 下 outbox.json；须在首次推送前调用）。
// English: enables outbox persistence (outbox.json under the data dir); call before first push.
func (n *Notifier) SetOutboxPersistPath(path string) {
	if n == nil || n.outbox == nil {
		return
	}
	n.outbox.SetPersistPath(path)
}

// SetQuietHours §GAP5.2 设置静默时段（"HH:MM"，任一为空=关闭）。可跨午夜（22:00~08:00）。
// English: configures quiet hours; either bound empty disables the window.
func (n *Notifier) SetQuietHours(start, end string) {
	s, ok1 := parseHM(start)
	e, ok2 := parseHM(end)
	n.mu.Lock()
	defer n.mu.Unlock()
	if !ok1 || !ok2 {
		n.quietStart, n.quietEnd = -1, -1
		return
	}
	n.quietStart, n.quietEnd = s, e
}

// parseHM 解析 "HH:MM" 为当日分钟数；非法返回 (0,false)。
func parseHM(s string) (int, bool) {
	var h, m int
	if n, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// inQuietHours 当前时刻是否处于静默窗口（支持跨午夜区间）。
func (n *Notifier) inQuietHours(now time.Time) bool {
	n.mu.RLock()
	s, e := n.quietStart, n.quietEnd
	n.mu.RUnlock()
	if s < 0 || e < 0 {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if s <= e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e // 跨午夜（如 22:00~08:00）
}

// Push 向所有 WS 客户端和 Webhook 地址推送消息（非阻塞）。
// §GAP5.2 静默时段内仅 LevelHigh 放行（交易信号/清仓/止损），低中级别留痕跳过；
// WS 客户端通道已满时直接丢弃该消息（防止慢消费者阻塞推送方）；Webhook 为异步 goroutine 发送，
// 失败进 outbox 补投队列。
// （Push delivers a message to all WS clients and Webhook URLs non-blockingly; quiet hours pass only
// LevelHigh; drops when a WS channel is full and sends to each Webhook async — failures land in the outbox.）
func (n *Notifier) Push(msg Message) {
	if msg.Level < LevelHigh && n.inQuietHours(time.Now()) {
		log.Printf("[notify] 静默时段抑制 level=%d title=%q", msg.Level, msg.Title)
		return
	}
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
		go func(u string) {
			if err := n.postWebhook(u, msg); err != nil {
				n.outbox.enqueue("webhook:"+u, msg, deliverWebhook(n, u))
			}
		}(url)
	}
}

// deliverWebhook 返回指定 URL 的投递函数（首次发送与 outbox 补投共用同一实现）。
func deliverWebhook(n *Notifier, url string) func(string, Message) error {
	return func(_ string, m Message) error { return n.postWebhook(url, m) }
}

// PushSignal 根据信号优先级自动选择告警级别并推送。
// 级别映射：P1/P2→高（弹桌面通知），P4→低，其余→中。
// （PushSignal maps a signal's priority to an alert level (P1/P2→high with desktop popup, P4→low, else medium) and pushes.）
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
// （PushHit sends a low-level hit reminder for a strategy-scored stock, without a desktop popup.）
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
// 比 PushHit 更高级别：弹桌面通知并推送含建议股数/金额的高级别消息。
// （PushTrade sends a strong trade alert (price/volume + suggested position + desktop popup), a higher level than PushHit.）
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

// RegisterWS 注册一个新的 WebSocket 客户端，返回消息通道。（Registers a new WebSocket client and returns its message channel.）
func (n *Notifier) RegisterWS(id string) chan Message {
	ch := make(chan Message, 100)
	n.mu.Lock()
	n.wsClients[id] = ch
	n.mu.Unlock()
	return ch
}

// SetWebhooks 设置 Webhook 回调地址列表（替换式，运行时热更新用）。
// （SetWebhooks replaces the Webhook URL list; used for hot reload at runtime.）
func (n *Notifier) SetWebhooks(urls []string) {
	n.mu.Lock()
	n.webhookURLs = append([]string(nil), urls...)
	n.mu.Unlock()
}

// UnregisterWS 注销 WebSocket 客户端。（Unregisters a WebSocket client.）
func (n *Notifier) UnregisterWS(id string) {
	n.mu.Lock()
	delete(n.wsClients, id)
	n.mu.Unlock()
}

// postWebhook 向单个 Webhook 地址发送 POST 请求（带 10s 超时，防慢端点堆积 goroutine）。
// 失败返回错误（由调用方决定是否进 outbox 补投）。
func (n *Notifier) postWebhook(url string, msg Message) error {
	data, _ := json.Marshal(msg)
	client := &http.Client{Timeout: 10 * time.Second} // §GAP5.2 此前 http.Post 无超时
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("webhook post error: %v", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("webhook non-2xx: %d", resp.StatusCode)
		log.Printf("webhook post error: %v", err)
		return err
	}
	return nil
}
