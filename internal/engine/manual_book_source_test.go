// manual_book_source_test.go — §MANUAL-BOOK-FALLBACK（2026-11-06）账户维度兜底的行为锁。
//
// owner 现场：券商禁令生效、云上 QMT 登录被禁，桥可能回传不了；owner 手动在持仓 tab
// 维护实盘结果，量化交易必须读它来算动态仓位与推送买卖。本文件锁的不是"接口能存"，
// 而是三件会往钱的方向错的事：
//
//	① 桥不新鲜且维护了手动账 ⇒ 计算输入真的换成那几行；
//	② 没维护手动账 ⇒ 保持今天 fail-closed，绝不把空账当成"零仓位"放行；
//	③ 手动数据只活在内存里：不写回 real_*，不伪造 UpdatedAt / CanUseQty，
//	   也就不会用"刚回报过"的形态解开 §M12-A 新鲜度硬闸与卖出 T+1 闸。
package engine

import (
	"testing"

	"quant-trading-v2/internal/store"
)

// bridgeDownEngine 把夹具的控制器摘掉，模拟「QMT 登录被禁、桥不再回报」。
// 柜台表里仍留着上一次回报的资金/持仓，正是断链当天的真实形态。
func bridgeDownEngine(t *testing.T) (*Engine, *store.DB) {
	t.Helper()
	e, db, _, _ := newQMTEngine(t, nil)
	e.mu.Lock()
	e.realStore = db
	e.qmtCtrl = nil
	e.mu.Unlock()
	return e, db
}

func putManualBook(t *testing.T, db *store.DB) {
	t.Helper()
	if err := db.PutManualAccount("u_1", 50_000, true, 200_000, "2026-11-06 10:00:00"); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	rows := []store.ManualPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 9.8},
		{TsCode: "000001.SZ", Name: "平安银行", Qty: 500, CostPrice: 11.2},
	}
	if _, _, err := db.PutManualPositions("u_1", rows, "2026-11-06 10:00:00"); err != nil {
		t.Fatalf("写手动持仓: %v", err)
	}
}

// TestManualBookFallbackBecomesTheComputationInput 桥断 + owner 已维护 ⇒ 真的切过去。
func TestManualBookFallbackBecomesTheComputationInput(t *testing.T) {
	e, db := bridgeDownEngine(t)
	putManualBook(t, db)
	book := e.accountBookFor("u_1")
	if !book.Manual || book.Source != accountSourceManual {
		t.Fatalf("桥断了却没切手动实盘: manual=%v source=%s", book.Manual, book.Source)
	}
	if book.Account.AvailableCash != 50_000 || book.Account.TotalAsset != 200_000 {
		t.Fatalf("动态仓位基数没吃到手动资金: %+v", book.Account)
	}
	// 不依赖返回顺序：store 按 ts_code 排序，按代码核才有意义。
	gotQty := map[string]int{}
	for _, p := range book.Positions {
		gotQty[p.TsCode] = p.Qty
	}
	if len(gotQty) != 2 || gotQty["600000.SH"] != 1000 || gotQty["000001.SZ"] != 500 {
		t.Fatalf("买卖推送的持仓没换成手动实盘行: %+v", book.Positions)
	}
	if book.AsOf != "2026-11-06 10:00:00" {
		t.Fatalf("读数面必须带 asof，got %q", book.AsOf)
	}
}

// TestManualBookNeverFakesCounterTruth 只做计算输入：两本柜台表和三个关键字段都不许被污染。
func TestManualBookNeverFakesCounterTruth(t *testing.T) {
	e, db := bridgeDownEngine(t)
	putManualBook(t, db)
	book := e.accountBookFor("u_1")
	if book.Account.UpdatedAt != "" {
		t.Fatalf("手动账不许伪造柜台更新时间（会解开新鲜度硬闸）: %q", book.Account.UpdatedAt)
	}
	for _, p := range book.Positions {
		if p.CanUseQty != nil {
			t.Fatalf("手动账不许伪造 T+1 可卖量: %+v", p)
		}
	}
	// real_positions 一行都没多：写回去就等于把用户声称当成券商证据。
	realPoses, err := db.RealPositionsForUser("u_1")
	if err != nil {
		t.Fatalf("读柜台持仓: %v", err)
	}
	if len(realPoses) != 0 {
		t.Fatalf("手动持仓被写回 real_positions 了: %+v", realPoses)
	}
	// real_account 也必须是夹具播种的那一行（1000 万），不是手动的 5 万。
	acc, err := db.GetRealAccountOwn("u_1")
	if err != nil {
		t.Fatalf("读柜台资金: %v", err)
	}
	if acc.AvailableCash != 10_000_000 {
		t.Fatalf("手动资金覆盖了柜台真值: %.0f", acc.AvailableCash)
	}
}

// TestNoMaintenanceStaysFailClosed owner 还没录 ⇒ 不许拿空账当零仓位放行。
func TestNoMaintenanceStaysFailClosed(t *testing.T) {
	e, db := bridgeDownEngine(t)
	book := e.accountBookFor("u_1")
	if book.Manual || book.Source != accountSourceCounter {
		t.Fatalf("没维护手动账时仍切了模式: manual=%v source=%s", book.Manual, book.Source)
	}
	// 读不到这个事实要保留，供调用方继续 fail-closed。
	_ = db
	if book.PositionsErr != nil {
		t.Logf("柜台持仓读数状态: %v", book.PositionsErr)
	}
}
