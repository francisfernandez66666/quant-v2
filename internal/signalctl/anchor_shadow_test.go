// anchor_shadow_test.go — §ANCHOR-FIRST（2026-10-24 裁决二）W2 影子对照层的判据锁。
//
// 两类断言，缺一不可：
//
//	① CompareAnchor 真值表：两个基准各算一遍线位、判线、移动止盈资格，以及"锚点未知"绝不
//	   折叠成"与成本相同"（那会让回填前的对照表恒绿＝恒盲）；
//	② 内核未换锚（W2 只影子）：sellProbe 对**只差 AnchorPrice** 的同一输入必须给出逐字相同的
//	   状态/处置/理由。这条是"本轮资金行为零变化"的证明；W3 切硬时必须被显式改写成"读锚"，
//	   它变红就是那把门被推开的信号，不是回归。
//
// English: two locks — the comparator's truth table (an unknown anchor is never folded into "same as
// cost"), and the W2 guarantee that the kernel ignores AnchorPrice. Lock ② turns red exactly when the
// W3 cutover flips the kernel: that is the gate opening, not a regression.
package signalctl

import (
	"encoding/json"
	"testing"
	"time"
)

// TestCompareAnchorShiftedBasis 高位加仓的标准形态：成本 15.05 / 锚点 10.05 / 高点 14 / 现价 14。
// 默认纪律 sl=6 tp=15 pb=6 dm=2 ⇒
//   - 止损价 14.147（成本）vs 9.447（锚点）：切硬会把止损线整体下移 4.70 元；
//   - 判线 成本口径 −6.98% 触止损 vs 锚点口径 +39.3% 止盈 ⇒ LineChanged（行为真的会变）；
//   - 移动止盈资格：high>cost 为假（今天资格被抹掉）、high>anchor 为真（裁决二买回资格）。
func TestCompareAnchorShiftedBasis(t *testing.T) {
	in := SellInput{Code: "600519.SH", EntryPrice: 15.05, AnchorPrice: 10.05, HighPrice: 14, CurPrice: 14}
	row := CompareAnchor(in, Policy{}, "live")
	if !row.AnchorKnown {
		t.Fatal("锚点已回填应记为已知")
	}
	if want := -33.222591362126245; absDiff(row.ShiftPct, want) > 1e-9 {
		t.Fatalf("偏移须为 (锚-成本)/成本 = %.6f, got %.6f", want, row.ShiftPct)
	}
	if absDiff(row.StopPriceByCost, 15.05*0.94) > 1e-9 || absDiff(row.StopPriceByAnchor, 10.05*0.94) > 1e-9 {
		t.Fatalf("两侧止损价: cost=%.4f anchor=%.4f", row.StopPriceByCost, row.StopPriceByAnchor)
	}
	if row.LineByCost != SellLineStopLoss.String() {
		t.Fatalf("成本口径应判止损线, got %q", row.LineByCost)
	}
	if row.LineByAnchor != SellLineTakeProfit.String() {
		t.Fatalf("锚点口径应判止盈线, got %q", row.LineByAnchor)
	}
	if !row.LineChanged {
		t.Fatal("判线两侧不同必须记 LineChanged（这张表的存在理由）")
	}
	if row.TrailReadyByCost {
		t.Fatal("前置校验：高点未超过摊薄成本时今天的移动止盈资格应为假")
	}
	if !row.TrailReadyByAnchor {
		t.Fatal("裁决二买回的正是这一格资格（high > 首买锚点）")
	}
	// 阈值随行带出：读表的人不必再去猜这轮用的是哪套纪律
	if row.StopLossPct != 6 || row.TakeProfitPct != 15 || row.PullbackPct != 6 || row.DeepMult != 2 {
		t.Fatalf("阈值应取默认纪律, got sl=%v tp=%v pb=%v dm=%v", row.StopLossPct, row.TakeProfitPct, row.PullbackPct, row.DeepMult)
	}
}

// TestCompareAnchorUnknownStaysUnknown 锚点未回填（0）必须点名"未知"，不得折叠成"与成本一致"。
// 锤实形态：把未回填行的 ByAnchor 直接等同于成本 ⇒ 回填完成前对照表报"差异 0"，
// 读起来像"切硬不影响任何持仓"，实际是"还没得比"。
func TestCompareAnchorUnknownStaysUnknown(t *testing.T) {
	row := CompareAnchor(SellInput{Code: "000001.SZ", EntryPrice: 12, HighPrice: 13, CurPrice: 11.3}, Policy{}, "paper")
	if row.AnchorKnown {
		t.Fatal("锚点为 0 必须记未回填")
	}
	if row.StopPriceByAnchor != 0 || row.TakePriceByAnchor != 0 || row.DeepPriceByAnchor != 0 {
		t.Fatalf("未回填侧线位必须留 0（算不出来），不得由成本代填: %+v", row)
	}
	if row.LineByAnchor != "" || row.PnlByAnchor != 0 {
		t.Fatalf("未回填侧不得出判线/涨跌幅: line=%q pnl=%v", row.LineByAnchor, row.PnlByAnchor)
	}
	if row.LineChanged {
		t.Fatal("未回填行不参与差异计数（LineChanged 必须为假，由 Unknown 桶单独点名）")
	}
	if row.LineByCost == "" {
		t.Fatal("成本口径一侧仍应正常出判线（影子表不能因为没锚点就不看盘）")
	}
	s := SummarizeAnchorRows([]AnchorShadowRow{row}, "2026-10-26", time.Now())
	if s.Rows != 1 || s.Unknown != 1 || s.Diverged != 0 || s.Shifted != 0 {
		t.Fatalf("总账须把未回填单独归一桶: %+v", s)
	}
}

// TestCompareAnchorNoShift 未加仓的持仓：锚点=成本 ⇒ 两价相同、判线相同、资格相同。
// 这一腿是"绝大多数持仓切硬无影响"的正向证据，也是上面两条的对照基线。
func TestCompareAnchorNoShift(t *testing.T) {
	in := SellInput{Code: "300750.SZ", EntryPrice: 100, AnchorPrice: 100, HighPrice: 100, CurPrice: 101}
	row := CompareAnchor(in, Policy{}, "report")
	if !row.AnchorKnown || row.ShiftPct != 0 || row.LineChanged {
		t.Fatalf("同价基准不应产生差异: %+v", row)
	}
	if row.LineByCost != row.LineByAnchor || row.TrailReadyByCost != row.TrailReadyByAnchor {
		t.Fatalf("两侧应完全一致: %+v", row)
	}
}

// TestAnchorShadowSummaryBuckets 总账分桶：已知/未回填、判线翻转、线位移动、分账本计数互不吞并。
func TestAnchorShadowSummaryBuckets(t *testing.T) {
	rows := []AnchorShadowRow{
		CompareAnchor(SellInput{Code: "A", EntryPrice: 15.05, AnchorPrice: 10.05, HighPrice: 14, CurPrice: 14}, Policy{}, "live"),
		CompareAnchor(SellInput{Code: "B", EntryPrice: 10, AnchorPrice: 10, HighPrice: 10, CurPrice: 10}, Policy{}, "live"),
		CompareAnchor(SellInput{Code: "C", EntryPrice: 20, HighPrice: 20, CurPrice: 20}, Policy{}, "paper"),
	}
	s := SummarizeAnchorRows(rows, "2026-10-26", time.Now())
	if s.Rows != 3 || s.Unknown != 1 {
		t.Fatalf("3 行、1 行未回填: %+v", s)
	}
	if s.Diverged != 1 {
		t.Fatalf("只有 A 判线翻转, got %d", s.Diverged)
	}
	if s.Shifted != 1 {
		t.Fatalf("只有 A 线位移动, got %d", s.Shifted)
	}
	if s.ByBook["live"] != 2 || s.ByBook["paper"] != 1 {
		t.Fatalf("分账本计数: %+v", s.ByBook)
	}
	if s.Day != "2026-10-26" {
		t.Fatalf("日键应由调用方给定（北京交易日）, got %q", s.Day)
	}
}

// TestSellProbeIgnoresAnchorInW2 W2 的资金行为零变化证明：同一输入只差 AnchorPrice 时，
// 状态、处置单、延持/观察理由必须逐字相同。
//
// 反证设计：若内核把风险线分母换成锚点（W3 语义），本用例会红——那是切硬门被推开的信号，
// 届时应改写为"必须读锚"，而不是删掉它。
// English: the W2 no-op proof. It is deliberately the lock that W3 flips (rewrite, never delete).
func TestSellProbeIgnoresAnchorInW2(t *testing.T) {
	now := time.Now()
	p := sellParamsOf(Policy{}.Discipline)
	base := SellInput{Code: "600519.SH", EntryPrice: 15.05, HighPrice: 14, CurPrice: 14}
	withAnchor := base
	withAnchor.AnchorPrice = 10.05

	// 状态机已锁在止损线、结算点已过 —— 只有走到 ⑤ 窗结算才会出处置单。
	// 反证设计（第一版踩过）：st==nil 的首轮只进观察窗、恒返回 nil 处置单，
	// 三条"逐字相同"于是变成恒真空跑，本锁没有射程。
	stale := func() sellState {
		return sellState{Line: SellLineStopLoss, FirstTouch: now.Add(-16 * time.Minute),
			WindowMin: p.exitMin, SettleStart: now.Add(-time.Minute), HighPrice: 14}
	}
	stA0, stB0 := stale(), stale()

	stA, dA, hA := sellProbe(&stA0, base, now, p)
	stB, dB, hB := sellProbe(&stB0, withAnchor, now, p)
	if hA != hB {
		t.Fatalf("hold 理由必须逐字相同: %q vs %q", hA, hB)
	}
	if mustJSON(t, stA) != mustJSON(t, stB) {
		t.Fatalf("状态机不得因锚点而变: %s vs %s", mustJSON(t, stA), mustJSON(t, stB))
	}
	if mustJSON(t, dA) != mustJSON(t, dB) {
		t.Fatalf("处置单不得因锚点而变: %s vs %s", mustJSON(t, dA), mustJSON(t, dB))
	}
	// 正向自证：这条输入在成本口径下确实触发了止损线（否则三条"相同"是恒真的空跑）
	if dA == nil {
		t.Fatal("前置不成立：该输入应已在成本口径下出处置单，否则本锁没有射程")
	}
	if absDiff(dA.PnlPct, (14-15.05)/15.05*100) > 1e-9 {
		t.Fatalf("处置单盈亏必须仍按成本口径（-7.0%%），得 %v", dA.PnlPct)
	}
	// 射程的第二维：换成锚点口径同一条输入会落到止盈线（+39.3%），W3 切硬时这里的 Line
	// 必然变红——所以断言锁死"仍是止损"，而不只是"两腿相同"。
	if dA.Line != SellLineStopLoss {
		t.Fatalf("W2 内核判线必须停在成本口径的止损线，得 %v", dA.Line)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	return string(raw)
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
