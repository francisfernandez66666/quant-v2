// §LOGCROSS（2026-10-22）：/api/signal-logs?days=N 的参数口径锁。
//
// days 含当日（1=仅当日），缺省/非法回落 5，上限 30（与 signalarchive.DefaultKeepDays 一致）。
// 这条解析一旦放宽到无上限，前端一次误传 100000 会让服务端扫目录挑全部归档；一旦把非法值
// 当 0 透传，上层 ReadRange 会把 days<1 夹成 1，用户选「近 10 个交易日」实际只看到当日。
//
// English: locks the days query parsing — default 5, clamp to 1..30.
package server

import (
	"net/http/httptest"
	"testing"
)

// TestSignalLogDays 逐档钉参数解析：缺省/非法一律 5，超过 30 夹回 30，1 保持「仅当日」。
// 反证靶心是「透传 0」：Write 侧 days 含当日，0 会被下游夹成 1，与 5 的缺省语义混为一谈。
func TestSignalLogDays(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"/api/signal-logs", 5},
		{"/api/signal-logs?days=1", 1},
		{"/api/signal-logs?days=10", 10},
		{"/api/signal-logs?days=30", 30},
		{"/api/signal-logs?days=31", 30},
		{"/api/signal-logs?days=0", 5},
		{"/api/signal-logs?days=-3", 5},
		{"/api/signal-logs?days=abc", 5},
		{"/api/signal-logs?days=", 5},
		{"/api/signal-logs?days=%20", 5},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.url, nil)
		if got := signalLogDays(r); got != c.want {
			t.Fatalf("%s → got %d want %d", c.url, got, c.want)
		}
	}
}
