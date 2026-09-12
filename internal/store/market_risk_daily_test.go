// market_risk_daily_test.go — §MARKET_RISK_GATE P8 日级风险档留痕读写 + 按档聚合。
package store

import (
	"math"
	"testing"
)

func fp(x float64) *float64 { return &x }

func TestMarketRiskDailyUpsertAndSummary(t *testing.T) {
	db, err := Open(t.TempDir() + "/risk.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 三日：Normal / Yellow / Red，Red 缺炸板率（NULL）
	if err := db.UpsertMarketRiskDaily(MarketRiskDailyRow{TradeDate: "2026-09-10", RiskTier: "", Emotion: "发酵", UpRatio: fp(0.6), BreakRate: fp(20), LimitUpCount: 60}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMarketRiskDaily(MarketRiskDailyRow{TradeDate: "2026-09-11", RiskTier: "Yellow", Emotion: "退潮", UpRatio: fp(0.3), BreakRate: fp(55)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMarketRiskDaily(MarketRiskDailyRow{TradeDate: "2026-09-12", RiskTier: "Red", Emotion: "冰点", UpRatio: fp(0.08), BreakRate: nil /* NaN 弃权 */}); err != nil {
		t.Fatal(err)
	}
	// 同日覆盖（幂等）：9/12 改 tier=Red 且补炸板率
	if err := db.UpsertMarketRiskDaily(MarketRiskDailyRow{TradeDate: "2026-09-12", RiskTier: "Red", Emotion: "冰点", UpRatio: fp(0.05), BreakRate: fp(80)}); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListMarketRiskDaily("2026-09-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("应 3 条, got %d", len(list))
	}
	// 覆盖后 9/12 炸板率应为 80（非首轮 nil）
	if list[2].BreakRate == nil || *list[2].BreakRate != 80 {
		t.Errorf("同日覆盖未生效, 9/12 break=%v", list[2].BreakRate)
	}

	summ, err := db.SummarizeMarketRiskDaily("2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	byTier := map[string]MarketRiskTierSummary{}
	for _, s := range summ {
		byTier[s.Tier] = s
	}
	if byTier["None"].Days != 1 || byTier["Yellow"].Days != 1 || byTier["Red"].Days != 1 {
		t.Errorf("分档日数异常: %+v", summ)
	}
	// Yellow 平均上涨占比应为 0.3
	if byTier["Yellow"].AvgUp == nil || math.Abs(*byTier["Yellow"].AvgUp-0.3) > 1e-9 {
		t.Errorf("Yellow 平均广度应 0.3, got %v", byTier["Yellow"].AvgUp)
	}
}

func TestMarketRiskDailyNaNToNull(t *testing.T) {
	db, _ := Open(t.TempDir() + "/risk2.db")
	defer db.Close()
	nan := math.NaN()
	// 传 NaN 指针 → 落库应 NULL（读回 nil）
	if err := db.UpsertMarketRiskDaily(MarketRiskDailyRow{TradeDate: "2026-09-13", RiskTier: "Yellow", UpRatio: &nan}); err != nil {
		t.Fatal(err)
	}
	list, _ := db.ListMarketRiskDaily("2026-09-13", "")
	if len(list) != 1 || list[0].UpRatio != nil {
		t.Fatalf("NaN 应存为 NULL(读回 nil), got %+v", list)
	}
}
