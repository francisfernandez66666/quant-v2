// Package notify 通知推送层：聚合桌面通知、SSE、Webhook、极光推送等通道，支持静默时段与重试出队。
package notify

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// NtfyGateway ntfy 推送网关：把运维级关键提醒 POST 到自订阅的 ntfy topic（手机 App 秒达）。
// 与极光（APK 内消息通道）互补：ntfy 走独立进程/独立服务商，引擎自身异常（kill-switch、
// 夜间链失败、备份失败）仍能触达人——通道冗余是这套告警唯一缺的"报丧鸟"。
// 主题名即凭证（随机串不可猜），无需额外鉴权；URL 为空默认公共服务器 https://ntfy.sh。
// （NtfyGateway posts ops-critical alerts to a subscribed ntfy topic — an independent channel
// from JPush, so engine-level failures still reach the owner's phone. The random topic IS the
// credential; empty URL defaults to the public https://ntfy.sh relay.）
type NtfyGateway struct {
	// ntfy 服务器地址（默认 https://ntfy.sh）
	URL string
	// 订阅主题（随机串，泄露=可伪造消息，需保密）
	Topic string
	// HTTP 超时（默认 5s）
	Timeout time.Duration
}

// NewNtfyGateway 创建 ntfy 网关；topic 为空返回 nil（该通道未启用）。
func NewNtfyGateway(url, topic string) *NtfyGateway {
	if topic == "" {
		return nil
	}
	if url == "" {
		url = "https://ntfy.sh"
	}
	return &NtfyGateway{URL: strings.TrimRight(url, "/"), Topic: topic, Timeout: 5 * time.Second}
}

// Send 把消息以纯文本 POST 到 <url>/<topic>，标题/优先级走 HTTP 头。
// 级别映射：LevelHigh→high(4)（强提醒不静音），中→default(3)，低→low(2)。
// 正文截断 4KB（ntfy 公共服务器消息上限 4096B）。
func (g *NtfyGateway) Send(msg Message) error {
	if g == nil || g.Topic == "" {
		return nil
	}
	pri := "3"
	switch msg.Level {
	case LevelHigh:
		pri = "4"
	case LevelLow:
		pri = "2"
	}
	body := msg.Content
	if len(body) > 4000 {
		body = body[:4000] + "…"
	}
	url := g.URL + "/" + g.Topic
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", msg.Title)
	req.Header.Set("Priority", pri)
	req.Header.Set("Tags", "warning")
	client := &http.Client{Timeout: g.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[notify] ntfy 推送失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy http %d", resp.StatusCode)
	}
	return nil
}
