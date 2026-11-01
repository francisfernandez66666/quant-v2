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
	}, config.RiskGateConfig{}), "")
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

// TestGetPaperSyncsMaxOrderAmount §F-4（2026-11-01）：懒加载出来的纸面引擎必须带上**该账号**
// 的 qmt.risk_gate.max_order_amount，而不是全局模板的值。
//
// 两条来源都要覆盖：
//   - 账号有 rules.paper（走 ConfigFromRules 单一构建器）
//   - 账号没有 rules.paper（走模板引擎配置 + 帽同步这一支）
//
// 反证靶心：若只在 ConfigFromRules 里接、不覆盖"模板回退"这一支，没配过模拟盘的账号会带着
// 别人的（或 0）帽跑——而纸面页只显示 enabled/fixed_amount，从任何读数上都看不出来。
func TestGetPaperSyncsMaxOrderAmount(t *testing.T) {
	cm := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	q := cm.GetQMTConfigFor("u_cap")
	if q == nil {
		t.Fatal("前置：GetQMTConfigFor 应返回非 nil（零值配置也算）")
	}
	q.RiskGate.MaxOrderAmount = 20000
	if err := cm.SetQMTConfigFor("u_cap", q); err != nil {
		t.Fatalf("设置账号 QMT 配置: %v", err)
	}
	if err := cm.SetPaperConfigFor("u_cap", func(p *config.PaperConfig) {
		p.Enabled = true
		p.InitialCapital = 100000
	}); err != nil {
		t.Fatalf("设置账号 paper 配置: %v", err)
	}
	// 模板引擎故意带一个**不同的**帽：若装配错取模板值，下面两条都会读到 999999。
	tmpl := paper.New(paper.ConfigFromRules(config.PaperConfig{Enabled: false, FixedAmount: 10000},
		config.RiskGateConfig{MaxOrderAmount: 999999}), "")
	reg := NewRegistry(EngineOptions{Paper: tmpl, CfgMgr: cm, DataDir: t.TempDir()})

	pe := reg.GetPaper("u_cap")
	if pe == nil {
		t.Fatal("GetPaper 返回 nil")
	}
	if got := pe.Cfg().MaxOrderAmount; got != 20000 {
		t.Fatalf("引擎应继承账号级绝对帽 20000（不是模板的 999999），got %v", got)
	}

	// ② 没有 rules.paper 的账号：仍必须按**自己的** risk_gate 同步帽。
	if err := cm.SetQMTConfigFor("u_no_rules", &config.QMTConfig{RiskGate: config.RiskGateConfig{MaxOrderAmount: 5000}}); err != nil {
		t.Fatalf("设置 u_no_rules QMT: %v", err)
	}
	reg2 := NewRegistry(EngineOptions{Paper: tmpl, CfgMgr: cm, DataDir: t.TempDir()})
	pe2 := reg2.GetPaper("u_no_rules")
	if pe2 == nil {
		t.Fatal("GetPaper(u_no_rules) 返回 nil")
	}
	if got := pe2.Cfg().MaxOrderAmount; got != 5000 {
		t.Fatalf("无 rules.paper 的账号也应同步自己的帽 5000，got %v（模板值 999999 不该漏出来）", got)
	}
}
