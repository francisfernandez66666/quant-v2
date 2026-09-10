// EnhanceConfig 增强开关回归测试：零值=全关闭（向后兼容存量配置）、逐开关可独立开启、
// JSON 往返保持状态。这组开关承载本轮全部 9 项信号/战法增强，确保"默认不改变行为"。
package config

import (
	"encoding/json"
	"testing"
)

// TestEnhanceZeroValueAllOff 零值 EnhanceConfig 全部关闭（向后兼容：老配置没有 enhance 字段）。
func TestEnhanceZeroValueAllOff(t *testing.T) {
	var e EnhanceConfig
	if e.NewsDecay || e.AuctionSignal || e.FactorDedup || e.NewsImpact ||
		e.SectorLinkage || e.MarketState || e.OrderFlow || e.DynWeight || e.T0 {
		t.Fatal("EnhanceConfig 零值必须全部关闭（默认不改变任何行为）")
	}
}

// TestEnhanceIndependentlyToggle 各开关独立可开，互不牵连。
func TestEnhanceIndependentlyToggle(t *testing.T) {
	e := EnhanceConfig{AuctionSignal: true, MarketState: true}
	if !e.AuctionSignal || !e.MarketState {
		t.Fatal("显式开启的开关必须生效")
	}
	if e.NewsDecay || e.FactorDedup || e.NewsImpact || e.SectorLinkage || e.OrderFlow || e.DynWeight || e.T0 {
		t.Fatal("未显式开启的开关必须保持关闭")
	}
}

// TestEnhanceJSONRoundTrip 配置 JSON 序列化往返保持全部开关状态（落盘/重载不丢）。
func TestEnhanceJSONRoundTrip(t *testing.T) {
	orig := EnhanceConfig{
		NewsDecay: true, AuctionSignal: true, FactorDedup: true, NewsImpact: true,
		SectorLinkage: true, MarketState: true, OrderFlow: true, DynWeight: true, T0: true,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	var got EnhanceConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	if got != orig {
		t.Errorf("往返不一致: want %+v got %+v", orig, got)
	}
}

// TestEnhanceAttachedToRules 开关挂载在 Rules.Enhance 下，随规则配置整体读写。
func TestEnhanceAttachedToRules(t *testing.T) {
	r := Rules{Enhance: EnhanceConfig{NewsDecay: true}}
	if !r.Enhance.NewsDecay {
		t.Fatal("Rules.Enhance 应承载 NewsDecay 开关")
	}
	// 其余默认关
	if r.Enhance.T0 {
		t.Fatal("未设置开关必须关闭")
	}
}
