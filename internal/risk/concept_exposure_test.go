// concept_exposure_test.go — §C1-V2 概念敞口闸单测（2026-10-27 实装验收）。
//
// 概念库覆盖率已到 100%，owner 决定把「同概念股票买入上限」实装进执行层。本文件钉四件事：
//  1. 同概念第二笔把敞口推过上限 → 拒单（Gate=concept_exposure）；
//  2. 概念未知的票 → 弃权放行（不误拦，"未知"不是"超限"）；
//  3. 概念库覆盖率低于门槛 → 弃权放行（数据不可信时不许拿它拒真单）；
//  4. 影子期只写 risk_gates 留痕，不拒单（既有灰度纪律）。
//
// English: §C1-V2 concept-exposure locks for the shipped 40% cap — the second same-concept buy is
// rejected, unknown/low-coverage abstains, and shadow records only.
package risk

import (
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// seedConceptBook 播种：账户 10 万 + 已持 600000.SH 2 万 + 概念「机器人」含 600000/600001。
// 覆盖率口径＝sync_state 表里 status=ok 且 member_count>0 的行 / 总行数，所以这里必须写一行 ok。
func seedConceptBook(t *testing.T, db *store.DB) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_concept", AvailableCash: 80000, TotalAsset: 100000, MarketValue: 20000,
	}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600000.SH", Name: "机器人甲", Qty: 1000, CostPrice: 18, CurPrice: 20, UserID: "u_concept",
	}}); err != nil {
		t.Fatalf("UpsertRealPositions: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	if err := db.ReplaceConceptMembers("308001", "机器人", "ths", []string{"600000.SH", "600001.SH"}, now); err != nil {
		t.Fatalf("ReplaceConceptMembers: %v", err)
	}
	if err := db.UpsertConceptSyncState("308001", "机器人", now, 2, "ok", ""); err != nil {
		t.Fatalf("UpsertConceptSyncState: %v", err)
	}
}

// TestConceptExposureBlocksOverCap 已持 2 万（同概念）+ 本单 1.5 万 = 3.5 万 / 10 万 = 35% > 30% → 拒。
func TestConceptExposureBlocksOverCap(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	g := NewGate(db, "u_concept", nil)

	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 30
	hardShadow := false
	cfg.RiskGate.ConceptValueShadow = &hardShadow

	v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 15000))
	if v.Pass || v.Gate != "concept_exposure" {
		t.Fatalf("同概念超限应被 concept_exposure 拦下, got %+v", v)
	}
	// 同一只票的第二笔若金额很小（不越线）→ 放行：闸判的是"买入后预计敞口"，不是"是否同概念"。
	v2 := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 5000))
	if !v2.Pass {
		t.Fatalf("未越线的同概念买入应放行, got %+v", v2)
	}
}

// TestConceptExposureUnknownAbstains 概念未知（库里有这只票没这条概念）→ 弃权，不许误拦。
func TestConceptExposureUnknownAbstains(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	g := NewGate(db, "u_concept", nil)
	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 10 // 极严的上限：若"未知"被当成超限，这里必拦
	hardShadow := false
	cfg.RiskGate.ConceptValueShadow = &hardShadow
	if v := g.CheckLiveOrder(cfg, sectorOrder("600519.SH", 30000)); !v.Pass {
		t.Fatalf("概念未知应弃权放行, got %+v", v)
	}
}

// TestConceptExposureLowCoverageAbstains 覆盖率门槛：概念库不可信时不许拿它拒真单。
func TestConceptExposureLowCoverageAbstains(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	// 再写一条 status=error 的概念行 ⇒ 覆盖率降到 50%，低于门槛 95% ⇒ 整条闸弃权。
	if err := db.UpsertConceptSyncState("308002", "半成品", "", 0, "error", "boom"); err != nil {
		t.Fatalf("UpsertConceptSyncState: %v", err)
	}
	g := NewGate(db, "u_concept", nil)
	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 10
	hardShadow := false
	cfg.RiskGate.ConceptValueShadow = &hardShadow
	if v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 30000)); !v.Pass {
		t.Fatalf("覆盖率不足应弃权放行, got %+v", v)
	}
}

// TestConceptExposureShadowRecordsOnly 影子期（nil=默认影子）：只留痕不拒单。
func TestConceptExposureShadowRecordsOnly(t *testing.T) {
	db := sectorGateDB(t)
	seedConceptBook(t, db)
	g := NewGate(db, "u_concept", nil)
	cfg := qmtCfg()
	cfg.RiskGate.ConceptValuePct = 10 // 一定越线
	cfg.RiskGate.ConceptValueShadow = nil
	if v := g.CheckLiveOrder(cfg, sectorOrder("600001.SH", 30000)); !v.Pass {
		t.Fatalf("影子期不得拒单, got %+v", v)
	}
}
