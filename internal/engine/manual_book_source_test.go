// manual_book_source_test.go — §MANUAL-BOOK-UNIFY（2026-11-06 晚批）账户维度 resolver 的行为锁。
//
// owner 现场：券商禁令生效、云上 QMT 登录被禁，桥可能回传不了；owner 手动在持仓 tab
// 维护实盘结果，量化交易必须读它来算动态仓位与推送买卖。
//
// 本批把上午那批的"手动账只做计算输入、绝不进必须新鲜硬闸"**改判**成"喂买卖硬闸"
// （owner 2026-11-06 裁决 1），所以这里锁的四件事也跟着换向：
//
//	① 桥不新鲜且维护了**当日**手动账 ⇒ 计算输入真的换成那几行；
//	② 没维护手动账、或 asof 停在昨天、或已被柜台接管 ⇒ 保持 fail-closed，
//	   绝不把空账当成"零仓位/零资金"放行（这是裁决 1 的两条前置，不是放松）；
//	③ 手动数据只活在内存里：不写回 real_*，不伪造 UpdatedAt / CanUseQty。
//	   伪造它们等于用"刚回报过"的形态去解开新鲜度闸——本批要的是**显式选源**，不是假时间戳；
//	④ 两维独立：柜台可能资金还在报而持仓快照停在昨天（或反过来），不许折成一个全局开关。
//
// asof 一律从 cntime.Now() 派生：写死日历日期＝隔天必红（本仓踩过两次的那条坑）。
package engine

import (
	"testing"

	"quant-trading-v2/internal/cntime"
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

// manualBookAsof 当日 asof（手动账可用性的前置判据之一，必须随日历走）。
func manualBookAsof() string { return cntime.DayOf(cntime.Now()) + " 10:00:00" }

// manualBookAsofYesterday 昨天 asof。
func manualBookAsofYesterday() string {
	return cntime.DayOf(cntime.Now().AddDate(0, 0, -1)) + " 10:00:00"
}

func putManualBook(t *testing.T, db *store.DB) {
	t.Helper()
	asof := manualBookAsof()
	if err := db.PutManualAccount("u_1", 50_000, true, 200_000, asof); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	rows := []store.ManualPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 9.8},
		{TsCode: "000001.SZ", Name: "平安银行", Qty: 500, CostPrice: 11.2},
	}
	if _, _, err := db.PutManualPositions("u_1", rows, asof); err != nil {
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
	if book.CashSource != accountSourceManual || book.PositionsSource != accountSourceManual {
		t.Fatalf("两维都应切手动，got cash=%q pos=%q", book.CashSource, book.PositionsSource)
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
	if book.AsOf != manualBookAsof() {
		t.Fatalf("读数面必须带 asof，got %q 期望 %q", book.AsOf, manualBookAsof())
	}
}

// liveBridgeEngine 控制器在场、账本已装配（柜台"还在报数"的形态）。
// 与 bridgeDownEngine 的唯一区别是**不摘 ctrl**：两维独立性必须在 ctrl 在场时才测得出来——
// 摘掉 ctrl 等于宣告"这台引擎没有实盘链路"，两维会一起切手动（那是另一格，已有用例锁）。
func liveBridgeEngine(t *testing.T) (*Engine, *store.DB) {
	t.Helper()
	e, db, _, _ := newQMTEngine(t, nil)
	e.mu.Lock()
	e.realStore = db
	e.mu.Unlock()
	return e, db
}

// TestManualBookDimensionsIndependent 两维独立：柜台资金新鲜、柜台持仓快照停在昨天 ⇒
// 资金维吃柜台、持仓维吃手动。折成一个开关会把"哪个数是谁给的"重新糊掉。
func TestManualBookDimensionsIndependent(t *testing.T) {
	e, db := liveBridgeEngine(t)
	// 把柜台资金行刷成"刚刚回报"（夹具种的就是当前时刻，这里显式再写一次以表达意图）。
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_1", AvailableCash: 7_777,
		UpdatedAt: cntime.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("刷新柜台资金: %v", err)
	}
	// 柜台持仓行故意种成昨天（桥断当天冻结的样子）。
	yesterday := cntime.DayOf(cntime.Now().AddDate(0, 0, -1)) + " 14:55:00"
	if _, err := db.ReconcilePositionsForUser("u_1", []store.RealPosition{
		{TsCode: "600000.SH", Qty: 4000, CostPrice: 8.0, UpdatedAt: yesterday},
	}); err != nil {
		t.Fatalf("种昨天持仓: %v", err)
	}
	putManualBook(t, db)
	book := e.accountBookFor("u_1")
	if book.CashSource != accountSourceCounter {
		t.Fatalf("柜台资金新鲜时资金维必须吃柜台，got %q", book.CashSource)
	}
	if book.Account.AvailableCash != 7_777 {
		t.Fatalf("资金维数值应为柜台 7777，got %.0f", book.Account.AvailableCash)
	}
	if book.PositionsSource != accountSourceManual {
		t.Fatalf("柜台持仓快照不属于今日，持仓维必须吃手动，got %q", book.PositionsSource)
	}
	if len(book.Positions) != 2 {
		t.Fatalf("持仓维应换成手动 2 行，got %+v", book.Positions)
	}
	// 合成展示值仍是 manual（任一维手动即标注"非柜台实时读数"），但 asof 取持仓维那一路。
	if book.Source != accountSourceManual || book.PositionsAsOf != manualBookAsof() {
		t.Fatalf("展示值与持仓维时刻都要点名，got source=%q posAsOf=%q", book.Source, book.PositionsAsOf)
	}
}

// TestManualBookStaleAsofDoesNotSwitch asof 停在昨天 ⇒ 两维都不许切（裁决 1 的前置判据）。
// 这一条锁的方向是"不许放开"：几天前手敲的那本账在下次桥断时顶上，等于用旧余额放行新买入。
func TestManualBookStaleAsofDoesNotSwitch(t *testing.T) {
	e, db := bridgeDownEngine(t)
	old := manualBookAsofYesterday()
	if err := db.PutManualAccount("u_1", 50_000, true, 200_000, old); err != nil {
		t.Fatalf("写昨日手动资金: %v", err)
	}
	if _, _, err := db.PutManualPositions("u_1", []store.ManualPosition{{TsCode: "600000.SH", Qty: 1000}}, old); err != nil {
		t.Fatalf("写昨日手动持仓: %v", err)
	}
	book := e.accountBookFor("u_1")
	if book.Manual || book.CashSource != accountSourceCounter || book.PositionsSource != accountSourceCounter {
		t.Fatalf("asof 停在昨天的手动账两维都不许参与: manual=%v cash=%q pos=%q",
			book.Manual, book.CashSource, book.PositionsSource)
	}
}

// TestManualBookTakenOverDoesNotResurrect 柜台恢复回报后再次断链 ⇒ 旧手动账不许自动复活（裁决 5）。
func TestManualBookTakenOverDoesNotResurrect(t *testing.T) {
	e, db := bridgeDownEngine(t)
	putManualBook(t, db)
	today := cntime.DayOf(cntime.Now())
	if _, err := db.MarkManualTakenOver("u_1", today+" 15:00:00"); err != nil {
		t.Fatalf("接管打标: %v", err)
	}
	book := e.accountBookFor("u_1")
	if book.Manual {
		t.Fatalf("被柜台接管过的手动账不许复活: %+v", book)
	}
	if book.PositionsSource != accountSourceCounter || book.CashSource != accountSourceCounter {
		t.Fatalf("两维都应回到柜台原值（调用方照旧 fail-closed），got cash=%q pos=%q",
			book.CashSource, book.PositionsSource)
	}
}

// TestManualBookNeverFakesCounterTruth 边界 ③：手动值可以参与判定，但不许冒充柜台回报——
// 不写回 real_*、不伪造 UpdatedAt、不伪造 CanUseQty。
// 改前这条的用途是"证明手动账进不了硬闸"；owner 裁决 1 之后它换成证明
// "进闸走的是显式选源，而不是把时间戳和可卖量伪造出来"——这两件事在屏上看起来一样，坏的时候完全不同。
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
