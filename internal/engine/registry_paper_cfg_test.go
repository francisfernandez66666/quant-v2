package engine

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
)

// TestGetPaperUsesAccountPaperRules §PAPER-CFG-DESYNC（2026-10-26）：
// 账号级 rules.paper.enabled=true 时，懒加载出来的纸面引擎必须 enabled；
// 旧实现直接拿全局模板（默认 enabled=false），导致 /api/paper/config 显示开、
// 引擎实跑关、前端菜单被隐藏且再也进不去重新打开。
func TestGetPaperUsesAccountPaperRules(t *testing.T) {
	cm := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if got := cm.Get().Paper.Enabled; got {
		t.Fatalf("前置：全局默认模拟盘应为关闭，实际 enabled=%v", got)
	}
	if err := cm.SetPaperConfigFor("u_paper_cfg", func(p *config.PaperConfig) {
		p.Enabled = true
		p.InitialCapital = 123456
	}); err != nil {
		t.Fatalf("设置账号级 paper 配置: %v", err)
	}

	// 模板引擎故意用关闭态：模拟启动时的全局 cfgMgr.Get().Paper。
	tmpl := paper.New(paper.ConfigFromRules(config.PaperConfig{
		Enabled:        false,
		FixedAmount:    10000,
		MaxPositions:   10,
		InitialCapital: 100000,
	}), "")
	reg := NewRegistry(EngineOptions{Paper: tmpl, CfgMgr: cm, DataDir: t.TempDir()})

	pe := reg.GetPaper("u_paper_cfg")
	if pe == nil {
		t.Fatal("GetPaper 返回 nil")
	}
	if !pe.Enabled() {
		t.Fatal("账号级 rules.paper.enabled=true，但引擎 enabled=false（规则层与运行时不同源）")
	}
	if got := pe.Cfg().InitialCapital; got != 123456 {
		t.Fatalf("引擎应继承账号级 initial_capital=123456，got %v", got)
	}
}
