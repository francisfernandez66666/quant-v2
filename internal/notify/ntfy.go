// Package notify 通知推送层：聚合桌面通知、SSE、Webhook、极光推送等通道，支持静默时段与重试出队。
package notify

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// §C9（2026-09-22 PM 批清扫）通道级短路（熔断）：ntfy 服务商/网络整体宕机时，
// 旧实现每条告警都要吃满 5s HTTP 超时再逐条 log（审计实录「高频 ntfy 失败」的放大器），
// 且「告警通道自身失败」没有任何告警——报丧鸟哑了没人知道。
// 现规则：连续 ntfyTripThreshold 次发送失败 → 短路窗口 ntfyTripCooldown 内 Send 立即
// 返回 ErrNtfyShortCircuit（不再发 HTTP、调用方不入补投队列）；窗口过后半开探测放行一次，
// 成功即闭合、失败则续窗。短路触发瞬间经 onTrip 回调做自监控告警
// （WS/Webhook 旁路 + opslog 留痕）。
// English: §C9 — channel-level circuit breaker. After N consecutive failures the channel
// short-circuits for a cooldown window (instant ErrNtfyShortCircuit, no HTTP, no per-message
// outbox churn), then half-open probes once. The trip fires onTrip — a self-monitoring alert
// so a mute "carrier pigeon" is itself announced.
const (
	ntfyTripThreshold = 3
	ntfyTripCooldown  = 10 * time.Minute
)

// ErrNtfyShortCircuit 短路窗口内的快速失败哨兵错误：调用方（PushGateway/补投投递函数）
// 据此跳过逐条日志与逐条入队，防「通道宕机 × 高频新告警」放大成日志/队列风暴。
// English: sentinel error returned during the open window so callers can stay quiet.
var ErrNtfyShortCircuit = errors.New("ntfy 通道短路中（连续失败熔断，冷却后自动探测）")

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

	// §C9 熔断状态（bmu 保护；零值=闭合）。
	// English: breaker state guarded by bmu; zero value = closed.
	bmu         sync.Mutex
	consecFails int       // 连续失败计数（一次成功即清零）
	openUntil   time.Time // 短路窗口截止时刻（零值=未短路）
	// onTrip 短路触发回调（SetNtfy 注入自监控告警；未注入时仅日志）。
	onTrip func(reason string)
}

// allowOrShortCircuit 纯判定：当前处于短路窗口内则返回哨兵错误（不加外部状态）。
func (g *NtfyGateway) allowOrShortCircuit(now time.Time) error {
	g.bmu.Lock()
	defer g.bmu.Unlock()
	if now.Before(g.openUntil) {
		return fmt.Errorf("%w（剩余 %s）", ErrNtfyShortCircuit, g.openUntil.Sub(now).Round(time.Second))
	}
	return nil
}

// recordFail 记一次发送失败；达到连续阈值时开短路窗并触发一次自监控回调。
func (g *NtfyGateway) recordFail(now time.Time, cause error) {
	g.bmu.Lock()
	g.consecFails++
	tripped := false
	if g.consecFails >= ntfyTripThreshold && !now.Before(g.openUntil) {
		g.openUntil = now.Add(ntfyTripCooldown)
		g.consecFails = 0 // 开窗后计数归零：窗口结束的半开探测重新计
		tripped = true
	}
	cb, fails, until := g.onTrip, g.consecFails, g.openUntil
	g.bmu.Unlock()
	if tripped {
		reason := fmt.Sprintf("连续 %d 次发送失败，短路至 %s（%v）", ntfyTripThreshold, until.Format("15:04:05"), cause)
		log.Printf("[notify][C9] ntfy 通道短路: %s", reason)
		if cb != nil {
			cb(reason)
		}
	} else {
		log.Printf("[notify] ntfy 推送失败（连续第 %d 次）: %v", fails, cause)
	}
}

// recordSuccess 一次成功即闭合：清失败计数与短路窗。
func (g *NtfyGateway) recordSuccess() {
	g.bmu.Lock()
	// openUntil 非零即曾处于短路态（半开探测发生在窗口到期之后），成功则播报恢复。
	reopened := !g.openUntil.IsZero()
	g.consecFails = 0
	g.openUntil = time.Time{}
	g.bmu.Unlock()
	if reopened {
		log.Printf("[notify][C9] ntfy 通道恢复（半开探测成功，短路解除）")
	}
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
	// §C9 短路窗内快速失败：不再吃 5s 超时、不再逐条占用 HTTP——窗口到期自然半开探测。
	if err := g.allowOrShortCircuit(time.Now()); err != nil {
		return err
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
		g.recordFail(time.Now(), err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		serr := fmt.Errorf("ntfy http %d", resp.StatusCode)
		g.recordFail(time.Now(), serr)
		return serr
	}
	g.recordSuccess()
	return nil
}
