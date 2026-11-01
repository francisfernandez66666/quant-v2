// quote_auth_error_test.go — §SUBSCRIPTION-A（2026-11-01 裁决 14）鉴权失败的分类必须是哨兵。
//
// 为什么要单独测这个：行情 feed 要把"钥匙被撤/无效"与"网关挂了"显示成两个不同状态
// （前者补凭证、后者查进程），而 data 侧 import 不了 trading（成环）、gatewayHTTPError 又是
// 本包未导出类型。所以包装只能在这里做。§0925EVE-W3-E 早定过"判据不许靠 Contains 猜状态码"，
// 本文件锁的就是这条没被破。
package trading

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestQuotesWrapsUnauthorizedAsSentinel 401/403 ⇒ errors.Is(err, data.ErrQuoteUnauthorized) 成立；
// 500/503 ⇒ 不成立（那是"通道挂了"，另一个状态）。
func TestQuotesWrapsUnauthorizedAsSentinel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantUnauth bool
	}{
		{"401 未授权", http.StatusUnauthorized, true},
		{"403 禁止", http.StatusForbidden, true},
		{"500 网关自己坏了", http.StatusInternalServerError, false},
		{"503 broker 未连接", http.StatusServiceUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"ok":false,"err":"stub"}`))
			}))
			t.Cleanup(srv.Close)
			c := NewQMTClient(srv.URL, "some-token", 2*time.Second, 0)
			_, err := c.Quotes(context.Background(), []string{"600000"})
			if err == nil {
				t.Fatalf("非 200 必须返回错误（%d）", tc.status)
			}
			if got := errors.Is(err, data.ErrQuoteUnauthorized); got != tc.wantUnauth {
				t.Fatalf("鉴权哨兵判定=%v 期望=%v err=%v", got, tc.wantUnauth, err)
			}
		})
	}
}
