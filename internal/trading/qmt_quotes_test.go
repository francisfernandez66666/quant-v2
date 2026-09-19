// qmt_quotes_test.go — §ENH-5 批E：QMTClient.Quotes 契约回归。
// 锁三点：请求带交易所后缀且携带 Bearer、响应带后缀 key 归一为裸码、非 200 透传网关错误。
// English: contract tests for the L1 feed client method (suffix on the wire, bare codes back,
// bearer auth carried, gateway errors surfaced).
package trading

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// quotesStub 假网关 /quotes：记录最后一次的鉴权头与 codes 参数，回预置 ticks。
type quotesStub struct {
	auth     string
	codes    string
	respBody string
	status   int
}

// ServeHTTP 最小 /quotes 路由（其余 404，路由写错即被客户端报错抓出）。
func (s *quotesStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/quotes" {
		http.NotFound(w, r)
		return
	}
	s.auth = r.Header.Get("Authorization")
	s.codes = r.URL.Query().Get("codes")
	w.Header().Set("Content-Type", "application/json")
	status := s.status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	w.Write([]byte(s.respBody))
}

func TestQMTClientQuotes(t *testing.T) {
	stub := &quotesStub{respBody: `{"ok":true,"ticks":{
		"600519.SH":{"lastPrice":1500.5,"open":1490,"high":1510,"low":1488,
		            "prevClose":1495,"volume":31000,"amount":4.6e7,"tickTime":1770000000000},
		"000001.SZ":{"lastPrice":12.3,"volume":100,"amount":1230,"tickTime":1770000000000}}}`}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk", 2e9, 0)

	ticks, err := c.Quotes(context.Background(), []string{"600519", "000001"})
	if err != nil {
		t.Fatalf("Quotes: %v", err)
	}
	if stub.auth != "Bearer tk" {
		t.Fatalf("必须携带 Bearer, got %q", stub.auth)
	}
	// 出网代码必须带交易所后缀（网关/QMT 侧口径）
	if stub.codes != "600519.SH,000001.SZ" {
		t.Fatalf("codes 应带后缀, got %q", stub.codes)
	}
	// 返回 key 归一为裸码（与 Fetcher.Stocks 键一致）
	if _, ok := ticks["600519"]; !ok {
		t.Fatalf("key 应归一为裸码, got %d 项", len(ticks))
	}
	if got := ticks["600519"].LastPrice; got != 1500.5 {
		t.Fatalf("lastPrice 解析错误: %v", got)
	}
	if got := ticks["000001"].TickTime; got != 1770000000000 {
		t.Fatalf("tickTime 解析错误: %v", got)
	}
	// 空池：不发请求直接回空 map
	empty, err := c.Quotes(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 codes 应回空 map: %v %v", empty, err)
	}
}

func TestQMTClientQuotesError(t *testing.T) {
	// broker 断连时 /quotes 仍应可用（真实网关把它放在连接闸之前）；
	// 这里锁客户端侧：网关回 503 时错误必须透传，供 feed 节流日志并回退新浪链。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ok":false,"err":"feed down"}`))
	}))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk", 2e9, 0)
	_, err := c.Quotes(context.Background(), []string{"600519"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("非 200 必须透传错误, got %v", err)
	}
}
