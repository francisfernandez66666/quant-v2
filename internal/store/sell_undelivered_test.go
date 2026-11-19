// sell_undelivered_test.go — §SELL-UNDELIVERED（2026-11-19，第 25 条告警）账本层回归锁。
//
// 这条规则的全部价值在于"只数该数的那些行"：多算一行＝半夜假警（通道断那天尤其会挨骂），
// 少算一行＝止损没送出去却没人知道（裸敞口一直开着）。所以四个维度逐一钉死：
// 日期、账号、方向、状态；另外两条负锁（买入失败不算、已报/已成不算）与日期格式的
// 严格性（猜错格式会让昨天的失败单算进今天）。
package store

import (
	"testing"
)

// seedSellUndelivered 造一批形态各异的委托行，覆盖计数判据的每个边界。
// 用真实 UpsertRealOrder 而不是裸 SQL：列名/默认值随 schema 演化时这里跟着一起动，
// 裸 INSERT 会在加列后静默写进半默认行，测试照样绿但测的已经不是生产形状。
func seedSellUndelivered(t *testing.T, db *DB) {
	t.Helper()
	rows := []RealOrder{
		// 计入：当日 + 本账号 + 卖出 + 发送失败（两笔）
		{OrderID: "o1", SignalID: "sell:600362:1", Code: "600362.SH", Side: "卖出", Status: "发送失败", Qty: 100, CreatedAt: "2026-11-19 09:31:02", UserID: "u_t"},
		{OrderID: "o2", SignalID: "sell:600362:2", Code: "600362.SH", Side: "卖出", Status: "发送失败", Qty: 200, CreatedAt: "2026-11-19 14:02:11", UserID: "u_t"},
		// 不计：买入失败（少买一笔不构成裸敞口，换一条规则管）
		{OrderID: "o3", SignalID: "buy:600588:1", Code: "600588.SH", Side: "买入", Status: "发送失败", Qty: 100, CreatedAt: "2026-11-19 09:32:00", UserID: "u_t"},
		// 不计：卖出但已送达（已报/已成/撤单中都不是"未送达"）
		{OrderID: "o4", SignalID: "sell:600588:2", Code: "600588.SH", Side: "卖出", Status: "已报", Qty: 100, CreatedAt: "2026-11-19 09:33:00", UserID: "u_t"},
		{OrderID: "o5", SignalID: "sell:600588:3", Code: "600588.SH", Side: "卖出", Status: "已成", Qty: 100, CreatedAt: "2026-11-19 09:34:00", UserID: "u_t"},
		{OrderID: "o6", SignalID: "sell:600588:4", Code: "600588.SH", Side: "卖出", Status: "撤单中", Qty: 100, CreatedAt: "2026-11-19 09:35:00", UserID: "u_t"},
		// 不计：昨日（跨日不复活——day-keyed 读面的统一纪律）
		{OrderID: "o7", SignalID: "sell:000001:1", Code: "000001.SZ", Side: "卖出", Status: "发送失败", Qty: 100, CreatedAt: "2026-11-18 09:31:00", UserID: "u_t"},
		// 不计：别的账号（成员引擎不许数到运营的失败单）
		{OrderID: "o8", SignalID: "sell:000001:2", Code: "000001.SZ", Side: "卖出", Status: "发送失败", Qty: 100, CreatedAt: "2026-11-19 09:36:00", UserID: "u_other"},
	}
	for _, r := range rows {
		if _, err := db.UpsertRealOrder(r); err != nil {
			t.Fatalf("seed %s: %v", r.OrderID, err)
		}
	}
}

// TestCountSellUndeliveredDayUserSideStatus 四个维度各自单独生效（任一维度失效即判红）。
func TestCountSellUndeliveredDayUserSideStatus(t *testing.T) {
	db := openTestDB(t)
	seedSellUndelivered(t, db)

	n, err := db.CountSellUndelivered("20261119", "u_t")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("今日卖出未送达应为 2 笔，实得 %d（多算＝假警；少算＝止损没送到没人知道）", n)
	}

	// 日期维度：昨天一行都不算。
	if n, _ := db.CountSellUndelivered("20261118", "u_t"); n != 1 {
		t.Fatalf("昨日应为 1 笔，实得 %d", n)
	}
	// 账号维度：别的账号只有它自己那一笔。
	if n, _ := db.CountSellUndelivered("20261119", "u_other"); n != 1 {
		t.Fatalf("他人账号应为 1 笔，实得 %d", n)
	}
	// 账号维度（负锁）：不存在的账号必须是 0，不能因为 SQL 丢了 user_id 条件而数到别人。
	if n, _ := db.CountSellUndelivered("20261119", "u_absent"); n != 0 {
		t.Fatalf("不存在的账号应为 0 笔，实得 %d（user_id 条件丢了）", n)
	}
}

// TestCountSellUndeliveredAcceptsDashAndCompactDay 两种日期写法必须同源。
// 调用方一边是 data.TradingDayDate（YYYYMMDD），一边是查询参数（YYYY-MM-DD），
// 归一如果不做，运行期会稳定地"查不到"——而 gt 0 的规则把查不到读成 0，静默失效。
func TestCountSellUndeliveredAcceptsDashAndCompactDay(t *testing.T) {
	db := openTestDB(t)
	seedSellUndelivered(t, db)
	compact, err1 := db.CountSellUndelivered("20261119", "u_t")
	dash, err2 := db.CountSellUndelivered("2026-11-19", "u_t")
	if err1 != nil || err2 != nil {
		t.Fatalf("count errs: %v / %v", err1, err2)
	}
	if compact != 2 || dash != 2 {
		t.Fatalf("两种日期写法必须同源：YYYYMMDD=%d YYYY-MM-DD=%d", compact, dash)
	}
}

// TestCountSellUndeliveredRejectsBadDay 非法日期必须报错而不是"当作今天/昨天"。
func TestCountSellUndeliveredRejectsBadDay(t *testing.T) {
	db := openTestDB(t)
	for _, bad := range []string{"", "2026-1-9", "2026119", "2026/11/19"} {
		if _, err := db.CountSellUndelivered(bad, "u_t"); err == nil {
			t.Fatalf("非法日期 %q 必须报错（猜格式会把别的日子的失败单算进今天）", bad)
		}
	}
}

// TestCountSellUndeliveredUsesOrdersTable 锁住数据源：orders（委托），不是 fills（成交）。
// 成交表里出现"发送失败"是不可能的；若有人把这条量规改去数 fills，
// 它会永远返回 0——一条永远安静的必推规则，比没有规则更危险。
func TestCountSellUndeliveredUsesOrdersTable(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.CountSellUndelivered("20261119", "u_t"); err != nil {
		t.Fatalf("空库也应可数（返回 0 而不是表不存在）: %v", err)
	}
	if _, err := db.db.Exec(`SELECT 1 FROM orders LIMIT 1`); err != nil {
		t.Fatalf("orders 表不存在: %v", err)
	}
}
