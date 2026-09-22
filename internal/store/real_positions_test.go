// 实盘账本（AUTO_TRADING_PLAN M1）存取测试：全量对账 upsert、成交回报应用（建仓/加仓/减仓/清仓）、幂等下单。
package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestUpsertRealPositions 验证全量对账：upsert 覆盖 + 移除已清仓行 + highest_price 单调不回退。
// §N-6（2026-09-22 傍晚批，裁决 11=本地含费优先）追加断言：qty 仍随快照刷新（份额数以柜台为准），
// cost_price/amount 则**不再**被快照的不含费 open_price 覆盖——本地已有非零含费成本时快照值仅作
// 兜底；amount 由「选定成本 × 快照数量」同源推导（400×10=4000，不是快照的 4200）。
func TestUpsertRealPositions(t *testing.T) {
	db := testDB(t)
	base := []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 300, CostPrice: 10, Amount: 3000, HighestPrice: 11, Strategy: "N形", SignalID: "SIG1"},
		{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 50, Amount: 5000, HighestPrice: 52},
	}
	if n, err := db.UpsertRealPositions(base); err != nil || n != 2 {
		t.Fatalf("first upsert: n=%d err=%v", n, err)
	}
	// 第二推：600000 数量变化 + 最高价降级（应保留旧最高价）；000001 消失（应删除）
	second := []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 400, CostPrice: 10.5, Amount: 4200, HighestPrice: 10.2},
	}
	if n, err := db.UpsertRealPositions(second); err != nil || n != 1 {
		t.Fatalf("second upsert: n=%d err=%v", n, err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil {
		t.Fatalf("by code: %v", err)
	}
	if p.Qty != 400 {
		t.Fatalf("qty 应随快照刷新为 400: %+v", p)
	}
	if p.CostPrice != 10 || p.Amount != 4000 {
		t.Fatalf("§N-6 本地含费成本不得被快照(10.5/4200)覆盖，且 amount 须=成本×数量: %+v", p)
	}
	if p.HighestPrice != 11 {
		t.Fatalf("highest_price 应保留旧峰值 11，got %v", p.HighestPrice)
	}
	if _, err := db.RealPositionByCode("000001.SZ"); err != sql.ErrNoRows {
		t.Fatalf("000001 应被移除，err=%v", err)
	}
	// 本地成本为 0（历史脏行/券商快照先建行）时快照仍可兜底回填——守卫不得把纠正通道焊死。
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "301001.SZ", Name: "新代码", Qty: 100, CostPrice: 0, Amount: 0, HighestPrice: 0},
	}); err != nil {
		t.Fatalf("seed zero-cost row: %v", err)
	}
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "301001.SZ", Name: "新代码", Qty: 100, CostPrice: 12.5, Amount: 1250, HighestPrice: 12.5},
	}); err != nil {
		t.Fatalf("backfill snapshot: %v", err)
	}
	if p, _ = db.RealPositionByCode("301001.SZ"); p.CostPrice != 12.5 || p.Amount != 1250 {
		t.Fatalf("本地成本为 0 时快照应兜底回填: %+v", p)
	}
}

// TestApplyRealFill 验证成交回报驱动：买入建仓 → 加仓加权成本 → 减仓 → 清仓删除 + fills 落库。
func TestApplyRealFill(t *testing.T) {
	db := testDB(t)
	// 建仓买入
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-08-20 09:35:00", SignalID: "SIG1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil || p.Qty != 100 || p.CostPrice != 10 || p.HighestPrice != 10 {
		t.Fatalf("open 异常: %+v err=%v", p, err)
	}
	// 加仓买入：加权成本应变为 (1000+12*100)/200=11
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "买入", Price: 12, Qty: 100, Amount: 1200, TradedAt: "2026-08-20 10:00:00"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Qty != 200 || p.CostPrice != 11 || p.HighestPrice != 12 {
		t.Fatalf("加仓加权成本异常: %+v", p)
	}
	// 减仓 50
	if err := db.ApplyRealFill(RealFill{OrderID: "O3", Code: "600000.SH", Side: "卖出", Price: 13, Qty: 50, Amount: 650, TradedAt: "2026-08-20 11:00:00"}); err != nil {
		t.Fatalf("reduce: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Qty != 150 || p.Amount != 1650 {
		t.Fatalf("减仓异常: %+v", p)
	}
	// 清仓
	if err := db.ApplyRealFill(RealFill{OrderID: "O4", Code: "600000.SH", Side: "卖出", Price: 13, Qty: 150, Amount: 1950, TradedAt: "2026-08-20 14:00:00"}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := db.RealPositionByCode("600000.SH"); err != sql.ErrNoRows {
		t.Fatalf("清仓后应删除，err=%v", err)
	}
	fills, err := db.RealFills()
	if err != nil || len(fills) != 4 {
		t.Fatalf("fills 应 4 条，got %d err=%v", len(fills), err)
	}
}

// TestSumFilledQtyPrefix §修复 FIX#1：卖出成交的 signal_id 是 base 追加 :r<剩余量> 后缀的完整键。
// SumFilledQty 须按 base 前缀聚合（base 与所有 :rN 桶的成交都计入），且按账号隔离。
// English: §FIX#1 regression — sells fill under the full signal id (base + ":r<remaining>"),
// so SumFilledQty must aggregate by base prefix and stay user-scoped.
func TestSumFilledQtyPrefix(t *testing.T) {
	db := testDB(t)
	base := "sell:600000:止损:2026-09-04"
	ins := func(uid, sid string, qty int) {
		t.Helper()
		_, err := db.db.Exec(`INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id)
			VALUES (?, '600000.SH', '卖出', 10, ?, 10*?, '2026-09-04 09:40:00', ?, ?)`,
			sid+"#"+uid, qty, qty, sid, uid)
		if err != nil {
			t.Fatalf("insert fill: %v", err)
		}
	}
	// 首笔部成 300（base 键）+ 补卖成交 400（:r700 桶）→ 前缀聚合应得 700
	ins("U1", base, 300)
	ins("U1", base+":r700", 400)
	// 另一账号同码同键 999 不得串入
	ins("U2", base, 999)
	if got := db.SumFilledQty("U1", base); got != 700 {
		t.Fatalf("SumFilledQty(U1, base) 应 700，got %d", got)
	}
	if got := db.SumFilledQty("U1", base+":r700"); got != 400 {
		t.Fatalf("SumFilledQty(U1, base:r700) 应 400，got %d", got)
	}
	if got := db.SumFilledQty("U2", base); got != 999 {
		t.Fatalf("SumFilledQty(U2, base) 应 999，got %d", got)
	}
}

// TestUpsertRealOrderIdempotent 验证同一 signal_id 重复下单被幂等拦截。
func TestUpsertRealOrderIdempotent(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "GW1", SignalID: "SIG1", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-20 09:30:00"}
	existed, err := db.UpsertRealOrder(o)
	if err != nil || existed {
		t.Fatalf("first order: existed=%v err=%v", existed, err)
	}
	existed, err = db.UpsertRealOrder(o)
	if err != nil || !existed {
		t.Fatalf("duplicate signal_id 应幂等返回 existed=true, got %v err=%v", existed, err)
	}
	orders, err := db.RealOrders()
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders 应 1 条，got %d err=%v", len(orders), err)
	}
}

// TestApplyRealFillEdge 成交回报边界：超卖钳制清仓、卖空仓不报错、清仓后再买入重建仓。
// English: fill edge cases — over-sell clamps to close, selling a non-existent position is a no-op,
// and a buy after close rebuilds a fresh position.
func TestApplyRealFillEdge(t *testing.T) {
	db := testDB(t)
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100, Amount: 1000, TradedAt: "t1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 超卖：卖 150（持仓仅 100）→ 钳制为 0 → 删除持仓行，不报错
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "卖出", Price: 11, Qty: 150, Amount: 1650, TradedAt: "t2"}); err != nil {
		t.Fatalf("over-sell should not error: %v", err)
	}
	if _, err := db.RealPositionByCode("600000.SH"); err != sql.ErrNoRows {
		t.Fatalf("over-sell 后应清仓删除，err=%v", err)
	}
	// 卖空仓：不报错、不建行
	if err := db.ApplyRealFill(RealFill{OrderID: "O3", Code: "000001.SZ", Side: "卖出", Price: 5, Qty: 100, Amount: 500, TradedAt: "t3"}); err != nil {
		t.Fatalf("sell non-existent should not error: %v", err)
	}
	if _, err := db.RealPositionByCode("000001.SZ"); err != sql.ErrNoRows {
		t.Fatalf("卖空仓不应建行，err=%v", err)
	}
	// 清仓后再买入：重建仓，成本/最高价按新价
	if err := db.ApplyRealFill(RealFill{OrderID: "O4", Code: "600000.SH", Side: "买入", Price: 15, Qty: 200, Amount: 3000, TradedAt: "t4"}); err != nil {
		t.Fatalf("re-entry: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil || p.Qty != 200 || p.CostPrice != 15 || p.HighestPrice != 15 {
		t.Fatalf("重建仓异常: %+v err=%v", p, err)
	}
	fills, _ := db.RealFills()
	if len(fills) != 4 {
		t.Fatalf("fills 应 4 条，got %d", len(fills))
	}
}

// TestRealPositionsForUser §GAP1.10 回归：持仓按归属账号过滤；
// 遗留全局行（user_id=”）对所有人可见；UpsertRealPositions 写入 user_id。
func TestRealPositionsForUser(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Name: "A", Qty: 100, UserID: "u_boss"},
		{TsCode: "000001.SZ", Name: "B", Qty: 200}, // 遗留全局行
	}); err != nil {
		t.Fatal(err)
	}
	boss, err := db.RealPositionsForUser("u_boss")
	if err != nil || len(boss) != 2 {
		t.Fatalf("归属账号应看到 自己的+遗留行 = 2, got %d err=%v", len(boss), err)
	}
	other, err := db.RealPositionsForUser("u_other")
	if err != nil || len(other) != 1 {
		t.Fatalf("其他账号应只看到遗留全局行 1 条, got %d err=%v", len(other), err)
	}
	if other[0].TsCode != "000001.SZ" {
		t.Fatalf("其他账号可见的应为遗留行, got %s", other[0].TsCode)
	}
	// 对账重写带 user_id → 归属更新
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "000001.SZ", Name: "B", Qty: 200, UserID: "u_boss"},
	}); err != nil {
		t.Fatal(err)
	}
	other2, _ := db.RealPositionsForUser("u_other")
	if len(other2) != 0 {
		t.Fatalf("遗留行归属更新后其他账号应不可见, got %d", len(other2))
	}
}

// TestReconcileLegacyRowPrunedWhenNotInSnapshot P2#17 回归：非空快照对账分支必须把
// 不在快照中的遗留全局行（user_id=”）一并清理——旧实现只删本账号 scoped 行，遗留行
// 既未被声明归属又不在快照中，成为对全账号可见的永驻残影（虚报对账计数/前向看起来像持仓）。
// 同时已验证真正的 scoped 行（其他账号）与快照内行不受影响。
// English: P2#17 regression — the non-empty snapshot reconcile branch must also purge legacy global rows
// (user_id=”) absent from the snapshot. The old code deleted only this account's scoped rows, so an
// unclaimed legacy row lingered indefinitely (a phantom visible to every account / inflating counts).
// Genuine scoped rows of other accounts and snapshot-present rows stay untouched.
func TestReconcileLegacyRowPrunedWhenNotInSnapshot(t *testing.T) {
	db := testDB(t)
	// 遗留全局行（无归属）600519 + 本账号 scoped 600000
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600519.SH", Name: "茅台", Qty: 100}, // 遗留全局行
		{TsCode: "600000.SH", Name: "浦发", Qty: 200, UserID: "u_a"},
	}); err != nil {
		t.Fatal(err)
	}
	// 用户 B 做全量对账：快照只有 600000（自身），不含 600519 也不含 600519 的归属声明
	if n, err := db.ReconcilePositionsForUser("u_b", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 300},
	}); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("对账后可见行应 1（600000），got %d", n)
	}
	// 600519 遗留行应已被清除，而非对所有人可见
	rows, _ := db.RealPositions()
	for _, p := range rows {
		if p.TsCode == "600519.SH" {
			// 允许已被任一账号声明归属（本测试未声明，不应出现）
			t.Fatalf("遗留全局行 600519 不应残留: %+v", p)
		}
	}
}

// TestApplyRealFillIdempotent §W3-b 回归：同一笔回报（同 order_id/traded_at/price/qty）重复投递时，
// 幂等唯一键命中 → 整体 no-op，持仓数量不被二次累加（根除 outbox 重试双倍记账）。
func TestApplyRealFillIdempotent(t *testing.T) {
	db := testDB(t)
	f := RealFill{OrderID: "O-IDEM", Code: "600519.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-08-26T10:00:00+08:00", SignalID: "S1", UserID: "u_x"}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("first fill: %v", err)
	}
	p, _ := db.RealPositionByCode("600519.SH")
	if p.Qty != 100 {
		t.Fatalf("首笔后持仓应 100, got %d", p.Qty)
	}
	// 同一笔重放：唯一键冲突 → 幂等成功（err==nil）且持仓不变
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("duplicate fill 应幂等成功而非报错: %v", err)
	}
	p, _ = db.RealPositionByCode("600519.SH")
	if p.Qty != 100 {
		t.Fatalf("重放后持仓仍应 100, got %d", p.Qty)
	}
	// 真正的新成交（不同回报时间戳）正常累加
	f2 := f
	f2.TradedAt = "2026-08-26T10:01:00+08:00"
	if err := db.ApplyRealFill(f2); err != nil {
		t.Fatalf("second distinct fill: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.Qty != 200 {
		t.Fatalf("新成交应累加到 200, got %d", p.Qty)
	}
}

// TestResetFailedRealOrderRotatesPlaceholder §修复 FIX#2：失败重试须换新占位单号
// （pend:<sid>:<attempt> 自增），避免旧 pend 行被 SweepOrders 反复误判为"从未到达网关"而降级重放。
// English: §FIX#2 regression — each retry of a failed order rotates to a fresh pend: order_id
// with an auto-incremented attempt, so SweepOrders never misjudges a retried real order as a ghost.
func TestResetFailedRealOrderRotatesPlaceholder(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:S1", SignalID: "S1", Code: "600000.SH", Side: "卖出", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-04T09:30:00+08:00", UserID: "u1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.MarkRealOrderSendFailed("u1", "S1"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	// 首次重试：旧式 pend:S1 → pend:S1:1
	ok, err := db.ResetFailedRealOrder("u1", "S1")
	if err != nil || !ok {
		t.Fatalf("first reset: ok=%v err=%v", ok, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].OrderID != "pend:S1:1" || os[0].Status != "已报" {
		t.Fatalf("重试 1 应得 pend:S1:1/已报，got %+v", os[0])
	}
	// 再次失败再重试：attempt 自增 → pend:S1:2
	if err := db.MarkRealOrderSendFailed("u1", "S1"); err != nil {
		t.Fatalf("mark failed 2: %v", err)
	}
	ok, err = db.ResetFailedRealOrder("u1", "S1")
	if err != nil || !ok {
		t.Fatalf("second reset: ok=%v err=%v", ok, err)
	}
	os, _ = db.RealOrdersForUser("u1")
	if os[0].OrderID != "pend:S1:2" || os[0].Status != "已报" {
		t.Fatalf("重试 2 应得 pend:S1:2/已报，got %+v", os[0])
	}
	// 非"发送失败"状态不可重试（真实在途/已成被唯一键拦截语义）
	if err := db.MarkRealOrderSendFailed("u1", "S1"); err != nil {
		t.Fatalf("mark failed 3: %v", err)
	}
	if err := db.UpdateRealOrderBySignalID("u1", "S1", "GW-1", "已成"); err != nil {
		t.Fatalf("backfill to 已成: %v", err)
	}
	if ok, _ := db.ResetFailedRealOrder("u1", "S1"); ok {
		t.Fatalf("已成单不应可重试")
	}
}

// TestUpdateRealOrderBySignalIDMonotonic §修复 FIX#2：下单回填加单调守卫——
// 若回报线程已把占位行推进到已成/部成，晚到的回填不得把它覆盖回"已报"。
// English: §FIX#2 regression — the order-id backfill must not roll a row back from
// a later (已成/部成) status to 已报.
func TestUpdateRealOrderBySignalIDMonotonic(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:S2", SignalID: "S2", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-04T09:30:00+08:00", UserID: "u1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 回报线程先推进到"已成"
	if ok, err := db.AdvanceRealOrderStatus("u1", "S2", "已成"); err != nil || !ok {
		t.Fatalf("advance to 已成: ok=%v err=%v", ok, err)
	}
	// 晚到的回填（重试成功的网关单号）不应回退状态
	if err := db.UpdateRealOrderBySignalID("u1", "S2", "GW-2", "已报"); err != nil {
		t.Fatalf("late backfill: %v", err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].Status != "已成" {
		t.Fatalf("已成状态被回填覆盖: %+v", os[0])
	}
}

// TestUpdateRealOrderBySignalIDSameRankBackfill §修复 U-1（2026-09-14 像素级 UAT）：
// 占位行（已报）与首次回填（已报）秩相等——旧 `<=` 守卫把这条唯一命中路径误杀，
// 网关单号永不落库、order_id 恒为 pend:，其后一切按网关单号的推进/撤单/对账 UPDATE 永不命中。
// 现守卫改 `<`：等秩必须回填成功（order_id 换真实单号、状态原地"已报"），
// 且低秩晚到仍被拦（FIX#2 语义不回归）。
// English: §U-1 regression — the placeholder row and the first gateway-id backfill both carry 已报
// (equal rank); the old `<=` guard silently killed that path, leaving order_id stuck at pend:.
// With the `<` guard the same-rank backfill must land (id swapped, status untouched), and a late
// lower-rank backfill is still refused (FIX#2 semantics preserved).
func TestUpdateRealOrderBySignalIDSameRankBackfill(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:S3", SignalID: "S3", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-14T09:30:00+08:00", UserID: "u1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 首次回填：网关受理返回真实单号，状态仍"已报"（等秩）——必须落库
	if err := db.UpdateRealOrderBySignalID("u1", "S3", "GW-3", "已报"); err != nil {
		t.Fatalf("same-rank backfill: %v", err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if os[0].OrderID != "GW-3" || os[0].Status != "已报" {
		t.Fatalf("等秩回填应换单号保状态, got %+v", os[0])
	}
	// 之后按网关单号推进到已成（R4-4 正常生命周期）
	if ok, err := db.UpdateRealOrderStatusMonotonic("u1", "GW-3", "已成"); err != nil || !ok {
		t.Fatalf("按网关单号推进已成应命中: ok=%v err=%v", ok, err)
	}
	// 再晚到的等秩回填（重试成功重复回调）不覆盖终态，也不改回 pend
	if err := db.UpdateRealOrderBySignalID("u1", "S3", "GW-LATE", "已报"); err != nil {
		t.Fatalf("late same-status backfill: %v", err)
	}
	os, _ = db.RealOrdersForUser("u1")
	if os[0].Status != "已成" || os[0].OrderID != "GW-3" {
		t.Fatalf("已成终态+原单号不得被低秩回填覆盖, got %+v", os[0])
	}
}

// TestSchemaMigrationP01P02 §P0-1/P0-2 旧库主键/唯一约束迁移：
// 模拟只含单 ts_code 主键的 real_positions 和单 signal_id 唯一的 orders，
// Open 后应自动重建为 (ts_code, user_id) 与 (user_id, signal_id)。
func TestSchemaMigrationP01P02(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "migrate.db")
	rawDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// 创建旧 schema
	if _, err := rawDB.Exec(`
		CREATE TABLE real_positions (
			ts_code TEXT PRIMARY KEY,
			name TEXT DEFAULT '',
			qty INTEGER NOT NULL DEFAULT 0,
			cost_price REAL NOT NULL DEFAULT 0,
			amount REAL NOT NULL DEFAULT 0,
			highest_price REAL NOT NULL DEFAULT 0,
			strategy TEXT DEFAULT '',
			signal_id TEXT DEFAULT '',
			updated_at TEXT NOT NULL,
			user_id TEXT DEFAULT ''
		);
		CREATE TABLE orders (
			order_id TEXT PRIMARY KEY,
			signal_id TEXT UNIQUE,
			code TEXT NOT NULL,
			side TEXT NOT NULL,
			status TEXT NOT NULL,
			price REAL,
			qty INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			user_id TEXT DEFAULT ''
		);
	`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	rawDB.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open after migration: %v", err)
	}
	defer db.Close()

	// 迁移后应支持多账号同股票
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Qty: 100, UserID: "u1"},
		{TsCode: "600000.SH", Qty: 200, UserID: "u2"},
	}); err != nil {
		t.Fatalf("multi-tenant positions upsert: %v", err)
	}
	all, _ := db.RealPositions()
	if len(all) != 2 {
		t.Fatalf("应存在 2 条持仓, got %d", len(all))
	}

	// 迁移后应支持同 signal_id 不同账号
	if _, err := db.UpsertRealOrder(RealOrder{OrderID: "O1", SignalID: "SIG-X", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-28T10:00:00+08:00", UserID: "u1"}); err != nil {
		t.Fatalf("u1 order: %v", err)
	}
	if _, err := db.UpsertRealOrder(RealOrder{OrderID: "O2", SignalID: "SIG-X", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-08-28T10:00:00+08:00", UserID: "u2"}); err != nil {
		t.Fatalf("u2 order: %v", err)
	}
	orders, _ := db.RealOrders()
	if len(orders) != 2 {
		t.Fatalf("应存在 2 条委托, got %d", len(orders))
	}
}

// TestApplyRealFillIdempotentDuplicate §WS-A A2：同笔成交（order_id+traded_at+price+qty 复合键）
// 二次投递必须判为重复且不二次累加持仓（原实现依赖 SQLite 错误文案匹配，本用例验证结构化判重）。
func TestApplyRealFillIdempotentDuplicate(t *testing.T) {
	db := testDB(t)
	base := RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-08-20 09:35:00", SignalID: "SIG1"}
	if err := db.ApplyRealFill(base); err != nil {
		t.Fatalf("first: %v", err)
	}
	// 同笔再投递
	if err := db.ApplyRealFill(base); err != nil {
		t.Fatalf("duplicate should be idempotent-success, got err=%v", err)
	}
	p, _ := db.RealPositionByCode("600000.SH")
	if p.Qty != 100 {
		t.Fatalf("重复投递后持仓应仍为 100, got %d", p.Qty)
	}
	fills, _ := db.RealFills()
	if len(fills) != 1 {
		t.Fatalf("fills 应只有 1 条, got %d", len(fills))
	}
	// 同 order_id 但不同 traded_at（部分成交第二笔）应正常累加
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-08-20 09:36:00", SignalID: "SIG1"}); err != nil {
		t.Fatalf("partial second: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Qty != 200 {
		t.Fatalf("部分成交第二笔应累加至 200, got %d", p.Qty)
	}
}

// TestBuyDateAndT1Sellable §WS-A A5：建仓写 buy_date；T+1 可卖量=持仓−当日买入（按账号隔离）。
func TestBuyDateAndT1Sellable(t *testing.T) {
	db := testDB(t)
	// u1 当日买入 100（T+1 锁定）
	if err := db.ApplyRealFill(RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100,
		Amount: 1000, TradedAt: "2026-09-08 09:35:00", SignalID: "SIG1", UserID: "u1"}); err != nil {
		t.Fatalf("open u1: %v", err)
	}
	// 隔夜加仓 50（前一日买入，可卖）
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "买入", Price: 12, Qty: 50,
		Amount: 600, TradedAt: "2026-09-07 14:00:00", SignalID: "SIG2", UserID: "u1"}); err != nil {
		t.Fatalf("open overnight: %v", err)
	}
	if got := db.TodayBoughtQty("u1", "600000.SH", "2026-09-08"); got != 100 {
		t.Fatalf("今日买入应 100, got %d", got)
	}
	// 可卖 = 150 − 100 = 50（当日买入的 100 股 T+1 锁定）
	if got := db.BuyableQtyForUserSell("u1", "600000.SH", "2026-09-08"); got != 50 {
		t.Fatalf("可卖应 50, got %d", got)
	}
	// 账号隔离：u2 看不到 u1 的买入
	if got := db.TodayBoughtQty("u2", "600000.SH", "2026-09-08"); got != 0 {
		t.Fatalf("u2 今日买入应 0, got %d", got)
	}
}

// TestScopedDeleteAfterClose §WS-A A3：跨账号清仓互不影响——u1 清仓只删 u1（含遗留全局）行，
// 绝不删除 u2 的同码持仓。
func TestScopedDeleteAfterClose(t *testing.T) {
	db := testDB(t)
	mk := func(uid string) {
		if err := db.ApplyRealFill(RealFill{OrderID: uid + "-B", Code: "600000.SH", Side: "买入", Price: 10, Qty: 100,
			Amount: 1000, TradedAt: "2026-09-07 09:35:00", SignalID: uid + "-S", UserID: uid}); err != nil {
			t.Fatalf("open %s: %v", uid, err)
		}
	}
	mk("u1")
	mk("u2")
	// u1 全卖清仓
	if err := db.ApplyRealFill(RealFill{OrderID: "u1-S1", Code: "600000.SH", Side: "卖出", Price: 11, Qty: 100,
		Amount: 1100, TradedAt: "2026-09-08 10:00:00", SignalID: "u1-SELL", UserID: "u1"}); err != nil {
		t.Fatalf("close u1: %v", err)
	}
	// u2 的持仓必须还在
	p2, err := db.RealPositionByCodeForUser("u2", "600000.SH")
	if err != nil || p2.Qty != 100 {
		t.Fatalf("u2 持仓应保留 100, got %+v err=%v", p2, err)
	}
	_, err = db.RealPositionByCodeForUser("u1", "600000.SH")
	if err != sql.ErrNoRows {
		t.Fatalf("u1 清仓后应无持仓, err=%v", err)
	}
}

// TestRealPositionsBuyDateRoundTrip §PROD-T1（2026-09-18 生产实录）：RealPositionsForUser
// 必须回读 buy_date——实盘建议层依赖开仓日判定"持仓超期离场"，且券商快照对账建的历史行
// （无 buy_date）要读出空串=未知，不得伪造。加仓成交不改写既有开仓日（保守取最早）。
// English: §PROD-T1 — RealPositionsForUser must expose buy_date (first-buy fill date),
// empty for broker-snapshot rows; later adds never rewrite the recorded opening date.
func TestRealPositionsBuyDateRoundTrip(t *testing.T) {
	db := testDB(t)
	// 券商快照对账先行：全量语义会删除快照外持仓，故先建"无 buy_date 的历史行"再走成交建仓
	if _, err := db.ReconcilePositionsForUser("u_boss", []RealPosition{{TsCode: "600000.SH", Name: "浦发", Qty: 300, CostPrice: 10}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := db.ApplyRealFill(RealFill{OrderID: "A7O1", Code: "603468.SH", Name: "津富士达", Side: "买入", Price: 22.61, Qty: 100, Amount: 2261, TradedAt: "2026-09-18 09:40:00", UserID: "u_boss", SignalID: "SIG-A7-1"}); err != nil {
		t.Fatalf("buy fill: %v", err)
	}
	// 同日加仓：开仓日保持首笔成交日期，不得被清空或改写
	if err := db.ApplyRealFill(RealFill{OrderID: "A7O2", Code: "603468.SH", Name: "津富士达", Side: "买入", Price: 23.00, Qty: 100, Amount: 2300, TradedAt: "2026-09-18 10:00:00", UserID: "u_boss", SignalID: "SIG-A7-2"}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	ps, err := db.RealPositionsForUser("u_boss")
	if err != nil || len(ps) != 2 {
		t.Fatalf("期望 2 持仓, got %d err=%v", len(ps), err)
	}
	var filled, snap *RealPosition
	for i := range ps {
		switch ps[i].TsCode {
		case "603468.SH":
			filled = &ps[i]
		case "600000.SH":
			snap = &ps[i]
		}
	}
	if filled == nil || filled.BuyDate != "2026-09-18" {
		t.Fatalf("成交建仓行 buy_date 应为 2026-09-18, got %+v", filled)
	}
	if snap == nil || snap.BuyDate != "" {
		t.Fatalf("券商快照行 buy_date 应为空(未知), got %+v", snap)
	}
}

// TestLocalBuyFrozenDayFilter §C1（2026-09-22 修复批）反例锁：冻结账必须只统计**当日**
// 已报/部成买单——前日滞留行绝不许跨日占用预算（旧 SQL 收了 day 参数却不用，卡死买单
// 永久锁死 daily_budget_amount）；撤单后即时释放；两种存量 created_at 格式（RFC3339 /
// 空格分隔）都须命中日期前缀；查询错误必须上抛而非吞成 0。
// English: §C1 regression — frozen amount is scoped to `day` (both stored timestamp shapes),
// releases on cancel, and surfaces query errors instead of silently returning 0.
func TestLocalBuyFrozenDayFilter(t *testing.T) {
	db := testDB(t)
	up := func(o RealOrder) {
		t.Helper()
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("upsert order %s: %v", o.SignalID, err)
		}
	}
	base := RealOrder{Code: "600000.SH", Side: "买入", Price: 10, UserID: "u1"}
	// 今日(RFC3339) 已报 100 股 → 全额 1000 计入冻结
	o1 := base
	o1.OrderID, o1.SignalID, o1.Status, o1.Qty = "O1", "S1", "已报", 100
	o1.CreatedAt = "2026-09-22T09:35:00+08:00"
	up(o1)
	// 今日(空格格式) 部成 300 股，其中 200 已成交 → 剩余 100 股计 1000
	o2 := base
	o2.OrderID, o2.SignalID, o2.Status, o2.Qty = "O2", "S2", "部成", 300
	o2.CreatedAt = "2026-09-22 10:00:00"
	up(o2)
	if err := db.ApplyRealFill(RealFill{OrderID: "O2", Code: "600000.SH", Side: "买入", Price: 10, Qty: 200, Amount: 2000, TradedAt: "2026-09-22 10:05:00", SignalID: "S2", UserID: "u1"}); err != nil {
		t.Fatalf("partial fill: %v", err)
	}
	// 前日 已报 1000 股：旧缺陷会永久计入，现必须被日期过滤排除
	o3 := base
	o3.OrderID, o3.SignalID, o3.Status, o3.Qty = "O3", "S3", "已报", 1000
	o3.CreatedAt = "2026-09-21T14:50:00+08:00"
	up(o3)

	frozen, err := db.LocalBuyFrozen("u1", "2026-09-22")
	if err != nil {
		t.Fatalf("frozen: %v", err)
	}
	if want := 2000.0; frozen != want {
		t.Fatalf("当日冻结应为 %v（S1 全额+S2 剩余），got %v（含前日僵尸行即 C1 复发）", want, frozen)
	}
	// 撤单即释放：S1 → 已撤 后冻结只剩 S2 的 1000
	if ok, err := db.UpdateRealOrderStatusMonotonic("u1", "O1", "已撤"); err != nil || !ok {
		t.Fatalf("撤单推进: ok=%v err=%v", ok, err)
	}
	frozen, _ = db.LocalBuyFrozen("u1", "2026-09-22")
	if frozen != 1000.0 {
		t.Fatalf("撤单后冻结应释放至 1000，got %v", frozen)
	}
	// 查询错误必须上抛（fail-closed 前置）：关库后调用得 err≠nil 而非 (0,nil)
	closed := testDB(t)
	_ = closed.db.Close()
	if _, err2 := closed.LocalBuyFrozen("u1", "2026-09-22"); err2 == nil {
		t.Fatal("库不可读时必须返回错误（旧实现吞错回 0 = fail-open 放水）")
	}
}

// TestSweepStaleBuyOrders §C1b（2026-09-22 修复批）反例锁：跨日仍停在 已报/部成 的
// 买单无条件降级 废单——买方向、严格早于 beforeDay 才动；当日行、卖方向、终态行、
// created_at 空值行一律不碰。
// English: §C1b regression — cross-day open buys demote to terminal 废单; today/sell/terminal/
// blank-timestamp rows stay untouched.
func TestSweepStaleBuyOrders(t *testing.T) {
	db := testDB(t)
	up := func(o RealOrder) {
		t.Helper()
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("upsert %s: %v", o.SignalID, err)
		}
	}
	up(RealOrder{OrderID: "O1", SignalID: "S1", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-21T14:50:00+08:00", UserID: "u1"})
	up(RealOrder{OrderID: "O2", SignalID: "S2", Code: "600000.SH", Side: "买入", Status: "部成", Price: 10, Qty: 100, CreatedAt: "2026-09-20 14:50:00", UserID: "u1"})
	up(RealOrder{OrderID: "O3", SignalID: "S3", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-22T09:35:00+08:00", UserID: "u1"})
	up(RealOrder{OrderID: "O4", SignalID: "S4", Code: "600000.SH", Side: "卖出", Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-21T14:50:00+08:00", UserID: "u1"})
	up(RealOrder{OrderID: "O5", SignalID: "S5", Code: "600000.SH", Side: "买入", Status: "已成", Price: 10, Qty: 100, CreatedAt: "2026-09-21T14:50:00+08:00", UserID: "u1"})
	up(RealOrder{OrderID: "O6", SignalID: "S6", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: "", UserID: "u1"})

	n, err := db.SweepStaleBuyOrders("u1", "2026-09-22")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("应恰降级 O1/O2 两笔跨日买单，got %d", n)
	}
	status := map[string]string{}
	orders, _ := db.RealOrdersForUser("u1")
	for _, o := range orders {
		status[o.OrderID] = o.Status
	}
	if status["O1"] != "废单" || status["O2"] != "废单" {
		t.Fatalf("跨日买单应变废单: O1=%s O2=%s", status["O1"], status["O2"])
	}
	if status["O3"] != "已报" {
		t.Fatalf("当日买单不许动，got %s", status["O3"])
	}
	if status["O4"] != "已报" {
		t.Fatalf("卖方向不在本路管辖，got %s", status["O4"])
	}
	if status["O5"] != "已成" || status["O6"] != "已报" {
		t.Fatalf("终态/时间戳空行不许动: O5=%s O6=%s", status["O5"], status["O6"])
	}
	// beforeDay 为空 = 不清扫（防御）
	if n, err := db.SweepStaleBuyOrders("u1", ""); err != nil || n != 0 {
		t.Fatalf("空 beforeDay 应无操作: n=%d err=%v", n, err)
	}
}

// TestApplyRealFillBuyCostIncludesFee §F1（2026-09-22 修复批）反例锁：买入成交的佣金
// 必须摊入持仓成本（含费加权平均，与 paper `p.Cost += cost + fee` 同口径）——
// 首仓每股成本 =(成交额+fee)/量；加仓 =(旧含费账+成交额+fee)/新量；印花税属卖出腿不摊买。
// 旧实现成本只记成交均价，费用腿凭空蒸发，实盘账面系统性偏乐观。
// English: §F1 regression — buy commission amortizes into position cost (fee-inclusive weighted
// average, same convention as paper); stamp tax (a sell-leg tax) stays out of buy cost.
func TestApplyRealFillBuyCostIncludesFee(t *testing.T) {
	db := testDB(t)
	if err := db.ApplyRealFill(RealFill{OrderID: "F-F1-1", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-09-22 09:31:00", UserID: "u1"}); err != nil {
		t.Fatalf("buy#1: %v", err)
	}
	p, err := db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("read pos: %v", err)
	}
	if p.CostPrice != 10.05 || p.Amount != 1005 {
		t.Fatalf("首仓应含费摊薄 cost=10.05/amount=1005, got %v/%v", p.CostPrice, p.Amount)
	}
	// 加仓 100@12 fee=6 → 含费总账 1005+1200+6=2211 → 每股 11.055
	if err := db.ApplyRealFill(RealFill{OrderID: "F-F1-2", Code: "600519.SH", Side: "买入",
		Price: 12, Qty: 100, Amount: 1200, Fee: 6, TradedAt: "2026-09-22 10:00:00", UserID: "u1"}); err != nil {
		t.Fatalf("buy#2: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.CostPrice != 11.055 || p.Amount != 2211 {
		t.Fatalf("加仓含费加权应得 11.055/2211, got %v/%v", p.CostPrice, p.Amount)
	}
	// 卖出费用腿不改持仓成本（印花税/佣金只进成交行，供盈亏统计扣减）
	if err := db.ApplyRealFill(RealFill{OrderID: "F-F1-3", Code: "600519.SH", Side: "卖出",
		Price: 13, Qty: 100, Amount: 1300, Fee: 6.5, StampTax: 6.5, TradedAt: "2026-09-22 14:00:00", UserID: "u1"}); err != nil {
		t.Fatalf("sell: %v", err)
	}
	if p, _ = db.RealPositionByCode("600519.SH"); p.Qty != 100 || p.CostPrice != 11.055 {
		t.Fatalf("卖出不改写每股成本, got qty=%d cost=%v", p.Qty, p.CostPrice)
	}
	// RealFills 读回必须带 fee/stamp_tax/user_id 腿（旧 SELECT 丢列——重放拿不到费用）
	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("fills: %v", err)
	}
	if len(fills) != 3 {
		t.Fatalf("应有 3 笔成交, got %d", len(fills))
	}
	var sawBuyFee, sawSellStamp bool
	for _, f := range fills {
		if f.OrderID == "F-F1-1" && f.Fee == 5 && f.UserID == "u1" {
			sawBuyFee = true
		}
		if f.OrderID == "F-F1-3" && f.StampTax == 6.5 && f.Fee == 6.5 {
			sawSellStamp = true
		}
	}
	if !sawBuyFee || !sawSellStamp {
		t.Fatalf("RealFills 费用腿回读失败: buyFee=%v sellStamp=%v", sawBuyFee, sawSellStamp)
	}
}

// TestReconcileRejectsInvalidTsCode §F2（2026-09-22 修复批）：对账入口字段级校验——
// 任一行 ts_code 为空或非法格式 → 整批拒收（ErrInvalidPositionReport），一行都不落库。
// 锤实形态：一行 ts_code=” 垃圾行会让上层「本地有仓+空快照 409 守卫」永久误触发，
// 真实全平无法经对账通道落账；且空主键行本身即脏数据。
// English: §F2 — any blank/malformed ts_code rejects the whole snapshot, nothing is written.
func TestReconcileRejectsInvalidTsCode(t *testing.T) {
	db := testDB(t)
	// 合法行 + 一行空 ts_code + 一行缺后缀：混批必须整体拒收
	bad := []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10},
		{TsCode: "", Name: "垃圾行", Qty: 100, CostPrice: 1},
		{TsCode: "600519", Name: "缺后缀", Qty: 100, CostPrice: 1},
	}
	if _, err := db.ReconcilePositionsForUser("u_f2", bad); err == nil {
		t.Fatal("混入非法 ts_code 的快照应整批拒收")
	} else if !errors.Is(err, ErrInvalidPositionReport) {
		t.Fatalf("拒收错误应包装 ErrInvalidPositionReport, got %v", err)
	}
	if _, err := db.UpsertRealPositions(bad); err == nil || !errors.Is(err, ErrInvalidPositionReport) {
		t.Fatalf("UpsertRealPositions 同样必须整批拒收, got %v", err)
	}
	// 一行都未落库（含批内合法行）
	all, err := db.RealPositions()
	if err != nil || len(all) != 0 {
		t.Fatalf("拒收后不得有任何落库, rows=%+v err=%v", all, err)
	}
	// 合法格式（含北交所 920 前缀 / 空快照=合法全平）放行
	ok := []RealPosition{
		{TsCode: "920001.BJ", Name: "北交所", Qty: 100, CostPrice: 10},
		{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 12},
	}
	if n, err := db.ReconcilePositionsForUser("u_f2", ok); err != nil || n != 2 {
		t.Fatalf("合法快照应正常对账, n=%d err=%v", n, err)
	}
	if _, err := db.ReconcilePositionsForUser("u_f2", nil); err != nil {
		t.Fatalf("空快照（合法全平语义）不应被校验拒收: %v", err)
	}
}

// TestReconcileDoesNotWashStrategyAttribution §M5（2026-09-22 修复批）：券商对账快照不带
// strategy/signal_id，旧实现无保护覆写会把本地战法归因洗成空串（对照同函数 highest_price
// 有 CASE 保护）。现仅当来源字段非空才覆盖；来源非空时新归因仍然生效（覆盖式修正）。
// English: §M5 — reconcile must not wipe local strategy/signal_id when the snapshot
// carries empty values; a non-empty snapshot value still wins.
func TestReconcileDoesNotWashStrategyAttribution(t *testing.T) {
	db := testDB(t)
	// 本地持仓带战法归因（模拟 ApplyRealFill 建仓后的行）
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Strategy: "N字反包", SignalID: "buy:600000"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 快照不带 strategy/signal_id（券商口径），仅数量变化
	if _, err := db.ReconcilePositionsForUser("", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 120, CostPrice: 10},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if p.Qty != 120 {
		t.Fatalf("数量应被快照刷新为 120, got %d", p.Qty)
	}
	if p.Strategy != "N字反包" || p.SignalID != "buy:600000" {
		t.Fatalf("§M5 空快照归因被洗白: strategy=%q signal_id=%q", p.Strategy, p.SignalID)
	}
	// 快照携带非空 strategy/signal_id → 新值覆盖（归因修正通道不被守卫误锁）
	if _, err := db.ReconcilePositionsForUser("", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 120, CostPrice: 10, Strategy: "龙头首阴", SignalID: "manual-fix"},
	}); err != nil {
		t.Fatalf("reconcile2: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Strategy != "龙头首阴" || p.SignalID != "manual-fix" {
		t.Fatalf("非空来源字段应覆盖旧值: strategy=%q signal_id=%q", p.Strategy, p.SignalID)
	}
	// UpsertRealPositions 的 strategy 同样受洗白保护
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10},
	}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	p, _ = db.RealPositionByCode("600000.SH")
	if p.Strategy != "龙头首阴" {
		t.Fatalf("§M5 UpsertRealPositions 空 strategy 不应洗掉归因, got %q", p.Strategy)
	}
}

// TestSumOpenSellQtyNet §N-3（2026-09-22 傍晚批复验）行为用例：当日在途卖量必须按
// 「每笔委托的未成交余量」计，而不是整笔委托量——调用方 realSoldOrOpenQtyToday 的算式是
// 「持仓 − Σ已成交 − Σ在途」，Σ已成交 已经数过部成单的那部分成交，整笔口径等于同一笔成交
// 扣两次，剩余量被压成负数 → 当天永不补卖（少卖=敞口留过夜）。
// 断言面：部成净额 / 回填失败（order_id 停留 pend: 前缀）靠 signal_id 关联成交 / 终态不计 /
// 发送失败不计 / 跨日不计 / 买入方向不计 / 他账号不串账 / filled>qty 异常钳 0。
// English: §N-3 — SumOpenSellQty must sum each open sell at its unfilled remainder (net of that
// ticket's own fills), matching the buy-side §BUDGET_FREEZE definition of 部成.
func TestSumOpenSellQtyNet(t *testing.T) {
	db := testDB(t)
	const day = "2026-09-22"
	odr := func(uid, orderID, sid, code, side, status string, qty int, createdAt string) {
		t.Helper()
		if _, err := db.UpsertRealOrder(RealOrder{OrderID: orderID, SignalID: sid, Code: code,
			Side: side, Status: status, Price: 10, Qty: qty, CreatedAt: createdAt, UserID: uid}); err != nil {
			t.Fatalf("insert order %s: %v", sid, err)
		}
	}
	fil := func(orderID, sid, code string, qty int) {
		t.Helper()
		if _, err := db.db.Exec(`INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id)
			VALUES (?, ?, '卖出', 10, ?, 10*?, ?, ?, 'u1')`,
			orderID, code, qty, qty, day+" 09:40:00", sid); err != nil {
			t.Fatalf("insert fill %s: %v", sid, err)
		}
	}

	// ① 挂 1000 部成 500：在途只占 500（旧口径会占满 1000 → 与 Σ已成交 重复扣 500）
	sid1 := "sell:600000:止损:" + day
	odr("u1", "GW1", sid1, "600000.SH", "卖出", "部成", 1000, day+" 09:35:00")
	fil("GW1", sid1+":r1000", "600000.SH", 500)
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 500 {
		t.Fatalf("① 部成 500 的 1000 股在途卖单应按净额 500 计, got %d", got)
	}
	// ② 同一 base 的第二桶（补卖 500 全部未成）：净额相加 = 500 + 500
	sid2 := sid1 + ":r500"
	odr("u1", "GW2", sid2, "600000.SH", "卖出", "已报", 500, day+" 09:50:00")
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 1000 {
		t.Fatalf("② 两笔在途（净 500 + 未成 500）应合计 1000, got %d", got)
	}
	// ③ 首桶再部成 300（累计 800）：净额降到 200，总在途 700
	fil("GW1", sid1+":r1000", "600000.SH", 300)
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 700 {
		t.Fatalf("③ 累计成交 800 后首桶净额应为 200（+第二桶 500）=700, got %d", got)
	}
	// ④ 首桶全成（1000）：净额 0，只剩第二桶 500
	fil("GW1", sid1+":r1000", "600000.SH", 200)
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 500 {
		t.Fatalf("④ 全成桶应按 0 计（终态前也只计未成交余量）, got %d", got)
	}
	if _, err := db.UpdateRealOrderStatusMonotonic("u1", "GW1", "已成"); err != nil {
		t.Fatalf("mark filled: %v", err)
	}
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 500 {
		t.Fatalf("④ 已成终态后仍为 500, got %d", got)
	}

	// ⑤ 网关单号回填失败（本地 order_id 停留 pend: 前缀）：成交只能按 signal_id 关联，
	//    若只认 order_id 会得 0 成交 → 整笔占额，正是本条要根除的双扣形态。
	sid5 := "sell:600519:止盈:" + day
	odr("u1", "pend:"+sid5, sid5, "600519.SH", "卖出", "部成", 1000, day+" 10:00:00")
	fil("GW-elsewhere", sid5, "600519.SH", 400)
	if got := db.SumOpenSellQty("u1", "600519.SH", day); got != 600 {
		t.Fatalf("⑤ pend: 占位行需按 signal_id 关联成交（净额 600）, got %d", got)
	}

	// ⑥ 终态/失败/跨日/反向一律不占额度
	odr("u1", "GW6", "sell:000001:已撤:"+day, "000001.SZ", "卖出", "已撤", 500, day+" 09:31:00")
	odr("u1", "GW7", "sell:000001:发送失败:"+day, "000001.SZ", "卖出", "发送失败", 500, day+" 09:32:00")
	odr("u1", "GW8", "sell:000001:昨日:"+day, "000001.SZ", "卖出", "已报", 500, "2026-09-21 14:55:00")
	odr("u1", "GW9", "buy:000001:"+day, "000001.SZ", "买入", "已报", 500, day+" 09:33:00")
	if got := db.SumOpenSellQty("u1", "000001.SZ", day); got != 0 {
		t.Fatalf("⑥ 终态/发送失败/跨日/买入都不该占在途卖量, got %d", got)
	}

	// ⑦ 他账号不串账（1000 股在途属于 u2）
	odr("u2", "GW10", "sell:600000:other:"+day, "600000.SH", "卖出", "已报", 1000, day+" 09:36:00")
	if got := db.SumOpenSellQty("u1", "600000.SH", day); got != 500 {
		t.Fatalf("⑦ 他账号在途卖单不得计入, got %d", got)
	}
	if got := db.SumOpenSellQty("u2", "600000.SH", day); got != 1000 {
		t.Fatalf("⑦ u2 自身在途应为 1000, got %d", got)
	}

	// ⑧ 异常行 filled>qty（网关重放/交割单回灌）：净额钳 0，绝不给出负在途把剩余量抬高成超卖
	odr("u1", "GW11", "sell:000002:异常:"+day, "000002.SZ", "卖出", "部成", 300, day+" 09:37:00")
	fil("GW11", "sell:000002:异常:"+day, "000002.SZ", 500)
	if got := db.SumOpenSellQty("u1", "000002.SZ", day); got != 0 {
		t.Fatalf("⑧ filled>qty 异常行净额须钳 0, got %d", got)
	}
}

// TestReconcileKeepsFeeInclusiveCost §N-6（2026-09-22 傍晚批复验，owner 裁决 11=本地含费优先）：
// 对账快照的不含费 open_price 不得裸写覆盖成交回报算出的含费成本；本地成本为 0/缺失时快照
// 仍可兜底回填；amount 必须由「选定成本 × 快照数量」同源推导，杜绝 qty×cost≠amount 错配；
// 守卫丢弃快照值时必须留痕（计数，本批主题是「静默失效」）。
// English: §N-6 — reconcile keeps the local fee-inclusive cost basis (ruling 11), back-fills only
// when local cost is missing, derives amount from the chosen cost × snapshot qty, and counts drops.
func TestReconcileKeepsFeeInclusiveCost(t *testing.T) {
	db := testDB(t)
	// 建仓：100 股 @10 佣金 5 → 含费成本 10.05（ApplyRealFill §F1 口径）
	if err := db.ApplyRealFill(RealFill{OrderID: "N6-1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-09-22 09:31:00", UserID: "u1"}); err != nil {
		t.Fatalf("buy: %v", err)
	}
	// 券商快照：不含费 10.00 / amount 1000 / 数量 120（柜台加了 20 股）
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 120, CostPrice: 10, Amount: 1200, UserID: "u1"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p, err := db.RealPositionByCode("600000.SH")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if p.CostPrice != 10.05 {
		t.Fatalf("§N-6 含费成本 10.05 不得被不含费快照 10 覆盖, got %v", p.CostPrice)
	}
	if p.Qty != 120 || p.Amount != 10.05*120 {
		t.Fatalf("§N-6 amount 须=选定成本×快照数量(10.05×120=%v), got qty=%d amount=%v", 10.05*120, p.Qty, p.Amount)
	}
	if db.CostGuardDrops() != 1 {
		t.Fatalf("§N-6 守卫丢弃快照值必须留痕计数, got %d", db.CostGuardDrops())
	}
	// 快照缺成本（0/缺失）：本地含费值原样保留，绝不退化成 0
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 120, CostPrice: 0, Amount: 0, UserID: "u1"},
	}); err != nil {
		t.Fatalf("reconcile zero-cost snapshot: %v", err)
	}
	if p, _ = db.RealPositionByCode("600000.SH"); p.CostPrice != 10.05 || p.Amount != 10.05*120 {
		t.Fatalf("§N-6 快照成本为 0 时不得清零本地含费账, got %v/%v", p.CostPrice, p.Amount)
	}
	// 本地成本为 0（历史脏行/券商先建行）：快照仍是唯一纠正通道
	// （账号用 u9 隔离——UpsertRealPositions 是全量对账语义，混在同一账号会把上面的 600000 行删掉）
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "301002.SZ", Name: "脏行", Qty: 100, CostPrice: 0, Amount: 0, UserID: "u9"},
	}); err != nil {
		t.Fatalf("seed zero row: %v", err)
	}
	if _, err := db.ReconcilePositionsForUser("u9", []RealPosition{
		{TsCode: "301002.SZ", Name: "脏行", Qty: 100, CostPrice: 12.5, Amount: 1250},
	}); err != nil {
		t.Fatalf("reconcile backfill: %v", err)
	}
	if p, _ = db.RealPositionByCode("301002.SZ"); p.CostPrice != 12.5 || p.Amount != 1250 {
		t.Fatalf("§N-6 本地无成本时快照必须兜底回填 12.5/1250, got %v/%v", p.CostPrice, p.Amount)
	}
	// 券商快照 highest_price 更低时不得拉回已回写的锚点（§N-7 与 §N-6 同一条 upsert 的协调点）
	if raised, err := db.RaiseRealPositionHigh("u1", "600000.SH", 20); err != nil || !raised {
		t.Fatalf("§N-7 锚点回写应成功且报变更: raised=%v err=%v", raised, err)
	}
	if _, err := db.ReconcilePositionsForUser("u1", []RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 120, CostPrice: 10, Amount: 1200, HighestPrice: 11, UserID: "u1"},
	}); err != nil {
		t.Fatalf("reconcile anchor: %v", err)
	}
	if p, _ = db.RealPositionByCode("600000.SH"); p.HighestPrice != 20 {
		t.Fatalf("§N-7 已回写的移动止盈锚点不得被对账快照(11)拉回, got %v", p.HighestPrice)
	}
}

// TestRaiseRealPositionHighOnlyUp §N-7 账本侧单调语义：只增不减、无有效锚点（≤0）绝不落库、
// 遗留全局行（user_id=”）对账号查询同样可回写。
// English: §N-7 — the anchor write-back is strictly monotonic, refuses non-positive highs, and
// still reaches legacy global rows.
func TestRaiseRealPositionHighOnlyUp(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 12, UserID: "u1"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if raised, err := db.RaiseRealPositionHigh("u1", "600000.SH", 11); err != nil || raised {
		t.Fatalf("更低的锚点必须被只增守卫拒写: raised=%v err=%v", raised, err)
	}
	if p, _ := db.RealPositionByCode("600000.SH"); p.HighestPrice != 12 {
		t.Fatalf("只增语义不得改写现值, got %v", p.HighestPrice)
	}
	if raised, err := db.RaiseRealPositionHigh("u1", "600000.SH", 0); err != nil || raised {
		t.Fatalf("锚点≤0 应直接跳过（绝不写 0 失明下游）: raised=%v err=%v", raised, err)
	}
	if raised, err := db.RaiseRealPositionHigh("u1", "600000.SH", 18); err != nil || !raised {
		t.Fatalf("更高锚点应写入并报告变更: raised=%v err=%v", raised, err)
	}
	if p, _ := db.RealPositionByCode("600000.SH"); p.HighestPrice != 18 {
		t.Fatalf("锚点应为 18, got %v", p.HighestPrice)
	}
	// 遗留全局行（user_id 空串）：按任意账号回写都要命中
	if _, err := db.db.Exec(`INSERT INTO real_positions(ts_code, name, qty, cost_price, amount, highest_price, user_id, updated_at)
		VALUES ('000001.SZ','平安',100,20,2000,21,'','2026-09-22 09:30:00')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if raised, err := db.RaiseRealPositionHigh("u1", "000001.SZ", 25); err != nil || !raised {
		t.Fatalf("遗留全局行应可回写: raised=%v err=%v", raised, err)
	}
	// 他账号私有行不得被误改
	if _, err := db.db.Exec(`INSERT INTO real_positions(ts_code, name, qty, cost_price, amount, highest_price, user_id, updated_at)
		VALUES ('000002.SZ','万科',100,5,500,6,'u2','2026-09-22 09:30:00')`); err != nil {
		t.Fatalf("seed u2 row: %v", err)
	}
	if raised, err := db.RaiseRealPositionHigh("u1", "000002.SZ", 99); err != nil || raised {
		t.Fatalf("跨账号不得改写他人行: raised=%v err=%v", raised, err)
	}
	if p, _ := db.RealPositionByCode("000002.SZ"); p.HighestPrice != 6 {
		t.Fatalf("u2 行 highest_price 应保持 6, got %v", p.HighestPrice)
	}
}
