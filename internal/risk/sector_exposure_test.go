// sector_exposure_test.go — §C1 行业敞口闸单测。
//
// 四条锁：
//  1. 同行业两笔买入，第二笔使行业占比超上限 → 拒单；
//  2. 不同行业互不影响；
//  3. 行业未知 / 总资产不可得 → fail-open；
//  4. 影子期只写 risk_gates 留痕，不拒单。
//
// English: §C1 sector-exposure locks — second same-sector buy is rejected once the cap is
// breached; different sectors are independent; unknown inputs fail open; shadow records only.
package risk

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

func sectorGateDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sector.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedSectorBook(t *testing.T, db *store.DB) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_sector", AvailableCash: 80000, TotalAsset: 100000, MarketValue: 20000,
	}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	// 已持有 600000.SH 市值 2 万（1000 股 × 20）。
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 18, CurPrice: 20, UserID: "u_sector",
	}}); err != nil {
		t.Fatalf("UpsertRealPositions: %v", err)
	}
}

func sectorOrder(code string, amount float64) LiveOrder {
	o := liveOrder(SideBuy)
	o.Code = code
	o.Amount = amount
	o.Price = 10
	o.Qty = int(amount / 10)
	return o
}

func TestSectorExposureBlocksOverCap(t *testing.T) {
	db := sectorGateDB(t)
	seedSectorBook(t, db)
	g := NewGate(db, "u_sector", nil)
	// 简化行业映射：600000/600001 同属“银行”，600519 属“白酒”。
	g.SetIndustrySource(func(code string) string {
		switch {
		case code == "600000.SH" || code == "600001.SH":
			return "银行"
		case code == "600519.SH":
			return "白酒"
		}
		return ""
	})
	cfg := qmtCfg()
	cfg.RiskGate.SectorValuePct = 30
	hardShadow := false
	cfg.RiskGate.SectorValueShadow = &hardShadow

	// 已有 2 万 + 本单 1.5 万 = 3.5 万 / 10 万 = 35% > 30% → 拒。
	v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000))
	if v.Pass || v.Gate != "sector_exposure" {
		t.Fatalf("同行业超限应被 sector_exposure 拦下, got %+v", v)
	}
	// 不同行业 → 放行。
	if v := g.CheckLiveOrder(cfg, sectorOrder("600519.SH", 15000)); !v.Pass {
		t.Fatalf("不同行业不应被拦, got %+v", v)
	}
	// 未知行业 → fail-open。
	if v := g.CheckLiveOrder(cfg, sectorOrder("300999.SZ", 15000)); !v.Pass {
		t.Fatalf("未知行业应弃权放行, got %+v", v)
	}
}

func TestSectorExposureShadowOnlyRecords(t *testing.T) {
	db := sectorGateDB(t)
	seedSectorBook(t, db)
	g := NewGate(db, "u_sector", nil)
	g.SetIndustrySource(func(code string) string { return "银行" })
	cfg := qmtCfg()
	cfg.RiskGate.SectorValuePct = 30
	shadow := true
	cfg.RiskGate.SectorValueShadow = &shadow

	if v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000)); !v.Pass {
		t.Fatalf("影子期不得拒单, got %+v", v)
	}
	day := cntime.In(time.Now()).Format("2006-01-02")
	hits, err := db.RiskGateHits("u_sector", day)
	if err != nil {
		t.Fatalf("RiskGateHits: %v", err)
	}
	if hits["sector_exposure"] == 0 {
		t.Fatalf("影子命中未写 risk_gates: %+v", hits)
	}
}
