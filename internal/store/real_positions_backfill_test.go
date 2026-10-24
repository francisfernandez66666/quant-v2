// real_positions_backfill_test.go — §ANCHOR-FIRST W3 存量锚点回填（实盘账本侧）的行为锁。
//
// 与 paper/report 两本账同一口径，但这里的反证方向更硬：实盘账本的 anchor_price 是**唯一**
// 允许券商快照都碰不到的风险线基准（见 real_positions_anchor_test.go）。回填若写成"无条件对齐成本"，
// 每次启动都会把高位加仓摊薄后的成本写进锚点，止损线当场被推远——这是资损向，必须锁死。
// English: the live-book W3 backfill. Same rule as the other two books; the counter-proof here matters
// more because this column is the one thing even broker snapshots may not touch.
package store

import "testing"

// TestBackfillRealPositionAnchorsLegacy 只补"无锚点且有成本"的行；已有锚点、无成本的行一律不动；
// 第二次调用回填 0（幂等）；仍无锚点的剩余行数如实报出（上层据此记日志）。
// English: fills only rows lacking an anchor but having a cost; leaves anchored/priceless rows alone;
// idempotent; reports how many rows still have no anchor.
func TestBackfillRealPositionAnchorsLegacy(t *testing.T) {
	db := testDB(t)
	if err := db.ensureAnchorPriceColumn(); err != nil {
		t.Fatalf("补列: %v", err)
	}
	// 三行存量形态（直插原始 SQL，绕开 ApplyRealFill 的建仓写锚逻辑）：
	// ① 无锚点有成本 → 应回填 12；② 无锚点无成本 → 跳过；③ 已有锚点 15.05 但成本 20 → 绝不动。
	rows := []struct {
		code, name string
		qty        int
		cost       float64
		anchor     any
	}{
		{"600000.SH", "浦发", 1000, 12, nil},
		{"600001.SH", "无价股", 100, 0, nil},
		{"600002.SH", "已锚定", 200, 20, 15.05},
	}
	for _, r := range rows {
		if _, err := db.db.Exec(`INSERT INTO real_positions
			(ts_code, name, qty, cost_price, amount, updated_at, user_id, anchor_price)
			VALUES (?, ?, ?, ?, ?, '2026-10-20 09:31:00', 'u_bf', ?)`,
			r.code, r.name, r.qty, r.cost, r.cost*float64(r.qty), r.anchor); err != nil {
			t.Fatalf("种存量行 %s: %v", r.code, err)
		}
	}

	filled, remaining, err := db.BackfillRealPositionAnchors()
	if err != nil {
		t.Fatalf("回填: %v", err)
	}
	if filled != 1 {
		t.Fatalf("应回填 1 行（只有 600000 同时满足无锚点+有成本）, got %d", filled)
	}
	if remaining != 1 {
		t.Fatalf("仍无锚点应为 1 行（600001 无成本价；600002 已有锚点不算）, got %d", remaining)
	}

	p1, err := db.RealPositionByCodeForUser("u_bf", "600000.SH")
	if err != nil {
		t.Fatalf("读回 600000: %v", err)
	}
	if p1.AnchorPrice != 12 {
		t.Fatalf("存量行锚点应回填为成本 12, got %v", p1.AnchorPrice)
	}
	p2, err := db.RealPositionByCodeForUser("u_bf", "600001.SH")
	if err != nil {
		t.Fatalf("读回 600001: %v", err)
	}
	if p2.AnchorPrice != 0 {
		t.Fatalf("无成本价的行不得被写入锚点, got %v", p2.AnchorPrice)
	}
	p3, err := db.RealPositionByCodeForUser("u_bf", "600002.SH")
	if err != nil {
		t.Fatalf("读回 600002: %v", err)
	}
	if p3.AnchorPrice != 15.05 {
		t.Fatalf("已有锚点绝不能被回填覆盖（会抹掉真首买价）, got %v", p3.AnchorPrice)
	}

	// 幂等：第二次不再回填。
	if filled2, _, err2 := db.BackfillRealPositionAnchors(); err2 != nil || filled2 != 0 {
		t.Fatalf("第二次回填应为 0（幂等）, got %d err=%v", filled2, err2)
	}
}
