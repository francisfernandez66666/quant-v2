// tech_filter_test.go — §C4 KDJ/长均线买入过滤锁。
//
// 覆盖：J>阈值拦截、跌破 MA60 拦截、影子只标注、读数缺失弃权、卖出不受影响。
// English: §C4 locks — KDJ/MA hits downgrade buys, shadow annotates only, missing data abstains,
// sells are never filtered.
package signalctl

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func techBuy() combat_agent.Signal {
	return combat_agent.Signal{
		Code: "600000", Name: "测试", Strategy: "龙头", Direction: "做多", Action: "买入",
		Price: 10, Confidence: 0.95,
	}
}

func TestTechFilterKDJEnforces(t *testing.T) {
	c := New()
	shadow := false
	pol := Policy{TechFilter: config.TechFilterConfig{
		KDJJMax: 90,
		Shadow:  &shadow,
	}}
	sig := techBuy()
	sig.KDJ_J = 95
	d := c.admit(ChannelLive, "u", sig, pol, time.Now())
	if d.Verdict != VerdictHold || d.Stage != StageTechFilter {
		t.Fatalf("J=95>90 应降级 hold, got %+v", d)
	}
	if d.Shadow {
		t.Fatal("正式期不应标 shadow")
	}
}

func TestTechFilterShadowAnnotatesOnly(t *testing.T) {
	c := New()
	shadow := true
	pol := Policy{TechFilter: config.TechFilterConfig{
		KDJJMax: 90,
		Shadow:  &shadow,
	}}
	sig := techBuy()
	sig.KDJ_J = 95
	d := c.admit(ChannelLive, "u", sig, pol, time.Now())
	if d.Verdict != VerdictPass || !d.Shadow || d.Stage != StageTechFilter {
		t.Fatalf("影子期应放行并标注, got %+v", d)
	}
}

func TestTechFilterMA(t *testing.T) {
	c := New()
	shadow := false
	pol := Policy{TechFilter: config.TechFilterConfig{
		MAFilter: "ma60",
		Shadow:   &shadow,
	}}
	sig := techBuy()
	sig.CloseVsMA = map[string]float64{"ma60": -0.02}
	d := c.admit(ChannelLive, "u", sig, pol, time.Now())
	if d.Verdict != VerdictHold || d.Stage != StageTechFilter {
		t.Fatalf("收盘低于 MA60 应拦, got %+v", d)
	}
	// 读数缺失（无 ma60 键）→ 弃权放行。
	sig2 := techBuy()
	d2 := c.admit(ChannelLive, "u", sig2, pol, time.Now())
	if d2.Verdict != VerdictPass {
		t.Fatalf("均线读数缺失应弃权, got %+v", d2)
	}
}

func TestTechFilterDoesNotTouchSell(t *testing.T) {
	c := New()
	shadow := false
	pol := Policy{TechFilter: config.TechFilterConfig{KDJJMax: 90, Shadow: &shadow}}
	sig := combat_agent.Signal{Code: "600000", Direction: "做多", Action: "卖出", KDJ_J: 99}
	if d := c.admit(ChannelLive, "u", sig, pol, time.Now()); d.Stage == StageTechFilter {
		t.Fatalf("卖出必须直通, got %+v", d)
	}
}
