// r4_test.go — §R4 Wave 回归测试：
// kill-switch 全路径拒绝（R4-1）、委托状态单调推进（R4-4）、撤单闭环（R4-1）、
// 券商口径真实资金回灌买入纪律（R4-3）。
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestKillSwitchRejectsAllOrders §R4-1：halted=true 时买入与卖出全路径拒绝；
// 解除后恢复正常。
func TestKillSwitchRejectsAllOrders(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), testDB(t), "u_g", cfg, nil)

	// 未置位：正常放行
	if _, err := ctrl.PlaceOrder(buyReq("SIG-HALT-OFF", 1000)); err != nil {
		t.Fatalf("未置位 kill-switch 不应拒绝: %v", err)
	}

	// 置位：买入拒绝
	ctrl.UpdateConfig(config.QMTConfig{Enabled: true, Halted: true})
	if _, err := ctrl.PlaceOrder(buyReq("SIG-HALT-BUY", 1000)); err == nil || !strings.Contains(err.Error(), "kill-switch") {
		t.Fatalf("置位后买入应被 kill-switch 拒绝, got %v", err)
	}
	// 置位：卖出同样拒绝（紧急停止语义下柜台动作全停）
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-HALT-SELL", Code: "600000.SH", Name: "浦发",
		Side: SideSell, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err == nil || !strings.Contains(err.Error(), "kill-switch") {
		t.Fatalf("置位后卖出应被 kill-switch 拒绝, got %v", err)
	}

	// 解除：恢复放行
	ctrl.UpdateConfig(config.QMTConfig{Enabled: true})
	if _, err := ctrl.PlaceOrder(buyReq("SIG-HALT-ON", 1000)); err != nil {
		t.Fatalf("解除后应放行: %v", err)
	}
}

// TestAdvanceRealOrderStatusMonotonic §R4-4：委托状态单调推进——
// 回报的 部成/已成/已撤 能写入本地行（旧实现被 INSERT OR IGNORE 吞掉）；
// 乱序/重放/回退（已成后重放已报、已成后来废单）绝不回退真实进度。
func TestAdvanceRealOrderStatusMonotonic(t *testing.T) {
	db := testDB(t)
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "GW-M1", SignalID: "SIG-M1",
		Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100,
		CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_m"}); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	// 正向推进：已报 → 部成 → 已成
	if ok, _ := db.AdvanceRealOrderStatus("u_m", "SIG-M1", "部成"); !ok {
		t.Fatal("已报→部成 应推进成功")
	}
	if ok, _ := db.AdvanceRealOrderStatus("u_m", "SIG-M1", "已成"); !ok {
		t.Fatal("部成→已成 应推进成功")
	}
	// 乱序/重放/回退：全部拒绝
	for _, stale := range []string{"已报", "部成", "已报待撤"} {
		if ok, _ := db.AdvanceRealOrderStatus("u_m", "SIG-M1", stale); ok {
			t.Fatalf("已成之后回放 %s 绝不允许覆盖", stale)
		}
	}
	// 终态互斥：已成之后来 废单/已撤 也不回退
	for _, stale := range []string{"废单", "已撤", "部撤"} {
		if ok, _ := db.AdvanceRealOrderStatus("u_m", "SIG-M1", stale); ok {
			t.Fatalf("已成之后 %s 绝不允许覆盖", stale)
		}
	}
	// 本地无此单：返回 false 且不报错（交由调用方决定补插）
	if ok, err := db.AdvanceRealOrderStatus("u_m", "SIG-NOPE", "已成"); ok || err != nil {
		t.Fatalf("未知 signal_id 应 (false,nil), got (%v,%v)", ok, err)
	}
	// 最终状态必须是 已成
	orders, _ := db.RealOrders()
	for _, o := range orders {
		if o.SignalID == "SIG-M1" && o.Status != "已成" {
			t.Fatalf("终态应为已成, got %s", o.Status)
		}
	}
}

// TestSweepOrdersStaleCancel §R4-1：撤单闭环——
// 滞留已报单自动撤销、fresh 已报单不动、pend: 占位超时降级为发送失败。
func TestSweepOrdersStaleCancel(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.CancelStaleSec = 120
	cfg.CloseSweepAt = -1 // 隔离收盘清单，单测只验超时撤
	ctrl := NewController(guardServer(), db, "u_g", cfg, nil)

	old := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	fresh := time.Now().Format(time.RFC3339)
	seed := []store.RealOrder{
		{OrderID: "GW-1", SignalID: "SIG-SW-A", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_g"},
		{OrderID: "GW-2", SignalID: "SIG-SW-B", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: fresh, UserID: "u_g"},
		{OrderID: "pend:SIG-SW-P", SignalID: "SIG-SW-P", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_g"},
		{OrderID: "GW-3", SignalID: "SIG-SW-C", Code: "600000.SH", Side: SideBuy, Status: "已成", Price: 10, Qty: 100, CreatedAt: old, UserID: "u_g"},
	}
	for _, o := range seed {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	res := ctrl.SweepOrders(time.Now())
	if res == nil {
		t.Fatal("SweepOrders 应执行")
	}
	if res.Cancelled != 1 || res.Demoted != 1 {
		t.Fatalf("期望 撤销=1 降级=1, got %+v", res)
	}
	// 本地状态核验：GW-1 → 已撤；pend:SIG-SW-P → 发送失败；GW-2 保持已报；GW-3 已成不动
	orders, _ := db.RealOrders()
	got := map[string]string{}
	for _, o := range orders {
		got[o.SignalID] = o.Status
	}
	if got["SIG-SW-A"] != "已撤" {
		t.Fatalf("GW-1 应已撤, got %s", got["SIG-SW-A"])
	}
	if got["SIG-SW-P"] != "发送失败" {
		t.Fatalf("pend 行应降级发送失败, got %s", got["SIG-SW-P"])
	}
	if got["SIG-SW-B"] != "已报" {
		t.Fatalf("fresh 单不应被撤, got %s", got["SIG-SW-B"])
	}
	if got["SIG-SW-C"] != "已成" {
		t.Fatalf("已成单不应被触碰, got %s", got["SIG-SW-C"])
	}
}

// TestRealCashGate §R4-3：券商口径可用资金（account 回报）作为额外硬约束——
// 超出即拒单，近似口径照常并行；回报过期（>10min）时不再约束。
func TestRealCashGate(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_g", cfg, nil)

	// 新鲜回报：可用 500 元
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_g", AvailableCash: 500,
		UpdatedAt: time.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CASH-BIG", 1000)); err == nil || !strings.Contains(err.Error(), "券商口径") {
		t.Fatalf("超出券商可用资金应拒绝, got %v", err)
	}
	// 额度内放行（近似口径 InitialCapital=100000 也通过）
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CASH-OK", 400)); err != nil {
		t.Fatalf("额度内应放行: %v", err)
	}
	// 过期回报（20 分钟前）：不再约束
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_g", AvailableCash: 0,
		UpdatedAt: time.Now().Add(-20 * time.Minute).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed stale account: %v", err)
	}
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CASH-STALE", 1000)); err != nil {
		t.Fatalf("过期回报不应约束(且 AvailableCash=0 不触发), got %v", err)
	}
}

// TestCancelPathMonotonic §P0-4 撤单路径单调状态机：本地订单已是 已成/已撤 等终态时，
// 撤单成功后不得把状态回退/覆盖为 已撤（防止晚到的撤单响应覆盖真实成交回报）。
func TestCancelPathMonotonic(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_cancel", cfg, nil)

	seed := func(orderID, signalID, status string) {
		if _, err := db.UpsertRealOrder(store.RealOrder{
			OrderID: orderID, SignalID: signalID, Code: "600000.SH", Side: SideBuy,
			Status: status, Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339),
			UserID: "u_cancel",
		}); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
	}

	seed("GW-FILLED", "SIG-FILLED", "已成")
	seed("GW-CANCELLED", "SIG-CANCELLED", "已撤")
	seed("GW-REPORTED", "SIG-REPORTED", "已报")

	// 直接调用 CancelOrder：对 guardServer() 来说任意 orderID 都能撤成功，
	// 但本地状态必须受单调秩保护。
	if err := ctrl.CancelOrder("GW-FILLED"); err != nil {
		t.Fatalf("撤单调用应成功: %v", err)
	}
	if err := ctrl.CancelOrder("GW-CANCELLED"); err != nil {
		t.Fatalf("撤单调用应成功: %v", err)
	}
	if err := ctrl.CancelOrder("GW-REPORTED"); err != nil {
		t.Fatalf("撤单调用应成功: %v", err)
	}

	orders, _ := db.RealOrdersForUser("u_cancel")
	for _, o := range orders {
		switch o.SignalID {
		case "SIG-FILLED":
			if o.Status != "已成" {
				t.Fatalf("已成单不得被撤单路径回退, got %s", o.Status)
			}
		case "SIG-CANCELLED":
			if o.Status != "已撤" {
				t.Fatalf("已撤单应保持已撤, got %s", o.Status)
			}
		case "SIG-REPORTED":
			if o.Status != "已撤" {
				t.Fatalf("已报单撤单后应变已撤, got %s", o.Status)
			}
		}
	}
}
