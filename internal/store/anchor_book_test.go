// §ANCHOR-T1（owner 2026-11-19 裁决）：清仓当天再买回来，首买锚点不漂移；跨日才重新冻结。
//
// 事故形态：`real_positions` 的一行就是"当前持仓"，清仓时整行被删——锚点跟着走。于是同一个
// 交易日内「卖出清仓 → 再买回来」会重新建行、把锚点换成第二次买入的成本，止盈/止损的分母
// 也跟着漂。owner 的口径是"即使清仓了也要 T+1 才清除"。
//
// 这一组锁定四条：
//
//	A1 同日清仓后再买入 ⇒ 锚点、来源仍是当天第一次建仓那一档；
//	A2 次日再买入 ⇒ 重新冻结（T+1 语义，旧锚点不许跨日沿用）；
//	A3 加仓不改写锚点，但会把当时的锚点登记进簿（供当天清仓后回来时沿用）；
//	A4 对账快照建的行（锚点为空）在买入时按簿里当天的锚点回填，簿里没有才用本笔成交冻结。
package store

import (
	"testing"

	"quant-trading-v2/internal/cntime"
)

func openAnchor(t *testing.T, db *DB, orderID, code string, price float64, qty int, fee float64, at string) {
	t.Helper()
	if err := db.ApplyRealFill(RealFill{OrderID: orderID, Code: code, Side: "买入",
		Price: price, Qty: qty, Amount: price * float64(qty), Fee: fee, TradedAt: at, UserID: "u1"}); err != nil {
		t.Fatalf("买入 %s: %v", code, err)
	}
}

func sellAll(t *testing.T, db *DB, orderID, code string, price float64, qty int, at string) {
	t.Helper()
	if err := db.ApplyRealFill(RealFill{OrderID: orderID, Code: code, Side: "卖出",
		Price: price, Qty: qty, Amount: price * float64(qty), TradedAt: at, UserID: "u1"}); err != nil {
		t.Fatalf("卖出 %s: %v", code, err)
	}
}

// TestAnchorBookKeepsSameDayAnchorAcrossRoundTrip A1：当天清仓后再买回来，锚点、来源不漂。
func TestAnchorBookKeepsSameDayAnchorAcrossRoundTrip(t *testing.T) {
	db := testDB(t)
	openAnchor(t, db, "K-1", "600519.SH", 10, 100, 5, "2026-11-19 09:31:00")
	p, err := db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("读回建仓: %v", err)
	}
	if p.AnchorPrice != 10.05 || p.AnchorSource != AnchorSourceFirstBuyOrder {
		t.Fatalf("建仓锚点应为含费 10.05/first_buy_order, got %v/%q", p.AnchorPrice, p.AnchorSource)
	}
	// 当天清仓：行被删除，锚点簿必须留下
	sellAll(t, db, "K-2", "600519.SH", 11, 100, "2026-11-19 10:15:00")
	if _, err := db.RealPositionByCode("600519.SH"); err == nil {
		t.Fatalf("清仓后持仓行应已删除")
	}
	book, err := db.AnchorBookForUser("u1")
	if err != nil {
		t.Fatalf("读锚点簿: %v", err)
	}
	if len(book) != 1 || book[0].AnchorPrice != 10.05 || book[0].AnchorDay != "2026-11-19" {
		t.Fatalf("清仓不得删锚点簿，且日子应为成交日 2026-11-19, got %+v", book)
	}
	// 当天再买回来（价格完全不同）：锚点必须还是第一次建仓那个价
	openAnchor(t, db, "K-3", "600519.SH", 12.6, 100, 5, "2026-11-19 13:20:00")
	p, err = db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("读回重建仓: %v", err)
	}
	if p.AnchorPrice != 10.05 {
		t.Fatalf("§ANCHOR-T1 同日回买不得改写锚点（应仍为 10.05）, got %v", p.AnchorPrice)
	}
	if p.AnchorSource != AnchorSourceFirstBuyOrder {
		t.Fatalf("§ANCHOR-T1 同日回买锚点来源应保持 first_buy_order, got %q", p.AnchorSource)
	}
	// 成本仍是新一笔的含费成本（盈亏口径与风险线口径分开）
	if p.CostPrice <= 12.6 {
		t.Fatalf("成本应按新一笔重算（≥12.6）, got %v", p.CostPrice)
	}
}

// TestAnchorBookKeepsStaleAnchorWhenClearedToday A5（owner 2026-11-19 原话的另一种形态）：
// "即使清仓了也要 T+1 才清除"——一个**前几个交易日建仓**的仓，今天清掉、今天再买回来，
// 沿用的仍是它当初的首买价；只有到明天才允许重新冻结。
// 这条与 A1 的差别在日键的锚点簿语义：清仓那一笔把日子按今天重登记（价不改），
// 所以"跨日持有 → 当日清仓 → 当日回买"不会被判成过期。纸面侧 rememberAnchorLocked 同口径。
func TestAnchorBookKeepsStaleAnchorWhenClearedToday(t *testing.T) {
	db := testDB(t)
	openAnchor(t, db, "C-1", "000001.SZ", 8, 1000, 5, "2026-11-17 09:31:00")
	// 11-18 持有不动，11-19 清仓（跨了两个交易日才卖）
	sellAll(t, db, "C-2", "000001.SZ", 9, 1000, "2026-11-19 09:40:00")
	book, err := db.AnchorBookForUser("u1")
	if err != nil {
		t.Fatalf("读锚点簿: %v", err)
	}
	if len(book) != 1 || book[0].AnchorDay != "2026-11-19" || book[0].AnchorPrice != 8.005 {
		t.Fatalf("清仓应把锚点按今天重登记（价仍为首买 8.005）, got %+v", book)
	}
	// 当天回买：锚点必须还是 11-17 那次建仓的价，不能漂到今天的 9.6
	openAnchor(t, db, "C-3", "000001.SZ", 9.6, 1000, 5, "2026-11-19 10:25:00")
	p, err := db.RealPositionByCode("000001.SZ")
	if err != nil {
		t.Fatalf("读回重建仓: %v", err)
	}
	if p.AnchorPrice != 8.005 {
		t.Fatalf("§ANCHOR-T1 跨日持有后当天清仓再回买，锚点应沿用首买 8.005, got %v", p.AnchorPrice)
	}
	// 边界：清仓后**当天不再买**，到下一个交易日才买 ⇒ 重新冻结（T+1 清除）。
	// 换句话说规则是"持仓谱系在清仓那天的收盘结束"，不是"每过一夜就换一次锚点"——
	// 反复当天清仓+回买属于同一条谱系，锚点与一直持有同形（纸面侧 rememberAnchorLocked 同口径）。
	sellAll(t, db, "C-4", "000001.SZ", 9.9, 1000, "2026-11-20 09:35:00")
	openAnchor(t, db, "C-5", "000001.SZ", 10.2, 1000, 5, "2026-11-21 10:05:00")
	p, err = db.RealPositionByCode("000001.SZ")
	if err != nil {
		t.Fatalf("读回次日重建仓: %v", err)
	}
	if p.AnchorPrice != 10.205 {
		t.Fatalf("§ANCHOR-T1 清仓后隔日再买应重新冻结为本笔含费成本 10.205, got %v", p.AnchorPrice)
	}
}

// TestAnchorBookResetsOnNextTradingDay A2：跨交易日再买回来 ⇒ 重新冻结（T+1 清除）。
func TestAnchorBookResetsOnNextTradingDay(t *testing.T) {
	db := testDB(t)
	openAnchor(t, db, "D-1", "000001.SZ", 10, 100, 0, "2026-11-19 09:31:00")
	sellAll(t, db, "D-2", "000001.SZ", 11, 100, "2026-11-19 10:00:00")
	openAnchor(t, db, "D-3", "000001.SZ", 13, 100, 0, "2026-11-20 09:31:00")
	p, err := db.RealPositionByCode("000001.SZ")
	if err != nil {
		t.Fatalf("读回次日建仓: %v", err)
	}
	if p.AnchorPrice != 13 {
		t.Fatalf("§ANCHOR-T1 次日应重新冻结锚点（13）, got %v", p.AnchorPrice)
	}
	book, err := db.AnchorBookForUser("u1")
	if err != nil {
		t.Fatalf("读锚点簿: %v", err)
	}
	if len(book) != 1 || book[0].AnchorDay != "2026-11-20" || book[0].AnchorPrice != 13 {
		t.Fatalf("锚点簿应只剩次日那一行, got %+v", book)
	}
}

// TestAnchorBookRegistersAddOnAnchorWithoutRewriting A3：加仓不改写锚点，但当天清仓后回来仍用首买价。
func TestAnchorBookRegistersAddOnAnchorWithoutRewriting(t *testing.T) {
	db := testDB(t)
	openAnchor(t, db, "N-1", "300750.SZ", 10, 100, 0, "2026-11-19 09:31:00")
	openAnchor(t, db, "N-2", "300750.SZ", 20, 100, 0, "2026-11-19 10:00:00")
	p, err := db.RealPositionByCode("300750.SZ")
	if err != nil {
		t.Fatalf("读回加仓: %v", err)
	}
	if p.AnchorPrice != 10 {
		t.Fatalf("加仓不得改写锚点, got %v", p.AnchorPrice)
	}
	sellAll(t, db, "N-3", "300750.SZ", 21, 200, "2026-11-19 14:00:00")
	openAnchor(t, db, "N-4", "300750.SZ", 22, 100, 0, "2026-11-19 14:30:00")
	p, err = db.RealPositionByCode("300750.SZ")
	if err != nil {
		t.Fatalf("读回回买: %v", err)
	}
	if p.AnchorPrice != 10 {
		t.Fatalf("同日回买仍应沿用首买价 10, got %v", p.AnchorPrice)
	}
}

// TestAnchorBookBackfillsSnapshotCreatedRow A4：快照建行（锚点空）在买入时按簿里当天锚点回填。
func TestAnchorBookBackfillsSnapshotCreatedRow(t *testing.T) {
	db := testDB(t)
	openAnchor(t, db, "B-1", "601318.SH", 10, 100, 0, "2026-11-19 09:31:00")
	sellAll(t, db, "B-2", "601318.SH", 11, 100, "2026-11-19 10:00:00")
	// 柜台快照先把行建回来（不带锚点），随后成交回报才到。
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{{
		TsCode: "601318.SH", Qty: 100, CostPrice: 12, Amount: 1200, UpdatedAt: "2026-11-19 10:30:00",
	}}, true); err != nil {
		t.Fatalf("对账建行: %v", err)
	}
	openAnchor(t, db, "B-3", "601318.SH", 12, 100, 0, "2026-11-19 10:31:00")
	p, err := db.RealPositionByCode("601318.SH")
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if p.AnchorPrice != 10 {
		t.Fatalf("快照建的空锚点应按簿里当天锚点回填为 10, got %v", p.AnchorPrice)
	}
	if p.AnchorSource != AnchorSourceFirstBuyOrder {
		t.Fatalf("回填来源应为 first_buy_order, got %q", p.AnchorSource)
	}
}

// TestAnchorBookPruneStaysInsideTheAccount：T+1 清理只许收本账号的行。
// 别人的锚点还在给他的持仓定价（止盈/止损的分母），拿"这张表该瘦身"当理由去删它，
// 坏掉的是另一本账的风险线，而且只有在他当天没成交时才看得出来。
func TestAnchorBookPruneStaysInsideTheAccount(t *testing.T) {
	db := testDB(t)
	// u1 昨日冻结 600519，u2 昨日冻结 000001
	openAnchor(t, db, "P-1", "600519.SH", 10, 100, 0, "2026-11-19 09:31:00")
	if err := db.ApplyRealFill(RealFill{OrderID: "P-2", Code: "000001.SZ", Side: "买入",
		Price: 8, Qty: 100, Amount: 800, TradedAt: "2026-11-19 09:32:00", UserID: "u2"}); err != nil {
		t.Fatalf("u2 买入: %v", err)
	}
	// u1 次日（T+1）重新冻结：顺手清理时不许碰到 u2
	openAnchor(t, db, "P-3", "600519.SH", 12, 100, 0, "2026-11-20 09:31:00")
	book2, err := db.AnchorBookForUser("u2")
	if err != nil {
		t.Fatalf("读 u2 锚点簿: %v", err)
	}
	if len(book2) != 1 || book2[0].TsCode != "000001.SZ" || book2[0].AnchorPrice != 8 {
		t.Fatalf("§ANCHOR-T1 清理跨账号删了别人的锚点, got %+v", book2)
	}
}

// TestAnchorBookRefusesTimeOnlyFillStamp：只有时分秒的历史成交串不能当 T+1 日键（退回当天）。
func TestAnchorBookRefusesTimeOnlyFillStamp(t *testing.T) {
	db := testDB(t)
	// 用"今天"这个北京交易日打底，才能断言"退回当天 ⇒ 沿用"这条语义而不依赖跑测试的日期。
	today := cntime.DayOf(cntime.Now())
	openAnchor(t, db, "T-1", "002415.SZ", 10, 100, 0, today+" 09:31:00")
	sellAll(t, db, "T-2", "002415.SZ", 11, 100, today+" 10:00:00")
	// 时间戳只有时分秒：算不出交易日，按"继续沿用"处理，绝不让旧锚点冒充新一天
	openAnchor(t, db, "T-3", "002415.SZ", 15, 100, 0, "13:20:00")
	p, err := db.RealPositionByCode("002415.SZ")
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if p.AnchorPrice != 10 {
		t.Fatalf("无法判定日子时应沿用（10）, got %v", p.AnchorPrice)
	}
}

// TestAnchorBookNeverRepricesAnchorWithoutSource A6：`anchor_price>0 且 anchor_source=”`
// 的历史存量行，价格是**真的风险线**、只是来源不可证。本笔加仓绝不能拿自己的含费成本去
// 顶替它——那正是 owner 报的"锚点漂移"，只不过发生在最容易被"顺手补齐来源"掩盖的地方。
// 清仓后当天回买同样沿用这个价，来源照旧留空（不可证就要显示不可证）。
func TestAnchorBookNeverRepricesAnchorWithoutSource(t *testing.T) {
	db := testDB(t)
	// 柜台快照建行：价 7、来源空（历史存量行的真实形态）
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{{
		TsCode: "600036.SH", Qty: 100, CostPrice: 7, Amount: 700,
		UpdatedAt: "2026-11-19 09:35:00", AnchorPrice: 7, AnchorSource: "",
	}}, true); err != nil {
		t.Fatalf("快照建行: %v", err)
	}
	// 存量形态：`anchor_source` 是后补的列（ALTER TABLE ... DEFAULT ''），迁移前就存在的行
	// 价有、来源空。这里显式把来源抹空来复现那一档，别把快照路径自带的 counter_snapshot 当存量。
	if _, err := db.db.Exec(`UPDATE real_positions SET anchor_source='' WHERE ts_code='600036.SH'`); err != nil {
		t.Fatalf("抹空来源: %v", err)
	}
	// 加仓：锚点不动（价与来源都不动）
	openAnchor(t, db, "N-1", "600036.SH", 9, 100, 5, "2026-11-19 09:40:00")
	p, err := db.RealPositionByCode("600036.SH")
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if p.AnchorPrice != 7 {
		t.Fatalf("§ANCHOR-T1 加仓不得用本笔成本顶替无来源的存量锚点（应仍为 7）, got %v", p.AnchorPrice)
	}
	if p.AnchorSource != "" {
		t.Fatalf("来源不可证时不许凭空补一个 first_buy_order, got %q", p.AnchorSource)
	}
	// 当天清仓 → 当天回买：价仍是 7（谱系未变），来源仍空
	sellAll(t, db, "N-2", "600036.SH", 10, 200, "2026-11-19 10:10:00")
	openAnchor(t, db, "N-3", "600036.SH", 12, 100, 0, "2026-11-19 13:40:00")
	p, err = db.RealPositionByCode("600036.SH")
	if err != nil {
		t.Fatalf("读回重建仓: %v", err)
	}
	if p.AnchorPrice != 7 {
		t.Fatalf("§ANCHOR-T1 无来源存量锚点在同日回买后应沿用 7, got %v", p.AnchorPrice)
	}
	if p.AnchorSource != "" {
		t.Fatalf("来源仍应留空（不可证就是不可证）, got %q", p.AnchorSource)
	}
}
