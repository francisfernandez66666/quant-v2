// §SIGID-TRUNC（2026-09-24）成交编号被柜台截到 24 位时，账本配对口径的存储层锁。
//
// 缺陷原文（owner 裁决"这个要修"，现网证据见 docs/FIX_PLAN_20260924.md 与 09-22 取证输出）：
// 柜台的 userOrderId 槽是短字段，本仓 25 字符编号（`buy:603468:fac_1:20260922`）提交时被削掉
// 末位日期 ⇒ 成交回报落库的 fills.signal_id 是 24 字符的 `buy:603468:fac_1:2026092`，
// 而 orders/dispatch 行存的是完整 25 字符。旧口径只认"成交编号以委托编号为前缀"这一个方向，
// 于是两条按编号配对的**钱查询**同时静默失明：
//
//	① SumFilledQty 把已成交量看成 0 → 补卖逻辑对未成交旧挂单叠加发单（卖出敞口超额）；
//	② ResetFailedRealOrder 的"已撤+零成交可重放"把**已部成的撤单**判成零成交 → 同键再发一次真单。
//
// 现网实录（09-22 603468.SH）：委托 seq:56「已撤 22.57×800」之下挂着 200+600 两笔成交，
// 全部落在截断编号上 ⇒ 旧口径当场判"这股没成交过、可以重放"。
//
// 本文件锁的是"读取端两边兼容"这一支修法（回填历史被否：fills 是实盘唯一客观流水，
// 编号是已写进账本的事实主键，判重索引与勘误台账都挂在它上面，改长度会让新旧行分裂）：
// 委托编号 ↔ 成交编号双向前缀匹配，反向腿再钉一条"成交行自身交易日必须字面出现在委托编号里"，
// 因为两天削掉末位后前缀完全相同（`...20260922` 与 `...20260923` 截断后都是 `...2026092`）。
// English: storage-level lock for the bidirectional signal-id pairing introduced by §SIGID-TRUNC,
// including the cross-day guard on the reverse leg and the two poles of the replay gate.
package store

import "testing"

// truncFill 造一笔"编号被柜台截断"的成交行（side/金额按买入形态，本文件只关心配对不关心方向）。
func truncFill(t *testing.T, db *DB, uid, signalID, tradedAt string, qty int) {
	t.Helper()
	_, err := db.db.Exec(`INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id)
		VALUES (?, '603468.SH', '买入', 22.57, ?, 22.57*?, ?, ?, ?)`,
		signalID+"#"+uid, qty, qty, tradedAt, signalID, uid)
	if err != nil {
		t.Fatalf("insert fill: %v", err)
	}
}

func TestSumFilledQtyMatchesCounterTruncatedID(t *testing.T) {
	db := testDB(t)
	orderSID := "buy:603468:fac_1:20260922" // 委托侧完整编号（25 字符）
	wireSID := orderSID[:24]                // 柜台回来只剩 24 位
	truncFill(t, db, "u_tr", wireSID, "2026-09-22T13:55:00+08:00", 200)
	truncFill(t, db, "u_tr", wireSID, "2026-09-22T13:55:00+08:00", 600)
	// 另一账号同日同前缀 900 股不得串入（反向腿也必须吃 user_id 约束）
	truncFill(t, db, "u_other", wireSID, "2026-09-22T13:55:00+08:00", 900)

	if got := db.SumFilledQty("u_tr", orderSID); got != 800 {
		t.Fatalf("§SIGID-TRUNC 截断编号的成交必须配回委托（应 800），got %d", got)
	}
	if got := db.SumFilledQty("u_other", orderSID); got != 900 {
		t.Fatalf("反向腿串了账号（应 900），got %d", got)
	}
	// 完整编号自身的正向匹配面不回退：精确等值仍算得进来
	truncFill(t, db, "u_tr", orderSID, "2026-09-22T14:00:00+08:00", 100)
	if got := db.SumFilledQty("u_tr", orderSID); got != 900 {
		t.Fatalf("精确编号行应照常计入（应 900），got %d", got)
	}
}

func TestSumFilledQtyReverseLegNeedsSameDay(t *testing.T) {
	db := testDB(t)
	d1 := "buy:603468:fac_1:20260922"
	d2 := "buy:603468:fac_1:20260923"
	if d1[:24] != d2[:24] {
		t.Fatalf("前提被改：两天截断后应当同前缀，否则本锁测不到塌缩")
	}
	// 09-22 的成交（柜台只回前 24 位）——它属于 d1，绝不能被 d2 那笔委托认领
	truncFill(t, db, "u_day", d1[:24], "2026-09-22T13:55:00+08:00", 800)
	if got := db.SumFilledQty("u_day", d1); got != 800 {
		t.Fatalf("同日委托应配到成交（应 800），got %d", got)
	}
	if got := db.SumFilledQty("u_day", d2); got != 0 {
		t.Fatalf("跨日塌缩必须被交易日闸挡住（应 0），got %d", got)
	}
	// 同前缀但成交行日期与编号日期一致的另一天：各配各的，不互相吞
	truncFill(t, db, "u_day", d2[:24], "2026-09-23T10:00:00+08:00", 300)
	if got := db.SumFilledQty("u_day", d2); got != 300 {
		t.Fatalf("09-23 委托应只配到 09-23 的成交（应 300），got %d", got)
	}
}

func TestSumFilledQtyEmptySignalIDIsZero(t *testing.T) {
	db := testDB(t)
	truncFill(t, db, "u_blank", "buy:603468:fac_1:20260922", "2026-09-22T13:55:00+08:00", 500)
	// 空前缀在 LIKE 语义下等于"全部成交"，一旦让空编号进来就会凭空造出一笔巨量
	if got := db.SumFilledQty("u_blank", ""); got != 0 {
		t.Fatalf("空 signal_id 必须判 0（否则会把全账当这笔的已成交量），got %d", got)
	}
	if got := db.SumFilledQty("u_blank", "   "); got != 0 {
		t.Fatalf("空白 signal_id 同样判 0，got %d", got)
	}
}

func TestResetCancelledWithTruncatedFillNotReplayable(t *testing.T) {
	db := testDB(t)
	orderSID := "sell:603468:dragon:20260922"
	wireSID := orderSID[:24]
	o := RealOrder{OrderID: "EXC-TR", SignalID: orderSID, Code: "603468.SH", Side: "卖出",
		Status: "已撤", Price: 22.57, Qty: 800, CreatedAt: "2026-09-22T13:54:58+08:00", UserID: "u_rep"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert order: %v", err)
	}
	// 极一：撤单前已部成（成交编号是被柜台截断的那 24 位）→ 目标已部分达成，不许同键重放
	truncFill(t, db, "u_rep", wireSID, "2026-09-22T13:55:00+08:00", 800)
	if ok, err := db.ResetFailedRealOrder("u_rep", orderSID); err != nil || ok {
		t.Fatalf("§SIGID-TRUNC 已部成的撤单不得重放（会再发一次真单），ok=%v err=%v", ok, err)
	}
	// 极二：同键另一笔真·零成交的撤单仍要放行（防把上面那条修成"一律不可重放"的单向锁）
	o2 := RealOrder{OrderID: "EXC-ZERO", SignalID: "sell:600000:dragon:20260922", Code: "600000.SH",
		Side: "卖出", Status: "已撤", Price: 10, Qty: 100,
		CreatedAt: "2026-09-22T13:54:58+08:00", UserID: "u_rep"}
	if _, err := db.UpsertRealOrder(o2); err != nil {
		t.Fatalf("upsert order2: %v", err)
	}
	if ok, err := db.ResetFailedRealOrder("u_rep", o2.SignalID); err != nil || !ok {
		t.Fatalf("已撤+零成交仍须可重放（§H1-MG 语义不回退），ok=%v err=%v", ok, err)
	}
}
