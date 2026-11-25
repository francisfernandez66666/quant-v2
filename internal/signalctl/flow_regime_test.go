// flow_regime_test.go — §FLOW-QUADRANT 四象限决策锁。
//
// 三条：① 牛/熊×放量/缩量×高仓位各自的决策正确；② 低/中仓位、平量、range、弃权一律 Neutral
// （反证：把高档判据写成绝对仓位，则熊市 bear 目标 15% 下永远不触发，用例2会红）；
// ③ 缩量双闸门：单一闸门不满足时归平量，不误判（owner 2026-11-25 修正 r_yest<1.0 且 r_ma5<0.95）。
package signalctl

import (
	"math"
	"testing"
)

func TestDecideQuadrantEightCases(t *testing.T) {
	// 目标档：bull .60 / bear .15（market_state.go 默认值）；高档 posRel≥1.3。
	// bull 高档：TotalPosPct=0.60*1.4=0.84；bear 高档：0.15*1.4=0.21。
	cases := []struct {
		name string
		in   QuadrantInput
		want string
	}{
		{"牛放量高档→减仓", QuadrantInput{"bull", 0.84, 0.60, 1.5, 1.4}, QuadrantReduce},
		{"牛缩量高档→禁加仓", QuadrantInput{"bull", 0.84, 0.60, 0.9, 0.8}, QuadrantBlockBuy},
		{"熊放量高档→禁抄底", QuadrantInput{"bear", 0.21, 0.15, 1.5, 1.4}, QuadrantBlockBuy},
		{"熊缩量高档→中性", QuadrantInput{"bear", 0.21, 0.15, 0.9, 0.8}, QuadrantNeutral},
		{"牛放量低档→中性", QuadrantInput{"bull", 0.30, 0.60, 1.5, 1.4}, QuadrantNeutral},
		{"熊放量中档→中性", QuadrantInput{"bear", 0.15, 0.15, 1.5, 1.4}, QuadrantNeutral},
		{"牛平量高档→中性", QuadrantInput{"bull", 0.84, 0.60, 1.1, 1.05}, QuadrantNeutral},
		{"range放量高档→中性(交做T)", QuadrantInput{"range", 0.84, 0.35, 1.5, 1.4}, QuadrantNeutral},
	}
	for _, c := range cases {
		got := DecideQuadrant(c.in)
		if got.Decision != c.want {
			t.Fatalf("%s: Decision=%s(%s) 期望 %s", c.name, got.Decision, got.Reason, c.want)
		}
	}
}

// TestPosRelUsesTargetNotAbsolute 相对口径反证：熊市 15% 目标下，仓位 21% 就算高档
// （posRel=1.4）。若误用绝对 70%，此处 posRel 会是 0.21<1.3 → 禁抄底永不触发。
func TestPosRelUsesTargetNotAbsolute(t *testing.T) {
	in := QuadrantInput{MarketState: "bear", TotalPosPct: 0.21, TargetPosPct: 0.15, RYest: 1.5, RMa5: 1.4}
	d := DecideQuadrant(in)
	if d.Decision != QuadrantBlockBuy {
		t.Fatalf("熊市相对高档应禁抄底，实际 %s(%s) posRel=%.2f", d.Decision, d.Reason, d.PosRel)
	}
	if math.Abs(d.PosRel-1.4) > 1e-9 {
		t.Fatalf("posRel=%.3f 期望 1.4", d.PosRel)
	}
}

// TestVolStateDoubleGate 缩量双闸门：只满足一个条件不算缩量（防平量误判）。
func TestVolStateDoubleGate(t *testing.T) {
	cases := []struct {
		name string
		y, m float64
		want string
	}{
		{"双满足→缩量", 0.9, 0.8, VolStateShrink},
		{"11-25现场0.79/0.92→缩量", 0.79, 0.92, VolStateShrink},
		{"昨比不满足→平量", 1.1, 0.8, VolStateNeutral},
		{"均比不满足→平量", 0.9, 1.1, VolStateNeutral},
		{"均比=0.95边界→平量", 0.9, 0.95, VolStateNeutral},
		{"双满足放量→放量", 1.5, 1.3, VolStateSurge},
		{"只昨比放量→平量", 1.5, 0.8, VolStateNeutral},
		{"NaN弃权→平量", math.NaN(), 0.8, VolStateNeutral},
	}
	for _, c := range cases {
		if got := VolStateOf(c.y, c.m); got != c.want {
			t.Fatalf("%s: VolStateOf(%v,%v)=%s 期望 %s", c.name, c.y, c.m, got, c.want)
		}
	}
}

// TestPosRelAbstain 目标档不可得→弃权（posRel=0），不触发任何硬决策。
func TestPosRelAbstain(t *testing.T) {
	if got := PosRelOf(0.5, 0); got != 0 {
		t.Fatalf("目标档为0应弃权返回0，实际 %v", got)
	}
	d := DecideQuadrant(QuadrantInput{"bull", 0.5, 0, 1.5, 1.4})
	if d.Decision != QuadrantNeutral {
		t.Fatalf("目标档弃权应中性，实际 %s", d.Decision)
	}
}
