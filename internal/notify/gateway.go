// Package notify 推送通知服务，支持 WebSocket 实时推送和 Webhook HTTP 回调两种方式。
// gateway.go 提供可插拔的推送网关适配：把关键提醒转发到外部推送服务（极光/个推/华为推送等
// 厂商 REST 网关，或自建的 APK 后台推送服务），实现 APK 后台/离线的系统通知到达。
// （gateway.go provides a pluggable push-gateway adapter that forwards critical alerts to an external
// push service — e.g. JPush/GeTui/Huawei REST gateways or a self-hosted APK background push relay —
// so notifications reach the APK even when the app is in the background or offline.）
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

// PushGateway 推送网关适配器：把一条消息投递给外部推送服务。
// 实现方可以是极光/个推/华为等厂商 SDK 适配器，或通用的 REST 网关。
// （PushGateway delivers a message to an external push service. Implementations may wrap a
// vendor SDK (JPush/GeTui/Huawei) or a generic REST gateway.）
type PushGateway interface {
	// Send 异步/同步投递一条推送；返回错误表示投递失败（由调用方记录日志）。
	Send(msg Message) error
}

// WebhookGateway 通用推送网关：把消息 POST 到配置的推送 URL。
// 该实现不绑定具体厂商 SDK，适合对接自建 APK 后台推送中转、或支持通用 JSON 的推送网关；
// 厂商专用通道（极光/个推/华为）可在此 URL 指向的网关层承接。
// （WebhookGateway posts each message to a configured URL. It is vendor-agnostic and suitable for a
// self-hosted APK relay or a generic JSON push gateway; vendor channels can terminate at this URL.）
type WebhookGateway struct {
	// 推送接收地址
	URL string
	// HTTP 请求超时（默认 5s）
	Timeout time.Duration
	// 可选的 User-Agent 标识
	UserAgent string
}

// NewWebhookGateway 创建通用推送网关，url 为推送接收地址。
// （NewWebhookGateway builds a generic push gateway posting to url.）
func NewWebhookGateway(url string) *WebhookGateway {
	return &WebhookGateway{URL: url, Timeout: 5 * time.Second}
}

// Send 把消息以 JSON POST 到网关地址；失败时记录日志并返回错误。
// （Send JSON-encodes and POSTs the message to the gateway URL, logging and returning the error on failure.）
func (g *WebhookGateway) Send(msg Message) error {
	if g == nil || g.URL == "" {
		return nil
	}
	payload := map[string]interface{}{
		"title":   msg.Title,
		"content": msg.Content,
		"level":   int(msg.Level),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: g.Timeout}
	req, err := http.NewRequest(http.MethodPost, g.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.UserAgent != "" {
		req.Header.Set("User-Agent", g.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[notify] 推送网关投递失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// §R3-8 P1-D 非 2xx 必须返回错误：此前打日志后 return nil，失败不进 outbox 补投——
		// 与 jpush.go 的失败语义（返回 error 进补投）不一致，同类通道两种结局。
		err := fmt.Errorf("gateway HTTP %d", resp.StatusCode)
		log.Printf("[notify] 推送网关返回非 2xx: %v（进入补投）", err)
		return err
	}
	return nil
}

// gateway 当前激活的推送网关（nil 表示未启用）。
// （gateway is the currently active push gateway; nil means disabled.）
type gatewayHolder struct {
	g PushGateway
}

// SetGateway 设置推送网关（替换式，运行时热更新用；传 nil 表示关闭）。
// （SetGateway swaps the active push gateway at runtime; pass nil to disable.）
func (n *Notifier) SetGateway(g PushGateway) {
	n.mu.Lock()
	n.gateway = g
	n.mu.Unlock()
}

// SetNtfy 设置 ntfy 运维告警通道（§HARDENING：与主网关并行的独立冗余通道；传 nil 关闭）。
// §C9：自动注入短路自监控回调——ntfy 连续失败开闸时经 alertChannelDown 用"还活着的通道"
// （WS/Webhook）播报"报丧鸟哑了"，并 opslog 留痕；调用方无需感知。
// （SetNtfy installs the ntfy ops-alert channel; §C9 it also wires the trip callback so a
// short-circuited ntfy announces itself through the surviving channels.）
func (n *Notifier) SetNtfy(g PushGateway) {
	if ng, ok := g.(*NtfyGateway); ok && ng != nil {
		ng.bmu.Lock()
		ng.onTrip = func(reason string) { n.alertChannelDown("ntfy 运维告警通道短路", reason) }
		ng.bmu.Unlock()
	}
	n.mu.Lock()
	n.ntfy = g
	n.mu.Unlock()
}

// PushGateway 若已配置，把消息投递给推送网关；未启用则直接返回。
// 供关键提醒（清仓/止损/交易信号）在 Webhook 之外再触达 APK 后台。
// §GAP5.2 投递失败进 outbox 补投队列（指数退避，5 次后死信）。
// §HARDENING 同时并投 ntfy 独立运维通道（同一 outbox 补投语义，key 区分网关）。
// （PushGateway forwards the message to the configured gateway if present; no-op when disabled.
// Failures land in the outbox retry queue.）
func (n *Notifier) PushGateway(msg Message) {
	n.mu.RLock()
	g := n.gateway
	nt := n.ntfy
	n.mu.RUnlock()
	if g == nil && nt == nil {
		return
	}
	go func() {
		if g != nil {
			if err := g.Send(msg); err != nil {
				log.Printf("[notify] 推送网关错误: %v（进入补投队列）", err)
				n.outbox.enqueue("gateway", msg, deliverGateway(n))
			}
		}
		if nt != nil {
			if err := nt.Send(msg); err != nil {
				// §C9 短路窗内的快速失败：不逐条 log、不入补投队列（触发时已有一次自监控
				// 播报；每条都排队只会把通道宕机放大成队列风暴）。
				if errors.Is(err, ErrNtfyShortCircuit) {
					return
				}
				log.Printf("[notify] ntfy 通道错误: %v（进入补投队列）", err)
				n.outbox.enqueue("ntfy", msg, deliverNtfy(n))
			}
		}
	}()
}

// deliverNtfy 返回 ntfy 通道投递函数（首次发送与 outbox 补投共用；通道下线视为成功出队）。
func deliverNtfy(n *Notifier) func(string, Message) error {
	return func(_ string, m Message) error {
		n.mu.RLock()
		nt := n.ntfy
		n.mu.RUnlock()
		if nt == nil {
			return nil
		}
		return nt.Send(m)
	}
}

// deliverGateway 返回网关投递函数（首次发送与 outbox 补投共用）。
func deliverGateway(n *Notifier) func(string, Message) error {
	return func(_ string, m Message) error {
		n.mu.RLock()
		g := n.gateway
		n.mu.RUnlock()
		if g == nil {
			return nil // 网关已下线：视为成功出队
		}
		return g.Send(m)
	}
}
