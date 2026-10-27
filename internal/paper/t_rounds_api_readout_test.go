// t_rounds_api_readout_test.go — §DYN-SIZE v4 目标仓位读数的行为锁（方案 §8.8 末条「面板展示」）。
//
// 锁四条，全部是"读数不能替引擎圆场"的形态：
//  1. 未注入定权上下文时 `market_scale` 必须是 0（**不许兜底成 1.0**）。
//     兜成 1.0 会让面板显示"满额目标"，而引擎其实还没定权——那是把"不知道"渲染成"一切正常"，
//     本仓在 §M2/§M3 上反复吃过同一形状的亏；
//  2. 天花板归一与下单路径逐字同参：`ceiling>1`（把 100% 写成 100 的手滑）必须钳回 1.0，
//     而 `per_trade_pct>1` 必须钳回出厂 0.10 而不是 1.0；
//  3. 反 T 总量帽的分母是 **min(总资产, 持仓市值)**（六轮 T6-2）：低仓账号按总资产算会拿到
//     一个比自己持仓还大的卖额上限，方向正好反了；
//  4. 战略当前数必须**减掉在途正 T 买腿、加回在途反 T 卖腿**：漏掉任一边，
//     日内回转就会把目标空间吃掉（少买）或凭空多出空间（少加），两种都是真金白银的偏差。
//
// English: locks for the v4 target readout — an uninjected scale stays 0 instead of being faked,
// normalization matches the sizing path, the sell-cap denominator is min(total, market value),
// and open T legs move the strategic current number in both directions.
package paper

import (
	"testing"
	"time"
)

// seedTPositionsForReadout 直接搭账本内部态（持仓 + 一条在途腿），避免测试借道撮合链。
func seedTPositionsForReadout(t *testing.T, e *Engine, code string, qty int, price float64) {
	t.Helper()
	e.mu.Lock()
	e.positions[code] = &Position{Code: code, Name: "读数样本", Qty: qty, CostPrice: price,
		Cost: price * float64(qty), Mark: price, AnchorPrice: price,
		AnchorSource: anchorSourceFirstLot, FilledAt: time.Now().Add(-48 * time.Hour)}
	e.mu.Unlock()
}

func TestTargetReadoutScaleIsNotFaked(t *testing.T) {
	e := New(Config{Enabled: true, AutoSell: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	seedTPositionsForReadout(t, e, "600000", 1000, 12.0)

	// ① 没注入定权上下文：scale 必须是 0，且目标市值按 0 算（"还不知道市场给多少"就是没有目标）。
	r0 := e.TargetReadout()
	if r0.MarketScale != 0 {
		t.Fatalf("未注入时 market_scale 必须是 0（不许兜成 1.0 假装满额），got %v", r0.MarketScale)
	}
	if r0.Target != 0 || r0.RatioUsed != 0 {
		t.Fatalf("未注入时目标市值应按 0 计，got target=%.2f ratio=%.3f", r0.Target, r0.RatioUsed)
	}
	if r0.Room != 0 {
		t.Fatalf("没有目标就没有空间，room 应为 0，got %.2f", r0.Room)
	}
	if r0.Total <= 0 {
		t.Fatalf("权益读数缺失，got %+v", r0)
	}

	// ② 注入正常读数：目标 = 总资产 × min(MS, 天花板)，Room = 目标 − 战略当前。
	e.SetSizingContext(0.60, 0.10, 0.30, 1.00, false)
	r := e.TargetReadout()
	if r.MarketScale != 0.60 || r.RatioUsed != 0.60 {
		t.Fatalf("MS 应原样透出并按 min 生效，got ms=%.3f ratio=%.3f", r.MarketScale, r.RatioUsed)
	}
	wantTarget := round2(r.Total * 0.60)
	if r.Target != wantTarget {
		t.Fatalf("目标市值应为 总资产×0.60=%.2f，got %.2f", wantTarget, r.Target)
	}
	wantCur := round2(r.MarketValue) // 无在途腿时战略当前数＝持仓市值
	if r.CurStrat != wantCur {
		t.Fatalf("无在途腿时 cur_strat 应等于持仓市值 %.2f，got %.2f", wantCur, r.CurStrat)
	}
	if r.Room != round2(wantTarget-wantCur) || r.Room < 0 {
		t.Fatalf("room 应等于 max(0, 目标−当前)，got room=%.2f target=%.2f cur=%.2f", r.Room, r.Target, r.CurStrat)
	}
	if r.TQuota != round2(r.Total-r.Target) {
		t.Fatalf("T 额度应等于战略让出来的那部分（总资产−目标），got %.2f", r.TQuota)
	}

	// ③ 天花板归一必须与下单路径同参：>1 是"把 100% 写成 100"的手滑，钳回 1.0 而不是当成 100 倍。
	e.SetSizingContext(0.60, 0.10, 0.30, 100, false)
	if r2 := e.TargetReadout(); r2.Ceiling != 1.00 || r2.RatioUsed != 0.60 {
		t.Fatalf("天花板 >1 必须钳回 1.0（与 dynamicPaperQtyLocked 同参），got ceiling=%.2f ratio=%.3f", r2.Ceiling, r2.RatioUsed)
	}
	// 单笔占比 >1 是手滑（把 10% 写成 10）：下限必须回落到**出厂 0.10**而不是按 1.0 执行，
	// 上限则被硬夹在 0.50（单笔不过半仓）。两端的兜底方向必须相反：手滑往小兜、越界往边界夹。
	e.SetSizingContext(0.60, 10, 10, 1.00, false)
	if r3 := e.TargetReadout(); r3.PerTradeLo != 0.10 || r3.PerTradeHi != 0.50 {
		t.Fatalf("单笔区间 >1 应 lo→0.10 / hi→0.50，got lo=%.3f hi=%.3f", r3.PerTradeLo, r3.PerTradeHi)
	}
	// 两键缺失 ⇒ 吃到厂默认区间 10%~30%，eff 按中性 k=1.0 取上限 0.30（与实盘"读数全缺→eff=hi"同向）。
	e.SetSizingContext(0.60, 0, 0, 0, false)
	if r4 := e.TargetReadout(); r4.Ceiling != 1.00 || r4.PerTradeLo != 0.10 || r4.PerTradeHi != 0.30 {
		t.Fatalf("两键缺失都必须吃到厂默认（天花板 1.00 / 区间 0.10~0.30），got ceiling=%.2f lo=%.2f hi=%.2f",
			r4.Ceiling, r4.PerTradeLo, r4.PerTradeHi)
	}
	// eff 回显的是**生效值**：k 未观测（LastK=0）按中性 1.0 ⇒ eff=hi=0.30，而 per_trade 随之放大。
	// 旧口径回显 0.10 而实际按 30% 买，正是本批要消灭的面板假读数。
	if r5 := e.TargetReadout(); r5.PerTradePct != 0.30 || r5.PerTrade != round2(r5.Total*0.30) {
		t.Fatalf("per_trade 必须回显 eff（0.30）且等于 Total×eff，got pct=%.3f per=%.2f total=%.2f",
			r5.PerTradePct, r5.PerTrade, r5.Total)
	}
}

func TestTargetReadoutSellCapDenominatorIsMinTotalAndValue(t *testing.T) {
	// 低仓形态：总资产 100（现金 60 + 市值 40）⇒ 帽的分母必须是 40，不是 100（六轮 T6-2）。
	e := New(Config{Enabled: true, AutoSell: true, FixedAmount: 10000, InitialCapital: 100}, "")
	seedTPositionsForReadout(t, e, "600000", 4, 10.0) // 市值 40
	// 现金必须显式压到 60：New() 的初始现金与买入无关，不压就会出现"总资产 140 / 市值 40"，
	// 而这一条要证的正是低仓形态下分母该取哪个数。
	e.mu.Lock()
	e.cash = 60
	e.mu.Unlock()
	e.SetSizingContext(0.80, 0.10, 0.30, 1.00, false)
	r := e.TargetReadout()
	if r.Total != 100 || r.MarketValue != 40 {
		t.Fatalf("账本前提不符（应 总资产100/市值40），got total=%.2f mv=%.2f", r.Total, r.MarketValue)
	}
	if r.SellCapBase != 40 {
		t.Fatalf("反 T 总量帽分母应为 min(总资产, 持仓市值)=40，got %.2f（按总资产算会给出比自己持仓还大的卖额）", r.SellCapBase)
	}

	// 高仓形态：现金 5 + 市值 95 ⇒ 分母换成总资产 100？不——仍是 min，即 100 与 95 里较小的 95。
	e2 := New(Config{Enabled: true, AutoSell: true, FixedAmount: 10000, InitialCapital: 100}, "")
	seedTPositionsForReadout(t, e2, "600001", 19, 5.0) // 市值 95
	e2.mu.Lock()
	e2.cash = 5
	e2.mu.Unlock()
	e2.SetSizingContext(0.80, 0.10, 0.30, 1.00, false)
	if r2 := e2.TargetReadout(); r2.SellCapBase != 95 {
		t.Fatalf("高仓形态分母仍应取较小值（持仓市值 95），got %.2f", r2.SellCapBase)
	}
}

func TestTargetReadoutCountsOpenTLegsBothDirections(t *testing.T) {
	// 一条在途正 T 买腿（市值 12×1000=12000）+ 一条在途反 T 卖腿（市值 9×2000=18000，冻结 18108）。
	e := New(Config{Enabled: true, AutoSell: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	seedTPositionsForReadout(t, e, "600000", 1000, 12.0)
	seedTPositionsForReadout(t, e, "600001", 6000, 9.0)
	// 回转账本直接搭内部态：SetSizingContext 要另取一次锁，所以它必须排在解锁之后。
	e.mu.Lock()
	e.tRounds = []TRound{
		{ID: "t0:600000:正T:20261027:1", Code: "600000", Mode: TRModeBuyFirst, Leg: tRLegBuy,
			LegQty: 1000, LegPrice: 12.0, State: tRStateOpen, Day: "20261027"},
		{ID: "t0:600001:反T:20261027:1", Code: "600001", Mode: TRModeSellFirst, Leg: tRLegSell,
			LegQty: 2000, LegPrice: 9.0, State: tRStateOpen, Day: "20261027", FrozenCash: 18108},
	}
	e.mu.Unlock()
	e.SetSizingContext(0.80, 0.10, 0.30, 1.00, false)

	r := e.TargetReadout()
	if r.OpenBuyLegs != 12000 || r.OpenSellLegs != 18000 {
		t.Fatalf("在途腿市值不对，got buy=%.2f sell=%.2f", r.OpenBuyLegs, r.OpenSellLegs)
	}
	// 战略当前数 = 持仓市值(66000) − 买腿(12000) + 卖腿(18000) = 72000。
	// 漏减买腿 ⇒ 目标空间被日内低吸吃掉，下一笔战略买入被误拦；
	// 漏加卖腿 ⇒ 高抛出去的那部分敞口被当成已经不在场，Room 凭空多出一块。两个方向都得在这里钉住。
	wantCur := 66000.0 - 12000 + 18000
	if r.CurStrat != round2(wantCur) {
		t.Fatalf("cur_strat 应为 市值−买腿+卖腿=%.0f，got %.2f", wantCur, r.CurStrat)
	}
	// 冻结款必须从可用 T 现金里扣掉（专款专用，战略买入不得动用）。
	if r.FrozenCash != 18108 {
		t.Fatalf("frozen_cash 应等于反 T 腿的冻结额，got %.2f", r.FrozenCash)
	}
	if r.CashT != round2(r.Cash-18108) {
		t.Fatalf("cash_t 必须已扣冻结专款，got cash=%.2f cash_t=%.2f", r.Cash, r.CashT)
	}
	if r.RoundsOpened != 2 || r.OpenLegs != 2 {
		t.Fatalf("当日开腿/在途计数应各为 2，got opened=%d open=%d", r.RoundsOpened, r.OpenLegs)
	}
	// 盲区（关键读数缺失）要如实透出，面板据此显示"今日不做 T"而不是照算目标。
	e.SetSizingContext(0.80, 0.10, 0.30, 1.00, true)
	if r2 := e.TargetReadout(); !r2.MissingKeyInput {
		t.Fatalf("missing_key_input 必须原样透出，got %v", r2.MissingKeyInput)
	}
}
