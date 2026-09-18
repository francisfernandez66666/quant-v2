// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"testing"

	"quant-trading-v2/internal/auth"
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

// §FIX-9a/9b(20260919)：原 TestConsultProModeRateLimit* 三条用例随限流实现一并删除。
// 盘中"15 分钟（后改 2 分钟）注入限流"已于 §生产 20260916 整体移除（带数据咨询改默认
// 能力后拒绝回答反而是缺陷），函数沦为死代码；且其中两条用 time.Local 构造时刻，在
// CI(UTC) 上判定漂移恒真——留着一个"环境相关才通过"的测试比没有测试更糟。
