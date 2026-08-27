// Package notify 通知推送层：聚合桌面通知、SSE、Webhook、极光推送等通道，支持静默时段与重试出队。
package notify

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestJPushAuthHeader 验证极光 Basic 鉴权头格式（appKey:masterSecret 的 base64）。
func TestJPushAuthHeader(t *testing.T) {
	g := NewJPushGateway("app123", "sec456", "")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("app123:sec456"))
	if got := g.buildAuthHeader(); got != want {
		t.Fatalf("auth header = %q, want %q", got, want)
	}
}

// TestJPushBuildPayload 验证 payload 结构：platform=all、alias 定位、标题/内容、通知渠道。
func TestJPushBuildPayload(t *testing.T) {
	g := NewJPushGateway("app123", "sec456", "quant_owner")
	// §GAP2-W2：默认别名路由（msg.Alias 为空 → 网关配置别名）
	data := g.buildPayload(Message{Title: "清仓提醒"}, "000001 触发清仓")
	var p struct {
		Platform string `json:"platform"`
		Audience struct {
			Alias []string `json:"alias"`
		} `json:"audience"`
		Notification struct {
			Android struct {
				Alert     string `json:"alert"`
				Title     string `json:"title"`
				ChannelID string `json:"channel_id"`
			} `json:"android"`
		} `json:"notification"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Platform != "all" {
		t.Fatalf("platform=%q, want all", p.Platform)
	}
	if len(p.Audience.Alias) != 1 || p.Audience.Alias[0] != "quant_owner" {
		t.Fatalf("alias=%v, want [quant_owner]", p.Audience.Alias)
	}
	if p.Notification.Android.Title != "清仓提醒" || p.Notification.Android.Alert != "000001 触发清仓" {
		t.Fatalf("title/alert=%q/%q", p.Notification.Android.Title, p.Notification.Android.Alert)
	}
	if p.Notification.Android.ChannelID != "quant_signals" {
		t.Fatalf("channel_id=%q, want quant_signals", p.Notification.Android.ChannelID)
	}
}

// TestJPushSend 用 httptest 服务端验证真实请求：路径、鉴权头、Content-Type、请求体结构。
func TestJPushSend(t *testing.T) {
	var gotAuth, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/push" {
			t.Fatalf("path=%s, want /v3/push", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	g := NewJPushGateway("app123", "sec456", "quant_owner")
	g.URL = srv.URL + "/v3/push"
	if err := g.Send(Message{Title: "止损", Content: "000001 触发止损"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("auth header=%q, want Basic prefix", gotAuth)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Fatalf("content-type=%q", gotCT)
	}
	if !strings.Contains(gotBody, `"quant_owner"`) || !strings.Contains(gotBody, `"止损"`) {
		t.Fatalf("body=%s, want alias and title", gotBody)
	}
}

// TestJPushSendNon2xx 极光返回 4xx/5xx 时 Send 应返回错误（便于上层记录）。
func TestJPushSendNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	g := NewJPushGateway("app123", "sec456", "quant_owner")
	g.URL = srv.URL
	if err := g.Send(Message{Title: "t", Content: "c"}); err == nil {
		t.Fatal("want error on non-2xx, got nil")
	}
}

// TestJPushSendNoCreds 未配置凭据时 Send 应静默跳过（不报错、不发请求）。
func TestJPushSendNoCreds(t *testing.T) {
	g := &JPushGateway{}
	if err := g.Send(Message{Title: "t", Content: "c"}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

// TestJPushDefaultAlias 未指定别名时默认 quant_owner。
func TestJPushDefaultAlias(t *testing.T) {
	g := NewJPushGateway("app", "sec", "")
	if g.Alias != "quant_owner" {
		t.Fatalf("alias=%q, want quant_owner", g.Alias)
	}
}

// TestJPushAliasRouting §GAP2-W2 回归：私有告警的 msg.Alias 覆盖网关默认别名——
// 归属账号的止损提醒直达本人设备，不再全员打向 quant_owner。
func TestJPushAliasRouting(t *testing.T) {
	g := NewJPushGateway("app123", "sec456", "quant_owner")
	decodeAlias := func(data []byte) string {
		var p struct {
			Audience struct {
				Alias []string `json:"alias"`
			} `json:"audience"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(p.Audience.Alias) != 1 {
			t.Fatalf("expect 1 alias, got %v", p.Audience.Alias)
		}
		return p.Audience.Alias[0]
	}
	// 空覆盖 → 默认 quant_owner
	if got := decodeAlias(g.buildPayload(Message{Title: "t"}, "c")); got != "quant_owner" {
		t.Fatalf("默认别名: got %q", got)
	}
	// 私有告警 → 归属账号别名（u_123 的设备）
	if got := decodeAlias(g.buildPayload(Message{Title: "t", Alias: "u_123"}, "c")); got != "u_123" {
		t.Fatalf("私有告警应路由到归属账号别名, got %q", got)
	}
}
