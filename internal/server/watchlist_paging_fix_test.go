// 纸面委托分页、自选补价与删除语义回归（§FIX-20261028-F10/F-11/F-13，2026-10-29）。
//
// 三条缺陷（终轮 UAT 实测）：
//
//	F-13 单账号 /api/paper/orders 616KB/2000 行，`?limit` 被忽略 ⇒ 传输层分页，但**响应体必须
//	     仍是裸数组**（前端 setOrders 直接按数组用，改形状会当场崩），总数走 X-Total-Count。
//	F-11 DELETE /api/watchlist 此前丢弃 Remove 的返回值、恒回 {"status":"ok"} ⇒ "没删掉"也报成功。
//	F-10 POST /api/watchlist 快照未命中会同步打上游（>15s），前端超时判失败而后端已落库。
//
// English: paging keeps the bare-array body (total in X-Total-Count), watchlist delete reports
// the truth (404 when absent), and adding a symbol never blocks on an upstream quote.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestPageWindowBoundaries 分页窗口的纯函数边界（末页收口 / 越界 / 非法值）。
func TestPageWindowBoundaries(t *testing.T) {
	cases := []struct {
		name                 string
		total, limit, offset int
		wantStart, wantEnd   int
	}{
		{"首页", 7, 3, 0, 0, 3},
		{"次页", 7, 3, 3, 3, 6},
		{"末页收口", 7, 3, 6, 6, 7},
		{"offset 越界", 7, 3, 99, 7, 7},
		{"offset 负值归一", 7, 3, -5, 0, 3},
		{"limit 非法回落缺省", 7, 0, 0, 0, 7},
		{"total=0", 0, 5, 0, 0, 0},
	}
	for _, c := range cases {
		gotS, gotE := pageWindow(c.total, c.limit, c.offset)
		if gotS != c.wantStart || gotE != c.wantEnd {
			t.Fatalf("%s: pageWindow(%d,%d,%d)=[%d,%d) want [%d,%d)",
				c.name, c.total, c.limit, c.offset, gotS, gotE, c.wantStart, c.wantEnd)
		}
	}
}

// TestPaperOrdersPagingParamsAndShape 参数校验 + 空引擎下仍返回**裸数组**与 X-Total-Count。
func TestPaperOrdersPagingParamsAndShape(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.registry = nil // 未注入纸面引擎 ⇒ handler 走"空数组"分支，正好用来验形状与参数闸

	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/orders", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("空引擎应 200，实到 %d", rr.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("响应必须是数组（前端按数组消费），实到 body=%s", rr.Body.String())
	}
	if len(rows) != 0 {
		t.Fatalf("空引擎应回空数组，实到 %d 行", len(rows))
	}
	if got := rr.Header().Get("X-Total-Count"); got != "0" {
		t.Fatalf("X-Total-Count 应为 0，实到 %q", got)
	}
	// 非法分页参数必须 400（不静默改语义）
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=2001", "?offset=-1", "?limit=abc"} {
		bad := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/paper/orders"+q, ""))
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实到 %d", q, bad.Code)
		}
	}
}

// TestWatchlistDeleteReportsTruth 删存在码 200+removed:true；删不存在码 404。
func TestWatchlistDeleteReportsTruth(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.watchlist = data.NewWatchlistManager(t.TempDir())
	if !s.watchlist.Add(admin.ID, "600000") {
		t.Fatal("seed watchlist 失败")
	}
	del := func(code string) (int, map[string]any) {
		t.Helper()
		rr := adminDo(s, adminReq(s, admin, http.MethodDelete, "/api/watchlist", `{"code":"`+code+`"}`))
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	if code, out := del("600000"); code != http.StatusOK || out["removed"] != true {
		t.Fatalf("删存在码应 200+removed:true，实到 %d %+v", code, out)
	}
	if code, out := del("600000"); code != http.StatusNotFound {
		t.Fatalf("删不存在码应 404（此前恒回 ok），实到 %d %+v", code, out)
	}
}

// TestWatchlistAddNeverBlocksOnUpstream 加新票不得因补价阻塞：有界等待后回 quote:pending，且已落库。
func TestWatchlistAddNeverBlocksOnUpstream(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.watchlist = data.NewWatchlistManager(t.TempDir())
	// 不注入 fetcher 与行情：快照恒不可得 ⇒ 走 quoteDisplayBounded 的等待上限。
	req := adminReq(s, admin, http.MethodPost, "/api/watchlist", `{"code":"601398"}`)
	done := make(chan *httptest.ResponseRecorder, 1)
	start := time.Now()
	go func() { done <- adminDo(s, req) }()

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("加自选超过 4s 未返回（上限应为 %v）：补价仍在同步打上游（F-10 回归）", addQuoteWait)
	}
	if elapsed := time.Since(start); elapsed > addQuoteWait+2*time.Second {
		t.Fatalf("加自选耗时 %v 超出有界等待（addQuoteWait=%v）", elapsed, addQuoteWait)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("加自选应 200，实到 %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status string         `json:"status"`
		Quote  string         `json:"quote"`
		Stock  map[string]any `json:"stock"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
	}
	if out.Status != "ok" {
		t.Fatalf("status 应为 ok，实到 %q", out.Status)
	}
	if out.Quote != "pending" {
		t.Fatalf("无行情时应回 quote=pending（不把 0 当现价），实到 %q", out.Quote)
	}
	// Add 先于补价：落库必须已发生（否则就是幽灵写入）
	list := s.watchlist.List(admin.ID)
	found := false
	for _, c := range list {
		if c == "601398" {
			found = true
		}
	}
	if !found {
		t.Fatalf("加自选应已落库，实到 %v", list)
	}
}
