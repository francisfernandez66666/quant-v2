// Package notify 通知推送层：聚合桌面通知、SSE、Webhook、极光推送等通道，支持静默时段与重试出队。
package notify

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// JPushGateway 极光推送 REST v3 网关：把关键提醒通过极光 API 下发到已注册的 APK 设备。
// 设备定位用固定别名（alias，默认 quant_owner），APK 端在启动时用同一别名 setAlias。
// 凭据 appKey/masterSecret 来自 config（服务端持有，不进入 APK）。
// （JPushGateway sends critical alerts via the JPush REST v3 API to registered APK devices,
// addressed by a fixed alias (default quant_owner) that the APK sets on startup. The
// appKey/masterSecret come from config — server-side only, never bundled into the APK.）
type JPushGateway struct {
	AppKey  string        // 极光 AppKey
	Secret  string        // 极光 Master Secret（REST 鉴权）
	Alias   string        // 推送目标别名（空则默认 quant_owner）
	URL     string        // 极光推送地址（默认 https://api.jpush.cn/v3/push）
	Timeout time.Duration // HTTP 超时（默认 5s）
}

// NewJPushGateway 创建极光推送网关。
func NewJPushGateway(appKey, secret, alias string) *JPushGateway {
	if alias == "" {
		alias = "quant_owner"
	}
	return &JPushGateway{AppKey: appKey, Secret: secret, Alias: alias, Timeout: 5 * time.Second}
}

// jpushPayload 极光 v3 推送请求体（自定义推送，platform=all，audience 按别名定位设备）。
type jpushPayload struct {
	Platform string `json:"platform"`
	Audience struct {
		Alias []string `json:"alias"`
	} `json:"audience"`
	Notification struct {
		Alert   string `json:"alert"`
		Android struct {
			Alert     string `json:"alert"`
			Title     string `json:"title"`
			ChannelID string `json:"channel_id"`
			Priority  int    `json:"priority"`
		} `json:"android"`
	} `json:"notification"`
	Options struct {
		TimeToLive int64 `json:"time_to_live"`
	} `json:"options"`
}

// buildPayload 构造极光推送请求体（标题+内容，走 Android 自定义通知渠道）。
// §GAP2-W2 别名路由：msg.Alias 非空时覆盖默认别名——私有告警直达归属账号设备，
// 不再全员打向 quant_owner 单别名（朋友的止损提醒误推 owner 手机的历史缺陷）。
func (g *JPushGateway) buildPayload(msg Message, content string) []byte {
	var p jpushPayload
	p.Platform = "all"
	alias := g.Alias
	if msg.Alias != "" {
		alias = msg.Alias // §GAP2-W2 归属账号别名优先
	}
	p.Audience.Alias = []string{alias}
	p.Notification.Alert = content
	p.Notification.Android.Alert = content
	p.Notification.Android.Title = msg.Title
	p.Notification.Android.ChannelID = "quant_signals"
	p.Notification.Android.Priority = 1
	p.Options.TimeToLive = 86400 // 1 天离线保留
	data, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return data
}

// Send 通过极光 REST API 下发一条推送；失败时记录日志并返回错误。
// 鉴权：HTTP Basic，账号 appKey、密码 masterSecret。
func (g *JPushGateway) Send(msg Message) error {
	if g == nil || g.AppKey == "" || g.Secret == "" {
		return nil
	}
	url := g.URL
	if url == "" {
		url = "https://api.jpush.cn/v3/push"
	}
	body := g.buildPayload(msg, msg.Content)
	if body == nil {
		return fmt.Errorf("极光推送 payload 构造失败")
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(g.AppKey+":"+g.Secret)))
	client := &http.Client{Timeout: g.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[notify] 极光推送失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// 极光返回 4xx 多为鉴权/内容问题，5xx 为服务端异常，统一记录状态码
		log.Printf("[notify] 极光推送返回非 2xx: %d", resp.StatusCode)
		return fmt.Errorf("jpush http %d", resp.StatusCode)
	}
	return nil
}

// buildAuthHeader 返回极光 Basic 鉴权头（供单测验证）。
func (g *JPushGateway) buildAuthHeader() string {
	if g == nil {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(g.AppKey+":"+g.Secret))
}
