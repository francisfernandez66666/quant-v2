// concept_exposure_test.go — §C1-V2 概念敞口闸单测。
//
// 锁：同概念第二笔超限拒单；不同概念互不影响；概念未知/覆盖率不足/数据过期弃权；影子只留痕。
// English: §C1-V2 concept-gate locks — same-concept cap, different concepts independent,
// unknown/low-coverage/stale abstain, shadow records only.
package risk

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

func seedConceptBook(t *testing.T, db *store.DB) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_c", AvailableCash: 80000, TotalAsset: 100000, MarketValue: 20000}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 18, CurPrice: 20, UserID: "u_c",
	}}); err != nil {
		t.Fatalf("UpsertRealPositions: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	if err := db.ReplaceConceptMembers("308001", "AI", "ths", []string{"600000.SH", "600001.SH"}, now); err != nil {
		t.Fatalf("ReplaceConceptMembers: %v", err)
	}
	if err := db.ReplaceConceptMembers("308002", "机器人", "ths", []string{"600519.SH"}, now); err != nil {
		t.Fatalf("ReplaceConceptMembers(2): %v", err)
	}
	if err := db.UpsertConceptSyncState("308001", "AI", now, 2, "ok", ""); err != nil {
		t.Fatalf("state1: %v", err)
	}
	if err := db.UpsertConceptSyncState("308002", "机器人", now, 1, "ok", ""); err != nil {
		t.Fatalf("state2: %v", err)
	}
}

func TestConceptExposureBlocksOverCap(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	g := NewGate(db, "u_c", nil)
	g.SetConceptPolicy(95, 48)
	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 30
	hard := false
	cfg.RiskGate.ConceptValueShadow = &hard

	// 已有同概念 2 万 + 本单 1.5 万 = 35% > 30% → 拒。
	v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000))
	if v.Pass || v.Gate != "concept_exposure" {
		t.Fatalf("同概念超限应拦, got %+v", v)
	}
	// 不同概念 → 放行。
	if v := g.CheckLiveOrder(cfg, sectorOrder("600519.SH", 15000)); !v.Pass {
		t.Fatalf("不同概念不应被拦, got %+v", v)
	}
	// 概念未知 → 弃权。
	if v := g.CheckLiveOrder(cfg, sectorOrder("300999.SZ", 15000)); !v.Pass {
		t.Fatalf("未知概念应弃权, got %+v", v)
	}
}

func TestConceptExposureAbstainsOnStaleOrLowCoverage(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	g := NewGate(db, "u_c", nil)
	// 覆盖率门槛 100%，当前 2/2=100% 才通过；先要求 100 验证不弃权。
	g.SetConceptPolicy(100, 1)
	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 30
	hard := false
	cfg.RiskGate.ConceptValueShadow = &hard
	if v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000)); v.Pass {
		t.Fatalf("覆盖率 100%% 时应正常拦, got %+v", v)
	}
	// 数据改成 3 小时前，陈小时=1 → 弃权。
	old := cntime.In(time.Now().Add(-3 * time.Hour)).Format(time.RFC3339)
	if err := db.UpsertConceptSyncState("308001", "AI", old, 2, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertConceptSyncState("308002", "机器人", old, 1, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000)); !v.Pass {
		t.Fatalf("数据过期应弃权放行, got %+v", v)
	}
}
