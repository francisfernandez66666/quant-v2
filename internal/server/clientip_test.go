// clientip_test.go — §GAP2-W1 客户端 IP 提取（可信代理收口）回归测试。
// 覆盖：公网直连伪造 XFF 必须被无视；可信代理转发时自右向左取真实客户端；
// 全可信链回退对端地址。English: regression tests for trusted-proxy client-IP extraction.
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPUntrustedPeerIgnoresXFF(t *testing.T) {
	// 模拟公网直连 8080：对端是公网 IP，XFF 无论怎么伪造都必须被无视。
	r := httptestReq("203.0.113.7:54321", "1.2.3.4, 8.8.8.8")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("不可信对端的 XFF 应被无视，取对端 IP; got %q", got)
	}
}

func TestClientIPTrustedProxyWalksRightToLeft(t *testing.T) {
	// Caddy 同机反代：对端 127.0.0.1 可信，XFF="真实客户端, 中间跳(内网)" → 取最右不可信。
	r := httptestReq("127.0.0.1:8080", "198.51.100.9, 10.0.0.3")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("应取最右不可信条目作为客户端; got %q", got)
	}
	// 攻击者经反代注入伪造链：最右被攻击者控制为假 IP？——Caddy 会把真实客户端追加在最右，
	// 因此最右即真相；此处验证纯内网链全部可信时回退对端。
	r2 := httptestReq("192.168.1.5:9999", "10.0.0.1, 172.16.0.9")
	if got := clientIP(r2); got != "192.168.1.5" {
		t.Fatalf("全可信链应回退对端地址; got %q", got)
	}
}

func TestClientIPNoHeaderBehindProxy(t *testing.T) {
	r := httptestReq("127.0.0.1:8080", "")
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("可信代理但无 XFF 应回退对端; got %q", got)
	}
}

// httptestReq 构造指定 RemoteAddr 与 XFF 头的最小请求。
func httptestReq(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}
