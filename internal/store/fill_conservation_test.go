// fill_conservation_test.go — §FILL-AMEND（2026-09-23）守恒自检的回归。
//
// 本文件的重点不是"自检通过"，而是**已知会破不变量的夹具必须被报出来**：
// 只断言 ok=true 的守卫用例是假绿（本仓 §ADJ-BASIS / §LIVEANCHOR 两批都有"守卫从不触发却
// 全绿"的实录教训）。故两条不变量各配一个反证夹具：
//
//	① 持仓不变量：一笔成交被勘误改判方向后，成交簿重放的净持仓与持仓账必然差 900 股
//	   （勘误按设计不回改历史写账）→ 自检必须输出**逐笔**线索（代码/重放量/账本量/差额/成因）；
//	② 现金不变量：期初 − 买入 − 佣金 + 卖出 − 印花税 与券商可用资金对不上时必须列出各腿与差额，
//	   并在缺基准（期初未配置/现金未回报）时如实标"未检查"而不是"通过"。
//
// English: regression for the read-only conservation check — deliberately broken fixtures must be
// reported with per-code lines and cash legs (a guard that never fires is worse than no guard), and
// missing baselines must surface as "not checked" rather than "passed".
package store

import (
	"path/filepath"
	"testing"
)

func newConservationDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "conservation.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestConservationReportsPositionBreak 持仓不变量反证：勘误生效后自检必须报出那 900 股的差额。
func TestConservationReportsPositionBreak(t *testing.T) {
	db := newConservationDB(t)
	f := RealFill{OrderID: "OC1", Code: "603468.SH", Name: "风语筑", Side: "买入",
		Price: 22.55, Qty: 900, Amount: 20295, TradedAt: "2026-09-22 10:08:00",
		SignalID: "sell:603468.SH:止盈:2026-09-22:r900", TradeID: "TC1", UserID: "u_cons"}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 未勘误前：成交簿重放（买入 900）与持仓账（ApplyRealFill 建的 900）恰好自洽——
	// 这正是本次事故最阴的地方：错账在**它自己的口径内**是平的，只有对照柜台事实才露出来。
	base, err := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !base.PositionsOK || len(base.PositionLines) != 0 {
		t.Fatalf("夹具基线应自洽（重放=账本），报出的差异=%+v", base.PositionLines)
	}

	var id int64
	if err := db.db.QueryRow(`SELECT id FROM fills WHERE trade_id='TC1'`).Scan(&id); err != nil {
		t.Fatalf("lookup id: %v", err)
	}
	am, err := db.CreateFillAmendment(id, "卖出", "柜台回单：09-22 10:08 实为卖出", "boss")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// pending 影子态：账目与自检结论都不该动
	shadow, err := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if err != nil {
		t.Fatalf("check shadow: %v", err)
	}
	if !shadow.PositionsOK || shadow.AppliedAmendments != 0 {
		t.Fatalf("pending 勘误不得影响自检结论: %+v", shadow)
	}

	if _, err := db.ApplyFillAmendment(am.ID, "boss"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if err != nil {
		t.Fatalf("check after apply: %v", err)
	}
	if rep.PositionsOK {
		t.Fatal("勘误后成交簿重放 0 股 vs 持仓账 900 股，自检必须判失败")
	}
	if rep.OK {
		t.Fatal("任一条不变量不成立时总判定必须为 false")
	}
	if len(rep.PositionLines) != 1 {
		t.Fatalf("应输出 1 条逐笔差异线索, got %d: %+v", len(rep.PositionLines), rep.PositionLines)
	}
	line := rep.PositionLines[0]
	if line.Code != "603468.SH" || line.ReplayedQty != 0 || line.BookQty != 900 || line.Diff != 900 {
		t.Fatalf("差异线索内容不符: %+v", line)
	}
	if line.Note == "" {
		t.Fatal("差异线索必须带成因说明（只报布尔值等于没做）")
	}
	if rep.AppliedAmendments != 1 {
		t.Fatalf("报告应自带生效勘误数, got %d", rep.AppliedAmendments)
	}

	// 撤销后回到自洽
	if _, err := db.RevokeFillAmendment(am.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	back, _ := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if !back.PositionsOK || len(back.PositionLines) != 0 {
		t.Fatalf("撤销后应回到自洽: %+v", back.PositionLines)
	}
}

// TestConservationReportsPositionBreak2 反向夹具：账上有仓、成交簿完全无凭（券商对账快照建的行）
// → 必须报"成交簿无凭"这一类线索，而不是把差异折叠成 replayed=0 的普通行。
func TestConservationReportsPositionBreak2(t *testing.T) {
	db := newConservationDB(t)
	if _, err := db.ReconcilePositionsForUser("u_cons", []RealPosition{
		{TsCode: "601156.SH", Name: "东宏股份", Qty: 300, CostPrice: 8.8, Amount: 2640},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rep, err := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rep.PositionsOK || len(rep.PositionLines) != 1 {
		t.Fatalf("对账来源持仓必须被报出: ok=%v lines=%+v", rep.PositionsOK, rep.PositionLines)
	}
	line := rep.PositionLines[0]
	if line.ReplayedQty != 0 || line.BookQty != 300 || line.Diff != 300 {
		t.Fatalf("线索数值不符: %+v", line)
	}
}

// TestConservationCashLegs 现金不变量：各腿与差额的数值正确性 + 缺基准时如实"未检查"。
func TestConservationCashLegs(t *testing.T) {
	db := newConservationDB(t)
	// 买入 1000 股 @10 = 10000，佣金 5；卖出 500 股 @12 = 6000，佣金 3 + 印花税 6
	if err := db.ApplyRealFill(RealFill{OrderID: "CB1", Code: "600010.SH", Side: "买入", Name: "包钢股份",
		Price: 10, Qty: 1000, Amount: 10000, TradedAt: "2026-09-22 09:35:00", TradeID: "CB1",
		UserID: "u_cons", Fee: 5}); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "CS1", Code: "600010.SH", Side: "卖出", Name: "包钢股份",
		Price: 12, Qty: 500, Amount: 6000, TradedAt: "2026-09-22 14:30:00", TradeID: "CS1",
		UserID: "u_cons", Fee: 3, StampTax: 6}); err != nil {
		t.Fatalf("seed sell: %v", err)
	}
	const initial = 100000.0
	// 期望现金 = 100000 − 10000 −(5+3) + 6000 − 6 = 95986
	if err := db.UpsertRealAccount(RealAccount{UserID: "u_cons", AvailableCash: 95000}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	rep, err := db.CheckBookConservation("u_cons", "2026-09-22", initial)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	c := rep.Cash
	if !c.Checked {
		t.Fatalf("两条腿齐备时必须检查, reason=%s", c.SkipReason)
	}
	if c.BuyAmount != 10000 || c.SellAmount != 6000 || c.FeeTotal != 8 || c.StampTaxTotal != 6 {
		t.Fatalf("现金腿数值不符: %+v", c)
	}
	if d := c.ExpectedCash - 95986.0; d > 0.001 || d < -0.001 {
		t.Fatalf("期望现金应为 95986, got %.4f", c.ExpectedCash)
	}
	if d := c.Diff - (95000 - 95986.0); d > 0.001 || d < -0.001 {
		t.Fatalf("现金差额应列出（账 − 期望）= -986, got %.4f", c.Diff)
	}
	if d := c.NetSpend - (10000 - (6000 - 14)); d > 0.001 || d < -0.001 {
		t.Fatalf("净占用现金口径 买入 − (卖出 − 费用腿) 不符: %+v", c)
	}
	if rep.OK {
		t.Fatal("现金差 -986 未归零时总判定必须为 false（自检只报数，不做\"看起来差不多\"的宽容判定）")
	}

	// 券商现金与期望一致时：现金条通过，但差异行仍要如实保留（不因"平了"就隐藏持仓差异）
	if err := db.UpsertRealAccount(RealAccount{UserID: "u_cons", AvailableCash: 95986}); err != nil {
		t.Fatalf("update account: %v", err)
	}
	rep2, err := db.CheckBookConservation("u_cons", "2026-09-22", initial)
	if err != nil {
		t.Fatalf("check2: %v", err)
	}
	if !rep2.Cash.Checked || rep2.Cash.Diff > 0.001 || rep2.Cash.Diff < -0.001 {
		t.Fatalf("现金账对齐后差额应归零: %+v", rep2.Cash)
	}

	// 缺基准：期初资金未配置 → 现金条必须标"未检查"，不得伪装成通过
	rep3, err := db.CheckBookConservation("u_cons", "2026-09-22", 0)
	if err != nil {
		t.Fatalf("check3: %v", err)
	}
	if rep3.Cash.Checked || rep3.Cash.SkipReason == "" {
		t.Fatalf("期初资金缺失时必须标未检查并给原因: %+v", rep3.Cash)
	}
}

// TestConservationIsReadOnly 自检全程只读：调用前后 fills / real_positions / real_account 的
// 行数与关键数值必须一字不差（"绝不自行动账"是本项边界，机械化才守得住）。
func TestConservationIsReadOnly(t *testing.T) {
	db := newConservationDB(t)
	// real_account 是 ensureRealAccountTable 惰性建表（网关首次上报才建），本用例直查该表计数，
	// 先落一行账户资产把表建起来（同时也让现金不变量真的走到"检查"分支）。
	if err := db.UpsertRealAccount(RealAccount{UserID: "u_cons", AvailableCash: 47000}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "OR1", Code: "600011.SH", Side: "买入",
		Price: 6, Qty: 500, Amount: 3000, TradedAt: "2026-09-22 10:00:00", TradeID: "OR1", UserID: "u_cons"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	type bookSnap struct {
		fills, positions, accounts int
		queuedQty                  float64
	}
	snap := func() bookSnap {
		var nf, npos, nacc int
		var amt float64
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM fills`).Scan(&nf); err != nil {
			t.Fatalf("count fills: %v", err)
		}
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM real_positions`).Scan(&npos); err != nil {
			t.Fatalf("count positions: %v", err)
		}
		if err := db.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM real_positions`).Scan(&amt); err != nil {
			t.Fatalf("sum qty: %v", err)
		}
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM real_account`).Scan(&nacc); err != nil {
			t.Fatalf("count accounts: %v", err)
		}
		return bookSnap{fills: nf, positions: npos, accounts: nacc, queuedQty: amt}
	}
	before := snap()
	if _, err := db.CheckBookConservation("u_cons", "2026-09-22", 50000); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, err := db.ListFillAmendments("", 50); err != nil {
		t.Fatalf("list: %v", err)
	}
	after := snap()
	if before != after {
		t.Fatalf("自检改动了账本数据！before=%v after=%v", before, after)
	}
}
