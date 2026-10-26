// anchor_backfill_test.go — §DYN-T 存量锚点回填的行为锁（方案 §1.6a / §6.1 锁 11④）。
//
// 口径在 2026-10-26 从"按摊薄成本回填"改成**按首买价回填**，因此这批锁的方向也跟着翻：
//  1. 层账可证 ⇒ 回填**最早那一批**的含费买入价（不是摊薄成本），来源 first_lot；
//  2. 无层账但 engine 给了买入历史 ⇒ first_buy_order；
//  3. 两者都没有 ⇒ 只有 owner 签字后的显式回填才允许写 cost_fallback，
//     而**加载期的自动回填绝不写这一档**（否则"打开账本"就等于把摊薄成本伪装成锚点，
//     正是本批要防的那类基准漂移从后门回来）；
//  4. 完全无价 ⇒ AnchorPrice 保持 0、来源 none，计入跳过；
//  5. 已有锚点**永不被覆盖**（哪怕与成本不等）：覆盖一次就等于抹掉真首买价；
//  6. 幂等：第二次调用回填 0 条；
//  7. **开 T 资格**：first_lot / first_buy_order 可开；cost_fallback 与 none 不可开。
//
// English: locks the v4 first-buy backfill — lot/history provenance, cost fallback only through the
// signed path (never at load), existing anchors untouched, idempotent, and T eligibility keyed off the
// provenance rather than the mere presence of a price.
package paper

import (
	"strings"
	"testing"
	"time"
)

// legacyPos 造一条"W2 之前落盘"的存量持仓（AnchorPrice=0）。
func legacyPos(code, name string, qty int, cost float64, filledAt time.Time, lots []PositionLot) *Position {
	return &Position{Code: code, Name: name, Qty: qty, CostPrice: cost, Cost: cost * float64(qty),
		FilledAt: filledAt, Mark: cost, Lots: lots}
}

func TestBackfillAnchorsPrefersFirstLotPrice(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d1 := time.Date(2026, 10, 10, 9, 31, 0, 0, time.Local)
	d2 := time.Date(2026, 10, 20, 14, 0, 0, 0, time.Local)
	e.mu.Lock()
	// 摊薄成本 8.0，但层账里最早那批是 9.2 ⇒ 必须回填 9.2（首买价），绝不能拿 8.0 冒充。
	e.positions["600000"] = legacyPos("600000", "摊薄股", 1000, 8.0, d2,
		[]PositionLot{{Qty: 400, Price: 9.2, FilledAt: d1}, {Qty: 600, Price: 7.2, FilledAt: d2}})
	e.mu.Unlock()

	filled, skipped := e.BackfillAnchors(nil)
	if filled != 1 || skipped != 0 {
		t.Fatalf("层账可证应回填 1 条，got filled=%d skipped=%d", filled, skipped)
	}
	e.mu.Lock()
	p := e.positions["600000"]
	e.mu.Unlock()
	if p.AnchorPrice != 9.2 {
		t.Fatalf("锚点必须是首买价 9.2（不是摊薄成本 8.0），got %v", p.AnchorPrice)
	}
	if p.AnchorSource != anchorSourceFirstLot {
		t.Fatalf("来源应记 first_lot，got %q", p.AnchorSource)
	}
	if !p.AnchorTicket() {
		t.Fatal("first_lot 来源的持仓必须有开 T 资格")
	}
	if !p.AnchorAt.Equal(d2) && p.AnchorAt.IsZero() {
		t.Fatalf("AnchorAt 必须盖章（建仓或本轮时刻），got %v", p.AnchorAt)
	}
	// 幂等
	if f2, _ := e.BackfillAnchors(nil); f2 != 0 {
		t.Fatalf("第二次回填必须为 0，got %d", f2)
	}
}

func TestBackfillAnchorsUsesBrokerHistoryWhenNoLots(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	e.mu.Lock()
	e.positions["600001"] = legacyPos("600001", "无层账股", 500, 7.5, d, nil)
	e.mu.Unlock()

	// engine 注入的买入历史回调：最早一笔成交 8.40（≠摊薄成本 7.5）
	filled, _ := e.BackfillAnchors(func(code string) (float64, bool) { return 8.4, true })
	if filled != 1 {
		t.Fatalf("有买入历史时应回填 1 条，got %d", filled)
	}
	e.mu.Lock()
	p := e.positions["600001"]
	e.mu.Unlock()
	if p.AnchorPrice != 8.4 || p.AnchorSource != anchorSourceFirstBuy {
		t.Fatalf("应回填 first_buy_order 8.4，got %v/%q", p.AnchorPrice, p.AnchorSource)
	}
	if !p.AnchorTicket() {
		t.Fatal("first_buy_order 来源同样有开 T 资格")
	}
}

func TestBackfillCostFallbackOnlyThroughSignedPath(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	e.mu.Lock()
	e.positions["600002"] = legacyPos("600002", "只有成本", 300, 6.0, d, nil)
	e.positions["600003"] = &Position{Code: "600003", Name: "完全无价", Qty: 100} // 无成本、无层账
	e.mu.Unlock()

	// ① 加载期自动回填：**不得**写成本档
	if filled, skipped := e.BackfillAnchorsOnLoad(); filled != 0 {
		t.Fatalf("加载期不得用摊薄成本冒充锚点，got filled=%d", filled)
	} else if skipped != 2 {
		// 600002 只有成本（不允许）、600003 无任何价 ⇒ 两条都算"无价可回填"
		t.Logf("加载期跳过 %d 条（预期 2：成本档须走签字路径 + 无价行）", skipped)
	}
	e.mu.Lock()
	if e.positions["600002"].AnchorPrice != 0 {
		e.mu.Unlock()
		t.Fatal("加载期把成本写成了锚点＝从后门放回本批要修的缺陷")
	}
	e.mu.Unlock()

	// ② owner 签字后的显式回填：允许 cost_fallback，但**不给开 T 资格**
	filled, skipped := e.BackfillAnchors(func(string) (float64, bool) { return 0, false })
	if filled != 1 || skipped != 1 {
		t.Fatalf("签字回填应写 1 条（成本档）、跳过 1 条（无任何价），got filled=%d skipped=%d", filled, skipped)
	}
	e.mu.Lock()
	costOnly := e.positions["600002"]
	noneRow := e.positions["600003"]
	e.mu.Unlock()
	if costOnly.AnchorPrice != 6.0 || costOnly.AnchorSource != anchorSourceCostFallback {
		t.Fatalf("成本档应记 6.0/cost_fallback，got %v/%q", costOnly.AnchorPrice, costOnly.AnchorSource)
	}
	if costOnly.AnchorTicket() {
		t.Fatal("cost_fallback 档**不得**有开 T 资格（可能被历史加仓或 T 腿摊薄过）")
	}
	// 完全无价的行：锚点保持 0（写 0 等于没写），来源也留空——计划报告里它才是 none，
	// 账本上不落一个"看起来像结论"的 none，免得前端把它读成"已处理"。
	if noneRow.AnchorPrice != 0 || noneRow.AnchorSource != "" {
		t.Fatalf("无价行必须保持未回填，got %v/%q", noneRow.AnchorPrice, noneRow.AnchorSource)
	}
	if noneRow.AnchorTicket() {
		t.Fatal("无价行不得有开 T 资格")
	}
	var sawNone bool
	for _, r := range e.AnchorBackfillPlan(func(string) (float64, bool) { return 0, false }, true) {
		if r.Code == "600003" {
			sawNone = r.Source == anchorSourceNone && r.WouldSkip
		}
	}
	if !sawNone {
		t.Fatal("dry-run 计划必须把完全无价的行标成 none/would_skip，供 owner 逐票核对")
	}
}

func TestBackfillNeverOverwritesExistingAnchor(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d := time.Date(2026, 10, 12, 9, 31, 0, 0, time.Local)
	e.mu.Lock()
	e.positions["600004"] = &Position{Code: "600004", Name: "已锚定", Qty: 100,
		CostPrice: 20, AnchorPrice: 15.05, AnchorSource: anchorSourceFirstLot, FilledAt: d}
	e.mu.Unlock()
	if filled, _ := e.BackfillAnchors(func(string) (float64, bool) { return 99, true }); filled != 0 {
		t.Fatalf("已有锚点不得被回填触碰，got %d", filled)
	}
	e.mu.Lock()
	p := e.positions["600004"]
	e.mu.Unlock()
	if p.AnchorPrice != 15.05 || p.AnchorSource != anchorSourceFirstLot {
		t.Fatalf("已有锚点被覆盖（真首买价被抹掉），got %v/%q", p.AnchorPrice, p.AnchorSource)
	}
}

func TestAnchorCoverageAndPlanReport(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d := time.Date(2026, 10, 10, 9, 31, 0, 0, time.Local)
	e.mu.Lock()
	e.positions["600005"] = &Position{Code: "600005", Name: "有锚", Qty: 1000, CostPrice: 10, Mark: 10,
		AnchorPrice: 10, AnchorSource: anchorSourceFirstLot, FilledAt: d}
	e.positions["600006"] = legacyPos("600006", "待回填", 500, 9, d, []PositionLot{{Qty: 500, Price: 11, FilledAt: d}})
	e.mu.Unlock()

	cov := e.AnchorCoverage(nil)
	if cov.Positions != 2 || cov.WithAnchor != 1 || cov.Missing != 1 {
		t.Fatalf("覆盖率读数错：%+v", cov)
	}
	if cov.Backfillable != 1 {
		t.Fatalf("待回填里应有 1 条可拿到首买价：%+v", cov)
	}
	if cov.CoveragePct != 50 {
		t.Fatalf("按行数覆盖率应 50%%，got %.1f", cov.CoveragePct)
	}
	plan := e.AnchorBackfillPlan(nil, true)
	if len(plan) != 1 || plan[0].Code != "600006" || plan[0].NewAnchor != 11 {
		t.Fatalf("dry-run 计划只列待回填行且给新锚点，got %+v", plan)
	}
	if plan[0].LineDeltaBP == 0 {
		t.Fatal("dry-run 必须打印回填前后触发价位移（owner 签字要看的就是这一列）")
	}
}

func TestTAnchorBlockReason(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	d := time.Date(2026, 10, 10, 9, 31, 0, 0, time.Local)
	cases := []struct {
		name string
		pos  *Position
		want string // 非空＝必须拒绝开 T
	}{
		{"无持仓", nil, "无持仓"},
		{"锚点为 0", &Position{Code: "A", Qty: 100, CostPrice: 10, AnchorPrice: 0, FilledAt: d}, "锚点缺失"},
		{"成本回落档", &Position{Code: "B", Qty: 100, AnchorPrice: 10, AnchorSource: anchorSourceCostFallback}, "来源不可信"},
		{"无来源也无层账", &Position{Code: "C", Qty: 100, AnchorPrice: 10}, "来源不可信"},
		{"首买价档", &Position{Code: "D", Qty: 100, AnchorPrice: 10, AnchorSource: anchorSourceFirstLot}, ""},
		{"买入历史档", &Position{Code: "E", Qty: 100, AnchorPrice: 10, AnchorSource: anchorSourceFirstBuy}, ""},
	}
	for _, c := range cases {
		if got := e.TAnchorBlockReason(c.pos); c.want == "" && got != "" {
			t.Fatalf("%s：应有开 T 资格，got %q", c.name, got)
		} else if c.want != "" && got == "" {
			t.Fatalf("%s：必须拒绝开 T（%s），却放行了", c.name, c.want)
		} else if c.want != "" && !strings.Contains(got, c.want) {
			t.Fatalf("%s：拒绝原因应含 %q，got %q", c.name, c.want, got)
		}
	}
}
