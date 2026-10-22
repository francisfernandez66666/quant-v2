// vwap_test.go — §C2 纸面成交 VWAP 落库锁。
package paper

import (
	"math"
	"testing"

	"quant-trading-v2/internal/data"
)

func TestPaperBuyRecordsVWAP(t *testing.T) {
	pe := New(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 10000}, "")
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Code: "600000.SH", Name: "浦发", Price: 10, ChangePct: 1, Amount: 1_000_000, Volume: 120_000},
	}
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("Buy: %v", err)
	}
	poses := pe.Positions()
	if len(poses) != 1 {
		t.Fatalf("应有一笔持仓, got %d", len(poses))
	}
	// VWAP = 1,000,000 / 120,000 = 8.3333…；成交价 10 → 偏差为正（买贵）。
	if math.Abs(poses[0].MarketVWAP-8.3333333) > 1e-6 {
		t.Fatalf("VWAP 落库不符: %v", poses[0].MarketVWAP)
	}
	if poses[0].VWAPPremiumPct <= 0 {
		t.Fatalf("成交价高于 VWAP 时偏差应为正, got %v", poses[0].VWAPPremiumPct)
	}
}
