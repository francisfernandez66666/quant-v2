// config 包单元测试：默认规则、配置读取/保存往返、子配置 Set/Get、LLM 流式开关。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultRules 出厂默认参数完整性：四战法权重与动量分归一、D1 规则可初始化。
func TestDefaultRules(t *testing.T) {
	d := DefaultRules
	if d == nil {
		t.Fatal("DefaultRules 为 nil")
	}
	s := d.Strategy
	w := s.Dragon.F1SealWeight + s.Dragon.F2ResonanceWeight + s.Dragon.F3PremiumWeight + s.Dragon.F4RsWeight
	if w != 1.0 {
		t.Errorf("Dragon 四因子权重合计应=1.0（e2e 验证组合）, got %.2f", w)
	}
	mo := s.Momentum
	if mo.VolumePriceWeight+mo.MACDWeight+mo.TrendWeight != 100 {
		t.Errorf("动量三分权重合计应=100, got %.0f", mo.VolumePriceWeight+mo.MACDWeight+mo.TrendWeight)
	}
	if s.NShape.NPatternScoreThreshold != 60 {
		t.Errorf("N形总分阈值默认应=60, got %.0f", s.NShape.NPatternScoreThreshold)
	}
}

// TestManagerLoadSaveRoundTrip 配置写入磁盘后二次加载仍能还原关键字段（D1 规则 + 策略覆盖）。
func TestManagerLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := NewManager(path)

	// 修改策略与 D1 后持久化
	newStrat := defaultStrategyConfig()
	newStrat.Dragon.F1SealWeight = 0.5
	m.SetStrategyConfig(&newStrat)

	d1 := &D1Config{Rules: []D1Rule{{Direction: "利好", Score: 0.8}}}
	m.SetD1Config(d1)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Set 后应落盘: %v", err)
	}

	// 用全新管理器从磁盘加载
	m2 := NewManager(path)
	if m2.Rules.Strategy.Dragon.F1SealWeight != 0.5 {
		t.Errorf("重载后 F1SealWeight 应=0.5, got %.2f", m2.Rules.Strategy.Dragon.F1SealWeight)
	}
	if len(m2.D1.Rules) != 1 || m2.D1.Rules[0].Direction != "利好" {
		t.Errorf("重载后 D1 规则丢失: %+v", m2.D1.Rules)
	}
}

// TestManagerMissingFileMissing 指向不存在路径时使用默认值而非崩溃。
func TestManagerLoadMissingFileUsesDefaults(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "nonexistent.json"))
	if m.Rules == nil {
		t.Fatal("缺失文件时应保有默认 Rules")
	}
	if m.GetStrategyConfig() == nil {
		t.Fatal("缺失文件时应保有默认策略")
	}
}

// TestManagerSetGet 各配置子项的 Get/Set 往返读取。
func TestManagerSetGet(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "cfg.json"))

	llm := &LLMConfig{APIURL: "https://x/v1", Model: "m", TimeoutSec: 42}
	m.SetLLMConfig(llm)
	if got := m.GetLLMConfig(); got.APIURL != "https://x/v1" || got.Model != "m" || got.TimeoutSec != 42 {
		t.Errorf("LLM Get/Set 往返失败: %+v", got)
	}
}

// TestStreamingEnabled LLM 流式开关：nil 默认开、显式 true/false 生效。
func TestStreamingEnabled(t *testing.T) {
	tr := true
	var fl bool
	if defaultLLM().StreamingEnabled() != true {
		t.Error("nil Stream 应默认开启流式（推理模型非流式首字极慢）")
	}
	c := &LLMConfig{Stream: &tr}
	if !c.StreamingEnabled() {
		t.Error("显式 true 应开启流式")
	}
	c2 := &LLMConfig{Stream: &fl}
	if c2.StreamingEnabled() {
		t.Error("显式 false 应关闭流式")
	}
	var nilCfg *LLMConfig
	if !nilCfg.StreamingEnabled() {
		t.Error("nil 接收者应返回默认开启")
	}
}

func defaultLLM() *LLMConfig { return &LLMConfig{} }