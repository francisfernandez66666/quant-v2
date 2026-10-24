// report_anchor_test.go — §ANCHOR-FIRST（2026-10-24 裁决二）手动/镜像账（report.ExecLog）锚点锁。
//
// report 账是三条"会动成本"的写路径最集中的一本：normalizeLots（加仓/减仓重算加权成本）、
// SetCostBasis（人工改成本）、镜像回灌。裁决二要求锚点在这三条路径下**等值不变**（方案 Q9），
// 而改风险线只能走显式 SetAnchor——本文件就是那条"AnchorPrice 不可变性"的承诺锁。
//
// English: the report book's anchor immutability lock (Q9): weighted-cost recomputation, manual cost
// edits and lot sells never move the anchor; only the explicit SetAnchor action does, and it can also
// declare "first-buy price unknown".
package report

import (
	"testing"
)

// heldOne 取唯一一条在持仓记录（用例都只开一仓，读不到即测试自身失败）。
func heldOne(t *testing.T, r *Report) ExecLog {
	t.Helper()
	held := r.HeldPositions()
	if len(held) != 1 {
		t.Fatalf("应恰有 1 条在持仓, got %d", len(held))
	}
	return held[0]
}

// TestReportAnchorFrozenAtOpen 建仓即锚点=入场价、时刻=入场时刻（同一次 time.Now()，不得分裂）。
func TestReportAnchorFrozenAtOpen(t *testing.T) {
	r := New("")
	r.LogSignalWithMeta("m1", "600276", "恒瑞医药", "做多", "dragon", 20, 50, 10, map[string]float64{"x": 1})
	l := heldOne(t, r)
	if l.AnchorPrice != 20 || l.EntryPrice != 20 {
		t.Fatalf("建仓锚点须等于入场价 20: cost=%v anchor=%v", l.EntryPrice, l.AnchorPrice)
	}
	if l.AnchorAt.IsZero() || !l.AnchorAt.Equal(l.EntryAt) {
		t.Fatalf("锚点时刻须与建仓时刻同批: anchorAt=%v entryAt=%v", l.AnchorAt, l.EntryAt)
	}
}

// TestReportAnchorImmuneToCostPaths 三条"动成本"的路径都不动锚点（Q9 裁决的落码面）。
//   - AddLot 高位加仓：加权成本被改写，锚点仍 20；
//   - SellLot 减仓：重算剩余批次成本，锚点仍 20；
//   - SetCostBasis 人工改成本：只改盈亏口径，锚点仍 20（风险线必须走显式 SetAnchor）。
//
// 成本侧只断言"必须与锚点分开"，不钉具体数字：report 的批次口径（建仓不写首笔 Lot）是既有语义，
// 本用例的职责是锚点不变性，不是重述摊薄算式。
func TestReportAnchorImmuneToCostPaths(t *testing.T) {
	r := New("")
	r.LogSignal("m2", "600276", "恒瑞医药", "做多", "dragon", 20, 50, 10)
	r.AddLot("m2", 38, 2)
	l := heldOne(t, r)
	if l.EntryPrice == 20 {
		t.Fatal("前置不成立：加仓必须改写成本口径，否则本用例证明不了两价分离")
	}
	if l.AnchorPrice != 20 {
		t.Fatalf("§ANCHOR-FIRST 加仓不得改写锚点, got %v（成本已变为 %v）", l.AnchorPrice, l.EntryPrice)
	}
	r.SellLot("m2", 40, 1) // 卖一半：剩余批次重算成本
	l = heldOne(t, r)
	if l.AnchorPrice != 20 {
		t.Fatalf("§ANCHOR-FIRST 减仓不得改写锚点, got %v（成本 %v）", l.AnchorPrice, l.EntryPrice)
	}
	r.SetCostBasis("m2", 50) // 人工修成本：盈亏口径变了，风险线基准不许跟着走
	l = heldOne(t, r)
	if l.EntryPrice != 50 {
		t.Fatalf("SetCostBasis 应改成本为 50, got %v", l.EntryPrice)
	}
	if l.AnchorPrice != 20 {
		t.Fatalf("§ANCHOR-FIRST 人工改成本不得动锚点（Q9：改风险线必须显式 SetAnchor）, got %v", l.AnchorPrice)
	}
}

// TestReportSetAnchorStates 显式改锚的四种返回态：写值/声明未知/幂等/记录不存在。
func TestReportSetAnchorStates(t *testing.T) {
	r := New("")
	r.LogSignal("m3", "300750", "宁德时代", "做多", "n_shape", 180, 30, 8)

	if !r.SetAnchor("m3", ptr(175)) {
		t.Fatal("改锚应成功")
	}
	l := heldOne(t, r)
	if l.AnchorPrice != 175 || l.AnchorAt.IsZero() {
		t.Fatalf("显式改锚须写值并刷新时刻: anchor=%v at=%v", l.AnchorPrice, l.AnchorAt)
	}
	if !r.SetAnchor("m3", ptr(175)) {
		t.Fatal("同值改锚应幂等返回 true")
	}
	// *price<=0 与 nil 同义＝声明「首买价未知」（镜像回灌存量持仓的那一支）：
	// 不是"非法取值被拒"，而是"显式把锚点清空"——两者都必须能红，语义不能靠调用方自觉。
	if !r.SetAnchor("m3", ptr(0)) {
		t.Fatal("*price<=0 应等价于声明未知并返回 true")
	}
	if l = heldOne(t, r); l.AnchorPrice != 0 || !l.AnchorAt.IsZero() {
		t.Fatalf("*price<=0 须把锚点与时刻一起归零: anchor=%v at=%v", l.AnchorPrice, l.AnchorAt)
	}
	// nil 再声明一次：已未知 ⇒ 幂等 true，且不得把 AnchorAt 重新点亮
	if !r.SetAnchor("m3", nil) {
		t.Fatal("声明未知应成功")
	}
	if l = heldOne(t, r); l.AnchorPrice != 0 || !l.AnchorAt.IsZero() {
		t.Fatalf("重复声明未知应保持归零: anchor=%v at=%v", l.AnchorPrice, l.AnchorAt)
	}
	// 记录不存在 ⇒ false（调用方据此出声，绝不静默）
	if r.SetAnchor("no-such-id", ptr(10)) {
		t.Fatal("不存在的记录应返回 false")
	}
	// 未知态下镜像/回填再写一次合法值：应能重新锚上（回填路径）
	if !r.SetAnchor("m3", ptr(166.5)) {
		t.Fatal("未知态应可被显式回填")
	}
	if l = heldOne(t, r); l.AnchorPrice != 166.5 {
		t.Fatalf("回填锚点应写入 166.5, got %v", l.AnchorPrice)
	}
}

// TestReportAnchorPersistsAcrossRestart 锚点随 JSON 落盘并在重启后回来（含"未知"态不复活成成本）。
func TestReportAnchorPersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/report.json"
	r := New(path)
	r.LogSignalWithMetaQtyUser("m4", "601138", "工业富联", "做多", "factor", 12.5, 30, 8, 1, map[string]float64{}, "u1")
	r.AddLot("m4", 20, 1) // 成本被批次口径改写，锚点必须仍是建仓的 12.5
	reopened := New(path)
	var got *ExecLog
	for i := range reopened.logs {
		if reopened.logs[i].SignalID == "m4" {
			got = &reopened.logs[i]
		}
	}
	if got == nil {
		t.Fatal("重启后应能读到记录")
	}
	if got.EntryPrice == 12.5 {
		t.Fatal("前置不成立：加仓未改写成本，落盘对照没有区分度")
	}
	if got.AnchorPrice != 12.5 {
		t.Fatalf("§ANCHOR-FIRST 锚点未随账本落盘: got %v（成本 %v）", got.AnchorPrice, got.EntryPrice)
	}
}

func ptr(f float64) *float64 { return &f }
