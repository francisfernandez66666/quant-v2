// paper_anchor_test.go — §ANCHOR-FIRST（2026-10-24 裁决二）模拟盘账锚点写入冻结锁。
//
// 钉死四件事（W2 影子层的 paper 侧）：
//
//	① 建仓即冻结：AnchorPrice 与当刻含费 CostPrice 同值、AnchorAt 同时刻；
//	② 高位加仓只摊成本，锚点与锚点时刻都不动（这正是裁决二修的形态：成本被抬到最高价之上会
//	   把移动止盈的 `high > 成本` 资格静默抹掉）；
//	③ 卖出一部分/全平都不改写锚点（全平后持仓消失，重新入场从零起算——由 ① 再走一遍）；
//	④ 锚点随 JSON 落盘并在重启后原样回来（paper 账无独立迁移，字段带出即成立，仍要实测一次）；
//	⑤ AnchorOrCost 的 0 值回退只服务 W3 读取口，W2 裁决不读它。
//
// English: paper-book anchor locks — frozen at open, untouched by add-ons/partial sells, survives the
// JSON round-trip; the cost fallback accessor exists only for the W3 read switch.
package paper

import (
	"testing"

	"quant-trading-v2/internal/data"
)

// TestPaperAnchorFrozenAcrossAddAndTrim 建仓冻结 → 高位加仓只摊成本 → 减仓锚点不动。
func TestPaperAnchorFrozenAcrossAddAndTrim(t *testing.T) {
	e := sellTestEngine(t, true)
	buyFill(t, e) // 1000 股 @10
	p := e.positions["300001"]
	if p.AnchorPrice <= 0 || p.AnchorPrice != p.CostPrice {
		t.Fatalf("建仓锚点须与当刻含费成本同值, got anchor=%v cost=%v", p.AnchorPrice, p.CostPrice)
	}
	if p.AnchorAt.IsZero() {
		t.Fatal("建仓锚点必须带冻结时刻（与 FilledAt 同批留痕）")
	}
	anchor, anchorAt := p.AnchorPrice, p.AnchorAt

	// 高位加仓 1000@30：成本被拉到两段之间，锚点必须原地不动
	q := map[string]*data.StockInfo{"300001": {Price: 30}}
	if err := e.BuyEx("300001", "测试股", "龙头", 30, 30, 1000, q); err != nil {
		t.Fatalf("加仓: %v", err)
	}
	p = e.positions["300001"]
	if p.Qty != 2000 {
		t.Fatalf("加仓后应 2000 股, got %d", p.Qty)
	}
	if p.CostPrice <= anchor {
		t.Fatalf("加仓必须摊薄成本（否则本用例没有证明两者是两回事）: cost=%v anchor=%v", p.CostPrice, anchor)
	}
	if p.AnchorPrice != anchor || !p.AnchorAt.Equal(anchorAt) {
		t.Fatalf("§ANCHOR-FIRST 加仓不得改写锚点/时刻: anchor %v→%v", anchor, p.AnchorPrice)
	}

	// 减仓一半：锚点仍不动（锚点只在建仓时写，卖出只减数量）
	t1Ready(e)
	if !e.ApplyUnifiedSell("300001", "trim", 28, "[统一裁决] 移动止盈减半") {
		t.Fatal("trim 处置应执行")
	}
	if p = e.positions["300001"]; p == nil || p.Qty != 1000 {
		t.Fatalf("trim 后应剩 1000 股, got %+v", p)
	}
	if p.AnchorPrice != anchor {
		t.Fatalf("§ANCHOR-FIRST 减仓不得改写锚点: got %v want %v", p.AnchorPrice, anchor)
	}
	// 全平：持仓行消失（锚点随行一起消失，重新入场从零起算——与裁决状态 Prune 同生命周期）
	if !e.ApplyUnifiedSell("300001", "close", 29, "[统一裁决] 窗结算") {
		t.Fatal("close 处置应执行")
	}
	if _, held := e.positions["300001"]; held {
		t.Fatal("close 后应无持仓")
	}
}

// TestPaperAnchorSurvivesRestart 锚点随 paper.json 落盘并在重启后原样回来。
// 这条是 W3 的前置：paper 账用 JSON 整体序列化（新字段自动带出），但仍要实测一次，
// 因为"字段没进 persistedState 白名单"在本仓是反复出现过的静默丢数据形态。
func TestPaperAnchorSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/paper.json"
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true}, path)
	e.SetStrategyPools([]string{"dragon"})
	buyFill(t, e)
	if err := e.BuyEx("300001", "测试股", "龙头", 30, 30, 1000, map[string]*data.StockInfo{"300001": {Price: 30}}); err != nil {
		t.Fatalf("加仓: %v", err)
	}
	want := e.positions["300001"].AnchorPrice
	wantCost := e.positions["300001"].CostPrice
	e.persist()

	reopened := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true}, path)
	p := reopened.positions["300001"]
	if p == nil {
		t.Fatal("重启后应恢复持仓")
	}
	if p.AnchorPrice != want {
		t.Fatalf("§ANCHOR-FIRST 锚点未随账本落盘: got %v want %v", p.AnchorPrice, want)
	}
	if p.CostPrice != wantCost {
		t.Fatalf("成本口径应原样恢复（锚点独立不代表成本变了）: got %v want %v", p.CostPrice, wantCost)
	}
}

// TestPaperAnchorOrCostFallback 0 值回退只服务 W3 读取口：存量未回填行按成本起算，
// 已回填行必须按锚点起算（不能被成本顶掉）。
func TestPaperAnchorOrCostFallback(t *testing.T) {
	legacy := &Position{Code: "300002", CostPrice: 12}
	if got := legacy.AnchorOrCost(); got != 12 {
		t.Fatalf("未回填锚点应回退成本, got %v", got)
	}
	anchored := &Position{Code: "300003", CostPrice: 15.05, AnchorPrice: 10.05}
	if got := anchored.AnchorOrCost(); got != 10.05 {
		t.Fatalf("已回填锚点必须优先于摊薄成本, got %v", got)
	}
	zero := &Position{Code: "300004"}
	if got := zero.AnchorOrCost(); got != 0 {
		t.Fatalf("两值皆 0 时不得凭空造价, got %v", got)
	}
}
