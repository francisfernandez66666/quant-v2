// sell_fill_decouple_test.go — §SELLFILL-DECOUPLE（2026-10-06 修复批 波 2 / P1-B）Go 读取侧回归。
//
// 缺陷本体在 Python 网关（store.apply_fill 的卖出分支在查不到持仓行时提前 return，
// 那笔卖出连 fills 流水都不落）。Go 侧 ApplyRealFill 一直是"缺仓卖出＝只记流水、不建仓"
// 的正确姿势，所以本文件的作用是**把两本账钉成同一本**，并在读取侧证明：
//  1. 无底仓卖出的流水确实进了 fills，且进了账目口径的唯一收敛点 fills_effective
//     （ListFillsByDay 是 /settlement 三方对账本地腿与 TodayRealizedPnl 的共同输入 ——
//     这条缺行如果存在，对账会把系统性缺行当成"每次都有一堆差"的噪声，真差异被淹没）；
//  2. 该笔卖出计入 SumSellFilledAmountByDay（当日回款）——少算回款会让预算占满后无法释放，
//     与 09-22「卖出记成买入 → 回款 0 → 当日预算占满」错账同族后果；
//  3. 成本不可知态 fail-open **不计入** TodayRealizedPnl（该笔被跳过而非按 0 成本算出
//     -成交额 的巨额假亏损）；这条口径本批一字未动，本腿只是防止有人"顺手"把它改成
//     按 0 成本计——那会把缺持仓的卖出直接推成熔断信号；
//  4. 同 trade_id 重放在无底仓卖出路径上仍幂等（解耦不许把"漏记"修成"重复记"）。
//
// 有意不在此重建整条三方对账机：settlement 装配与差异判读有专测（§FILL-AMEND / P2-E 波 5），
// 本文件只保证「这一行在读取面上可见、且两把尺子各按口径读到它」。
// English: storage-side regression pinning the Go ledger parity — an orphan sell is journaled and
// visible through the single effective-view convergence point, counts toward sell proceeds, stays out
// of realized P&L when the cost basis is unanswerable (fail-open, not a fake zero-cost loss), and
// remains idempotent by trade_id.
package store

import (
	"path/filepath"
	"testing"
)

// newDecoupleDB 建本文件专用的临时实盘账本（不与 risk_gates_test.go 的夹具共用，
// 免得那边的 helper 一改就把这边的用例拖成编译失败）。
func newDecoupleDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "decouple.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// orphanSell 构造一笔"柜台说卖了、本地查无底仓"的卖出成交（P1-B 事故形态）。
func orphanSell(tradeID string) RealFill {
	return RealFill{
		OrderID:  "O-ORPHAN",
		TradeID:  tradeID,
		Serial:   "S-ORPHAN",
		Code:     "600580.SH",
		Name:     "卧龙电驱",
		Side:     "卖出",
		Price:    20.0,
		Qty:      900,
		Amount:   18000.0,
		TradedAt: "2026-10-05 14:30:00",
		SignalID: "sell:600580:stop_loss:20261005",
		UserID:   "u_dec",
		Fee:      5.0,
		StampTax: 9.0,
	}
}

// TestOrphanSellJournaledAndVisible E1 的 Go 侧对照：无底仓卖出流水照落，且账目口径看得到。
func TestOrphanSellJournaledAndVisible(t *testing.T) {
	db := newDecoupleDB(t)

	if err := db.ApplyRealFill(orphanSell("T-ORPHAN")); err != nil {
		t.Fatalf("ApplyRealFill(无底仓卖出): %v", err)
	}

	// 持仓账：绝不建幽灵行（qty<=0 的行会被持仓页/卖出逻辑当真仓读走）
	pos, err := db.RealPositionsForUser("u_dec")
	if err != nil {
		t.Fatalf("RealPositionsForUser: %v", err)
	}
	if len(pos) != 0 {
		t.Fatalf("无底仓卖出不得创建持仓行，实得 %+v", pos)
	}

	// 流水账：ListFillsByDay 读的是 fills_effective（唯一收敛点），
	// 这一行必须在 ⇒ /settlement 本地腿与熔断闸看得到同一笔事实。
	fills, err := db.ListFillsByDay("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("ListFillsByDay: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("§SELLFILL-DECOUPLE 无底仓卖出必须恰好落 1 行流水，实得 %d 行", len(fills))
	}
	got := fills[0]
	if got.Side != "卖出" || got.Code != "600580.SH" || got.Qty != 900 || got.Amount != 18000.0 {
		t.Fatalf("流水字段被降级（方向/量额必须照回报）：%+v", got)
	}
	// 可复核与费用腿：/settlement 的费用差对账依赖这两列，缺行时它们也一起没了
	if got.TradeID != "T-ORPHAN" || got.Serial != "S-ORPHAN" {
		t.Fatalf("身份锚不得丢（判重与对账关联键）：%+v", got)
	}
	if got.Fee != 5.0 || got.StampTax != 9.0 {
		t.Fatalf("费用腿不得被抹成 0：%+v", got)
	}
}

// TestOrphanSellCountsProceedsButNotPnl E2/G2：回款计入、盈亏按成本不可知 fail-open 不计入。
func TestOrphanSellCountsProceedsButNotPnl(t *testing.T) {
	db := newDecoupleDB(t)
	if err := db.ApplyRealFill(orphanSell("T-ORPHAN")); err != nil {
		t.Fatalf("ApplyRealFill: %v", err)
	}

	// 回款腿：少了这一笔＝当日预算占满后无法释放（与 09-22 错账同族后果）
	proceeds, err := db.SumSellFilledAmountByDay("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("SumSellFilledAmountByDay: %v", err)
	}
	if proceeds != 18000.0 {
		t.Fatalf("无底仓卖出的回款必须计入（系统性缺行会把真差异淹成对账噪声）：实得 %v 应为 18000", proceeds)
	}

	// 盈亏腿：无持仓且当日无买入 ⇒ costBasisFor 回 0 ⇒ 该笔 fail-open 跳过。
	// 若有人改成"按 0 成本计"，这里会变成 -18000（成交额全算亏损）→ 熔断闸被数据缺口误触发。
	pnl, err := db.TodayRealizedPnl("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("TodayRealizedPnl: %v", err)
	}
	if pnl != 0 {
		t.Fatalf("成本不可知态必须 fail-open 不计入（不许把缺持仓翻译成假亏损）：实得 %v", pnl)
	}

	// 成对反证：同一条腿在成本可知时**必须**计盈亏——否则上面那条 0 就是读挂了蒙对的。
	// 刻意用**部分卖出**（卖 300/900）而不是清仓：清仓会删持仓行，costBasisFor 便落到
	// "今日买入均价"兜底分支（该分支只按 价×量 摊、不含买入费），断言就把不到 §F1
	// "佣金摊入持仓成本" 这条真实口径上了。部分卖出保住持仓行 ⇒ 成本=含费成本。
	// English: a partial sell keeps the position row so the cost basis is the fee-loaded
	// position cost (§F1); a full liquidation would fall back to the fee-less average-buy path.
	db2 := newDecoupleDB(t)
	buy := RealFill{OrderID: "O-B", TradeID: "T-B", Code: "600580.SH", Name: "卧龙电驱",
		Side: "买入", Price: 18.0, Qty: 900, Amount: 16200.0,
		TradedAt: "2026-10-05 09:30:00", UserID: "u_dec", Fee: 4.0}
	if err := db2.ApplyRealFill(buy); err != nil {
		t.Fatalf("ApplyRealFill(买入建仓): %v", err)
	}
	sell := orphanSell("T-COSTED")
	sell.OrderID = "O-COSTED"
	sell.Qty = 300
	sell.Amount = 6000.0
	if err := db2.ApplyRealFill(sell); err != nil {
		t.Fatalf("ApplyRealFill(有底仓卖出): %v", err)
	}
	pnl2, err := db2.TodayRealizedPnl("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("TodayRealizedPnl(成本可知): %v", err)
	}
	// 成本口径：建仓含费摊入 (16200+4)/900 = 18.004444…；
	// 卖出腿扣 fee+stamp_tax（§0927AUDIT-D1）⇒ (20-cost)*300 - 5 - 9。
	want := (20.0-(16200.0+4.0)/900.0)*300.0 - 5.0 - 9.0
	if pnl2 < want-0.01 || pnl2 > want+0.01 {
		t.Fatalf("成本可知时该笔必须计入盈亏（证明上一条 0 不是读挂）：实得 %v 应为 %.4f", pnl2, want)
	}
}

// TestOrphanSellReplayStaysOneRow E3 的 Go 侧对照：解耦后 trade_id 幂等锚照常生效。
func TestOrphanSellReplayStaysOneRow(t *testing.T) {
	db := newDecoupleDB(t)
	if err := db.ApplyRealFill(orphanSell("T-REPLAY")); err != nil {
		t.Fatalf("首次入账: %v", err)
	}
	// 网关 outbox 对同一笔回报重试：命中 trade_id 判重 ⇒ 事务回滚、幂等成功返回 nil。
	if err := db.ApplyRealFill(orphanSell("T-REPLAY")); err != nil {
		t.Fatalf("重放应幂等成功（返回 nil 而非报错）：%v", err)
	}
	fills, err := db.ListFillsByDay("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("ListFillsByDay: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("同 trade_id 重放不得追加第二行（把漏记修成重复记同样是错账）：实得 %d 行", len(fills))
	}
	// 回款读数同样不得被重放放大（放大会让当日预算被虚增的回款释放出去）
	proceeds, err := db.SumSellFilledAmountByDay("u_dec", "2026-10-05")
	if err != nil {
		t.Fatalf("SumSellFilledAmountByDay: %v", err)
	}
	if proceeds != 18000.0 {
		t.Fatalf("重放后的当日回款必须仍是一笔：实得 %v", proceeds)
	}

	// 两笔不同 trade_id 的真实部成必须各计一次（§M4 语义在无底仓分支同样成立）。
	second := orphanSell("T-REPLAY-2")
	second.Qty = 100
	second.Amount = 2000.0
	if err := db.ApplyRealFill(second); err != nil {
		t.Fatalf("第二笔部成入账: %v", err)
	}
	fills, _ = db.ListFillsByDay("u_dec", "2026-10-05")
	if len(fills) != 2 {
		t.Fatalf("不同成交编号的两笔部成不得被并成一行：实得 %d 行", len(fills))
	}
	if proceeds, err = db.SumSellFilledAmountByDay("u_dec", "2026-10-05"); err != nil || proceeds != 20000.0 {
		t.Fatalf("两笔部成回款应累加（got=%v err=%v，应为 20000）", proceeds, err)
	}
}
