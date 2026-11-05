// signal_gone_20261105_test.go — §SIGNAL-GONE（2026-11-05）"信号消失即撤"的行为锁。
//
// 这条腿的方向是**少下单**：误判一次就撤掉一笔本来会成交的合规挂单，而当日幂等键下
// 撤了不会自动补挂。所以用例的重心不是"能撤"，而是"哪种情况下它必须一动不动"：
// 开关关着、打分链停摆、今天压根没有决策读数、认不出信号键、卖出方向、手动单、禁撤窗口。
//
// 时刻一律走 ctrl.SetNow + bjAt（北京时固定日），不读真实钟：本组要判的是"宽限秒数"这种
// 时长关系，用真实时间会随时区/跑测时刻漂移（本仓的日期漂移型 flaky 老题材）。
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// sgDay 本组用例的交易日（bjAt 固定 2026-10-28，周三，交易日）。
const sgDay = "20261028"

// seedDecision 落一条当日动态仓位快照（引擎每轮写的那张表，本组只当活跃度真源用）。
func seedDecision(t *testing.T, db *store.DB, uid, code, strategy string, at time.Time, qty int) {
	t.Helper()
	if err := db.UpsertSignalDecision(store.SignalDecision{
		UserID: uid, Day: sgDay, Channel: "live", Code: code, Strategy: strategy,
		At: at.Format(time.RFC3339), Stage: "evaluated", Qty: qty,
	}); err != nil {
		t.Fatalf("seed decision %s|%s: %v", code, strategy, err)
	}
}

// seedBuy 落一笔已报买入挂单（幂等键按 engine.go 的形状 buy:<纯码>:<战法键>:<日>）。
func seedBuy(t *testing.T, db *store.DB, uid, orderID, code, strategy string, createdAt time.Time) {
	t.Helper()
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: orderID, SignalID: "buy:" + code + ":" + strategy + ":" + sgDay, Code: code + ".SH",
		Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: createdAt.Format(time.RFC3339),
		UserID: uid,
	}); err != nil {
		t.Fatalf("seed order %s: %v", orderID, err)
	}
}

// sgFixture 组装一个只开着"信号消失即撤"的控制器：
// cancel_stale_sec=3600（把超时撤单那条路拉到一小时外）、close 清单关掉，
// 于是本轮里**唯一**可能发撤单请求的理由就是信号消失。
func sgFixture(t *testing.T, graceSec int) (*Controller, *store.DB, *countingExec, time.Time) {
	t.Helper()
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 3600
	cfg.CloseSweepAt = -1
	cfg.SignalGoneCancelSec = graceSec
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_sg", cfg, nil)
	now := bjAt(10, 30)
	ctrl.SetNow(func() time.Time { return now })
	return ctrl, db, exec, now
}

// orderStatusRow 读回一笔委托的状态与撤单留痕（用例核验用）。
// 必须走 RealOrdersForUser 而不是 RealOrders()：cancel_source/cancel_reason 三列只在
// realOrderSelectColumns 那份**全字段读单**里（RealOrders() 的列清单没有它们），
// 用错读者会得到一个恒空的 CancelSource——用例就变成"断言永远不成立"的摆设。
func orderStatusRow(t *testing.T, db *store.DB, uid, orderID string) store.RealOrder {
	t.Helper()
	orders, err := db.RealOrdersForUser(uid)
	if err != nil {
		t.Fatalf("读委托失败: %v", err)
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return o
		}
	}
	t.Fatalf("找不到委托 %s", orderID)
	return store.RealOrder{}
}

// TestSignalGoneOffIsByteIdentical 开关关着＝今天的行为：老挂单一笔都不因"消失"被撤。
// 这条锁的是 owner 的一贯取向（新行为挂开关、缺省＝今天），摘掉缺省判定就会立刻红。
func TestSignalGoneOffIsByteIdentical(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 0)
	seedBuy(t, db, "u_sg", "GW-OFF", "600000", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600000", "n_shape", now.Add(-60*time.Minute), 100)

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 {
		t.Fatalf("开关关着不应撤任何单, calls=%d res=%+v", exec.cancelCalls, res)
	}
	if res.SignalGoneCancelled != 0 || res.SignalGoneIdle || res.SignalGoneUnknown != 0 {
		t.Fatalf("开关关着三个读数都必须静默为零: %+v", res)
	}
	if got := orderStatusRow(t, db, "u_sg", "GW-OFF").Status; got != "已报" {
		t.Fatalf("状态应保持已报, got %s", got)
	}
}

// TestSignalGoneCancelsStaleSignal 该撤的要撤：挂单 5 分钟前报出、它对应的信号
// 最后一次被算出是 10 分钟前（宽限 300s），且**别的票此刻还在被算**（打分链活着）
// ⇒ 撤单，且撤单来源记成 signal_gone（不是 sweep）。
func TestSignalGoneCancelsStaleSignal(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-GONE", "600000", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600000", "n_shape", now.Add(-10*time.Minute), 100)
	// 另一只票 1 分钟前刚被算出来 ⇒ 证明"链在跑"，这条消失才是真的消失。
	seedDecision(t, db, "u_sg", "600519", "龙头", now.Add(-1*time.Minute), 200)

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 1 || res.Cancelled != 1 || res.SignalGoneCancelled != 1 {
		t.Fatalf("应因信号消失撤 1 笔, calls=%d res=%+v", exec.cancelCalls, res)
	}
	row := orderStatusRow(t, db, "u_sg", "GW-GONE")
	if row.Status != "撤单中" {
		t.Fatalf("请求已发出应置撤单中, got %s", row.Status)
	}
	if row.CancelSource != SweepSourceSignalGone {
		t.Fatalf("撤单来源必须记 signal_gone（与超时撤单区分）, got %q", row.CancelSource)
	}
	acts := ctrl.SweepActions()
	if len(acts) != 1 || acts[0].Kind != SweepActionRequested || acts[0].Source != SweepSourceSignalGone {
		t.Fatalf("动作快照应一条 requested/signal_gone, got %+v", acts)
	}
}

// TestSignalGoneKeepsFreshSignal 信号还在被算出来＝不撤。
// 这条是"别把好单子撤掉"的正锁：把宽限判反（比如写成 now.Sub(at) >= 0）立刻红。
func TestSignalGoneKeepsFreshSignal(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-FRESH", "600000", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600000", "n_shape", now.Add(-60*time.Second), 100)

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 {
		t.Fatalf("信号仍在被算出时不许撤, calls=%d res=%+v", exec.cancelCalls, res)
	}
	if orderStatusRow(t, db, "u_sg", "GW-FRESH").Status != "已报" {
		t.Fatal("新鲜信号的挂单必须保持已报")
	}
}

// TestSignalGoneIdleWhenPipelineDead 整条打分链停摆 ⇒ 一条都不撤。
// 这是本条腿最要紧的一课：**"没有新读数"与"信号都消失了"长得一模一样**。
// 引擎崩了/熔断/休市时所有决策行的 at 一起变老，此时撤单等于对着停摆的观测面动钱。
func TestSignalGoneIdleWhenPipelineDead(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-DEAD", "600000", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600000", "n_shape", now.Add(-30*time.Minute), 100)
	seedDecision(t, db, "u_sg", "600519", "龙头", now.Add(-20*time.Minute), 200) // 全场最老也超窗

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if !res.SignalGoneIdle {
		t.Fatal("打分链停摆必须报 Idle（否则运维会把'没撤单'读成'没有消失的信号'）")
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 {
		t.Fatalf("链停摆时一条都不许撤, calls=%d res=%+v", exec.cancelCalls, res)
	}
}

// TestSignalGoneNoDecisionsToday 今日零行＝没有读数，不是"信号全消失"。
func TestSignalGoneNoDecisionsToday(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-NOROW", "600000", "n_shape", now.Add(-5*time.Minute))

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if !res.SignalGoneIdle || exec.cancelCalls != 0 {
		t.Fatalf("今日零决策行必须 Idle 且不撤, idle=%v calls=%d", res.SignalGoneIdle, exec.cancelCalls)
	}
}

// TestSignalGoneUnknownKey 认不出信号键就不许动手：非 buy: 幂等形状（这里是 T 腿与手动单）
// 记 Unknown 计数、不发撤单。把 Unknown 并进 Skipped 会让"键空间漂移"看起来像"没有该撤的单"。
func TestSignalGoneUnknownKey(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 3600
	cfg.CloseSweepAt = -1
	cfg.SignalGoneCancelSec = 300
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_sg2", cfg, nil)
	now := bjAt(10, 30)
	ctrl.SetNow(func() time.Time { return now })
	seedDecision(t, db, "u_sg2", "600000", "n_shape", now.Add(-3*time.Hour), 100)
	seedDecision(t, db, "u_sg2", "600519", "龙头", now.Add(-time.Minute), 200) // 链活着
	for _, o := range []store.RealOrder{
		{OrderID: "GW-T", SignalID: "t0:600000:buy_first:" + sgDay + ":1", Code: "600000.SH",
			Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), UserID: "u_sg2"},
		{OrderID: "GW-M", SignalID: "ext:123456", Code: "600000.SH",
			Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), UserID: "u_sg2"},
		{OrderID: "GW-OLDDAY", SignalID: "buy:600000:n_shape:20261027", Code: "600000.SH",
			Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), UserID: "u_sg2"},
	} {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed %s: %v", o.OrderID, err)
		}
	}

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 {
		t.Fatalf("认不出的键一律不许撤, calls=%d res=%+v", exec.cancelCalls, res)
	}
	// ext: 手动单在更早的分支就被跳过（不进 Unknown）；T 腿与跨日键各计一次 Unknown。
	if res.SignalGoneUnknown != 2 {
		t.Fatalf("应有 2 笔认不出信号键（T 腿 + 跨日键），手动单更早跳过, got %d", res.SignalGoneUnknown)
	}
	for _, id := range []string{"GW-T", "GW-M", "GW-OLDDAY"} {
		if got := orderStatusRow(t, db, "u_sg2", id).Status; got != "已报" {
			t.Fatalf("%s 必须保持已报, got %s", id, got)
		}
	}
}

// TestSignalGoneNeverCancelsSells 卖出方向永不被这条腿撤（它是"买入挂单跟着信号走"）。
// 故意给卖单一个形状合法的 buy: 键：只认买入方向这道判定必须挡下来。
func TestSignalGoneNeverCancelsSells(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 3600
	cfg.CloseSweepAt = -1
	cfg.SignalGoneCancelSec = 300
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_sg3", cfg, nil)
	now := bjAt(10, 30)
	ctrl.SetNow(func() time.Time { return now })
	seedDecision(t, db, "u_sg3", "600000", "n_shape", now.Add(-3*time.Hour), 100)
	seedDecision(t, db, "u_sg3", "600519", "龙头", now.Add(-time.Minute), 200)
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-SELL", SignalID: "buy:600000:n_shape:" + sgDay, Code: "600000.SH",
		Side: SideSell, Status: "已报", Price: 10, Qty: 100,
		CreatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), UserID: "u_sg3",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 || res.SignalGoneUnknown != 0 {
		t.Fatalf("卖单既不撤也不计 Unknown, calls=%d res=%+v", exec.cancelCalls, res)
	}
}

// TestSignalGoneRespectsNoCancelWindow 禁撤窗口内：判定成立也不发请求，且动作快照记下
// "要撤但没撤"（卡上必须能分清这单还挂在柜台上）。
func TestSignalGoneRespectsNoCancelWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 3600
	cfg.CloseSweepAt = -1
	cfg.SignalGoneCancelSec = 300
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_sg4", cfg, nil)
	now := bjAt(9, 25) // 集合竞价窗口：交易所不接受撤单
	ctrl.SetNow(func() time.Time { return now })
	seedDecision(t, db, "u_sg4", "600000", "n_shape", now.Add(-3*time.Hour), 100)
	seedDecision(t, db, "u_sg4", "600519", "龙头", now.Add(-30*time.Second), 200)
	seedBuy(t, db, "u_sg4", "GW-WIN", "600000", "n_shape", now.Add(-5*time.Minute))

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 {
		t.Fatalf("禁撤窗口内一个请求都不许发, got %d", exec.cancelCalls)
	}
	if res.WindowSkipped != 1 || res.Cancelled != 0 {
		t.Fatalf("应记 1 笔窗口跳过, res=%+v", res)
	}
	if orderStatusRow(t, db, "u_sg4", "GW-WIN").Status != "已报" {
		t.Fatal("窗口内挂单必须保持已报")
	}
	acts := ctrl.SweepActions()
	if len(acts) != 1 || acts[0].Kind != SweepActionWindow {
		t.Fatalf("动作快照应一条 window（要撤但没撤）, got %+v", acts)
	}
}

// TestSignalGoneMissingRowIsUnknown 幂等键形状认得出，但当日决策表里**没有这一行**
// （例如非动态模式根本不写快照）⇒ 记 Unknown 而不是撤单。
// 这条锁的是 map 取不到键时返回零值时刻那个坑：零值会让"比宽限"恒真，
// 于是"从没被算过的信号"全被判成"消失了"——一笔钱都不会留下解释。
func TestSignalGoneMissingRowIsUnknown(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-NOROW2", "600999", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600519", "龙头", now.Add(-30*time.Second), 200) // 链活着，但没有 600999 的行

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if res.SignalGoneIdle {
		t.Fatalf("链是活的，不该报 Idle: %+v", res)
	}
	if exec.cancelCalls != 0 || res.Cancelled != 0 || res.SignalGoneUnknown != 1 {
		t.Fatalf("缺行应记 Unknown 且不撤, calls=%d res=%+v", exec.cancelCalls, res)
	}
	if got := orderStatusRow(t, db, "u_sg", "GW-NOROW2").Status; got != "已报" {
		t.Fatalf("状态必须保持已报, got %s", got)
	}
}

// TestSignalGonePaperChannelIgnored 纸面通道的决策行不许给实盘挂单当活跃度：
// 两条链各写各的，实盘那条停了而纸面还在跑时，据纸面的"新鲜"去撤实盘单就是拿错读数动钱。
func TestSignalGonePaperChannelIgnored(t *testing.T) {
	ctrl, db, exec, now := sgFixture(t, 300)
	seedBuy(t, db, "u_sg", "GW-PAPER", "600000", "n_shape", now.Add(-5*time.Minute))
	seedDecision(t, db, "u_sg", "600000", "n_shape", now.Add(-3*time.Hour), 100)
	// 唯一"新鲜"的一行来自纸面通道 ⇒ live 侧没有活跃度证据 ⇒ Idle。
	if err := db.UpsertSignalDecision(store.SignalDecision{
		UserID: "u_sg", Day: sgDay, Channel: "paper", Code: "600519", Strategy: "龙头",
		At: now.Add(-10 * time.Second).Format(time.RFC3339), Stage: "evaluated", Qty: 100,
	}); err != nil {
		t.Fatalf("seed paper: %v", err)
	}

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if !res.SignalGoneIdle || exec.cancelCalls != 0 {
		t.Fatalf("纸面行不得充当实盘活跃度, idle=%v calls=%d res=%+v", res.SignalGoneIdle, exec.cancelCalls, res)
	}
}
