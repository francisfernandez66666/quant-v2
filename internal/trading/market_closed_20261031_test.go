// 本文件：§CANCEL-STATE 的**交易日维度**（2026-10-31 owner 质疑「禁挂窗口不应该是交易日吗」）。
//
// 缺口本体：窗口判定只看钟点（session_guard.go 的 inWindow），周六/节假日的 09:20–09:30
// 也会被它拒，而拒绝理由写的是"集合竞价/尾盘窗口交易所不接受委托"——休市日根本没有集合竞价，
// 那句话是**假话**，会把操作人引向"等 09:30 再试"。现在：手动入口（挂单/手动委托/撤单）
// 先判今天开不开市——非交易日一律以「今日休市」如实拒，交易日的窗口语义一字不变。
// 改动面**只在这一层**：Controller.PlaceOrder/CancelOrder 与引擎自动链保持原语义（自动链本来
// 就被 scoring_loop 的 IsActiveSession 挡在非交易日之外），所以不存在"周末把整片下单用例打红"的回潮。
// **紧急停止仍全撤**（HaltAll 直发 exec、不经 CancelOrder）这条止损不变量单独上锁。
//
// English: trading-day dimension for the §CANCEL-STATE windows — honest refusal on non-trading
// days, unchanged window semantics on trading days, and the emergency-stop bypass stays intact.
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// nonTradingNow 周六（2026-11-07）的指定时刻：IsTradingDay 在日历未加载时只判周末，
// 所以这个形状在任何环境/任何时钟下都同向成立。
func nonTradingNow(hh, mm int) time.Time {
	return time.Date(2026, 11, 7, hh, mm, 0, 0, cntime.Loc)
}

// tradingNow 周三（2026-11-04）的指定时刻（与 intraday_t_live_test.go 钉的是同一天）。
func tradingNow(hh, mm int) time.Time {
	return time.Date(2026, 11, 4, hh, mm, 0, 0, cntime.Loc)
}

// manualGateCtrl 构造一个"实盘已开、窗口取出厂默认、时钟可钉"的控制器。
func manualGateCtrl(t *testing.T, exec Executor, uid string) *Controller {
	t.Helper()
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	return NewController(exec, testDB(t), uid, cfg, nil)
}

// TestManualOrderRefusalHonestOnNonTradingDay 休市日撞窗口时刻：新单与撤单都必须给"今日休市"，
// 且**不许**再出现"集合竞价"这句（那是交易日才成立的解释）。
func TestManualOrderRefusalHonestOnNonTradingDay(t *testing.T) {
	ctrl := manualGateCtrl(t, guardServer(), "u_mc_honest")
	ctrl.SetNow(func() time.Time { return nonTradingNow(9, 25) }) // 09:25 既在禁挂也在禁撤窗口内

	for _, forCancel := range []bool{false, true} {
		msg := ctrl.ManualOrderRefusal(ctrl.now(), forCancel)
		if !strings.Contains(msg, "今日休市") {
			t.Fatalf("休市日 forCancel=%v 应给「今日休市」, got %q", forCancel, msg)
		}
		if strings.Contains(msg, "集合竞价") {
			t.Fatalf("休市日不能再用集合竞价的理由（那天没有集合竞价）, got %q", msg)
		}
	}
}

// TestManualOrderRefusalKeepsWindowTextOnTradingDay 交易日撞窗口：文案与语义必须与改动前逐字同形
// （这条是"别把交易日的行为顺手改掉"的锁）。
func TestManualOrderRefusalKeepsWindowTextOnTradingDay(t *testing.T) {
	ctrl := manualGateCtrl(t, guardServer(), "u_mc_window")
	ctrl.SetNow(func() time.Time { return tradingNow(9, 25) })

	if msg := ctrl.ManualOrderRefusal(ctrl.now(), false); !strings.Contains(msg, "集合竞价") {
		t.Fatalf("交易日禁挂窗口应保留原文案, got %q", msg)
	}
	if msg := ctrl.ManualOrderRefusal(ctrl.now(), true); !strings.Contains(msg, "不可撤单") {
		t.Fatalf("交易日禁撤窗口应保留原文案, got %q", msg)
	}
}

// TestManualOrderRefusalAllowsOutsideWindow 交易日窗口外：两条腿都放行（空串）。
func TestManualOrderRefusalAllowsOutsideWindow(t *testing.T) {
	ctrl := manualGateCtrl(t, guardServer(), "u_mc_allow")
	ctrl.SetNow(func() time.Time { return tradingNow(10, 30) })

	for _, forCancel := range []bool{false, true} {
		if msg := ctrl.ManualOrderRefusal(ctrl.now(), forCancel); msg != "" {
			t.Fatalf("窗口外 forCancel=%v 应放行, got %q", forCancel, msg)
		}
	}
}

// TestHaltAllStillCancelsOnNonTradingDay 止损不变量：紧急停止走 execRef().Cancel 直发，
// 不受"休市/窗口"这类闸影响——给闸加日历维度时最容易被顺手挡掉的就是这条。
func TestHaltAllStillCancelsOnNonTradingDay(t *testing.T) {
	db := testDB(t)
	exec := &countingExec{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(exec, db, "u_mc_halt", cfg, nil)
	ctrl.SetNow(func() time.Time { return nonTradingNow(9, 25) }) // 休市日 + 窗口内，最坏形状

	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "GW-MC-HALT-1", SignalID: "SIG-MC-HALT-1", Code: "600000.SH", Side: SideBuy,
		Status: "已报", Price: 10, Qty: 100, CreatedAt: time.Now().Format(time.RFC3339), UserID: "u_mc_halt",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := ctrl.HaltAll()
	if exec.cancelCalls != 1 {
		t.Fatalf("紧急停止在休市日+禁撤窗口内仍应尝试撤单, got %d 次", exec.cancelCalls)
	}
	if res.Cancelled != 1 {
		t.Fatalf("期望撤单请求发出 1 笔, got %d", res.Cancelled)
	}
	orders, _ := db.RealOrdersForUser("u_mc_halt")
	if len(orders) != 1 || orders[0].Status != "撤单中" {
		t.Fatalf("紧急停止撤单后应落 撤单中, got %+v", orders)
	}
}
