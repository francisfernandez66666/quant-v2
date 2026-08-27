// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/data"
)

// newTestServerAuth 创建一个基于临时目录的认证管理器并初始化为 Server。
func newTestServerAuth(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	return &Server{auth: mgr}
}

// TestConsultProModeDefaultOff 专业模式开关默认应为关。
func TestConsultProModeDefaultOff(t *testing.T) {
	s := newTestServerAuth(t)
	if s.consultProModeEnabled("u_test") {
		t.Fatal("专业模式默认应为关闭")
	}
}

// TestConsultProModeSetAndGet 开关开启后应能读回，且跨实例（模拟重启）保留。
func TestConsultProModeSetAndGet(t *testing.T) {
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	s := &Server{auth: mgr}
	if err := s.auth.SetConfig("u_test", consultProModeKey, "1"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if !s.consultProModeEnabled("u_test") {
		t.Fatal("开启后 consultProModeEnabled 应返回 true")
	}
	// 新实例（模拟重启）读同一个 auth.json，应仍为开
	mgr2 := auth.NewManager(dir)
	if err := mgr2.Init(); err != nil {
		t.Fatalf("auth init 2: %v", err)
	}
	s2 := &Server{auth: mgr2}
	if !s2.consultProModeEnabled("u_test") {
		t.Fatal("重启后专业模式开关应保留（落盘 auth.json）")
	}
}

// TestConsultProModeRateLimitInTradeTime 交易时段 15 分钟限流生效。
func TestConsultProModeRateLimitInTradeTime(t *testing.T) {
	s := newTestServerAuth(t)
	// 模拟交易日 10:00（周一）
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local)
	if !data.IsTradeTime(now) {
		t.Fatalf("测试时间应处于交易时段")
	}
	// 首次调用不限流
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("首次调用不应限流, got %v", wait)
	}
	// 记录最近一次使用（5 分钟前），应命中限流
	if err := s.auth.SetConfig("u_test", consultProModeLastUsed, fmt.Sprint(now.Add(-5*time.Minute).Unix())); err != nil {
		t.Fatalf("set last used: %v", err)
	}
	wait := s.consultProModeRateLimited("u_test", now)
	if wait <= 0 || wait > 15*time.Minute {
		t.Fatalf("5 分钟前用过应提示剩余约 10 分钟, got %v", wait)
	}
	// 16 分钟前用过，限流解除
	if err := s.auth.SetConfig("u_test", consultProModeLastUsed, fmt.Sprint(now.Add(-16*time.Minute).Unix())); err != nil {
		t.Fatalf("set last used: %v", err)
	}
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("16 分钟前用过不应限流, got %v", wait)
	}
}

// TestConsultProModeNoRateLimitOffHours 盘后/盘前不限流。
func TestConsultProModeNoRateLimitOffHours(t *testing.T) {
	s := newTestServerAuth(t)
	// 交易日晚间 20:00
	now := time.Date(2026, 8, 3, 20, 0, 0, 0, time.Local)
	if data.IsTradeTime(now) {
		t.Fatalf("20:00 不应属于交易时段")
	}
	_ = s.auth.SetConfig("u_test", consultProModeLastUsed, strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10))
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("盘后不应限流, got %v", wait)
	}
}

// TestConsultProModeRateLimitWeekend 周末不限流。
func TestConsultProModeRateLimitWeekend(t *testing.T) {
	s := newTestServerAuth(t)
	// 周六 10:00
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.Local)
	if now.Weekday() != time.Saturday {
		t.Fatalf("测试时间应为周六")
	}
	_ = s.auth.SetConfig("u_test", consultProModeLastUsed, strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10))
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("周末不应限流, got %v", wait)
	}
}
