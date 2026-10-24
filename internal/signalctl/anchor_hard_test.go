// anchor_hard_test.go — §ANCHOR-FIRST-W3 切硬开关的行为锁（owner 裁决二）。
//
// 要证明的只有一件事：**同一笔输入，只差 `UseAnchor` 一个开关，判定线必须不同**。
// 这是等值锁的正确方向——如果两态结果相同，开关就是个摆设（改前 W2 的形态：AnchorPrice
// 随行但 sellProbe 一行不读，对照表恒绿即恒盲）。所以这里刻意不断言"锚点算出来的数是某个值"，
// 而是断言"两种基准落在不同的线上"，并各自点名具体线位。
//
// English: §ANCHOR-FIRST-W3 — the only claim is that flipping `UseAnchor` on identical input
// changes the locked line (a switch that changes nothing is exactly the W2 blind spot).
package signalctl

import (
	"testing"

	"quant-trading-v2/internal/config"
)

// TestSellProbeAnchorSwitchChangesLine 成本 15.05 / 锚点 10.05 / 现价 14：
// 按成本 −6.98%（触止损线），按锚点 +39.3%（触止盈线）。两态必须落在不同线上。
func TestSellProbeAnchorSwitchChangesLine(t *testing.T) {
	p := sellParamsOf(config.DefaultDisciplineConfig())
	now := st(10, 0)
	base := SellInput{Code: "600519.SH", EntryPrice: 15.05, AnchorPrice: 10.05, HighPrice: 14, CurPrice: 14}

	costSt, _, _ := sellProbe(nil, base, now, p)
	anchorIn := base
	anchorIn.UseAnchor = true
	anchorSt, _, _ := sellProbe(nil, anchorIn, now, p)

	if costSt == nil || anchorSt == nil {
		t.Fatalf("两态都应产出状态机快照, cost=%v anchor=%v", costSt, anchorSt)
	}
	if costSt.Line == anchorSt.Line {
		t.Fatalf("开关切换后判定线必须不同，两态都是 %s（开关等于没接线）", costSt.Line)
	}
	if costSt.Line != SellLineStopLoss {
		t.Fatalf("按摊薄成本 15.05 应触止损线, got %s", costSt.Line)
	}
	if anchorSt.Line != SellLineTakeProfit {
		t.Fatalf("按首买价 10.05 应触止盈线, got %s", anchorSt.Line)
	}
}

// TestSellProbeAnchorUnknownFallsBackToCost 锚点未知（存量未回填，AnchorPrice=0）时，
// 即使开了切硬也必须**回落成本**：拿 0 当基准会让百分比判据除零或恒触发，比用旧口径危险得多。
func TestSellProbeAnchorUnknownFallsBackToCost(t *testing.T) {
	p := sellParamsOf(config.DefaultDisciplineConfig())
	now := st(10, 0)
	base := SellInput{Code: "600519.SH", EntryPrice: 15.05, HighPrice: 14, CurPrice: 14}

	costSt, _, _ := sellProbe(nil, base, now, p)
	unknownIn := base
	unknownIn.UseAnchor = true // 开关打开但 AnchorPrice=0
	unknownSt, _, _ := sellProbe(nil, unknownIn, now, p)

	if costSt.Line != unknownSt.Line {
		t.Fatalf("锚点未知时必须回落成本口径：关=%s 开=%s", costSt.Line, unknownSt.Line)
	}
	if unknownSt.Line != SellLineStopLoss {
		t.Fatalf("回落成本后应仍触止损线, got %s", unknownSt.Line)
	}
}

// TestSellProbeAnchorSwitchOffKeepsCostBasis 开关关（默认）时锚点在场也不得参与判线：
// 这是"切硬需要显式表态"的反向锁——W2 的全部行为必须原样保留。
func TestSellProbeAnchorSwitchOffKeepsCostBasis(t *testing.T) {
	p := sellParamsOf(config.DefaultDisciplineConfig())
	now := st(10, 0)
	withAnchor := SellInput{Code: "600519.SH", EntryPrice: 15.05, AnchorPrice: 10.05, HighPrice: 14, CurPrice: 14}
	noAnchor := SellInput{Code: "600519.SH", EntryPrice: 15.05, HighPrice: 14, CurPrice: 14}

	a, _, _ := sellProbe(nil, withAnchor, now, p)
	b, _, _ := sellProbe(nil, noAnchor, now, p)
	if a.Line != b.Line {
		t.Fatalf("开关关闭时锚点不得影响判线：带锚=%s 不带锚=%s", a.Line, b.Line)
	}
}

// TestAnchorHardEnabledDefaultsOff 配置默认值锁：nil 与显式 false 都是"未切硬"，
// 只有显式 true 才切。资金路径上的默认值不允许"看起来像开了"。
func TestAnchorHardEnabledDefaultsOff(t *testing.T) {
	var c config.QMTConfig
	if c.AnchorHardEnabled() {
		t.Fatalf("零值配置不得视为已切硬")
	}
	off := false
	c.AnchorHard = &off
	if c.AnchorHardEnabled() {
		t.Fatalf("显式 false 不得视为已切硬")
	}
	on := true
	c.AnchorHard = &on
	if !c.AnchorHardEnabled() {
		t.Fatalf("显式 true 必须视为已切硬")
	}
}
