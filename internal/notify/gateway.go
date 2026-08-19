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
	URL       string        // 推送接收地址
	Timeout   time.Duration // HTTP 请求超时（默认 5s）
	UserAgent string        // 可选的 User-Agent 标识
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
		log.Printf("[notify] 推送网关返回非 2xx: %d", resp.StatusCode)
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

// PushGateway 若已配置，把消息投递给推送网关；未启用则直接返回。
// 供关键提醒（清仓/止损/交易信号）在 Webhook 之外再触达 APK 后台。
// （PushGateway forwards the message to the configured gateway if present; no-op when disabled.
// Used so critical alerts also reach the APK in the background, beyond the Webhook path.）
func (n *Notifier) PushGateway(msg Message) {
	n.mu.RLock()
	g := n.gateway
	n.mu.RUnlock()
	if g == nil {
		return
	}
	go func() {
		if err := g.Send(msg); err != nil {
			log.Printf("[notify] 推送网关错误: %v", err)
		}
	}()
}
