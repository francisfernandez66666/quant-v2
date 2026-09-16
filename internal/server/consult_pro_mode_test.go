// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
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

// TestConsultProModeDefaultOn §生产 20260916 翻转契约：带数据咨询是默认行为，
// 未显式配置（或配 "1"）均为开；只有显式设 "0" 才关闭。
func TestConsultProModeDefaultOn(t *testing.T) {
	s := newTestServerAuth(t)
	if !s.consultProModeEnabled("u_test") {
		t.Fatal("专业模式（带数据咨询）默认应为开启")
	}
	if err := s.auth.SetConfig("u_test", consultProModeKey, "0"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if s.consultProModeEnabled("u_test") {
		t.Fatal("显式设 0 后应关闭")
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

// TestConsultProModeRateLimitInTradeTime 交易时段 2 分钟限流生效（§生产 20260916：默认开启后 15min→2min）。
func TestConsultProModeRateLimitInTradeTime(t *testing.T) {
	s := newTestServerAuth(t)
	// 模拟交易日 10:00（周一）。§CI 2026-09-11：写作时刻固定用北京时区——
	// IsTradeTime 内部按北京墙钟判定，CI(UTC) 上若用 time.Local，10:00 UTC=北京 18:00 非交易时段误 FAIL。
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, cntime.Loc)
	if !data.IsTradeTime(now) {
		t.Fatalf("测试时间应处于交易时段")
	}
	// 首次调用不限流
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("首次调用不应限流, got %v", wait)
	}
	// 记录最近一次使用（1 分钟前），应命中限流
	if err := s.auth.SetConfig("u_test", consultProModeLastUsed, fmt.Sprint(now.Add(-1*time.Minute).Unix())); err != nil {
		t.Fatalf("set last used: %v", err)
	}
	wait := s.consultProModeRateLimited("u_test", now)
	if wait <= 0 || wait > 2*time.Minute {
		t.Fatalf("1 分钟前用过应提示剩余约 1 分钟, got %v", wait)
	}
	// 3 分钟前用过，限流解除
	if err := s.auth.SetConfig("u_test", consultProModeLastUsed, fmt.Sprint(now.Add(-3*time.Minute).Unix())); err != nil {
		t.Fatalf("set last used: %v", err)
	}
	if wait := s.consultProModeRateLimited("u_test", now); wait != 0 {
		t.Fatalf("3 分钟前用过不应限流, got %v", wait)
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
