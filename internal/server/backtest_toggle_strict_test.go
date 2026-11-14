// 文件概述（internal/server/backtest_toggle_strict_test.go）
// §F2-20261114 B 类补缺（2026-11-15 本地栈写探针锤出）：POST /api/research/backtest-toggle
// 旧写法是 json.NewDecoder 裸解码 + `Enabled bool`，探针只发 {"_probe":true} 时未知键被静默吞、
// 零值 false 被真的写进调度配置——夜间回测开关就这样被关掉，正是方案 §F2 B1 禁止的形态。
// 本用例锁三件事：① 未知键必须回报 ignored_keys；② 键缺失必须响亮 400；
// ③ 开关状态绝不因无效载荷被动。④ 合法显式值仍能正常写（严格解码不打断原契约）。
// English: backtest toggle uses strict decoding and never applies a zero-value flag.
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestF2BacktestToggleUnknownKeyNeverFlips(t *testing.T) {
	s, _ := newAdminTestServer(t)
	// 初始显式打开：旧写法一旦回归，未知键载荷会把 bool 零值 false 落盘，本断言立刻红。
	next := s.cfg.Get().Scheduler
	next.Nightly.BacktestEnabled = true
	if err := s.cfg.SetSchedulerConfig(&next); err != nil {
		t.Fatalf("SetSchedulerConfig: %v", err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		r := f2WithUser(httptest.NewRequest(http.MethodPost,
			"/api/research/backtest-toggle", strings.NewReader(body)), "u_f2toggle")
		withIgnoredKeys(s.handleResearchBacktestToggle)(rr, r) // 与 registerRoutes 的包装同形
		return rr
	}
	// ① 只有未知键 -> 400（enabled 必填）+ ignored_keys 回报，且开关没被改成 false
	rr := call(`{"totally_unknown_key":1}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("未知键载荷应 400（不得把零值写成显式 false）, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ignored_keys") {
		t.Fatalf("未知键必须如实回报, got %s", rr.Body.String())
	}
	if !s.cfg.Get().Scheduler.Nightly.BacktestEnabled {
		t.Fatal("无效载荷把夜间回测开关改成了 false（状态被动）")
	}
	// ② 合法显式 true -> 200，严格解码不得打断原有写面
	rr2 := call(`{"enabled":true}`)
	if rr2.Code != http.StatusOK {
		t.Fatalf("合法 enabled=true 应 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	// ③ 合法值 + 未知键 -> 200 且回报 ignored_keys（不静默吞）
	rr3 := call(`{"enabled":false,"_probe":true}`)
	if rr3.Code != http.StatusOK {
		t.Fatalf("合法值带未知键应 200, got %d body=%s", rr3.Code, rr3.Body.String())
	}
	if !strings.Contains(rr3.Body.String(), "ignored_keys") {
		t.Fatalf("合法值带未知键也应回报 ignored_keys, got %s", rr3.Body.String())
	}
}
