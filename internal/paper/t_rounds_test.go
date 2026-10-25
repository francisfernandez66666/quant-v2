// t_rounds_test.go — §T-MODE 做 T（2026-10-24 裁决四，W6 正 T / W7 反 T）的行为锁。
//
// 这一批锁的是七件"改前根本不存在、改错会直接亏钱"的事：
//
//	① 正 T 两腿成对后**持仓回到原量、锁定价差为正**，且当日可卖量按层账如实下降（机动仓被 T+1 锁住）；
//	② 正 T 的买腿到尾盘仍没价差 ⇒ **强制卖出等量底仓**，绝不把机动仓留过夜；
//	③ 反 T 的买回被拦（现金不足）⇒ 按 Q8 **收摊不追买**，腿留在普通减仓口径里、仓位不回补；
//	④ 反 T 卖腿**永远留一手底仓**（卖光会删行 + 记冷却，买回腿当场被自家闸门挡死）；
//	⑤ 做 T **不烧当日减仓槽**（trimDone 空），否则一次做 T 会吞掉当天真正的退出裁决；
//	⑥ 做 T **不改写锚点**（风险线基准是首买价，做 T 只摊薄成本）；
//	⑦ 前置闸：legacy 预算档整轮禁用（Q5）、同一代码同一时刻只有一条开腿（两腿串行）、
//	   单日次数用尽后不再开新腿。
//
// English: behavioural locks for §T-MODE — a completed buy-first round restores the position size and
// locks a positive spread while sellable drops by the T+1-locked intraday layer; the forced end-of-day
// pairing; the Q8 unwind when the buy-back is blocked; sell-first always keeps one lot; T legs never
// burn the trim slot and never rewrite the anchor; and the four preconditions (legacy budget, one open
// leg per code, daily round cap).
package paper

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// seedBase 做 T 用例的夹具：种一条昨日底仓 → 把预算口径切成 dynamic（Q5：legacy 档下做 T 禁用，
// 夹具必须是 dynamic 才有被测面）。再入场冷却在 partialTestEngine 里已关（冷却属另一条闸，
// 会遮住本文件的被测行为）。
//
// ⚠ 顺序是硬要求：**先按显式股数种底仓，再切 dynamic**。dynamic 口径下 `fillLocked` 会忽略
// 显式股数、按池权益×单票上限定仓（10 万×35%÷10 元 = 3500 股），先切口径会让夹具种出 3500 股
// 而不是想要的那一档，所有关于"整手取整/留一手"的断言都会当场失去区分度。
// English: fixture — seed a yesterday-dated base position with an explicit share count FIRST, then flip
// the budget pipeline to dynamic. Under dynamic the fill ignores the requested quantity and sizes off the
// pool equity (3500 shares here), which would silently invalidate every lot-rounding assertion.
func seedBase(t *testing.T, code string, qty int) *Engine {
	t.Helper()
	e := partialTestEngine(t)
	if err := e.BuyExInPool(code, "测试股", "龙头", "dragon", 0, 10, qty, nil); err != nil {
		t.Fatalf("预置 %d 股底仓失败: %v", qty, err)
	}
	t1Ready(e) // 变成昨日持仓（可卖）
	e.mu.Lock()
	e.cfg.BudgetMode = "dynamic"
	e.mu.Unlock()
	pinTRClock(e)
	return e
}

// pinTRClock 把做 T 用例的时钟钉在**当日 10:00（交易时段内、未到 14:50 尾盘线）**。
//
// 这不是洁癖：`RunIntradayT` 用"现在"判断是否已过 `ForceUnwindHM`，过线就 `force=true` 直接
// 跳过开腿。取真实时钟的后果是——**同一份代码，上午跑全绿、傍晚跑全红**（正 T 两条用例在 14:50
// 之后 100% 失败）。这种用例等于没有用例，还会把真实回归淹掉。时钟缝在生产路径留 nil，行为不变。
// English: pins the T-round clock at 10:00 today. RunIntradayT compares "now" against the forced-pairing
// line and short-circuits leg opening once past it, so a wall clock makes the buy-first cases fail 100%
// of the time after 14:50 — green in the morning, red in the evening. The seam is nil in production.
func pinTRClock(e *Engine) {
	n := cntime.In(time.Now())
	pinned := time.Date(n.Year(), n.Month(), n.Day(), 10, 0, 0, 0, n.Location())
	e.mu.Lock()
	e.nowFn = func() time.Time { return pinned }
	e.mu.Unlock()
}

// tRoundParams 默认参数的做 T 入参（单码单日 1 轮、机动仓 30%、最小价差 1%、尾盘 14:50）。
// English: default T parameters (one round/day/code, 30% intraday cap, 1% spread, 1450 unwind).
func tRoundParams() TParams {
	return TParams{BuyFirst: true, SellFirst: true, MaxRounds: 1, CapPct: 30, MinSpreadPct: 1.0, ForceUnwindHM: "1450"}
}

// TestTRoundBuyFirstPairsAndLocksSpread ① 正 T 完整一轮：低吸 300 股 → 涨 3% 卖出 300 底仓。
// 断言持仓回到 1000（原量）、回转 done、价差为正、可卖量降到 700（当日买的 300 被 T+1 锁住）、
// 锚点仍是 10（未被做 T 改写）、trimDone 未被烧、订单留痕带"正T买入/正T卖出"。
// English: a full buy-first round — the position returns to 1000 shares, the round is done with a
// positive spread, sellable drops to 700 (the 300 bought today stay T+1-locked), the anchor stays 10,
// the trim slot is untouched and both legs are audited with their own order kinds.
func TestTRoundBuyFirstPairsAndLocksSpread(t *testing.T) {
	e := seedBase(t, "300001", 1000) // 1000 股 @10，锚点=10，昨日持仓（可卖 1000）
	p := e.positions["300001"]
	if p.AnchorPrice != 10 {
		t.Fatalf("夹具锚点应为 10, got %v", p.AnchorPrice)
	}
	params := tRoundParams()

	// 第一腿：现价 9.5（相对锚点 10 跌 5% ≥ 1%）⇒ 低吸 300 股（机动仓上限 30%×1000）。
	e.RunIntradayT(quotesOf(9.5), params)
	if len(e.tRounds) != 1 {
		t.Fatalf("应开 1 条回转, got %d", len(e.tRounds))
	}
	r := e.tRounds[0]
	if r.State != tRStateOpen || r.Leg != tRLegBuy || r.LegQty != 300 {
		t.Fatalf("第一腿应为 open/buy/300股, got %+v", r)
	}
	if got := e.positions["300001"].Qty; got != 1300 {
		t.Fatalf("买腿后持仓应为 1300, got %d", got)
	}
	if e.positions["300001"].AnchorPrice != 10 {
		t.Fatalf("买腿不得改写锚点, got %v", e.positions["300001"].AnchorPrice)
	}
	if e.trimDone["300001"] != "" {
		t.Fatalf("做 T 不得烧当日减仓槽, got %q", e.trimDone["300001"])
	}

	// 第二腿：现价 9.8（相对 9.5 涨 3.16% ≥ 1%）⇒ 卖出等量底仓 300 股。
	e.RunIntradayT(quotesOf(9.8), params)
	r = e.tRounds[0]
	if r.State != tRStateDone {
		t.Fatalf("第二腿后回转应为 done, got %+v", r)
	}
	if r.PairQty != 300 {
		t.Fatalf("配对量应为 300, got %d", r.PairQty)
	}
	// 价差 = (9.8 − 9.5) × 300 = 90（夹具无滑点/无手续费档）。
	if r.PnL <= 0 {
		t.Fatalf("锁定价差应为正, got %v（配对价 %v）", r.PnL, r.PairPrice)
	}
	pos := e.positions["300001"]
	if pos.Qty != 1000 {
		t.Fatalf("配对后持仓应回到原量 1000, got %d", pos.Qty)
	}
	// 当日买的 300 股被 T+1 锁住 ⇒ 今日可卖只剩 700（这是正 T 的必然代价，不是缺陷）。
	if got := e.sellableQtyLocked(pos, time.Now()); got != 700 {
		t.Fatalf("配对后可卖量应为 700（底仓1000−卖出300）, got %d", got)
	}
	if pos.AnchorPrice != 10 {
		t.Fatalf("卖腿不得改写锚点, got %v", pos.AnchorPrice)
	}
	if e.trimDone["300001"] != "" {
		t.Fatalf("卖腿同样不得烧当日减仓槽, got %q", e.trimDone["300001"])
	}
	// 订单留痕：两条腿各有自己的 Kind，事后能看出"这两笔是做 T"。
	var buyKind, sellKind bool
	for _, o := range e.orders {
		switch o.Kind {
		case "正T买入":
			buyKind = o.Status == "filled" && o.Qty == 300
		case "正T卖出":
			sellKind = o.Status == "filled" && o.Qty == 300
		}
	}
	if !buyKind || !sellKind {
		t.Fatalf("应留痕 正T买入/正T卖出 两条 filled 订单, got buy=%v sell=%v", buyKind, sellKind)
	}
}

// TestTRoundBuyFirstForcedUnwindAtClose ② 买腿后价差始终没到 ⇒ 尾盘（force=true）强制卖等量底仓。
// 反证：若有人把"到点强制配平"删掉，这里会停在 open 且持仓停在 1300（机动仓隔夜）。
// English: with no spread the forced end-of-day pass still sells an equal base amount — without it the
// round would stay open and 1300 shares would go overnight.
func TestTRoundBuyFirstForcedUnwindAtClose(t *testing.T) {
	e := seedBase(t, "300001", 1000)
	params := tRoundParams()

	e.RunIntradayT(quotesOf(9.5), params) // 开买腿 300
	if e.tRounds[0].State != tRStateOpen {
		t.Fatalf("第一腿后应为 open, got %+v", e.tRounds[0])
	}
	// 同一价位再来一轮：价差 0 < 1% ⇒ 不配对（保持 open，持仓 1300）。
	e.RunIntradayT(quotesOf(9.5), params)
	if e.tRounds[0].State != tRStateOpen {
		t.Fatalf("价差未到不得配对, got %+v", e.tRounds[0])
	}
	if got := e.positions["300001"].Qty; got != 1300 {
		t.Fatalf("价差未到持仓应仍为 1300, got %d", got)
	}

	// 尾盘时刻：ForceUnwindHM 设成 "0000" ⇒ 任何时刻都算到点，强制配平。
	closeParams := params
	closeParams.ForceUnwindHM = "0000"
	e.RunIntradayT(quotesOf(9.5), closeParams)
	r := e.tRounds[0]
	if r.State != tRStateDone {
		t.Fatalf("尾盘强制配平后应为 done, got %+v", r)
	}
	if !strings.Contains(r.Reason, "配对完成") {
		t.Fatalf("原因应说明是配对完成, got %q", r.Reason)
	}
	if got := e.positions["300001"].Qty; got != 1000 {
		t.Fatalf("强制配平后持仓应回到 1000, got %d", got)
	}
}

// TestTRoundSellFirstUnwindsWhenBuyBackBlocked ③ 反 T 买回被拦（现金抽干）⇒ 收摊、不追买、仓位不回补。
// 这条是 Q8 的直接体现：已经证明"买不回来"之后继续加注，方向是错的。
// English: Q8 — a blocked buy-back unwinds the round instead of retrying; the position stays trimmed.
func TestTRoundSellFirstUnwindsWhenBuyBackBlocked(t *testing.T) {
	e := seedBase(t, "300001", 1000) // 1000 @10，昨日持仓
	params := tRoundParams()

	// 高抛：现价 11（相对锚点 10 涨 10%）⇒ 卖 300 底仓。
	e.RunIntradayT(quotesOf(11), params)
	if len(e.tRounds) != 1 || e.tRounds[0].Leg != tRLegSell || e.tRounds[0].LegQty != 300 {
		t.Fatalf("反T第一腿应为 sell/300股, got %+v", e.tRounds)
	}
	if got := e.positions["300001"].Qty; got != 700 {
		t.Fatalf("卖腿后持仓应为 700, got %d", got)
	}
	if e.trimDone["300001"] != "" {
		t.Fatalf("反T卖腿不得烧当日减仓槽, got %q", e.trimDone["300001"])
	}

	// 抽干现金与所属池 ⇒ 买回必然被 errCash 拦下。
	e.mu.Lock()
	e.cash = 0
	e.pools["dragon"] = 0
	e.mu.Unlock()

	// 回踩：现价 10.5（相对 11 跌 4.5% ≥ 1%）⇒ 买回被拦 ⇒ 收摊。
	e.RunIntradayT(quotesOf(10.5), params)
	r := e.tRounds[0]
	if r.State != tRStateUnwound {
		t.Fatalf("买回被拦应收摊（unwound）, got %+v", r)
	}
	if !strings.Contains(r.Reason, "买回被拦") {
		t.Fatalf("原因应写明买回被拦, got %q", r.Reason)
	}
	if got := e.positions["300001"].Qty; got != 700 {
		t.Fatalf("收摊后仓位应停在 700（不追买）, got %d", got)
	}
	// 被拒的买回腿也要留痕（事后能看出"为什么没买回来"）。
	var rejected bool
	for _, o := range e.orders {
		if o.Kind == "反T买回" && o.Status == "rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("买回被拒应留痕一条 rejected 订单")
	}
}

// TestTRoundSellFirstNeverSellsOut ④ 反 T 卖腿永远留一手底仓：400 股 ⇒ 只卖 100（整手且 ≤ 400−100）。
// 反证：若把 tRMinBaseKeep 去掉，这里会卖 120 股，持仓跌破整手边界；若上界算错成整仓，
// 会卖 300 股留 100，与"机动仓上限 30%=120 取整手 100"这条口径不符。
// English: sell-first keeps one lot — 400 shares yields a 100-share leg (whole lots, capped by both the
// 30% intraday cap and base−100), so the position never drops to a flat/zombie row.
func TestTRoundSellFirstNeverSellsOut(t *testing.T) {
	e := seedBase(t, "300001", 400) // 400 股底仓（整手 + 留一手的上界都会压到这条腿上）
	params := tRoundParams()

	e.RunIntradayT(quotesOf(11), params) // 涨 10% ⇒ 高抛
	if len(e.tRounds) != 1 {
		t.Fatalf("应开 1 条反T回转, got %d", len(e.tRounds))
	}
	if got := e.tRounds[0].LegQty; got != 100 {
		t.Fatalf("卖腿应为 100 股（30%%×400=120 取整手，且 ≤ 400−100）, got %d", got)
	}
	if got := e.positions["300001"].Qty; got != 300 {
		t.Fatalf("卖腿后应留 300 股底仓, got %d", got)
	}
	if got := e.positions["300001"].Qty; got <= 0 {
		t.Fatal("反T绝不能把持仓卖光（卖光会删行+记冷却，买回腿当场被挡死）")
	}

	// 底仓 100 股（不足两手）⇒ 上界 100−100=0，整轮不开。
	e2 := seedBase(t, "300002", 100)
	e2.RunIntradayT(map[string]*data.StockInfo{}, params)
	if len(e2.tRounds) != 0 {
		t.Fatalf("100 股底仓做不了反T（留一手后无可卖量）, got %+v", e2.tRounds)
	}
}

// TestTRoundRespectsPreconditions ⑦ 三道前置：legacy 预算档禁用、同一代码只有一条开腿、单日次数用尽。
// 三条各自都能独立把"多开一条腿"挡下来，所以分三段断言而不是合成一条。
// English: three preconditions asserted separately — legacy budget disables T entirely, one code never
// has two open legs, and the daily round cap stops a second round even after the intraday layer matured.
func TestTRoundRespectsPreconditions(t *testing.T) {
	params := tRoundParams()

	// ① legacy 预算档（Q5）：整轮禁用，一条回转都不开。
	e := seedBase(t, "300001", 1000)
	e.mu.Lock()
	e.cfg.BudgetMode = "legacy"
	e.mu.Unlock()
	e.RunIntradayT(quotesOf(9.5), params)
	if len(e.tRounds) != 0 {
		t.Fatalf("legacy 预算档下不得开做 T 回转, got %+v", e.tRounds)
	}

	// ② 同一代码同一时刻只有一条开腿：买腿开着时再来一次低吸，不得叠加第二条。
	e2 := seedBase(t, "300001", 1000)
	e2.RunIntradayT(quotesOf(9.5), params)
	e2.RunIntradayT(quotesOf(9.4), params) // 更低的价，仍不该开第二条
	if len(e2.tRounds) != 1 {
		t.Fatalf("同一代码不得有两条开腿（两腿串行）, got %d 条", len(e2.tRounds))
	}
	if e2.tRounds[0].State != tRStateOpen {
		t.Fatalf("开腿应保持 open, got %+v", e2.tRounds[0])
	}

	// ③ 单日次数用尽：完成一轮后把机动仓回拨成底仓（模拟次日），再触发低吸 ⇒ MaxRounds=1 拦住。
	e3 := seedBase(t, "300001", 1000)
	e3.RunIntradayT(quotesOf(9.5), params) // 开买腿
	e3.RunIntradayT(quotesOf(9.8), params) // 配对完成
	if e3.tRounds[0].State != tRStateDone {
		t.Fatalf("夹具要求第一轮已完成, got %+v", e3.tRounds[0])
	}
	t1Ready(e3) // 让当日批次变成昨日批次（次日形态）
	before := len(e3.tRounds)
	e3.RunIntradayT(quotesOf(9.5), params)
	if len(e3.tRounds) != before {
		t.Fatalf("单日回转次数已用尽（MaxRounds=1）, 不得再开新腿, got %d→%d", before, len(e3.tRounds))
	}
}

// TestTRoundSellFirstForceUnwindAtClose ②' 反 T 卖腿到点仍未回踩 ⇒ 按普通减仓收摊（Q8 不追买）。
// 与正 T 的强制配平相反：正 T 是"把机动仓卖回去"，反 T 是"认下这次减仓"，两个方向不能混。
// English: an unpaired sell-first leg at the forced time is unwound as a plain trim (Q8) — the opposite
// of buy-first, where the forced pass sells the base back.
func TestTRoundSellFirstForceUnwindAtClose(t *testing.T) {
	e := seedBase(t, "300001", 1000)
	params := tRoundParams()

	e.RunIntradayT(quotesOf(11), params) // 开卖腿 300
	if e.tRounds[0].State != tRStateOpen {
		t.Fatalf("第一腿后应为 open, got %+v", e.tRounds[0])
	}
	closeParams := params
	closeParams.ForceUnwindHM = "0000"
	e.RunIntradayT(quotesOf(11), closeParams) // 到点、价格没回踩
	r := e.tRounds[0]
	if r.State != tRStateUnwound {
		t.Fatalf("反T尾盘未回踩应收摊, got %+v", r)
	}
	if !strings.Contains(r.Reason, "不追买") {
		t.Fatalf("原因应写明不追买, got %q", r.Reason)
	}
	if got := e.positions["300001"].Qty; got != 700 {
		t.Fatalf("收摊后仓位停在 700（那笔高抛就是一次减仓）, got %d", got)
	}
	// 收摊后不得再开新腿（尾盘只收摊）。
	if len(e.tRounds) != 1 {
		t.Fatalf("尾盘不得开新腿, got %d 条", len(e.tRounds))
	}
}
