// risk_gates_test.go — 单日买入笔数「已成交」口径（CountBuyFilledOrdersByDay）回归。
//
// 2026-09-18 修正：单日买入笔数纪律闸原先数 orders 表里「今日已报」的委托数，报单即占额度——
// 一笔被券商废掉、或挂在委托簿上没成交的报单同样吃掉一天的买入额度，用户会因为一堆没成交的
// 报单被锁死买入权。改为只数 fills（柜台回报的客观成交事实）后，这里的用例钉住计数键语义：
// 同一委托的多次部分成交算 1 笔；卖出/非当日/他人账号不计。
//
// English: regression for the "filled, not submitted" daily buy-count metric — partial fills of one
// order collapse to one; sells, other days and other accounts are excluded.
package store

import (
	"path/filepath"
	"testing"
)

// newRiskTestDB 建临时实盘账本。
func newRiskTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rg.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fill 便捷构造一笔成交。
func fill(orderID, serial, code, side, tradedAt string) RealFill {
	return RealFill{
		OrderID: orderID, Serial: serial, Code: code, Side: side,
		Price: 10, Qty: 100, Amount: 1000, UserID: "u_rg", TradedAt: tradedAt,
	}
}

// TestCountBuyFilledOrdersByDay 计数口径：仅当日买入成交，且同一委托去重。
func TestCountBuyFilledOrdersByDay(t *testing.T) {
	db := newRiskTestDB(t)
	// 他账号的一笔买入：必须在计数之外
	other := fill("O5", "", "600005.SH", "买入", "2026-09-18 11:00:00")
	other.UserID = "u_other"
	fills := []RealFill{
		// 同一委托 O1 的两笔部分成交 → 1 笔
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:00"),
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:05"),
		// 另一委托 → 第 2 笔
		fill("O2", "", "600002.SH", "买入", "2026-09-18 10:00:00"),
		// 卖出不计
		fill("O3", "", "600003.SH", "卖出", "2026-09-18 10:30:00"),
		// 非当日不计
		fill("O4", "", "600004.SH", "买入", "2026-09-17 10:00:00"),
		// 他账号不计
		other,
		// 无 order_id：回落券商交割流水号去重（同流水号两行 → 1 笔）
		fill("", "S1", "600006.SH", "买入", "2026-09-18 13:00:00"),
		fill("", "S1", "600006.SH", "买入", "2026-09-18 13:00:01"),
		// 无 order_id 无流水号：各自算一笔（宁可多计也不把多笔成交并成 1 笔而少计额度）
		fill("", "", "600007.SH", "买入", "2026-09-18 14:00:00"),
		fill("", "", "600008.SH", "买入", "2026-09-18 14:01:00"),
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	// 期望：O1(2 行→1) + O2 + S1(2 行→1) + 2 行无键 = 5 笔
	got, err := db.CountBuyFilledOrdersByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("CountBuyFilledOrdersByDay: %v", err)
	}
	if got != 5 {
		t.Fatalf("今日已成交买入笔数应为 5（O1 两笔部分成交算 1）got=%d", got)
	}
	// 非当日/他账号口径为空
	if got, err = db.CountBuyFilledOrdersByDay("u_rg", "2026-09-17"); err != nil || got != 1 {
		t.Fatalf("9-17 应为 1 笔, got %d err=%v", got, err)
	}
	if got, err = db.CountBuyFilledOrdersByDay("u_none", "2026-09-18"); err != nil || got != 0 {
		t.Fatalf("无成交账号应为 0, got %d err=%v", got, err)
	}
}

// TestSumBuyFilledAmountByDay 预算闸冻结账的「已成交」半边：Σ 当日买入成交金额
// （amount 优先，旧数据缺失时回落 price×qty），卖出/非当日/他人账号不计。
// 与 LocalBuyFrozen（orders 状态派生的「在途冻结」半边）合起来才是完整占用。
func TestSumBuyFilledAmountByDay(t *testing.T) {
	db := newRiskTestDB(t)
	other := fill("O5", "", "600005.SH", "买入", "2026-09-18 11:00:00")
	other.UserID = "u_other"
	fills := []RealFill{
		// 正常 amount 行：按 amount 计
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:00"),
		// 同一委托的另一笔部分成交：金额照加（金额口径不去重，笔数口径才去重）
		fill("O1", "", "600001.SH", "买入", "2026-09-18 09:31:05"),
		// 旧数据 amount=0：回落 price×qty = 10×100 = 1000
		func() RealFill {
			f := fill("O2", "", "600002.SH", "买入", "2026-09-18 10:00:00")
			f.Amount = 0
			return f
		}(),
		// 卖出 / 非当日 / 他账号不计
		fill("O3", "", "600003.SH", "卖出", "2026-09-18 10:30:00"),
		fill("O4", "", "600004.SH", "买入", "2026-09-17 10:00:00"),
		other,
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	got, err := db.SumBuyFilledAmountByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("SumBuyFilledAmountByDay: %v", err)
	}
	if got != 3000 {
		t.Fatalf("今日已成交买入金额应为 3000（1000×2 + 回落1000）got=%.0f", got)
	}
	if got, err = db.SumBuyFilledAmountByDay("u_rg", "2026-09-17"); err != nil || got != 1000 {
		t.Fatalf("9-17 应为 1000, got %.0f err=%v", got, err)
	}
	if got, err = db.SumBuyFilledAmountByDay("u_none", "2026-09-18"); err != nil || got != 0 {
		t.Fatalf("无成交账号应为 0, got %.0f err=%v", got, err)
	}
}

// TestSumSellFilledAmountByDay 冻结账「回款」半边：Σ 当日卖出成交金额，与买入口径同源
// （amount 优先、旧数据回落 price×qty），买入/非当日/他人账号不计。预算闸用它对冲当日占用。
func TestSumSellFilledAmountByDay(t *testing.T) {
	db := newRiskTestDB(t)
	other := fill("O9", "", "600009.SH", "卖出", "2026-09-18 11:00:00")
	other.UserID = "u_other"
	fills := []RealFill{
		fill("O6", "", "600006.SH", "卖出", "2026-09-18 09:35:00"),
		// 旧数据 amount=0：回落 price×qty = 1000
		func() RealFill {
			f := fill("O7", "", "600007.SH", "卖出", "2026-09-18 10:05:00")
			f.Amount = 0
			return f
		}(),
		// 买入不计（买入是占用侧，不是回款侧）
		fill("O8", "", "600008.SH", "买入", "2026-09-18 10:30:00"),
		fill("O10", "", "600010.SH", "卖出", "2026-09-17 10:00:00"),
		other,
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	got, err := db.SumSellFilledAmountByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("SumSellFilledAmountByDay: %v", err)
	}
	if got != 2000 {
		t.Fatalf("今日卖出回款应为 2000（1000 + 回落1000）got=%.0f", got)
	}
	if got, err = db.SumSellFilledAmountByDay("u_none", "2026-09-18"); err != nil || got != 0 {
		t.Fatalf("无成交账号应为 0, got %.0f err=%v", got, err)
	}
}

// TestCountBuyFilledOrdersByDayIgnoresUnfilledOrders 「报单不占额度」的数据层对照：
// 只有 orders 行（还没成交）时计数恒为 0——旧口径在这里会数出 N 笔。
func TestCountBuyFilledOrdersByDayIgnoresUnfilledOrders(t *testing.T) {
	db := newRiskTestDB(t)
	for i, id := range []string{"SIG-A", "SIG-B"} {
		if _, err := db.UpsertRealOrder(RealOrder{
			OrderID: "GW-" + id, SignalID: id, Code: "600000.SH", Side: "买入",
			Status: "已报", Price: 10, Qty: 100, UserID: "u_rg",
			CreatedAt: "2026-09-18T09:30:0" + string(rune('0'+i)) + "+08:00",
		}); err != nil {
			t.Fatalf("UpsertRealOrder: %v", err)
		}
	}
	got, err := db.CountBuyFilledOrdersByDay("u_rg", "2026-09-18")
	if err != nil {
		t.Fatalf("CountBuyFilledOrdersByDay: %v", err)
	}
	if got != 0 {
		t.Fatalf("2 笔已报未成交的报单不应占用笔数额度, got %d", got)
	}
}

// TestSumBuyFilledAmountByDayForStrategy 按战法聚合已成交买入金额：signal_id 标准格式中的
// stratKey（如 dragon/momentum/fac_1）用冒号边界精确匹配，短名不撞长名前缀。
func TestSumBuyFilledAmountByDayForStrategy(t *testing.T) {
	db := newRiskTestDB(t)
	fills := []RealFill{
		func() RealFill {
			f := fill("OA", "", "600001.SH", "买入", "2026-09-18 09:31:00")
			f.SignalID = "buy:600001.SH:dragon_return:2026-09-18"
			return f
		}(),
		func() RealFill {
			f := fill("OB", "", "600002.SH", "买入", "2026-09-18 10:00:00")
			f.SignalID = "buy:600002.SH:dragon_return:2026-09-18"
			return f
		}(),
		func() RealFill {
			f := fill("OC", "", "600003.SH", "买入", "2026-09-18 10:30:00")
			f.SignalID = "buy:600003.SH:dragon:2026-09-18"
			return f
		}(),
		func() RealFill {
			f := fill("OD", "", "600004.SH", "买入", "2026-09-18 11:00:00")
			f.SignalID = "buy:600004.SH:momentum:2026-09-18"
			return f
		}(),
		func() RealFill {
			f := fill("OE", "", "600005.SH", "买入", "2026-09-18 11:30:00")
			f.SignalID = "buy:600005.SH:fac_1:2026-09-18"
			return f
		}(),
		func() RealFill {
			f := fill("OF", "", "600001.SH", "卖出", "2026-09-18 12:00:00")
			f.SignalID = "sell:600001.SH:dragon:2026-09-18"
			return f
		}(),
	}
	for i, f := range fills {
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill #%d: %v", i, err)
		}
	}
	// 验证各战法归集金额正确（默认 amount=1000/笔）
	got, _ := db.SumBuyFilledAmountByDayForStrategy("u_rg", "2026-09-18", "dragon_return")
	if got != 2000 {
		t.Fatalf("dragon_return expected=2000 got=%.0f", got)
	}
	got, _ = db.SumBuyFilledAmountByDayForStrategy("u_rg", "2026-09-18", "dragon")
	if got != 1000 {
		t.Fatalf("dragon expected=1000 got=%.0f", got)
	}
	got, _ = db.SumBuyFilledAmountByDayForStrategy("u_rg", "2026-09-18", "momentum")
	if got != 1000 {
		t.Fatalf("momentum expected=1000 got=%.0f", got)
	}
	got, _ = db.SumBuyFilledAmountByDayForStrategy("u_rg", "2026-09-18", "fac_1")
	if got != 1000 {
		t.Fatalf("fac_1 expected=1000 got=%.0f", got)
	}
	got, _ = db.SumBuyFilledAmountByDayForStrategy("u_rg", "2026-09-18", "pat_1")
	if got != 0 {
		t.Fatalf("pat_1 (no fills) expected=0 got=%.0f", got)
	}
}

// TestStrategySignalIDTruncationMatch §STRATEGY-FIX（2026-10-06 波 1）战法键匹配谓词的三条口径：
// 柜台 24 字符截断仍要认得、跨日不得张冠李戴、下划线不得当通配符。
// 旧实现是 `signal_id LIKE '%:'||key||':%'`（要求键两侧都有冒号），三种情形里有两种判错：
//   - 截断行 `buy:600001:dragon_return`（编号共 33 字符、柜台只回前 24 位，尾冒号连日期一起被吃掉）
//     恒不命中 ⇒ 龙回头这一路的战法日预算恒读 0（即便键空间修好了也仍失明）；
//   - LIKE 的 `_` 是单字符通配符 ⇒ 键 fac_1 会顺带命中 faxx1 这类异段，把别人的钱记到本战法头上。
//
// English: the colon-segment matcher must survive the counter's 24-char truncation, must scope by
// the fill's own trade date, and must not treat the underscore inside strategy keys as a LIKE wildcard.
func TestStrategySignalIDTruncationMatch(t *testing.T) {
	db := newRiskTestDB(t)
	const day = "2026-09-18"
	truncDR := "buy:600001:dragon_return" // 恰好 24 字符：柜台上限处正落在键名末尾
	if len(truncDR) != 24 {
		t.Fatalf("截断腿前提被破坏：该形态应恰为 24 字符，实得 %d（%q）", len(truncDR), truncDR)
	}
	truncFac := "buy:603468:fac_1:2026092" // 现网实录形态（§SIGID-TRUNC）：日期被切掉两位
	if len(truncFac) != 24 {
		t.Fatalf("截断腿前提被破坏：现网实录形态应恰为 24 字符，实得 %d（%q）", len(truncFac), truncFac)
	}
	seeds := []struct {
		orderID, code, signalID, tradedAt string
	}{
		{"TA", "600001.SH", truncDR, day + " 09:31:00"},                           // 截断·尾冒号被吃
		{"TB", "603468.SH", truncFac, day + " 09:32:00"},                          // 截断·日期残缺
		{"TC", "600002.SH", "buy:600002:dragon:" + "20260918", day + " 09:33:00"}, // 完整
		{"TD", "600003.SH", "buy:600003:faxx1:" + "20260918", day + " 09:34:00"},  // 下划线通配陷阱
		{"TE", "600004.SH", truncDR, "2026-09-17 09:31:00"},                       // 前一日的截断行（同前缀，跨日不得混入）
	}
	for _, s := range seeds {
		f := fill(s.orderID, "", s.code, "买入", s.tradedAt)
		f.SignalID = s.signalID
		if err := db.ApplyRealFill(f); err != nil {
			t.Fatalf("ApplyRealFill %s: %v", s.orderID, err)
		}
	}
	cases := []struct {
		key  string
		want float64
		why  string
	}{
		{"dragon_return", 1000, "截断行必须计入，且前一日的同前缀行不得混入（跨日靠 traded_at 独立约束）"},
		{"fac_1", 1000, "日期残缺的截断行必须计入，且不得顺带命中 faxx1（LIKE 的 _ 是通配符，instr 不是）"},
		{"faxx1", 1000, "异段自成一键，不得被 fac_1 吸走"},
		{"dragon", 1000, "短键不得把 dragon_return 的钱并进来（旧口径靠两侧冒号勉强挡住，新口径靠整段相等挡死）"},
		{"momentum", 0, "无成交的战法必须读 0（防「键没命中」被当成「命中了个空账」）"},
	}
	for _, c := range cases {
		got, err := db.SumBuyFilledAmountByDayForStrategy("u_rg", day, c.key)
		if err != nil {
			t.Fatalf("Sum %s: %v", c.key, err)
		}
		if got != c.want {
			t.Fatalf("key=%s expected=%.0f got=%.0f（%s）", c.key, c.want, got, c.why)
		}
	}
}

// TestLocalBuyFrozenByStrategy §STRATEGY-FIX（2026-10-06 波 1）战法维度的在途冻结账：
// §STRATEGY_ALLOC 子闸 2b 的「已成交 + 在途」口径数据源。三条语义各自钉死：
// 只算本战法、只算当日未成交余量、空键＝不限战法且必须与全局那本账加得起来。
// English: per-strategy in-flight freeze — restricted to the key's own orders and the given day,
// and the empty key must behave exactly as the global ledger (so the two books reconcile).
func TestLocalBuyFrozenByStrategy(t *testing.T) {
	db := newRiskTestDB(t)
	const day = "2026-09-18"
	orders := []RealOrder{
		{OrderID: "FZ1", SignalID: "buy:600010:pat_9:20260918", Code: "600010.SH", Side: "买入", Status: "已报", Price: 9, Qty: 100, CreatedAt: day + "T10:00:00+08:00", UserID: "u_rg"},
		{OrderID: "FZ2", SignalID: "buy:600011:dragon:20260918", Code: "600011.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: day + "T10:01:00+08:00", UserID: "u_rg"},
		{OrderID: "FZ3", SignalID: "buy:600012:pat_9:20260918", Code: "600012.SH", Side: "买入", Status: "已成", Price: 10, Qty: 100, CreatedAt: day + "T10:02:00+08:00", UserID: "u_rg"},
		{OrderID: "FZ4", SignalID: "buy:600013:pat_9:20260917", Code: "600013.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-17T10:03:00+08:00", UserID: "u_rg"},
		{OrderID: "FZ5", SignalID: "buy:600014:pat_9x:20260918", Code: "600014.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: day + "T10:04:00+08:00", UserID: "u_rg"},
	}
	for _, o := range orders {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("UpsertRealOrder %s: %v", o.OrderID, err)
		}
	}
	got, err := db.LocalBuyFrozenByStrategy("u_rg", day, "pat_9")
	if err != nil {
		t.Fatalf("frozen pat_9: %v", err)
	}
	if got != 900 {
		t.Fatalf("pat_9 在途应仅 FZ1 的 900（FZ3 已成/跨日 FZ4/pat_9x FZ5 都不算），got %.0f", got)
	}
	if got, _ := db.LocalBuyFrozenByStrategy("u_rg", day, "dragon"); got != 1000 {
		t.Fatalf("dragon 在途应 1000（FZ2），got %.0f", got)
	}
	if got, _ := db.LocalBuyFrozenByStrategy("u_rg", day, "momentum"); got != 0 {
		t.Fatalf("无在途单的战法必须 0，got %.0f", got)
	}
	total, err := db.LocalBuyFrozen("u_rg", day)
	if err != nil {
		t.Fatalf("frozen total: %v", err)
	}
	if total != 2900 {
		t.Fatalf("全局在途应为 900(FZ1)+1000(FZ2)+1000(FZ5)=2900（已成 FZ3、跨日 FZ4 不算），got %.0f", total)
	}
	// 空键＝不限战法：与全局那本账等值（两本账共用一份实现，这条等值就是"没各写一份"的证据）。
	empty, err := db.LocalBuyFrozenByStrategy("u_rg", day, "")
	if err != nil {
		t.Fatalf("frozen empty key: %v", err)
	}
	if empty != total {
		t.Fatalf("空键必须等于全局在途（%.0f），got %.0f——空串被当成过滤器＝子闸静默读到 0", total, empty)
	}
	// 各战法分项之和必须等于全局（防"漏一个战法键"造成两本账悄悄分叉）。
	sum := 0.0
	for _, k := range []string{"pat_9", "dragon", "pat_9x"} {
		v, err := db.LocalBuyFrozenByStrategy("u_rg", day, k)
		if err != nil {
			t.Fatalf("frozen %s: %v", k, err)
		}
		sum += v
	}
	if sum != total {
		t.Fatalf("分项之和 %.0f 必须等于全局 %.0f（少一个键就是子闸对那笔在途无感知）", sum, total)
	}
}
