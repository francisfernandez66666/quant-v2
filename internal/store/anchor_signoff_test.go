package store

// anchor_signoff_test.go —— §ANCHOR-SIGNOFF 签字回填的行为锁。
//
// 盯的不是覆盖率，是「不能越过的边界」：
//   1. plan 正确分类（可开T/不可开T/无价）；
//   2. apply 只补不可开 T 的行，已有白名单档绝不覆盖（只补不拉）；
//   3. 幂等（二次 apply=0）；
//   4. --codes 白名单只动指定票；
//   5. 每笔写入落审计表，旧值可查（可回滚）；
//   6. 缺签字/缺依据整批拒。

import (
	"path/filepath"
	"testing"
)

func newSignoffTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "signoff.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedPositions(t *testing.T, db *DB, rows []RealPosition) {
	t.Helper()
	n, err := db.UpsertRealPositions(rows)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n != len(rows) {
		t.Fatalf("upsert count=%d want=%d", n, len(rows))
	}
}

func TestAnchorSignoffPlanClassifies(t *testing.T) {
	db := newSignoffTestDB(t)
	seedPositions(t, db, []RealPosition{
		{TsCode: "000001.SZ", UserID: "u1", Qty: 100, CostPrice: 10, AnchorSource: "", AnchorPrice: 0},                         // 可回填
		{TsCode: "000002.SZ", UserID: "u1", Qty: 100, CostPrice: 20, AnchorSource: AnchorSourceCostFallback, AnchorPrice: 20},  // 可回填
		{TsCode: "000003.SZ", UserID: "u1", Qty: 100, CostPrice: 30, AnchorSource: AnchorSourceFirstBuyOrder, AnchorPrice: 29}, // 已可开T
		{TsCode: "000004.SZ", UserID: "u1", Qty: 0, CostPrice: 40, AnchorSource: ""},                                           // 清仓，不计
		{TsCode: "000005.SZ", UserID: "u1", Qty: 100, CostPrice: 0, AnchorSource: ""},                                          // 无价，跳过
	})
	p, err := db.AnchorSignoffPlanBackfill()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if p.Total != 4 { // qty>0 的四行
		t.Fatalf("total=%d want=4", p.Total)
	}
	if p.Eligible != 2 {
		t.Fatalf("eligible=%d want=2", p.Eligible)
	}
	if p.AlreadyOK != 1 || p.Skipped != 1 {
		t.Fatalf("already_ok=%d skipped=%d want=1/1", p.AlreadyOK, p.Skipped)
	}
}

func TestAnchorSignoffApplyOnlyTAndIdempotent(t *testing.T) {
	db := newSignoffTestDB(t)
	seedPositions(t, db, []RealPosition{
		{TsCode: "000001.SZ", UserID: "u1", Qty: 100, CostPrice: 10, AnchorSource: "", AnchorPrice: 0},
		{TsCode: "000002.SZ", UserID: "u1", Qty: 100, CostPrice: 20, AnchorSource: AnchorSourceFirstBuyOrder, AnchorPrice: 19.5}, // 已有真锚点
	})
	filled, skipped, err := db.AnchorSignoffApply("owner", "owner 确认无加减仓，成本价即首买价", nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if filled != 1 || skipped != 0 {
		t.Fatalf("filled=%d skipped=%d want=1/0", filled, skipped)
	}
	// 回填行：source=first_buy_order, price=cost
	got, err := db.RealPositions()
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]RealPosition{}
	for _, p := range got {
		byCode[p.TsCode] = p
	}
	if byCode["000001.SZ"].AnchorSource != AnchorSourceFirstBuyOrder || byCode["000001.SZ"].AnchorPrice != 10 {
		t.Fatalf("backfilled anchor wrong: %+v", byCode["000001.SZ"])
	}
	// 已有白名单档绝不覆盖
	if byCode["000002.SZ"].AnchorPrice != 19.5 || byCode["000002.SZ"].AnchorSource != AnchorSourceFirstBuyOrder {
		t.Fatalf("overwrote an existing provable anchor: %+v", byCode["000002.SZ"])
	}
	// 幂等：二次 apply 不再动
	filled2, _, err := db.AnchorSignoffApply("owner", "same basis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if filled2 != 0 {
		t.Fatalf("second apply filled=%d want=0 (idempotent)", filled2)
	}
}

// TestAnchorSignoffCodesWhitelist 验证逐票白名单只回填指定代码：
// 签字回填的 --codes 是防整批误伤的硬闸，未列入的持仓一行都不许动。
func TestAnchorSignoffCodesWhitelist(t *testing.T) {
	db := newSignoffTestDB(t)
	seedPositions(t, db, []RealPosition{
		{TsCode: "000001.SZ", UserID: "u1", Qty: 100, CostPrice: 10},
		{TsCode: "000002.SZ", UserID: "u1", Qty: 100, CostPrice: 20},
	})
	filled, skipped, err := db.AnchorSignoffApply("owner", "basis", []string{"000001.SZ"})
	if err != nil {
		t.Fatal(err)
	}
	if filled != 1 || skipped != 1 {
		t.Fatalf("filled=%d skipped=%d want=1/1", filled, skipped)
	}
	got, _ := db.RealPositions()
	for _, p := range got {
		if p.TsCode == "000002.SZ" && (p.AnchorSource != "" || p.AnchorPrice != 0) {
			t.Fatalf("code not in whitelist was touched: %+v", p)
		}
	}
}

func TestAnchorSignoffAuditTrail(t *testing.T) {
	db := newSignoffTestDB(t)
	seedPositions(t, db, []RealPosition{
		{TsCode: "000001.SZ", UserID: "u1", Qty: 100, CostPrice: 10, AnchorSource: AnchorSourceCostFallback, AnchorPrice: 10},
	})
	if _, _, err := db.AnchorSignoffApply("owner-zhang", "依据原文ABC", nil); err != nil {
		t.Fatal(err)
	}
	logs, err := db.AnchorSignoffLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("audit rows=%d want=1", len(logs))
	}
	row := logs[0]
	if row["new_anchor_source"] != AnchorSourceFirstBuyOrder ||
		row["old_anchor_source"] != AnchorSourceCostFallback ||
		row["signer"] != "owner-zhang" || row["basis"] != "依据原文ABC" {
		t.Fatalf("audit row wrong: %+v", row)
	}
}

func TestAnchorSignoffRejectsMissingSignerOrBasis(t *testing.T) {
	db := newSignoffTestDB(t)
	seedPositions(t, db, []RealPosition{
		{TsCode: "000001.SZ", UserID: "u1", Qty: 100, CostPrice: 10},
	})
	if _, _, err := db.AnchorSignoffApply("", "basis", nil); err == nil {
		t.Fatal("empty signer must be rejected")
	}
	if _, _, err := db.AnchorSignoffApply("owner", "", nil); err == nil {
		t.Fatal("empty basis must be rejected")
	}
	// 拒绝后库里没有审计行、没有锚点
	logs, _ := db.AnchorSignoffLog()
	if len(logs) != 0 {
		t.Fatalf("rejected apply left %d audit rows", len(logs))
	}
}
