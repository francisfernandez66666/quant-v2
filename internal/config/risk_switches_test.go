// risk_switches_test.go — §C1/C2/C3/C4/C6/B7 配置开关默认值锁。
//
// 默认必须是安全侧：敞口/风险预算/VWAP 过滤影子开、全卖出自动关、技术过滤档位可归一。
// English: default-value locks for the new risk switches — shadow-first, all-source auto-sell off,
// and technical-filter normalization.
package config

import "testing"

func TestNewRiskSwitchDefaults(t *testing.T) {
	var q QMTConfig
	if !q.RiskGate.SectorValueShadowOn() || !q.RiskGate.ConceptValueShadowOn() || !q.RiskGate.RiskBudgetShadowOn() {
		t.Fatal("行业/概念/ATR 风险预算默认应为影子")
	}
	if !q.VWAPPremiumShadowOn() {
		t.Fatal("VWAP 硬过滤默认应为影子")
	}
	if q.AutoSellAllSourcesOn() {
		t.Fatal("全卖出自动默认必须关闭（回滚安全侧）")
	}
	if q.AutoCautionOn() {
		t.Fatal("AutoRiskCaution 默认必须关闭")
	}
}

func TestTechFilterNormalize(t *testing.T) {
	cases := map[string]string{
		"": "off", "off": "off", "MA20": "ma20", " ma60 ": "ma60",
		"ma120_250": "ma120_250", "ma120250": "ma120_250", "bogus": "off",
	}
	for in, want := range cases {
		got := TechFilterConfig{MAFilter: in}.MAFilterOf()
		if got != want {
			t.Fatalf("MAFilterOf(%q)=%q, want %q", in, got, want)
		}
	}
	if (TechFilterConfig{}).Enabled() {
		t.Fatal("空技术过滤应为关闭")
	}
	if !(TechFilterConfig{KDJJMax: 90}).Enabled() {
		t.Fatal("配置 KDJ 上限后应启用")
	}
}
