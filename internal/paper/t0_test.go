package paper

import (
	"testing"
	"time"
)

func TestAllocLayers(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)
	todayFill := now.Add(-1 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)

	l := AllocLayers(100, 10.0, todayFill, now)
	if l.BaseQty != 0 || l.Sellable != 0 || l.IntradayQty != 100 {
		t.Errorf("today fill -> all intraday, got %+v", l)
	}
	l2 := AllocLayers(100, 10.0, yesterday, now)
	if l2.BaseQty != 100 || l2.Sellable != 100 || l2.IntradayQty != 0 {
		t.Errorf("prior fill -> all base, got %+v", l2)
	}
	if AllocLayers(0, 10, yesterday, now).TotalQty != 0 {
		t.Errorf("zero qty must yield empty layers")
	}
}

func TestAllocLayersAcross(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)
	a := &Position{Code: "X", Qty: 100, CostPrice: 10, FilledAt: now.Add(-24 * time.Hour)}
	b := &Position{Code: "X", Qty: 50, CostPrice: 12, FilledAt: now.Add(-1 * time.Hour)} // 当日低吸
	all := AllocLayersAcross([]*Position{a, b}, now)
	if all.TotalQty != 150 || all.BaseQty != 100 || all.Sellable != 100 || all.IntradayQty != 50 {
		t.Errorf("aggregate wrong: %+v", all)
	}
	// 加权成本 (100*10 + 50*12)/150 = 10.667
	if all.WeightedCost < 10.6 || all.WeightedCost > 10.7 {
		t.Errorf("weighted cost want ~10.667 got %.3f", all.WeightedCost)
	}
}

func TestT0Cap(t *testing.T) {
	if c := T0Cap(1000, 0.3); c != 300 {
		t.Errorf("cap 30%% want 300 got %d", c)
	}
	if c := T0Cap(1000, 0); c != 300 {
		t.Errorf("default cap want 300 got %d", c)
	}
	if c := T0Cap(1000, 2); c != 1000 {
		t.Errorf("cap clamped to total, got %d", c)
	}
	if c := T0Cap(0, 0.3); c != 0 {
		t.Errorf("zero holding -> 0 cap, got %d", c)
	}
}

func TestT0Settle(t *testing.T) {
	layers := PositionLayers{BaseQty: 500, Sellable: 500, TotalQty: 500, WeightedCost: 10.0}
	spread, imp, cost := T0Settle(layers, 100, 9.8, 10.2)
	if spread <= 0 {
		t.Errorf("positive spread expected, got %.2f", spread)
	}
	if imp <= 0 {
		t.Errorf("positive cost improvement expected, got %.2f", imp)
	}
	if cost >= 10.0 {
		t.Errorf("new cost should drop below 10, got %.3f", cost)
	}
	// 卖价低于买价 → 不做 T+0（零记账）
	spread2, imp2, cost2 := T0Settle(layers, 100, 9.8, 9.5)
	if spread2 != 0 || imp2 != 0 || cost2 != 10.0 {
		t.Errorf("non-profitable round must be no-op, got (%.2f,%.2f,%.2f)", spread2, imp2, cost2)
	}
	// 可卖不足则按可卖量缩水卖出
	low := PositionLayers{BaseQty: 100, Sellable: 100, TotalQty: 100, WeightedCost: 10}
	_, imp3, _ := T0Settle(low, 500, 9.5, 10.0)
	if imp3 <= 0 {
		t.Errorf("sellable-limited settle should still improve cost, got %.2f", imp3)
	}
}