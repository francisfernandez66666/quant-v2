// t1_partial_test.go — §T1-PARTIAL（2026-10-24 裁决三）模拟盘层账与残量意图的行为锁。
//
// 这一批锁的是四件在改前**根本不存在**的事，所以每条都必须能区分"改前/改后"：
//
//	① 可卖量＝底仓量（**等值**，不是 ≥）：当日加仓后同一持仓里"昨日底仓 + 今日机动仓"可分，
//	   这正是改前那个真缺陷的收口——旧实现加仓不刷新 FilledAt，整仓被按旧日期判"全可卖"（漏放）；
//	② close 撞 T+1 时**减量成交**并把残量登记成次日无条件执行的意图，次日执行完意图归零；
//	③ 意图的合并规则：同码反复触发只留一条，**保留最初触发日与到期日**（不许被信号无限顺延），
//	   且清仓档不被随后的减仓档降级；
//	④ 意图与"每日一次减仓"（trimDone）互不吞：trim 成功照常烧槽、不产生意图；close 残量登记意图
//	   但不烧 trim 槽。
//
// English: behavioural locks for ruling #3 — the per-batch ledger (sellable == base layers, exactly),
// the degraded close that parks its residual as a next-day intent, the intent merge rule (window never
// extended, close outranks trim), and the fact that the residual queue and the once-a-day trim slot
// never swallow each other.
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// partialTestEngine 层账用例的夹具：与 sellTestEngine 同形，但**关掉再入场冷却**——
// 冷却会在"次日续卖/买回"这类用例里把持仓路径挡掉，属于另一条闸，与本文件的被测面无关。
// English: same fixture as sellTestEngine with the re-entry cooldown disabled — the cooldown is a
// different gate and would mask the lot-ledger behaviour under test.
func partialTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := sellTestEngine(t, true)
	e.cfg.ReentryCooldownMin = 0
	return e
}

// addToday 在当日给已有持仓加仓 qty 股（显式股数只在非 dynamic 口径下生效，夹具正是该口径）。
// English: adds qty shares to an existing position today (explicit qty only works outside dynamic mode,
// which is the fixture's mode).
func addToday(t *testing.T, e *Engine, qty int) {
	t.Helper()
	if err := e.BuyExInPool("300001", "测试股", "龙头", "dragon", 0, 11, qty, nil); err != nil {
		t.Fatalf("当日加仓 %d 股失败: %v", qty, err)
	}
}

// TestT1PartialSellableEqualsBaseLayer ① 可卖量＝底仓量（等值锁）。
// 构造：昨日成交 1000 股（t1Ready 回拨）＋ 当日加仓 500 股 ⇒ 持仓 1500，可卖必须**恰好 1000**。
// 反向自证写在断言里：若有人把 T+1 判据退回"整仓一个时间戳"，这里会读成 1500（漏放）；
// 若有人把可卖量按整仓比例折算，会读到别的数——两个方向都过不去。
// English: sellable must equal the base layer exactly (1000 of 1500). A regression to the old
// whole-position timestamp would read 1500 (the leak this batch closed); any proportional guess fails too.
func TestT1PartialSellableEqualsBaseLayer(t *testing.T) {
	e := partialTestEngine(t)
	buyFill(t, e) // 当日 1000 股
	t1Ready(e)    // 变成"昨日底仓"
	addToday(t, e, 500)

	p, ok := e.positions["300001"]
	if !ok || p.Qty != 1500 {
		t.Fatalf("混合持仓应为 1500 股, got %+v", p)
	}
	if got := e.sellableQtyLocked(p, time.Now()); got != 1000 {
		t.Fatalf("可卖量必须**等值**于昨日底仓 1000（不是整仓 1500）, got %d", got)
	}
	layers := LayersOfPosition(p, time.Now())
	if layers.BaseQty != 1000 || layers.IntradayQty != 500 || layers.TotalQty != 1500 {
		t.Fatalf("层账应为 底仓1000/机动500/总1500, got %+v", layers)
	}
	if layers.Sellable != 1000 {
		t.Fatalf("层账可卖量应 1000, got %d", layers.Sellable)
	}
	// 层账不得成为第三个价格口径：WeightedCost 必须回摊薄成本（§FIX#3 不变）。
	if layers.WeightedCost != p.CostPrice {
		t.Fatalf("WeightedCost 必须取摊薄 CostPrice（%v），got %v", p.CostPrice, layers.WeightedCost)
	}
}

// TestT1PartialLegacyPositionSeedsOldShares ① 存量持仓（无层账）首笔加仓前，旧股必须先补成一批。
// 不补的话层账只覆盖新加那笔，可卖量会凭空少掉旧股——"该卖的卖不掉"，方向与漏放相反但同样危险。
// English: a legacy position with no ledger must have its existing shares seeded as a batch on the first
// add-on, otherwise sellable silently drops the older shares.
func TestT1PartialLegacyPositionSeedsOldShares(t *testing.T) {
	e := partialTestEngine(t)
	buyFill(t, e)
	t1Ready(e)
	p := e.positions["300001"]
	// 模拟本批之前落盘的存量持仓：清空层账（旧 JSON 没有 lots 字段）。
	p.Lots = nil
	if got := e.sellableQtyLocked(p, time.Now()); got != 1000 {
		t.Fatalf("无层账的存量持仓应按整仓二值口径判可卖 1000, got %d", got)
	}
	addToday(t, e, 500)
	if len(p.Lots) != 2 {
		t.Fatalf("首笔加仓应补出两批（旧股+新买）, got %d 批 %+v", len(p.Lots), p.Lots)
	}
	if p.Lots[0].Qty != 1000 || p.Lots[1].Qty != 500 {
		t.Fatalf("批次应为 [1000, 500], got %+v", p.Lots)
	}
	if got := e.sellableQtyLocked(p, time.Now()); got != 1000 {
		t.Fatalf("补批后可卖量应仍为 1000（旧股没被吃掉）, got %d", got)
	}
}

// TestT1PartialCloseSellsBaseAndParksResidual ② close 撞 T+1：卖可卖的、残量登记意图、次日清零。
// 这是裁决三的主形态（"加仓后遇到退出，先只卖可卖部分"），也是"退出被 T+1 整单挡死"的收口。
// English: ruling #3's main shape — a close sells the sellable part, parks the residual, and the next
// trading day the intent empties the position.
func TestT1PartialCloseSellsBaseAndParksResidual(t *testing.T) {
	e := partialTestEngine(t)
	buyFill(t, e) // 当日 1000
	t1Ready(e)    // 昨日底仓 1000
	addToday(t, e, 500)

	if !e.ApplyUnifiedSell("300001", "close", 11, "[测试] 清仓裁决") {
		t.Fatal("持仓在账，close 处置应受理")
	}
	p := e.positions["300001"]
	if p == nil || p.Qty != 500 {
		t.Fatalf("close 应只卖掉可卖的 1000 股、余 500 股当日仓, got %+v", p)
	}
	if got := len(e.pendingExits); got != 1 {
		t.Fatalf("残量意图应恰好 1 条, got %d", got)
	}
	ex := e.pendingExits["300001"]
	if ex == nil || ex.WantQty != 500 || ex.Kind != pendingExitClose {
		t.Fatalf("意图应为 close 档 500 股, got %+v", ex)
	}
	if e.trimDone["300001"] != "" {
		t.Fatal("close 残量不得顺手烧掉 trim 的当日槽（两套去重互不吞）")
	}
	// 当日不自家补卖：意图登记当日必须跳过（残量正是当日买入，本来就不可卖）。
	e.RunPendingExits(quotesOf(11))
	if p := e.positions["300001"]; p == nil || p.Qty != 500 {
		t.Fatalf("登记当日不得补卖, got %+v", p)
	}

	// 模拟次一交易日：把当日批次与意图的触发日一起回拨（真实运行里由日期自然翻页）。
	yesterday := cntime.DayCompactOf(time.Now().AddDate(0, 0, -1))
	e.mu.Lock()
	for i := range p.Lots {
		p.Lots[i].FilledAt = time.Now().AddDate(0, 0, -1)
	}
	p.FilledAt = time.Now().AddDate(0, 0, -1)
	e.pendingExits["300001"].FiredDay = yesterday
	e.mu.Unlock()

	e.RunPendingExits(quotesOf(11))
	if _, held := e.positions["300001"]; held {
		t.Fatalf("次日残量应被卖光并删除持仓, got %+v", e.positions["300001"])
	}
	if got := len(e.pendingExits); got != 0 {
		t.Fatalf("执行完意图应清零, got %d", got)
	}
	// 留痕：次日那笔必须落成 sell 订单（否则事后看不出残量卖没卖）。
	last := e.orders[len(e.orders)-1]
	if last.Side != "sell" || last.Qty != 500 {
		t.Fatalf("次日残量应留 500 股卖单, got %+v", last)
	}
}

// TestT1PartialIntentMergeKeepsWindow ③ 意图合并规则：反复触发不得无限顺延有效期，
// 且清仓档不被随后的减仓档降级（否则"该清仓的"会被一条减仓信号改小目标）。
// English: the merge rule — repeated triggers must not extend the window, and close outranks trim.
func TestT1PartialIntentMergeKeepsWindow(t *testing.T) {
	e := partialTestEngine(t)
	now := time.Now()
	e.recordPendingExitLocked("300001", 500, pendingExitClose, "首触发", now)
	first := e.pendingExits["300001"]
	if first == nil {
		t.Fatal("首条意图应登记")
	}
	fired, expire := first.FiredDay, first.ExpireOn

	// 隔一天再来一条减仓档：残量刷新为 300，但档位不得从 close 降级，**窗口不得顺延**。
	// 这里刻意用"次日"而不是"同日再触发"——同一天改写窗口与不改写读出来完全一样，
	// 那样的锁没有区分度（本仓吃过"等值锁恒绿即恒盲"的教训）。
	// English: the second trigger is deliberately on the *next* day; re-triggering on the same day would
	// be indistinguishable from not touching the window at all, i.e. a lock with no discriminating power.
	e.recordPendingExitLocked("300001", 300, pendingExitTrim, "后续减仓", now.AddDate(0, 0, 1))
	merged := e.pendingExits["300001"]
	if merged.WantQty != 300 {
		t.Fatalf("残量应刷新为 300, got %d", merged.WantQty)
	}
	if merged.Kind != pendingExitClose {
		t.Fatalf("清仓档不得被减仓档降级, got %q", merged.Kind)
	}
	if merged.FiredDay != fired || merged.ExpireOn != expire {
		t.Fatalf("反复触发不得顺延窗口: fired %s→%s expire %s→%s", fired, merged.FiredDay, expire, merged.ExpireOn)
	}
	if len(e.pendingExits) != 1 {
		t.Fatalf("同码只留一条意图, got %d", len(e.pendingExits))
	}
}

// TestT1PartialTrimBurnsSlotWithoutIntent ④ trim 与意图互不吞的另一侧：trim 成功照常烧当日槽，
// 且**不产生残量意图**（目标量本就按可卖量钳位，成交即目标，没有"没卖完"这回事）。
// English: the other half of the mutual-non-swallowing — a successful trim burns the daily slot and
// creates no intent (its target is already capped by the sellable quantity).
func TestT1PartialTrimBurnsSlotWithoutIntent(t *testing.T) {
	e := partialTestEngine(t)
	buyFill(t, e) // 当日 1000
	t1Ready(e)    // 昨日 1000
	addToday(t, e, 500)

	if !e.ApplyUnifiedSell("300001", "trim", 11, "[测试] 减仓裁决") {
		t.Fatal("trim 处置应受理")
	}
	p := e.positions["300001"]
	// 半仓目标 750→整手 700，且 ≤ 可卖 1000 ⇒ 实卖 700，余 800。
	if p == nil || p.Qty != 800 {
		t.Fatalf("trim 应卖 700 股整手、余 800, got %+v", p)
	}
	if e.trimDone["300001"] == "" {
		t.Fatal("trim 成功应烧掉当日槽（防同日重复减仓）")
	}
	if got := len(e.pendingExits); got != 0 {
		t.Fatalf("trim 成交即目标，不应产生残量意图, got %d", got)
	}
	// 同日再 trim 应被槽挡住（证明槽真的生效，而不是被意图机制顺手绕开）。
	before := p.Qty
	e.ApplyUnifiedSell("300001", "trim", 11, "[测试] 同日重复")
	if p.Qty != before {
		t.Fatalf("同日二次 trim 应被去重, %d → %d", before, p.Qty)
	}
}
