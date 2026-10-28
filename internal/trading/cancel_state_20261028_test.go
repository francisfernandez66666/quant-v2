// cancel_state_20261028_test.go — §CANCEL-STATE（2026-10-28）行为腿：
// 「撤单中」状态机 + 禁挂/禁撤时段闸。
//
// 为什么这批用例值得单独锁：2026-10-28 的 600362 事故（尾盘 14:57:48 新单 → 15:00:03 撤单
// 撞收盘集合竞价 → 本地把"撤单请求被受理"当成"已撤" → 实际成交既无 fills 也无持仓）
// 同时踩了两个洞：**时点闸缺失**（不该在那个窗口发单/发撤单）与**状态语义缺失**
// （请求 ≠ 确认）。只修其中一个，下一次还会复现。
//
// English: behaviour legs for the 撤单中 state machine and the session-window guards.
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// bjAt 构造一个北京时区的指定时刻（用于注入控制器时钟）。
func bjAt(hh, mm int) time.Time {
	return time.Date(2026, 10, 28, hh, mm, 0, 0, cntime.Loc)
}

// TestSessionWindowBoundaries §CANCEL-STATE：时段闸的边界判定（09:20/09:30/14:52/14:57/15:00）。
// 锁的是"边界到底含不含"——差一分钟就是另一种行为（A 股 09:30:00 起接受撤单、
// 15:00:00 起集合竞价结束），写成 <= 会把关闸时间多压一分钟。
func TestSessionWindowBoundaries(t *testing.T) {
	sw := DefaultSessionWindows()
	cases := []struct {
		name     string
		h, m     int
		wantNew  bool // 禁挂单窗口
		wantCanc bool // 禁撤单窗口
	}{
		{"开盘前正常", 9, 15, false, false},
		{"09:19 正常", 9, 19, false, false},
		{"09:20 禁挂禁撤", 9, 20, true, true},
		{"09:29 禁挂禁撤", 9, 29, true, true},
		{"09:30 恢复", 9, 30, false, false},
		{"13:00 正常", 13, 0, false, false},
		{"14:51 正常", 14, 51, false, false},
		{"14:52 禁挂（撤仍允许）", 14, 52, true, false},
		{"14:56 禁挂", 14, 56, true, false},
		{"14:57 禁挂禁撤", 14, 57, true, true},
		{"14:59 禁挂禁撤", 14, 59, true, true},
		{"15:00 恢复", 15, 0, false, false},
	}
	for _, c := range cases {
		now := bjAt(c.h, c.m)
		if got, _ := sw.NewOrderBlocked(now); got != c.wantNew {
			t.Errorf("%s: 禁挂窗口=%v, 期望 %v", c.name, got, c.wantNew)
		}
		if got, _ := sw.CancelBlocked(now); got != c.wantCanc {
			t.Errorf("%s: 禁撤窗口=%v, 期望 %v", c.name, got, c.wantCanc)
		}
	}
}

// TestSessionWindowMalformedFallsBackToDefault §CANCEL-STATE：窗口配置写坏时回退出厂默认
// 而不是"静默失效"。静默失效＝闸门形同虚设＝事故条件重新具备。
func TestSessionWindowMalformedFallsBackToDefault(t *testing.T) {
	sw, err := NewSessionWindows("不是窗口", "09:20-09:30")
	if err == nil {
		t.Fatal("非法窗口串应返回错误")
	}
	// 即便非法，出厂禁撤窗口仍必须生效（14:57 命中）
	if got, _ := sw.CancelBlocked(bjAt(14, 57)); !got {
		t.Fatal("非法配置后应回退出厂默认（14:57 仍应禁撤）")
	}
	// 合法的那半个窗口照常生效
	if got, _ := sw.NewOrderBlocked(bjAt(9, 25)); !got {
		t.Fatal("合法配置的窗口应生效（09:25 应禁挂）")
	}
}

// TestPlaceOrderBlockedInNoNewOrderWindow §CANCEL-STATE：禁挂窗口内下单被拒，且**不落占位行**。
// 为什么锁"不落占位行"：占位行会占住 signal_id 的幂等键，窗口一过就再也发不出这单
// （唯一键拦截 → duplicate），用户看到的是"信号消失了"而不是"窗口挡了一下"。
func TestPlaceOrderBlockedInNoNewOrderWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_win", cfg, nil)
	ctrl.SetNow(func() time.Time { return bjAt(14, 55) })

	_, err := ctrl.PlaceOrder(buyReq("SIG-WIN-1", 10000))
	if err == nil {
		t.Fatal("禁挂窗口内下单应被拒")
	}
	if !strings.Contains(err.Error(), "不可挂单") {
		t.Fatalf("拒绝原因应说明是不可挂单时段, got %v", err)
	}
	orders, _ := db.RealOrdersForUser("u_win")
	if len(orders) != 0 {
		t.Fatalf("被窗口拒绝的下单不应留下占位行, got %+v", orders)
	}
}

// TestPlaceOrderAllowedOutsideWindow 反证：窗口外同一路径必须放行（防止闸门写死成"永拒"）。
func TestPlaceOrderAllowedOutsideWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_win2", cfg, nil)
	ctrl.SetNow(func() time.Time { return bjAt(10, 30) })

	res, err := ctrl.PlaceOrder(buyReq("SIG-WIN-2", 10000))
	if err != nil {
		t.Fatalf("窗口外下单应放行: %v", err)
	}
	if res == nil || !res.OK {
		t.Fatalf("窗口外下单应成功, got %+v", res)
	}
}

// TestCancelOrderBlockedInNoCancelWindow §CANCEL-STATE：禁撤窗口内撤单被拒且**不发请求**。
// 断言"没发请求"是这道用例的核心：发了请求就可能拿到一次假受理回执（事故形态之二）。
func TestCancelOrderBlockedInNoCancelWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_cw", cfg, nil)
	ctrl.SetNow(func() time.Time { return bjAt(14, 58) })

	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-CW-1", SignalID: "SIG-CW-1", Code: "600000.SH", Side: SideBuy,
		Status: "已报", Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_cw",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := ctrl.CancelOrder("GW-CW-1")
	if err == nil {
		t.Fatal("禁撤窗口内撤单应被拒")
	}
	if !strings.Contains(err.Error(), "不可撤单") {
		t.Fatalf("拒绝原因应说明是不可撤单时段, got %v", err)
	}
	if exec.cancelCalls != 0 {
		t.Fatalf("禁撤窗口内不应向网关发撤单请求, got %d 次", exec.cancelCalls)
	}
	// 本地行不得被推进（连"撤单中"都不该有——请求根本没发出去）
	orders, _ := db.RealOrdersForUser("u_cw")
	if orders[0].Status != "已报" {
		t.Fatalf("被窗口拒绝的撤单不得推进本地状态, got %s", orders[0].Status)
	}
}

// TestCancelOrderSuccessMarksCancelPending §CANCEL-STATE：撤单请求发出后本地落「撤单中」
// 而不是「已撤」，并留痕请求时刻与来源。
func TestCancelOrderSuccessMarksCancelPending(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_cp", cfg, nil)
	ctrl.SetNow(func() time.Time { return bjAt(10, 30) })

	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-CP-1", SignalID: "SIG-CP-1", Code: "600000.SH", Side: SideBuy,
		Status: "已报", Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_cp",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ctrl.CancelOrder("GW-CP-1"); err != nil {
		t.Fatalf("撤单调用应成功: %v", err)
	}
	orders, _ := db.RealOrdersForUser("u_cp")
	o := orders[0]
	if o.Status != "撤单中" {
		t.Fatalf("撤单请求发出后应落 撤单中, got %s", o.Status)
	}
	if o.CancelRequestedAt == "" {
		t.Fatal("撤单中应留痕请求时刻")
	}
	if o.CancelSource != "manual" {
		t.Fatalf("撤单来源应记 manual, got %q", o.CancelSource)
	}
}

// TestSweepOrdersSkippedInNoCancelWindow §CANCEL-STATE：禁撤窗口内例行撤单整轮跳过，
// 且**不消费**收盘清单的当日标记（否则当天收盘清单就再也不会执行）。
func TestSweepOrdersSkippedInNoCancelWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 120
	cfg.CloseSweepAt = 1452
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_sw", cfg, nil)
	now := bjAt(14, 58)
	ctrl.SetNow(func() time.Time { return now })

	old := now.Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-SW-1", SignalID: "SIG-SW-1", Code: "600000.SH", Side: SideBuy,
		Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_sw",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 0 {
		t.Fatalf("禁撤窗口内不应发撤单请求, got %d 次", exec.cancelCalls)
	}
	if res.Cancelled != 0 {
		t.Fatalf("禁撤窗口内撤销笔数应为 0, got %d", res.Cancelled)
	}
	if res.WindowSkipped != 1 {
		t.Fatalf("应记 1 笔窗口跳过, got %d", res.WindowSkipped)
	}
	// 收盘清单标记不得被消费：窗口过后同一交易日仍要能执行收盘清单。
	ctrl.mu.RLock()
	lastDay := ctrl.lastCloseSweepDay
	ctrl.mu.RUnlock()
	if lastDay != "" {
		t.Fatalf("禁撤窗口内不得消费收盘清单当日标记, got %q", lastDay)
	}
}

// TestHaltAllStillAttemptsInNoCancelWindow §CANCEL-STATE（owner 裁决）：紧急停止在禁撤窗口内
// **仍然尝试**撤单（全撤是止损动作，不能被闸门挡成"没撤"），被交易所拒绝的逐条进 Failed 明细。
func TestHaltAllStillAttemptsInNoCancelWindow(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	exec := &countingExec{}
	ctrl := NewController(exec, db, "u_hw", cfg, nil)
	ctrl.SetNow(func() time.Time { return bjAt(14, 58) })

	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-HW-1", SignalID: "SIG-HW-1", Code: "600000.SH", Side: SideBuy,
		Status: "已报", Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_hw",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := ctrl.HaltAll()
	if exec.cancelCalls != 1 {
		t.Fatalf("紧急停止在禁撤窗口内仍应尝试撤单, got %d 次", exec.cancelCalls)
	}
	if res.Cancelled != 1 {
		t.Fatalf("期望撤单请求发出 1 笔, got %d", res.Cancelled)
	}
	orders, _ := db.RealOrdersForUser("u_hw")
	if orders[0].Status != "撤单中" {
		t.Fatalf("紧急停止撤单后应落 撤单中, got %s", orders[0].Status)
	}
}

// countingExec 记录撤单调用次数的假网关（§CANCEL-STATE 用例用）。
type countingExec struct {
	guardStub
	cancelCalls int
}

// Cancel 记录调用次数。
func (c *countingExec) Cancel(orderID string) error {
	c.cancelCalls++
	return nil
}

// ── §FIX-20261028-F05（2026-10-29）：窗口回显必须是"生效值" ──
//
// 缺陷本体：Describe() 回的是配置原文（rawCancel/rawNewOrder），配置为空串时生效的是出厂默认
//（NewSessionWindows 的回落分支），界面却会显示「禁撤 （空）」这种无意义读数——
// 客户看到的规则和真正执行的规则不是同一个。

// TestDescribeFallsBackToDefaultsWhenRawEmpty 空配置回显出厂默认串（不是空串）。
func TestDescribeFallsBackToDefaultsWhenRawEmpty(t *testing.T) {
	sw, err := NewSessionWindows("", "")
	if err != nil {
		t.Fatalf("空配置不应报错: %v", err)
	}
	got := sw.Describe()
	if got["no_cancel_windows"] != DefaultNoCancelWindows {
		t.Fatalf("禁撤窗口空配置应回出厂默认 %q，实到 %q", DefaultNoCancelWindows, got["no_cancel_windows"])
	}
	if got["no_new_order_windows"] != DefaultNoNewOrderWindows {
		t.Fatalf("禁挂窗口空配置应回出厂默认 %q，实到 %q", DefaultNoNewOrderWindows, got["no_new_order_windows"])
	}
}

// TestDescribeKeepsCustomSpec 自定义配置原样回显（不得被默认值覆盖）。
func TestDescribeKeepsCustomSpec(t *testing.T) {
	sw, err := NewSessionWindows("09:25-09:31", "14:50-15:00")
	if err != nil {
		t.Fatalf("自定义配置应可解析: %v", err)
	}
	got := sw.Describe()
	if got["no_cancel_windows"] != "09:25-09:31" {
		t.Fatalf("自定义禁撤窗口应原样回显，实到 %q", got["no_cancel_windows"])
	}
	if got["no_new_order_windows"] != "14:50-15:00" {
		t.Fatalf("自定义禁挂窗口应原样回显，实到 %q", got["no_new_order_windows"])
	}
}
