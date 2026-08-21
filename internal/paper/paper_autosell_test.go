// 阶段1.1 卖出信号自动成交测试：SellAction 词汇归一、清仓/减仓自动执行、当日减仓去重、
// AutoSell 开关、开仓/清仓镜像回调（阶段1.2 两本账合一）。
// English: full-auto sell tests — SellAction vocabulary normalization, auto close/trim execution,
// same-day trim dedup, the AutoSell switch, and open/close mirror callbacks (unified books).
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// sellTestEngine 构造已启用、单池、无持久化的模拟盘（固定金额 10000 → 10 元股价买 1000 股）。
// English: builds an enabled single-pool in-memory engine (fixed 10000 → 1000 shares at price 10).
func sellTestEngine(t *testing.T, autoSell bool) *Engine {
	t.Helper()
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: autoSell}, "")
	e.SetStrategyPools([]string{"dragon"})
	return e
}

// quotesOf 构造实时快照行情。
// English: builds a live-quote snapshot.
func quotesOf(price float64) map[string]*data.StockInfo {
	return map[string]*data.StockInfo{"300001": {Price: price}}
}

// buyFill 先用买入信号成交一笔持仓（1000 股 @10）。
// English: fills one position from a buy signal (1000 shares @10).
func buyFill(t *testing.T, e *Engine) {
	t.Helper()
	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头", StrategyType: "dragon",
		Action: "buy", Price: 10, ATR: 0.2, GeneratedAt: time.Now()}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10))
	p, ok := e.positions["300001"]
	if !ok || p.Qty != 1000 {
		t.Fatalf("预置持仓失败: %+v", e.positions["300001"])
	}
}

// TestSellActionVocabulary SellAction 词汇归一：清仓→close；减仓/利空抛售→trim；
// 硬止盈/止损（Action）→close；软提示/关注/跌幅提醒→""；做空方向词→""。
// English: SellAction vocabulary — 清仓→close; 减仓/利空抛售→trim; hard TP/SL by Action→close;
// soft 提示/关注/跌幅提醒→""; short-direction badge→"".
func TestSellActionVocabulary(t *testing.T) {
	cases := []struct {
		s    combat_agent.Signal
		want string
	}{
		{combat_agent.Signal{Direction: "提醒", Action: "卖出", AlertType: "清仓"}, "close"},
		{combat_agent.Signal{Direction: "提醒", Action: "卖出", AlertType: "减仓"}, "trim"},
		{combat_agent.Signal{Direction: "提醒", Action: "卖出", AlertType: "利空抛售"}, "trim"},
		{combat_agent.Signal{Direction: "提醒", Action: "止盈", AlertType: "止盈"}, "close"},
		{combat_agent.Signal{Direction: "提醒", Action: "止损", AlertType: "止损"}, "close"},
		{combat_agent.Signal{Direction: "提醒", Action: "关注", AlertType: "提示"}, ""},
		{combat_agent.Signal{Direction: "提醒", Action: "关注", AlertType: "跌幅提醒"}, ""},
		{combat_agent.Signal{Direction: "做空", Action: "buy", AlertType: ""}, ""},
	}
	for i, c := range cases {
		if got := combat_agent.SellAction(c.s); got != c.want {
			t.Errorf("case %d: want %q got %q (%+v)", i, c.want, got, c.s)
		}
	}
}

// TestOnSignalsAutoClose 清仓信号自动全平：现金回池、镜像 onClose 触发、重复清仓 no-op。
// English: a 清仓 signal closes fully — cash back to pool, mirror onClose fired, repeats are no-ops.
func TestOnSignalsAutoClose(t *testing.T) {
	e := sellTestEngine(t, true)
	var closedCode string
	var closedReason string
	e.SetMirror(nil, func(code string, price, qty float64, reason string) {
		closedCode, closedReason = code, reason
	})
	buyFill(t, e)

	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头",
		Direction: "提醒", Action: "卖出", AlertType: "清仓", Reason: "炸板全出"}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(11))

	if _, held := e.positions["300001"]; held {
		t.Fatal("清仓信号后应无持仓")
	}
	if closedCode != "300001" || closedReason != "自动清仓" {
		t.Fatalf("镜像未正确平仓: code=%s reason=%s", closedCode, closedReason)
	}
	poolCash := e.pools["dragon"]
	if poolCash <= 100000*0.5 {
		t.Fatalf("清仓收益应回 dragon 池, got %.2f", poolCash)
	}
	// 重复清仓 no-op（不 panic、不再产生卖出记录）
	before := len(e.trades)
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(11))
	if len(e.trades) != before {
		t.Fatalf("重复清仓不应新增成交: %d → %d", before, len(e.trades))
	}
}

// TestOnSignalsAutoTrimOnceDaily 减仓信号半仓且每码每日一次：首轮卖 500 股，同日第二轮不减。
// English: trim signals halve once per day — first round sells 500 shares, a same-day second round does nothing.
func TestOnSignalsAutoTrimOnceDaily(t *testing.T) {
	e := sellTestEngine(t, true)
	buyFill(t, e)

	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头",
		Direction: "提醒", Action: "卖出", AlertType: "减仓", Reason: "移动止盈"}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10.5))
	if p := e.positions["300001"]; p == nil || p.Qty != 500 {
		t.Fatalf("首次减仓应剩 500 股, got %+v", e.positions["300001"])
	}
	// 同日第二轮减仓告警（如另一来源）不再减
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10.5))
	if p := e.positions["300001"]; p == nil || p.Qty != 500 {
		t.Fatalf("同日二次减仓应被去重, got %+v", e.positions["300001"])
	}
}

// TestOnSignalsHardTPSLCloses 硬止盈/止损（CheckPositionAlerts 的 Action=止盈/止损）同样全平。
// English: hard take-profit / stop-loss alerts (Action=止盈/止损) also close fully.
func TestOnSignalsHardTPSLCloses(t *testing.T) {
	for _, at := range []string{"止盈", "止损"} {
		e := sellTestEngine(t, true)
		buyFill(t, e)
		sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头",
			Direction: "提醒", Action: at, AlertType: at}
		e.OnSignals([]combat_agent.Signal{sig}, quotesOf(12))
		if _, held := e.positions["300001"]; held {
			t.Fatalf("%s 信号应全平持仓", at)
		}
	}
}

// TestAutoSellDisabled AutoSell=false 时卖出告警不自动成交（仅提醒）。
// English: with AutoSell off, sell alerts never auto-close (reminder-only).
func TestAutoSellDisabled(t *testing.T) {
	e := sellTestEngine(t, false)
	buyFill(t, e)
	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头",
		Direction: "提醒", Action: "卖出", AlertType: "清仓"}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(9))
	if p := e.positions["300001"]; p == nil || p.Qty != 1000 {
		t.Fatal("AutoSell 关闭时不应自动平仓")
	}
}

// TestMirrorOpenOnBuy 开仓镜像：自动撮合与手动买入都触发 onOpen，加仓不触发。
// English: open mirror fires for both auto fills and manual buys; add-ons don't fire it.
func TestMirrorOpenOnBuy(t *testing.T) {
	e := sellTestEngine(t, true)
	var opens []Position
	e.SetMirror(func(p Position) { opens = append(opens, p) }, nil)

	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头", StrategyType: "dragon",
		Action: "buy", Price: 10, ATR: 0.2, GeneratedAt: time.Now()}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10))
	if len(opens) != 1 || opens[0].Qty != 1000 || opens[0].ATR != 0.2 {
		t.Fatalf("自动撮合应触发一次开仓镜像(含ATR): %+v", opens)
	}
	// 手动买入也触发
	manualQuotes := map[string]*data.StockInfo{
		"300001": {Price: 10},
		"600000": {Price: 5},
	}
	if err := e.Buy("600000", "手动股", "手动", 0, manualQuotes); err != nil {
		t.Fatalf("手动买入失败: %v", err)
	}
	if len(opens) != 2 {
		t.Fatalf("手动买入应触发开仓镜像, opens=%d", len(opens))
	}
}

// TestOrderLifecycle 订单生命周期留痕（阶段1.3）：成交→filled、减仓→partial、
// 现金不足被拒→rejected（含原因），Orders() 最新在前。
// English: order-lifecycle audit — filled / partial / rejected(with reason); Orders() newest first.
func TestOrderLifecycle(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 1000, AutoSell: true}, "")
	e.SetStrategyPools([]string{"dragon"})
	// 1000 元本金买不起一手 10 元股 → rejected
	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头", StrategyType: "dragon",
		Action: "buy", Price: 10, GeneratedAt: time.Now()}
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10))
	if len(e.orders) != 1 || e.orders[0].Status != "rejected" {
		t.Fatalf("现金不足应留 rejected 订单: %+v", e.orders)
	}
	if e.orders[0].Reason == "" {
		t.Fatal("rejected 订单应带原因")
	}
	// 重置资金后成交 → filled
	e.mu.Lock()
	e.cash = 100000
	e.pools["dragon"] = 50000
	e.mu.Unlock()
	e.OnSignals([]combat_agent.Signal{sig}, quotesOf(10))
	p, ok := e.orders[len(e.orders)-1], true
	_ = ok
	if p.Status != "filled" || p.Qty != 1000 || p.Price != 10 {
		t.Fatalf("成交应留 filled 订单: %+v", p)
	}
	// 减仓 → partial
	trim := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头",
		Direction: "提醒", Action: "卖出", AlertType: "减仓"}
	e.OnSignals([]combat_agent.Signal{trim}, quotesOf(11))
	last := e.orders[len(e.orders)-1]
	if last.Status != "partial" || last.Qty != 500 {
		t.Fatalf("减仓应留 partial 订单(500股): %+v", last)
	}
	// Orders() 最新在前
	all := e.Orders()
	if all[0].CreatedAt.Before(all[len(all)-1].CreatedAt) {
		t.Fatal("Orders() 应按时间倒序")
	}
}
