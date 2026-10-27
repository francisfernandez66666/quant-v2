// dynbudget_b0_test.go — §B0 碎单治理的执行层锁（owner 2026-10-27）。
//
// 三条口径（每条的"故意破坏必红"都写在用例注释里）：
//  1. 最小成交额闸在 **dynamic 下也生效**（旧实现用 !dynamicBudget 整段跳过它）；
//  2. 实盘持仓数上限只拦**新票**，已持仓票的加仓不受限；
//  3. 仓位帽钳制（>100 → 100）本身可测（写在下单路径里会被单笔上限遮蔽）。
//
// English: execution-layer locks for the anti-crumb batch — the min-notional gate applies in
// dynamic mode too, the position-count cap only blocks NEW codes, and the cap clamp is testable.
package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// TestClampCapPct 仓位帽钳制：≤0 兜 35、>100 钳 100、正常值原样。
// 反证：把 >100 的钳制删掉 → 这里必红（而集成路径看不出来，因为单笔上限先命中）。
func TestClampCapPct(t *testing.T) {
	cases := map[float64]float64{-1: 35, 0: 35, 35: 35, 100: 100, 150: 100, 1e9: 100}
	for in, want := range cases {
		if got := clampCapPct(in); got != want {
			t.Fatalf("clampCapPct(%v)=%v 期望 %v", in, got, want)
		}
	}
}

// TestB0MinOrderBlocksDynamic 最小成交额闸在 dynamic 下生效：总额 5 万、MS=SS=0.5 ⇒
// eff=0.15 ⇒ 7500 元 < 1 万 ⇒ 拒买。
// 反证：把 `!dynamicBudget &&` 加回去（旧实现）→ 本用例必红。
func TestB0MinOrderBlocksDynamic(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = 0 // 0 = 出厂默认（1 万），显式写出来免得 harness 的 -1 干扰
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", AvailableCash: 50000, TotalAsset: 50000,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-B0M", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, dynSizingCfg())
	if len(*orders) != 0 {
		t.Fatalf("7500 元 < 1 万应被碎单闸拦下，实得 %d 单: %+v", len(*orders), *orders)
	}
}

// TestB0MinOrderPassesAboveFloor 对照腿：同样的闸，总额 10 万 ⇒ eff=0.15 ⇒ 15000 ≥ 1 万 ⇒ 放行。
func TestB0MinOrderPassesAboveFloor(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = 0
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db) // 10 万
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-B0N", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, dynSizingCfg())
	if len(*orders) != 1 {
		t.Fatalf("15000 元 ≥ 1 万应放行，实得 %d 单", len(*orders))
	}
}

// TestB0MaxPositionsBlocksOnlyNewCodes 持仓数上限 2：已持两只 ⇒ 第三只新票被拦；
// **已持仓的那只加仓照常放行**（否则"满仓后永不补仓"）。
// 反证：把 held 豁免删掉 → 第二个断言必红；把上限判断删掉 → 第一个断言必红。
func TestB0MaxPositionsBlocksOnlyNewCodes(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MaxPositions = 2
		c.MinOrderAmount = -1 // 本用例不谈碎单闸
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	// 两只小持仓（各 1 万），只为占住"两个代码"。
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600001.SH", Name: "甲", Qty: 1000, CostPrice: 10, CurPrice: 10, UserID: "u_1"},
		{TsCode: "600002.SH", Name: "乙", Qty: 1000, CostPrice: 10, CurPrice: 10, UserID: "u_1"},
	}); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	wireDynEngine(t, e)

	// ① 第三只新票 → 拦。
	sigNew := combat_agent.Signal{ID: "SIG-B0P1", Code: "600003", Name: "丙", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sigNew, map[string]*data.StockInfo{"600003": {Code: "600003", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.8, StockScale: 1.0}, dynSizingCfg())
	if len(*orders) != 0 {
		t.Fatalf("持仓数已达 2，第三只新票应被拦，实得 %d 单", len(*orders))
	}

	// ② 已持仓的 600001 加仓 → 放行（上限只管新票）。
	sigAdd := combat_agent.Signal{ID: "SIG-B0P2", Code: "600001", Name: "甲", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sigAdd, map[string]*data.StockInfo{"600001": {Code: "600001", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.8, StockScale: 1.0}, dynSizingCfg())
	if len(*orders) != 1 {
		t.Fatalf("已持仓票的加仓不受持仓数上限约束，应放行 1 单，实得 %d", len(*orders))
	}
}

// TestB0HeldPositionsCountInCurStrat 已有持仓必须计入 CurStrat（本批修掉的漏项）：
// 持 5.5 万、MS=0.70 ⇒ 目标 7 万、剩余 1.5 万 ⇒ 本单被 Room 截到 1500 股（price=10）。
// 反证：把 poses 循环里的 stratVal 累加删掉（旧行为）→ Room 变 7 万 → 出 2400 股（单笔上限）→ 本用例红。
func TestB0HeldPositionsCountInCurStrat(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = -1
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600009.SH", Name: "底仓", Qty: 5500, CostPrice: 10, CurPrice: 10, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-B0C", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.70, StockScale: 1.0}, dynSizingCfg())
	if q := qtyOf(t, orders); q != 1500 {
		t.Fatalf("持仓 5.5 万应把剩余空间压到 1.5 万（1500 股），got %v", q)
	}
}
