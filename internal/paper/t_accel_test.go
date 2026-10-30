// t_accel_test.go — §W-06-R4 / §W-09 纸面侧：加速配平、行情门、量能门的同口径锁。
//
// 纸面不是"另一套策略"，而是灰度期唯一的对照物：实盘改了规则、纸面不改，两边的
// done/unwound 比例就不可比，成本标定随之失效。所以这里的标签常量直接复用
// paper.TAccel*，实盘侧 t_accel_test.go 也引用同一组常量（一份期望，两处断言）。
package paper

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// H11 行情门（形态 + 量能）在纸面开腿链上与实盘同位置、同文案。
func TestPaperRegimeAndVolumeGate(t *testing.T) {
	const code = "300001"
	newFix := func() (*Engine, map[string]*data.StockInfo) {
		minuteVNow = 0
		e := seedBase(t, code, 1000)
		q := tQuote(code, 9.4, 10) // 正 T 方向（现价低于分时均线 6%）
		return e, q
	}
	block := func(e *Engine, q map[string]*data.StockInfo, p TParams, now time.Time) string {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.tOpenBlockReason(code, q, p, cntime.DayCompactOf(now), now, 1e9)
	}

	// ① bear（新鲜）⇒ 不开新腿，理由含「行情门」。
	e, q := newFix()
	p := tParams()
	p.MarketState, p.MarketStateAt = "bear", e.clockNow()
	if why := block(e, q, p, e.clockNow()); !strings.Contains(why, "行情门") {
		t.Fatalf("bear 行情应被形态门拦下，实得 %q", why)
	}

	// ② range（新鲜）⇒ 形态门放行；量能正常时整条链应能走到开腿（不因形态门误拦）。
	e2, q2 := newFix()
	p2 := tParams()
	p2.MarketState, p2.MarketStateAt = "range", e2.clockNow()
	p2.VolRatioMax = 1.8
	p2.VolRatio = func(string) (float64, bool) { return 1.2, true }
	e2.RunIntradayT(q2, p2)
	if len(e2.tRounds) != 1 {
		t.Fatalf("range + 量比 1.2 应允许开腿，实得 %d 条（形态门/量能门误拦）", len(e2.tRounds))
	}

	// ③ 放量 ⇒ step0.6 拦下，文案与实盘逐字一致。
	e3, q3 := newFix()
	p3 := tParams()
	p3.MarketState, p3.MarketStateAt = "range", e3.clockNow()
	p3.VolRatioMax = 1.8
	p3.VolRatio = func(string) (float64, bool) { return 2.4, true }
	if why := block(e3, q3, p3, e3.clockNow()); why != "step0.6 放量：折算量比 2.40 ≥ 1.80（放量行情不做 T）" {
		t.Fatalf("放量门文案与实盘不一致，实得 %q", why)
	}

	// ④ 读数缺失/无源 ⇒ 按不过处理（fail-closed），不许当"没放量"放行。
	e4, q4 := newFix()
	p4 := tParams()
	p4.MarketState, p4.MarketStateAt = "range", e4.clockNow()
	p4.VolRatioMax = 1.8
	p4.VolRatio = func(string) (float64, bool) { return 0, false }
	if why := block(e4, q4, p4, e4.clockNow()); why != "step0.6 量比读数不可得，按不过处理" {
		t.Fatalf("量比不可得必须 fail-closed，实得 %q", why)
	}
	e5, q5 := newFix()
	p5 := tParams()
	p5.MarketState, p5.MarketStateAt = "range", e5.clockNow()
	p5.VolRatioMax = 1.8
	p5.VolRatio = nil
	if why := block(e5, q5, p5, e5.clockNow()); why != "step0.6 量比读数无源（未接 VolRatio），按不过处理" {
		t.Fatalf("量比无源必须 fail-closed，实得 %q", why)
	}
}

// ACC-21 同一组输入（买 1000@9.4、现价 9.0、RoundStopPct=2.0）⇒ 纸面与实盘返回同一个标签常量。
func TestPaperAccelMirrorsLive(t *testing.T) {
	const code = "300001"
	minuteVNow = 0
	e := seedBase(t, code, 1000)
	e.RunIntradayT(tQuote(code, 9.4, 10), tParams())
	if len(e.tRounds) != 1 || e.tRounds[0].Leg != tRLegBuy {
		t.Fatalf("前置：应开一条正 T 买腿，实得 %+v", e.tRounds)
	}
	r := &e.tRounds[0]
	e.mu.Lock()
	got := e.accelReasonLocked(r, 9.0, tParams()) // 9.0 vs 9.4 ≈ −4.3% ≤ −2.0%
	e.mu.Unlock()
	if got != TAccelStop {
		t.Fatalf("纸面止损标签应与实盘同一常量 %q，实得 %q", TAccelStop, got)
	}
	// 与实盘 ACC-1 一样：加速必须真的把第二腿发出去（不是只返回标签）。
	e.RunIntradayT(tQuote(code, 9.0, 10), tParams())
	if r.State != tRStateDone || r.PairQty != r.LegQty {
		t.Fatalf("加速配平应发第二腿并 done，实得 %+v", r)
	}
}

// ACC-22 加速配平后：T 腿退出 cur（tOpenBuyValue=0）、冻结款归零、done 的 Pnl 为负、
// 持仓回到开腿前的数量。价格在两次调用间已从 9.4 跌到 9.0，绝对 curStrat 会随市值变化，
// 所以断言"结构回到开腿前"（只剩余战略底仓），而不是拿两个不同时点的绝对值比大小。
func TestPaperAccelStopPnlAndRoom(t *testing.T) {
	const code = "300001"
	minuteVNow = 0
	e := seedBase(t, code, 1000)
	baseQty := 1000
	e.RunIntradayT(tQuote(code, 9.4, 10), tParams())
	if len(e.tRounds) != 1 {
		t.Fatalf("前置：应开正 T 买腿，实得 %d", len(e.tRounds))
	}
	e.RunIntradayT(tQuote(code, 9.0, 10), tParams())
	r := e.tRounds[0]
	if r.State != tRStateDone {
		t.Fatalf("加速配平成功应记 done（不是 unwound/converted），实得 %+v", r)
	}
	if r.PnL >= 0 {
		t.Fatalf("9.4 买、9.0 配平，Pnl 必须为负（价差 −4.3%%），实得 %.2f", r.PnL)
	}
	e.mu.Lock()
	tOpenBuy := e.tOpenBuyValueLocked()
	tOpenSell := e.tOpenSellValueLocked()
	cur := e.poolEquityLocked("dragon") - e.pools["dragon"]
	if cur < 0 {
		cur = 0
	}
	cur -= tOpenBuy
	cur += tOpenSell
	qty := e.positions[code].Qty
	e.mu.Unlock()
	if tOpenBuy != 0 || tOpenSell != 0 {
		t.Fatalf("配平后 T 腿必须退出 cur（tOpenBuy=%.2f tOpenSell=%.2f）", tOpenBuy, tOpenSell)
	}
	if qty != baseQty {
		t.Fatalf("配平后持仓应回到开腿前的 %d 股，实得 %d", baseQty, qty)
	}
	if cur <= 0 {
		t.Fatalf("配平后仍应剩战略底仓市值（cur=%.2f）", cur)
	}
	if e.TFrozenCash() != 0 {
		t.Fatalf("终态必须释放冻结款，实得 %.2f", e.TFrozenCash())
	}
}

// ACC-23 force（尾盘）⇒ premiseGoneLocked 不判，交给 pair 的尾盘分支；
// 盘中同一读数 ⇒ 反 T 转 unwound（P3）。
func TestPaperPremiseGoneSkipsAtForce(t *testing.T) {
	const code = "300001"
	minuteVNow = 0
	e := seedBase(t, code, 1000)
	e.RunIntradayT(tQuote(code, 10.55, 10), tParams()) // 反 T 卖腿
	if len(e.tRounds) != 1 || e.tRounds[0].Leg != tRLegSell {
		t.Fatalf("前置：应开一条反 T 卖腿，实得 %+v", e.tRounds)
	}
	p := tParams()
	p.MarketPlungePct, p.IndexChangePct = -2.0, -3.0 // 大盘急跌 ⇒ 纸面 P3 判据成立
	e.mu.Lock()
	stateForce, _ := e.premiseGoneLocked(code, p, true)
	stateIntraday, why := e.premiseGoneLocked(code, p, false)
	e.mu.Unlock()
	if stateForce != "" {
		t.Fatalf("尾盘 force 分支不许走 premiseGone（应与实盘一样交给买回许可），实得 %q", stateForce)
	}
	if stateIntraday != tRStateUnwound || !strings.Contains(why, "大盘急跌") {
		t.Fatalf("盘中熔断读数应让反 T 转 unwound，实得 %q / %q", stateIntraday, why)
	}
}
