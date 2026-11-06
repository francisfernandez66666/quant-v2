// subscription_guard_test.go — §SUBSCRIPTION（2026-11-01 裁决 6）撤单护栏的行为锁。
//
// 护栏的形状是**白名单跳过**：只有「仅申购段 ∧ 0 值登记形态」这一类可以不撤，
// 其余（含字段脏的真实委托、陌生信号前缀、价量齐全的申购段行）一律照撤。
// 为什么这个方向必须锁死：紧急停止是最不能"静默漏撤"的路径，一旦判据写成
// "凡 0 值都不撤"或"穷举我们的派发前缀"，将来新增一种前缀或回报缺字段，
// 就会把"跳过申购"换成"漏撤一笔真单"——那是比拒单噪声严重得多的错。
//
// 三条既有不变量在本文件里同时被证：
//
//	① 开关关着 ⇒ 与今天逐字节一致（全都发撤单，SubscriptionSkipped 恒 0）；
//	② 既有的"紧急停止仍撤可撤真实委托"用例（market_closed/cancel_state）不受影响；
//	③ 例行撤单跳过 ext: 手动单那条既有语义没被本批改动（见 controller.go 里两条判据并列）。
package trading

// §SESSION-WINDOW：本文件 Controller 构造统一走 newPinnedController（见 testclock_test.go），钉到当日北京时 10:15；不固定会在 09:15–09:30 与 14:52 后假红。

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// subGuardRows 一套横跨四类形状的夹具：真实单、申购登记行、价量齐全的申购段、字段脏的真实单。
// CreatedAt 用当前时刻（HaltAll 不看时间，Sweep 用 old 版本另算）。
func subGuardRows(uid string) []store.RealOrder {
	return []store.RealOrder{
		{OrderID: "GW-SG-1", SignalID: "buy:600000:dragon:20261104", Code: "600000.SH", Side: SideBuy,
			Status: "已报", Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid},
		{OrderID: "GW-SG-2", SignalID: "sell:601000:stop:20261104", Code: "601000.SH", Side: SideSell,
			Status: "部成", Price: 8, Qty: 200, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid},
		// 申购登记行：仅申购段 + 0 值 ⇒ 护栏唯一该跳过的形状
		{OrderID: "GW-SG-3", SignalID: "ext:370181", Code: "370181.SZ", Side: SideBuy,
			Status: "已报", Price: 0, Qty: 0, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid},
		// 仅申购段但价量齐全 ⇒ 不跳过（那条形态更像柜台登记异常，撤失败进明细比静默跳过可查）
		{OrderID: "GW-SG-4", SignalID: "ext:730899", Code: "730899.SH", Side: SideBuy,
			Status: "已报", Price: 50, Qty: 1000, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid},
		// 真实代码 + 0 值（回报缺字段的脏单）⇒ **必须照撤**，这是白名单形状与黑名单形状的分水岭
		{OrderID: "GW-SG-5", SignalID: "zz:unknown-prefix", Code: "600519.SH", Side: SideBuy,
			Status: "已报", Price: 0, Qty: 0, CreatedAt: time.Now().Format(time.RFC3339), UserID: uid},
	}
}

func seedSubGuard(t *testing.T, db *store.DB, uid string) {
	t.Helper()
	for _, o := range subGuardRows(uid) {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed %s: %v", o.OrderID, err)
		}
	}
}

// TestHaltAllSkipsSubscriptionCancelsAllReal 护栏开：只跳过 1 笔申购登记行，其余 4 笔照撤。
// 断言按笔数而不是"有没有跳过"——笔数能把"多跳"和"少跳"两种错都抓住。
func TestHaltAllSkipsSubscriptionCancelsAllReal(t *testing.T) {
	db := testDB(t)
	exec := &countingExec{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.SubscriptionCancelGuard = true
	ctrl := newPinnedController(exec, db, "u_sgh", cfg, nil)
	seedSubGuard(t, db, "u_sgh")

	res := ctrl.HaltAll()
	if exec.cancelCalls != 4 {
		t.Fatalf("应发 4 笔撤单（跳过 1 笔申购登记行），got %d", exec.cancelCalls)
	}
	if res.Cancelled != 4 {
		t.Fatalf("成功计数应 4, got %d", res.Cancelled)
	}
	orders, _ := db.RealOrdersForUser("u_sgh")
	for _, o := range orders {
		if o.OrderID == "GW-SG-3" && o.Status == "撤单中" {
			t.Fatal("申购登记行被发了撤单：护栏没生效")
		}
	}
}

// TestHaltAllEqualsLegacyWhenGuardOff 护栏关 ⇒ 5 笔全撤（与今天逐字节一致）。
// 这条是"这批改动要做开关、不能直接上"的兑现证据。
func TestHaltAllEqualsLegacyWhenGuardOff(t *testing.T) {
	db := testDB(t)
	exec := &countingExec{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	// 显式不开护栏
	ctrl := newPinnedController(exec, db, "u_sgo", cfg, nil)
	seedSubGuard(t, db, "u_sgo")

	res := ctrl.HaltAll()
	if exec.cancelCalls != 5 {
		t.Fatalf("护栏关着时必须与今天一致（5 笔全撤），got %d", exec.cancelCalls)
	}
	if res.Cancelled != 5 {
		t.Fatalf("关状态成功计数应 5, got %d", res.Cancelled)
	}
}

// TestHaltAllCancelsUnknownSignalIdPrefix 单独钉白名单形状：陌生信号前缀 + 真实代码 + 0 值 ⇒ 照撤。
// 判据若被改回"穷举我们的派发前缀"，这条当场红。
func TestHaltAllCancelsUnknownSignalIdPrefix(t *testing.T) {
	db := testDB(t)
	exec := &countingExec{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.SubscriptionCancelGuard = true
	ctrl := newPinnedController(exec, db, "u_sgk", cfg, nil)
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-UNK-1", SignalID: "zz:some-future-prefix", Code: "002961.SZ", Side: SideBuy,
		Status: "已报", Price: 0, Qty: 0, CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_sgk",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := ctrl.HaltAll()
	if exec.cancelCalls != 1 || res.Cancelled != 1 {
		t.Fatalf("真实代码 + 0 值 + 陌生前缀必须照撤（calls=%d cancelled=%d）：跳过它是漏撤真单",
			exec.cancelCalls, res.Cancelled)
	}
}

// TestSweepSubscriptionSkippedCounted 例行撤单同样跳过申购登记行，并单列计数
// （并进 Skipped 会把"这单不需要撤"与"这不是可撤对象"混成一件事）。
func TestSweepSubscriptionSkippedCounted(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 120
	cfg.CloseSweepAt = 1452
	cfg.SubscriptionCancelGuard = true
	exec := &countingExec{}
	ctrl := newPinnedController(exec, db, "u_sgs", cfg, nil)
	now := bjAt(10, 30)
	ctrl.SetNow(func() time.Time { return now })

	old := now.Add(-10 * time.Minute).Format(time.RFC3339)
	for _, o := range []store.RealOrder{
		{OrderID: "GW-SW-1", SignalID: "buy:600000:dragon:20261104", Code: "600000.SH", Side: SideBuy,
			Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_sgs"},
		// 非 ext: 的申购登记行 ⇒ 由本批护栏负责跳过并单列计数
		{OrderID: "GW-SW-2", SignalID: "sig:370181-reg", Code: "370181.SZ", Side: SideBuy,
			Status: "已报", Price: 0, Qty: 0, CreatedAt: old, UserID: "u_sgs"},
		// 手动单那条形：既有语义在前（ext: 先被"例行撤单跳过手动单"挡掉），护栏不得把它重复计数，
		// 否则同一个跳过原因被记两次，运维读到的 SubscriptionSkipped 就会虚高。
		{OrderID: "GW-SW-3", SignalID: "ext:GW-SW-3", Code: "380123.SZ", Side: SideBuy,
			Status: "已报", Price: 0, Qty: 0, CreatedAt: old, UserID: "u_sgs"},
	} {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	res := ctrl.SweepOrders(now)
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if exec.cancelCalls != 1 {
		t.Fatalf("只应撤真实在途单 1 笔, got %d", exec.cancelCalls)
	}
	if res.SubscriptionSkipped != 1 {
		t.Fatalf("申购登记行应单列计数 1 笔（ext: 那条归既有手动单语义、不重复计）, got %d", res.SubscriptionSkipped)
	}
	if res.WindowSkipped != 0 {
		t.Fatalf("10:30 不在禁撤窗口，WindowSkipped 应为 0, got %d", res.WindowSkipped)
	}
}

// TestCancelSkipsSubscriptionNeedsBothConditions 判据两半缺一不可（段 ∧ 0 值）。
func TestCancelSkipsSubscriptionNeedsBothConditions(t *testing.T) {
	cases := []struct {
		code  string
		qty   int
		price float64
		want  bool
	}{
		{"370181.SZ", 0, 0, true},      // 段 + 0 值 ⇒ 跳过
		{"370181.SZ", 1000, 50, false}, // 段但价量齐全 ⇒ 照撤
		{"600000.SH", 0, 0, false},     // 0 值但非申购段 ⇒ 照撤
		{"920001.BJ", 0, 0, false},     // 校准前的段不参与判定 ⇒ 照撤
	}
	for _, c := range cases {
		if got := CancelSkipsSubscriptionRow(c.code, c.qty, c.price); got != c.want {
			t.Fatalf("CancelSkipsSubscriptionRow(%s, qty=%d, price=%.2f)=%v，期望 %v",
				c.code, c.qty, c.price, got, c.want)
		}
	}
}
