// verdict_audit_test.go — §D-2 装配面回归：Engine.New 把留痕落盘绑到 <dataDir>/verdicts，
// 两个引擎实例（模拟重启）间裁定可回放；dataDir 空则纯内存不报错。
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/signalctl"
)

func TestEngineNewWiresVerdictAudit(t *testing.T) {
	dir := t.TempDir()
	e := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, dir)
	pol := signalctl.Policy{Strategies: []string{"momentum"}}
	sig := combat_agent.Signal{Code: "600000", Name: "测试", Action: "buy", Direction: "做多", Strategy: "龙头", StrategyType: "dragon"}
	if d := e.SignalCtl().Admit(signalctl.ChannelLive, "u_x", sig, pol, time.Now()); d.Verdict != signalctl.VerdictBlock {
		t.Fatalf("应拦截, got %+v", d)
	}
	// 模拟重启：同目录新引擎回灌当日环
	e2 := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, dir)
	tail := e2.SignalVerdicts(10)
	if len(tail) == 0 {
		t.Fatal("重启后裁定留痕应可读（JSONL 回灌）")
	}
	if tail[0].Code != "600000" || tail[0].Account != "u_x" {
		t.Fatalf("回灌内容不符: %+v", tail[0])
	}
	// 纯内存引擎（dataDir=""）不受影响
	e3 := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "")
	if got := e3.SignalVerdicts(5); len(got) != 0 {
		t.Fatalf("空 dataDir 应为独立内存环, got %d", len(got))
	}
}
