package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
)

// TestNormalizeWatchStockCode 自选代码归一/校验（股票-only）。
func TestNormalizeWatchStockCode(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		hasErr bool
	}{
		{"600519", "600519", false},
		{"600519.SH", "600519", false},
		{"sh600519", "600519", false},
		{"000001", "000001", false},
		{"000001.SZ", "000001", false},
		{"430047", "430047", false},
		{"920001", "920001", false},
		{" ZZzzZZ ", "", true},
		{"399001", "", true},    // 指数未开放
		{"510300", "", true},    // 基金未开放
		{"110043", "", true},    // 沪市转债未开放
		{"123456", "", true},    // 非股票代码段
		{"600519.SZ", "", true}, // 市场与代码不匹配
		{"60051", "", true},     // 位数不足
		{"", "", true},
	}
	for _, c := range cases {
		got, err := normalizeWatchStockCode(c.in)
		if c.hasErr {
			if err == nil {
				t.Errorf("normalize(%q) 应报错，got=%q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("normalize(%q)=%q,err=%v want=%q", c.in, got, err, c.want)
		}
	}
}

// TestAddWatchlistRejectsInvalidBeforeWrite 非法代码必须在写文件/入监控池之前返回 400。
func TestAddWatchlistRejectsInvalidBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	wl := data.NewWatchlistManager(dir)
	s := &Server{watchlist: wl}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(`{"code":"ZZZZZZ"}`))
	s.handleFixAddWatchlist(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("非法代码状态码=%d 期望 400 body=%s", rr.Code, rr.Body.String())
	}
	if got := wl.List(""); len(got) != 0 {
		t.Fatalf("非法代码不得写入自选，实到=%v", got)
	}
}

// TestAddWatchlistNormalizesStock 合法股票带后缀时写入裸 6 位。
func TestAddWatchlistNormalizesStock(t *testing.T) {
	dir := t.TempDir()
	wl := data.NewWatchlistManager(dir)
	s := &Server{watchlist: wl}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(`{"code":"600519.SH"}`))
	s.handleFixAddWatchlist(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("合法股票状态码=%d 期望 200 body=%s", rr.Code, rr.Body.String())
	}
	if got := wl.List(""); len(got) != 1 || got[0] != "600519" {
		t.Fatalf("自选应写入裸码 600519，实到=%v", got)
	}
}
