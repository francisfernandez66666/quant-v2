// buy_reprice_20261105_test.go — §BUY-REPRICE（2026-11-05）买入挂单跟 VWAP 改价的行为锁。
//
// 这组用例的重心仍然是"哪种情况它必须一动不动"：开关关着＝今天；没有参考价；部成单；
// 做 T 的腿；手动单；当日已撤两次。改价这条腿方向是"让挂单更贴近市场参考价"，
// 但每一次动作都在消耗柜台撤单额度并关掉一次同键重放的机会，所以宁可不撤。
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// repriceNow 打分轮的时刻：北京 2026-10-28（周三）10:30，既不在禁撤窗也不在禁挂窗。
var repriceNow = time.Date(2026, 10, 28, 10, 30, 0, 0, cntime.Loc)

// repriceDay 与 repriceNow 同源的交易日键（限次计数按这个键判"今天"）。
const repriceDay = "20261028"

// seedTicket 落一笔委托（默认已报买入、幂等键按战略买入的形状）。
func seedTicket(t *testing.T, db *store.DB, oid, sid, code, side, status string, price float64, qty int, cancelSource string) {
	t.Helper()
	created := repriceNow.Add(-3 * time.Minute)
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: oid, SignalID: sid, Code: code, Side: side, Status: status,
		Price: price, Qty: qty, CreatedAt: created.Format(time.RFC3339), UserID: "u_1",
		CancelSource: cancelSource,
	}); err != nil {
		t.Fatalf("seed %s: %v", oid, err)
	}
	if cancelSource != "" {
		// UpsertRealOrder 不带 cancel_source 的写路径（那三列只在撤单请求时写），
		// 这里直接按同一处定义补一次留痕，保证限次计数读到的形态与真实链路一致。
		if _, err := db.SetCancelTrace("u_1", oid, cancelSource, "用例预置的改价撤单留痕"); err != nil {
			t.Fatalf("trace %s: %v", oid, err)
		}
	}
}

// repriceEngine 造一个开着改价阈值的引擎（阈值 0＝关，用于"缺省＝今天"那支）。
// 必须显式钉 e.userID：这条腿按账号读当日委托（budgetUID→RealOrdersForUser），
// 而 harness 建引擎时不填 userID——不填就会去查"遗留全局行"，读不到本账号的委托，
// 整条腿静默返回，用例就成了"永远看不到被测代码"的摆设（今天第一次跑就是这么空过的）。
func repriceEngine(t *testing.T, pct float64) (*Engine, *store.DB) {
	t.Helper()
	e, db, _, _ := newQMTEngine(t, func(c *config.QMTConfig) {
		c.Enabled = true
		c.VWAPRepricePct = pct
	})
	e.mu.Lock()
	e.userID = "u_1"
	e.mu.Unlock()
	return e, db
}

// quoteAt 一条能算出 VWAP 的行情快照（Amount/Volume = 参考价）。
func quoteAt(ref float64) map[string]*data.StockInfo {
	return map[string]*data.StockInfo{"600000": {Price: ref, Volume: 100000, Amount: ref * 100000}}
}

// TestRepriceDeviationMath 纯算术两支：偏离百分比与"够不够撤"。
// 两个方向都算（挂高了会多付，挂低了不会成交），阈值 0 与拿不到参考价都不撤。
func TestRepriceDeviationMath(t *testing.T) {
	dev, ok := repriceDeviationPct(10, 9)
	if !ok || dev < 11.0 || dev > 11.2 {
		t.Fatalf("挂价 10 对参考价 9 的偏离应约 +11.1%%, got (%.2f,%v)", dev, ok)
	}
	if _, ok := repriceDeviationPct(10, 0); ok {
		t.Fatal("参考价缺失时不许给偏离读数（把没读到当 0 偏离就是永不撤）")
	}
	if !repriceWorthCancelling(11.1, 2) || !repriceWorthCancelling(-11.1, 2) {
		t.Fatal("高挂与低挂两个方向的偏离都必须触发")
	}
	if repriceWorthCancelling(1.9, 2) {
		t.Fatal("未达阈值不该撤")
	}
	if repriceWorthCancelling(50, 0) {
		t.Fatal("阈值 0＝关，任何偏离都不撤（缺省＝今天）")
	}
}

// TestRepriceStrategicTicketShape 可撤对象只有"战略买入的已报单"：
// 部成、做 T 腿、手动单、占位行、卖单一律排除。
func TestRepriceStrategicTicketShape(t *testing.T) {
	base := store.RealOrder{OrderID: "GW-1", SignalID: "buy:600000:n_shape:" + repriceDay,
		Code: "600000.SH", Side: trading.SideBuy, Status: "已报", Price: 10}
	if !isStrategicBuyTicket(base) {
		t.Fatal("战略买入的已报单必须是可撤对象")
	}
	part := base
	part.Status = "部成"
	if isStrategicBuyTicket(part) {
		t.Fatal("部成单不许撤（撤的是未成交部分，但同键重放会把已成交那部分的机会一起关掉）")
	}
	sell := base
	sell.Side = trading.SideSell
	if isStrategicBuyTicket(sell) {
		t.Fatal("卖单不在本条腿射程")
	}
	tLeg := base
	tLeg.SignalID = "t0:600000:buy_first:" + repriceDay + ":1"
	tLeg.IntradayT = true
	if isStrategicBuyTicket(tLeg) {
		t.Fatal("做 T 的腿有自己的让价与配对节奏，不许被战略改价腿撤")
	}
	manual := base
	manual.SignalID = "ext:9001"
	if isStrategicBuyTicket(manual) {
		t.Fatal("手动单是用户自己的动作，撤它属于越权")
	}
	pend := base
	pend.OrderID = "pend:buy:600000:n_shape:" + repriceDay
	if isStrategicBuyTicket(pend) {
		t.Fatal("占位行压根没到柜台，没有可撤的东西")
	}
}

// TestRepriceCancelsDriftedBuy 正锁：挂价 10、参考价 9（偏离 +11%，阈值 2%）⇒ 撤，
// 留痕来源记 vwap_reprice，动作快照同步（卡与提醒据此显示"改价重挂"）。
func TestRepriceCancelsDriftedBuy(t *testing.T) {
	e, db := repriceEngine(t, 2)
	seedTicket(t, db, "GW-RP1", "buy:600000:n_shape:"+repriceDay, "600000.SH", trading.SideBuy, "已报", 10, 100, "")
	ctrl := e.QMTController()
	e.repriceStaleBuys(ctrl, quoteAt(9), repriceNow)

	row := repriceRow(t, db, "GW-RP1")
	if row.Status != "撤单中" {
		t.Fatalf("偏离够大应置撤单中等确认, got %s", row.Status)
	}
	if row.CancelSource != trading.SweepSourceVWAPReprice {
		t.Fatalf("撤单来源必须记 vwap_reprice（与超时/消失分开）, got %q", row.CancelSource)
	}
	acts := ctrl.SweepActions()
	if len(acts) != 1 || acts[0].Source != trading.SweepSourceVWAPReprice || acts[0].Kind != trading.SweepActionRequested {
		t.Fatalf("动作快照应一条 vwap_reprice/requested, got %+v", acts)
	}
}

// TestRepriceOffIsToday 开关关着＝今天：一笔都不撤（缺省态的行为等值锁）。
func TestRepriceOffIsToday(t *testing.T) {
	e, db := repriceEngine(t, 0)
	seedTicket(t, db, "GW-OFF1", "buy:600000:n_shape:"+repriceDay, "600000.SH", trading.SideBuy, "已报", 10, 100, "")
	ctrl := e.QMTController()
	e.repriceStaleBuys(ctrl, quoteAt(9), repriceNow)
	if got := repriceRow(t, db, "GW-OFF1").Status; got != "已报" {
		t.Fatalf("关着时状态必须保持已报, got %s", got)
	}
	if len(ctrl.SweepActions()) != 0 {
		t.Fatal("关着时不该有动作快照")
	}
}

// TestRepriceNeedsReference 拿不到参考价就不撤：把"没有 VWAP"当成"价没问题"是另一种穿透，
// 但反过来"没有参考价就撤"更是拿猜测动钱。
func TestRepriceNeedsReference(t *testing.T) {
	e, db := repriceEngine(t, 2)
	seedTicket(t, db, "GW-NOREF", "buy:600000:n_shape:"+repriceDay, "600000.SH", trading.SideBuy, "已报", 10, 100, "")
	e.repriceStaleBuys(e.QMTController(), map[string]*data.StockInfo{}, repriceNow)
	if got := repriceRow(t, db, "GW-NOREF").Status; got != "已报" {
		t.Fatalf("无参考价时不许撤, got %s", got)
	}
}

// TestRepriceDailyCap 一码当日最多两次（计数来自 orders 表的留痕，不是内存）：
// 已经有两笔为改价撤过的今天不再撤第三笔。
func TestRepriceDailyCap(t *testing.T) {
	e, db := repriceEngine(t, 2)
	seedTicket(t, db, "GW-CAP1", "buy:600000:n_shape:"+repriceDay, "600000.SH", trading.SideBuy, "撤单中", 10, 100, trading.SweepSourceVWAPReprice)
	seedTicket(t, db, "GW-CAP2", "buy:600000:n_shape2:"+repriceDay, "600000.SH", trading.SideBuy, "撤单中", 10, 100, trading.SweepSourceVWAPReprice)
	// 第三笔仍是同一只票（限次按代码计，不按幂等键计：同码多战法一天内共享两次额度）
	seedTicket(t, db, "GW-CAP3", "buy:600000:n_shape3:"+repriceDay, "600000.SH", trading.SideBuy, "已报", 10, 100, "")
	e.repriceStaleBuys(e.QMTController(), quoteAt(9), repriceNow)
	if got := repriceRow(t, db, "GW-CAP3").Status; got != "已报" {
		t.Fatalf("同码已达两次上限时不该再撤, got %s", got)
	}
}

// TestRepriceHonoursNoCancelWindow 禁撤窗口内：判定成立也不发请求（共用同一条撤单出口，
// 窗口闸只有一份）。
func TestRepriceHonoursNoCancelWindow(t *testing.T) {
	e, db := repriceEngine(t, 2)
	seedTicket(t, db, "GW-WIN1", "buy:600000:n_shape:"+repriceDay, "600000.SH", trading.SideBuy, "已报", 10, 100, "")
	ctrl := e.QMTController()
	inWindow := time.Date(2026, 10, 28, 14, 58, 0, 0, cntime.Loc)
	e.repriceStaleBuys(ctrl, quoteAt(9), inWindow)
	if got := repriceRow(t, db, "GW-WIN1").Status; got != "已报" {
		t.Fatalf("禁撤窗口内不许发撤单请求, got %s", got)
	}
	acts := ctrl.SweepActions()
	if len(acts) != 1 || acts[0].Kind != trading.SweepActionWindow {
		t.Fatalf("窗口内应记一条 window（要撤但没撤）, got %+v", acts)
	}
}

// repriceRow 读回一笔委托（全字段读者，撤单三列在里面）。
func repriceRow(t *testing.T, db *store.DB, oid string) store.RealOrder {
	t.Helper()
	rows, err := db.RealOrdersForUser("u_1")
	if err != nil {
		t.Fatalf("读委托失败: %v", err)
	}
	for _, o := range rows {
		if o.OrderID == oid {
			return o
		}
	}
	t.Fatalf("找不到委托 %s", oid)
	return store.RealOrder{}
}
