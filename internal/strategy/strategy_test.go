// strategy 核心类型与 Pourpender/捞等评分工具：类型常量、动作分级与综合评分。
package strategy

import (
	"math"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestActionTypes 交易动作与优先级常量取值。
func TestActionTypes(t *testing.T) {
	if SignalDragon != "dragon" || SignalNShape != "n_shape" || SignalDragonReturn != "dragon_return" {
		t.Error("信号类型常量不一致")
	}
	if ActionBuy != "buy" || ActionSell != "sell" || ActionHold != "hold" || ActionWatch != "watch" {
		t.Error("交易动作常量不一致")
	}
	if P1 >= P2 || P2 >= P3_5 || P3_5 >= P3 || P3 >= P4 {
		t.Error("优先级常量应单调递增")
	}
}

// TestIsActionWatchOrAbove 动作分级判定。
func TestIsActionWatchOrAbove(t *testing.T) {
	for _, a := range []string{"buy", "sell", "hold", "watch"} {
		if !IsActionWatchOrAbove(a) {
			t.Errorf("%s 应属于 watch 及以上", a)
		}
	}
	if IsActionWatchOrAbove("unknown") || IsActionWatchOrAbove("") {
		t.Error("未知动作不应判定为 watch 级以上")
	}
}

// lad 返回测试用 Laodeng 配置。
func lad() *config.LaodengConfig {
	return &config.LaodengConfig{
		Enabled:      true,
		MarketCapMin: 100e8, // 100 亿
		PeMax:        30,
		TurnoverMin:  5,
		TechPenalty:  0.2,
		WeightScore:  1.0,
	}
}

// TestScoreLaodengFull 四维全达标 → 0.8 分（非科技板块）。
func TestScoreLaodengFull(t *testing.T) {
	got := ScoreLaodeng(lad(), 200e8, 20, 8, "白酒")
	if got < 0.79 || got > 0.81 {
		t.Errorf("全达标应≈0.8, got %.3f", got)
	}
}

// TestScoreLaodengTechWeight 科技板块命中加权乘 (1+TechPenalty)。
func TestScoreLaodengTechWeight(t *testing.T) {
	base := ScoreLaodeng(lad(), 200e8, 20, 8, "白酒")
	tech := ScoreLaodeng(lad(), 200e8, 20, 8, "半导体")
	if tech <= base {
		t.Errorf("科技板块应加权, base=%.3f tech=%.3f", base, tech)
	}
}

// TestScoreLaodengPartial 市值/换手不足按比例线性折算。
func TestScoreLaodengPartial(t *testing.T) {
	// 市值减半 → 0.3 折半
	got := ScoreLaodeng(lad(), 50e8, 20, 8, "白酒")
	want := 0.15 + 0.3 + 0.2 // 0.65
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("半市值应=%.3f, got %.3f", want, got)
	}
}

// TestScoreLaodengNope 无 PE 数据给保底 0.1。
func TestScoreLaodengNope(t *testing.T) {
	got := ScoreLaodeng(lad(), 200e8, 0, 8, "白酒")
	want := 0.3 + 0.1 + 0.2
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("无PE应=%.3f, got %.3f", want, got)
	}
}

// TestScoreLaodengDisabled 配置未启用/为 nil 返回 0。
func TestScoreLaodengDisabled(t *testing.T) {
	if ScoreLaodeng(nil, 200e8, 20, 8, "白酒") != 0 {
		t.Error("nil 配置应返回 0")
	}
	c := lad()
	c.Enabled = false
	if ScoreLaodeng(c, 200e8, 20, 8, "白酒") != 0 {
		t.Error("禁用配置应返回 0")
	}
}
