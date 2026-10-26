// pagination_test.go — §F9（2026-10-26）：统一分页边界与响应头等值锁。
package server

import (
	"net/http/httptest"
	"testing"
)

func TestPageBounds(t *testing.T) {
	cases := []struct {
		total, page, size int
		start, end        int
		more              bool
	}{
		{0, 1, 100, 0, 0, false},
		{250, 1, 100, 0, 100, true},
		{250, 2, 100, 100, 200, true},
		{250, 3, 100, 200, 250, false},
		{250, 4, 100, 250, 250, false},
		{250, 1, 0, 0, 250, false}, // size=0 不分页，保持旧行为
	}
	for _, c := range cases {
		start, end, more := pageBounds(c.total, c.page, c.size)
		if start != c.start || end != c.end || more != c.more {
			t.Fatalf("pageBounds(%d,%d,%d)=(%d,%d,%v)，want (%d,%d,%v)",
				c.total, c.page, c.size, start, end, more, c.start, c.end, c.more)
		}
	}
}

func TestParsePageParamsAndHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/qmt/trades?page=2&page_size=999", nil)
	page, size := parsePageParams(r, 100, 500)
	if page != 2 || size != 500 {
		t.Fatalf("page/size 解析或上限钳制错误: page=%d size=%d", page, size)
	}
	w := httptest.NewRecorder()
	setPaginationHeaders(w, 250, 2, 100)
	if got := w.Header().Get("X-Total-Count"); got != "250" {
		t.Fatalf("X-Total-Count=%q", got)
	}
	if got := w.Header().Get("X-Total-Pages"); got != "3" {
		t.Fatalf("X-Total-Pages=%q", got)
	}
	if got := w.Header().Get("X-Has-More"); got != "true" {
		t.Fatalf("X-Has-More=%q", got)
	}
}
