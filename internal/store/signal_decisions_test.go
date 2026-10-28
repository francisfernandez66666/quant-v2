package store

// signal_decisions_test.go — §DECISION-TODAY（2026-10-28）：当日动态仓位快照的读写面。
// 锁三件事：同键 last-write（一天一信号一行）、按日隔离（每日重新定权，不串日）、保留期清理只删旧日。

import "testing"

// TestSignalDecisionUpsertAndDayIsolation 覆盖 last-write 与跨日隔离。
func TestSignalDecisionUpsertAndDayIsolation(t *testing.T) {
	db := testDB(t)
	base := SignalDecision{
		UserID: "u_sd", Day: "20261028", Channel: "live", Code: "600000",
		Strategy: "fac_1", At: "2026-10-28T10:00:00+08:00", Stage: "evaluated",
		MarketScale: 0.8, StockScale: 0.9, PerTradePct: 0.18,
		Total: 100000, Cash: 50000, Target: 80000, CurStrat: 60000, Room: 20000,
		IndustryRoom: 9000, StockRoom: 8000, ClusterRoom: 7000,
		CappedBy: "industry", Qty: 500, Notional: 5000, Reason: "受限于 industry",
	}
	if err := db.UpsertSignalDecision(base); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 同一键再写：last-write（阶段/数量/原因覆盖，不产生第二行）。
	upd := base
	upd.At = "2026-10-28T10:05:00+08:00"
	upd.Stage = "skipped"
	upd.Qty, upd.Notional = 0, 0
	upd.Reason = "持仓数已达上限 6（当前 6 只）"
	if err := db.UpsertSignalDecision(upd); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	items, err := db.SignalDecisionsForDay("u_sd", "20261028")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("同键应只有一行, got %d", len(items))
	}
	if items[0].Stage != "skipped" || items[0].Qty != 0 || items[0].Reason == "" {
		t.Fatalf("last-write 未生效: %+v", items[0])
	}
	// 其它账号/其它日隔离：每日重新定权，不串账。
	if got, _ := db.SignalDecisionsForDay("u_other", "20261028"); len(got) != 0 {
		t.Fatalf("其它账号不应看到快照, got %d", len(got))
	}
	if got, _ := db.SignalDecisionsForDay("u_sd", "20261029"); len(got) != 0 {
		t.Fatalf("其它交易日不应看到快照, got %d", len(got))
	}
}

// TestSignalDecisionPruneOnlyOldDays 保留期清理只删早于保留线的行。
func TestSignalDecisionPruneOnlyOldDays(t *testing.T) {
	db := testDB(t)
	for _, day := range []string{"20260901", "20261020", "20261028"} {
		if err := db.UpsertSignalDecision(SignalDecision{
			UserID: "u_p", Day: day, Channel: "live", Code: "600000",
			Strategy: "fac_1", At: day, Stage: "skipped", Reason: "test",
		}); err != nil {
			t.Fatalf("seed %s: %v", day, err)
		}
	}
	n, err := db.PruneSignalDecisions("20261001")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只删 1 条旧日, got %d", n)
	}
	if got, _ := db.SignalDecisionsForDay("u_p", "20261020"); len(got) != 1 {
		t.Fatalf("保留线以内的行被误删")
	}
}
