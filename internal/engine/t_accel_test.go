package engine

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/trading"
)

// tAccelFnSource 取某个函数的源码（到行首 } 为止），供"源码级负锁"判据使用。
// 这类判据存在的理由：方向性错误（把配平写成结掉）在所有行为断言下都照样绿，
// 只有源码级判据能在写错的那一刻就红。
func tAccelFnSource(t *testing.T, file, fn string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", file, err)
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	inFn := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "func ") && strings.Contains(ln, fn) {
			inFn = true
		}
		if inFn {
			out = append(out, ln)
			if ln == "}" {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// ── §W-06-R4 断言：三个触发源、一个出口 ────────────────────────────────────────
//
// 这一批用例的共同目的不是"覆盖代码"，而是**钉住方向**：R4 在定型过程中翻过三次
// （释放 → 只管入口 → 与 P1′ 合并成配平），每次翻案的后果都是敞口方向反了。
// 单测在 range 下跑全都绿，所以每条用例都必须显式摆出"方向不对就会红"的那个读数。

// accelFix 造一条在途腿（不入账本，纯结构），供 accelReason 的纯函数判定用。
func accelFix(t *testing.T, hh, mm int) (*tLiveFix, *tLiveRound) {
	t.Helper()
	f := newTLiveFix(t, hh, mm, nil)
	p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0 })
	r := f.round(p, f.quotes())
	return f, r
}

// 腿龄基准必须走**夹具时钟**（f.now），不能用 time.Now：夹具钟是固定交易日（2026-11-04 10:30），
// 与真实钟差若干年 ⇒ 用真实钟算出来的"30 秒前"在夹具眼里是几年前，腿龄门形同不设（假绿）。
func buyRoundAt(now time.Time, openedAgo time.Duration, filled int, price float64) *TRoundLive {
	return &TRoundLive{ID: "t0:600000:buy_first:x:1", Code: "600000", Name: "浦发",
		Dir: tLDirBuy, Qty: filled, BuyFilled: filled, BuyPrice: price,
		OpenedAt: now.Add(-openedAgo).Format(time.RFC3339), Status: tLStatusOpen}
}

func sellRoundAt(now time.Time, openedAgo time.Duration, filled int, price float64) *TRoundLive {
	return &TRoundLive{ID: "t0:600000:sell_first:x:1", Code: "600000", Name: "浦发",
		Dir: tLDirSell, Qty: filled, SellFilled: filled, SellPrice: price,
		OpenedAt: now.Add(-openedAgo).Format(time.RFC3339), Status: tLStatusOpen}
}

// ACC-1 正 T 亏到线 ⇒ "stop" ⇒ 跳过价差门发卖腿，终态是 done 不是 unwound。
func TestAccelStopSellsEarly(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	// 现价 9.60 ⇒ 价差率 −4.00% ≤ −2.0%
	if got := r.accelReason(rnd, 9.60); got != paper.TAccelStop {
		t.Fatalf("价差 −4%% 应触发 stop，实得 %q", got)
	}
	// 端到端：价差门（min_spread 2.0）没到，但加速必须把卖腿发出去。
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 1000, 10.0, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirBuy, Qty: 1000}, 1000, 10.0)
	p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0 })
	f.round(p, f.quotes(f.quote(code, "浦发", 9.60, 10.0, 10.6, 9.5, 10.0))).execute()
	f.mustCount(1) // 卖腿已报（价差门被跳过）
	// 配对腿成交后本轮才收敛：加速配平**成功**的终态是 done，不是 unwound——
	// 这正是 v3 与 v2 的核心差别（v2 会把正 T 的 P2/Q1 转成 unwound，让 room 变小、方向反）。
	f.legFill(key+tLPairSuffix, code, "卖出", 1000, 9.60, 0)
	f.round(p, f.quotes(f.quote(code, "浦发", 9.60, 10.0, 10.6, 9.5, 10.0))).execute()
	got := f.ledger().roundOf(key)
	if got == nil || got.Status != tLStatusDone {
		t.Fatalf("加速配平成功应记 done（不是 unwound），实得 %+v", got)
	}
}

// ACC-2 反 T 同一算式、同一出口：卖飞了 ⇒ 买回腿被报出。
func TestAccelStopBuysBack(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	rnd := sellRoundAt(f.now, 3*time.Minute, 400, 10.0)
	// 现价 10.40 ⇒ 卖额 4000 − 买额 4160 ⇒ −4.00%（与正 T 完全同一算式）
	if got := r.accelReason(rnd, 10.40); got != paper.TAccelStop {
		t.Fatalf("反 T 价差 −4%% 应触发 stop（正反同算式，无方向分支），实得 %q", got)
	}
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "sell_first", 1)
	f.legFill(key, code, "卖出", 400, 10.0, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirSell, Qty: 400}, 400, 10.0)
	p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0 })
	f.round(p, f.quotes(f.quote(code, "浦发", 10.40, 10.0, 10.6, 9.5, 10.0))).execute()
	// 断言"买回腿被报出"就够了：成交侧（done）与其他用例同源，且反 T 的买回受专款约束，
	// 在这里再断言终态会把"现金够不够"这件无关的事也钉进来。
	f.mustCount(1)
}

// ACC-3 腿龄门：同一条腿，30 秒不判止损、3 分钟判。
func TestAccelStopRespectsLegGrace(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	young := buyRoundAt(f.now, 30*time.Second, 1000, 10.0)
	if got := r.accelReason(young, 9.60); got != "" {
		t.Fatalf("腿龄 30s < tLPremiseGrace，止损不许判（开腿抖动会拿错价格基准），实得 %q", got)
	}
	old := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if got := r.accelReason(old, 9.60); got != paper.TAccelStop {
		t.Fatalf("腿龄 3min 应判止损，实得 %q", got)
	}
}

// ACC-4 止血轮的不对称：stop 不触发，但 verdict/regime 仍然触发（它们不是止损，是"回到开腿前"）。
func TestAccelStopRespectsDisabled(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0; p.Enabled = false })
	r := f.round(p, f.quotes())
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if got := r.accelReason(rnd, 9.60); got != "" {
		t.Fatalf("止血期间不许因为亏到线而主动卖底仓（那是新动作），实得 %q", got)
	}
	// 同一条腿：裁决出现 ⇒ 仍然触发（止血停的是新动作，不是收摊）
	f.e.tSellVerdicts = map[string]time.Time{"600000": time.Now()}
	if got := r.accelReason(rnd, 9.60); got != paper.TAccelVerdict {
		t.Fatalf("止血期间裁决仍应触发配平（不是止损），实得 %q", got)
	}
}

// ACC-5/6 卖出裁决触发源与其 5 分钟 TTL。
func TestAccelVerdictSellsEarly(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	f.e.tSellVerdicts = map[string]time.Time{"600000": f.now}
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if got := r.accelReason(rnd, 10.05); got != paper.TAccelVerdict {
		t.Fatalf("有活跃卖出裁决应触发 verdict（价差 +0.5%% 还没到门），实得 %q", got)
	}
}

func TestAccelVerdictExpiry(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	// TTL 判据内部走 time.Since（真实钟），所以这里必须用 time.Now 而不是夹具钟。
	f.e.tSellVerdicts = map[string]time.Time{"600000": time.Now().Add(-6 * time.Minute)}
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if got := r.accelReason(rnd, 10.05); got != "" {
		t.Fatalf("裁决超过 5 分钟 TTL 应失效（回到价差门），实得 %q", got)
	}
}

// ACC-7/8/9 行情翻转触发源：新鲜度门与"只在离开 range 时生效"。
func TestAccelRegimeFlip(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	for _, st := range []string{"bear", "bull"} {
		p := f.params(func(p *paper.TParams) {
			p.RoundStopPct = 2.0
			p.MarketState = st
			p.MarketStateAt = f.now.Add(-1 * time.Minute)
		})
		r := f.round(p, f.quotes())
		if got := r.accelReason(rnd, 10.05); got != paper.TAccelRegime {
			t.Fatalf("%s 且读数新鲜 ⇒ 在途腿应立刻配平（不是挂到下午），实得 %q", st, got)
		}
	}
}

func TestAccelRegimeStaleDoesNotFire(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	p := f.params(func(p *paper.TParams) {
		p.RoundStopPct = 2.0
		p.MarketState = "bear"
		p.MarketStateAt = f.now.Add(-30 * time.Minute)
	})
	r := f.round(p, f.quotes())
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	// 出口比入口更严：过期读数若误触发就是真金白银的成交，宁可弃权。
	if got := r.accelReason(rnd, 10.05); got != "" {
		t.Fatalf("行情读数过期 30min ⇒ 不许拿它去配平，实得 %q", got)
	}
}

func TestAccelRegimeRangeDoesNotFire(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	p := f.params(func(p *paper.TParams) {
		p.RoundStopPct = 2.0
		p.MarketState = "range"
		p.MarketStateAt = f.now.Add(-1 * time.Minute)
	})
	r := f.round(p, f.quotes())
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if got := r.accelReason(rnd, 10.05); got != "" {
		t.Fatalf("range 是放行档 ⇒ 行情门在出口不生效（否则等于把价差门废掉），实得 %q", got)
	}
}

// ACC-10 回归锁：三个源都不命中时，价差门必须还在（加速没有把它整体拆掉）。
func TestNoAccelKeepsSpreadGate(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 1000, 10.0, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirBuy, Qty: 1000}, 1000, 10.0)
	p := f.params(func(p *paper.TParams) {
		p.RoundStopPct = 2.0
		p.MarketState = "range"
		p.MarketStateAt = f.now.Add(-1 * time.Minute)
	})
	// 价差 +0.5% < min_spread 2.0 ⇒ 不配平
	f.round(p, f.quotes(f.quote(code, "浦发", 10.05, 10.0, 10.6, 9.5, 10.0))).execute()
	f.mustCount(0)
	if got := f.ledger().roundOf(key); got == nil || got.Status != tLStatusOpen {
		t.Fatalf("无加速且价差未到 ⇒ 保持 open，实得 %+v", got)
	}
}

// ── 止损口径（v3：纯价差，一条分支）────────────────────────────────────────────

// ACC-16 负锁：口径里不许掺费用。价差 −1.90% 必须**不**触发；含费口径是 −2.0488% 会触发。
func TestSpreadPctExcludesFees(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	pct, _, ok := r.roundSpreadPctNow(rnd, 9.81)
	if !ok {
		t.Fatal("价差率应可算")
	}
	if math.Abs(pct-(-1.90)) > 0.005 {
		t.Fatalf("价差率应约 −1.90%%，实得 %.4f", pct)
	}
	if got := r.accelReason(rnd, 9.81); got != "" {
		t.Fatalf("−1.90%% 不应触发止损（含费口径才会触发 ⇒ 口径被掺了费用），实得 %q", got)
	}
}

// ACC-17 混合口径：第二腿已部成时，已实现与浮盈一起算。
func TestSpreadPctBlendsPartialPair(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	rnd := buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	rnd.SellFilled, rnd.SellPrice = 400, 10.30
	pct, _, ok := r.roundSpreadPctNow(rnd, 9.60)
	if !ok {
		t.Fatal("价差率应可算")
	}
	// 卖额 4120+5760=9880、买额 10000 ⇒ −120/10000 = −1.20%
	if math.Abs(pct-(-1.20)) > 1e-4 {
		t.Fatalf("混合口径应得 −1.20%%，实得 %.4f", pct)
	}
	if got := r.accelReason(rnd, 9.60); got != "" {
		t.Fatalf("−1.20%% 不应触发（只按首腿价算会得出 −4.00%% ⇒ 误触发），实得 %q", got)
	}
}

// ACC-18 正反 T 同一算式：两侧 pct 绝对值相等、符号都为负。
func TestSpreadPctSameForBothDirections(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	pb, _, _ := r.roundSpreadPctNow(buyRoundAt(f.now, 3*time.Minute, 1000, 10.0), 9.74)
	ps, _, _ := r.roundSpreadPctNow(sellRoundAt(f.now, 3*time.Minute, 1000, 10.0), 10.26)
	if math.Abs(pb-ps) > 1e-9 || pb >= 0 {
		t.Fatalf("正反 T 应同一算式：正 %.4f / 反 %.4f（应相等且都为负）", pb, ps)
	}
	if math.Abs(pb-(-2.60)) > 0.005 {
		t.Fatalf("应约 −2.60%%，实得 %.4f", pb)
	}
}

// ── premiseGone：只剩 P3，且不许把行情/止损写进来 ─────────────────────────────

// ACC-13 正 T 亏 4% 但非熔断日 ⇒ 不改终态（P2/Q1 已移出本函数）。
func TestPremiseGoneOnlyDayBlocked(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	r.dayBlocked = false
	if st, why := r.premiseGone(buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)); st != "" || why != "" {
		t.Fatalf("非熔断日正 T 亏 4%% ⇒ premiseGone 应为空（提前配平由 accelReason 做），实得 %q/%q", st, why)
	}
	r.dayBlocked = true
	rnd := sellRoundAt(f.now, 3*time.Minute, 1000, 10.0)
	if st, _ := r.premiseGone(rnd); st != tLStatusUnwound {
		t.Fatalf("熔断日反 T 卖腿已成交 ⇒ 应转 unwound，实得 %q", st)
	}
}

// ACC-14 负锁：裁决活跃也不许把 verdict 加回终态路径（那是 v1 的错误形态）。
func TestPremiseGoneIgnoresSellVerdict(t *testing.T) {
	f, r := accelFix(t, 10, 30)
	f.e.tSellVerdicts = map[string]time.Time{"600000": f.now}
	if st, _ := r.premiseGone(buyRoundAt(f.now, 3*time.Minute, 1000, 10.0)); st != "" {
		t.Fatalf("裁决活跃 ⇒ premiseGone 仍应为空（出口动作是配平不是结掉），实得 %q", st)
	}
}

// ACC-11 第二腿已在柜台挂着 ⇒ 加速也不许叠单。
//
// 撤单与成交有竞态：撤单指令在途时成交回报仍可能到达，此时再发一条同向腿就会造出
// **无主成交**（仓位变了，但没有一条腿认领它）。所以"跳过价差门"只跳价差，不跳在途检查。
func TestAccelStillRespectsPendingPair(t *testing.T) {
	f, _ := accelFix(t, 10, 30)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 1000, 10.0, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirBuy, Qty: 1000}, 1000, 10.0)
	// 第二腿已报未成交：账本事实里有这一条在途委托。
	f.legOrder(key+tLPairSuffix, code, "卖出", 1000, 10.0, "已报", "GW-"+key+tLPairSuffix)
	p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0 })
	f.round(p, f.quotes(f.quote(code, "浦发", 9.60, 10.0, 10.6, 9.5, 10.0))).execute()
	f.mustCount(0)
	if got := f.ledger().roundOf(key); got == nil || got.Status != tLStatusOpen {
		t.Fatalf("第二腿在途时加速不许叠单（等它成交或尾盘 force），实得 %+v", got)
	}
}

// ACC-12 加速路径失败即收摊：①② 是行为变更（现状是 markBlocked + 继续等），③ 本来就是收摊。
//
// 关键反向断言：同一组输入**去掉加速** ⇒ ①② 必须仍是 markBlocked 且 Status=open。
// 否则就是"顺手把既有行为一起改了"，F 系列断言会连锁变化。
func TestAccelFailureUnwinds(t *testing.T) {
	t.Run("正T底仓可卖不足", func(t *testing.T) {
		newFix := func(price float64, minSpread float64) (*tLiveFix, string) {
			f, _ := accelFix(t, 10, 30)
			code := "600000"
			// 底仓 500 + 当日 T 买 1000 ⇒ 可卖 500 < 需卖 1000（T+1）。
			f.seedPosition(code, "浦发", 500, 10, 10)
			key := tLiveKey(code, "buy_first", 1)
			f.legFill(key, code, "买入", 1000, 10.0, 0)
			f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirBuy, Qty: 1000}, 1000, 10.0)
			p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0; p.MinSpreadPct = minSpread })
			f.round(p, f.quotes(f.quote(code, "浦发", price, 10.0, 10.6, 9.5, 10.0))).execute()
			return f, key
		}
		// 加速（价差 −4% ⇒ stop）：失败必须收摊。
		f, key := newFix(9.60, 2.0)
		got := f.ledger().roundOf(key)
		if got == nil || got.Status != tLStatusUnwound {
			t.Fatalf("加速配平失败应记 unwound，实得 %+v", got)
		}
		if !strings.Contains(got.UnwindReason, "加速配平失败") {
			t.Fatalf("失败原因应含「加速配平失败」以便复盘分组，实得 %q", got.UnwindReason)
		}
		// 非加速（价差已过门但无触发源）：既有行为原样保留。
		f2, key2 := newFix(10.05, 0.2)
		got2 := f2.ledger().roundOf(key2)
		if got2 == nil || got2.Status != tLStatusOpen {
			t.Fatalf("非加速路径不许被这次改动带跑：应保持 open，实得 %+v", got2)
		}
		if !strings.Contains(got2.LastBlockStep, "step7") {
			t.Fatalf("非加速路径应仍走 markBlocked(step7)，实得 %q", got2.LastBlockStep)
		}
	})

	t.Run("正T卖腿被拒", func(t *testing.T) {
		newFix := func(price float64, minSpread float64, badGateway bool) (*tLiveFix, string) {
			f, _ := accelFix(t, 10, 30)
			if badGateway {
				// 柜台不可达：连接被拒 ⇒ sellLeg 返回 error（与柜台拒单同一条失败分支）。
				cfg := f.ctrl.Config()
				f.ctrl = trading.NewController(
					trading.NewQMTClient("http://127.0.0.1:1", "", 200*time.Millisecond, 0), f.db, "u_1", cfg, nil)
				f.e.SetQMT(f.ctrl, f.db)
			}
			code := "600000"
			f.seedPosition(code, "浦发", 1000, 10, 10)
			key := tLiveKey(code, "buy_first", 1)
			f.legFill(key, code, "买入", 1000, 10.0, 0)
			f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirBuy, Qty: 1000}, 1000, 10.0)
			p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0; p.MinSpreadPct = minSpread })
			f.round(p, f.quotes(f.quote(code, "浦发", price, 10.0, 10.6, 9.5, 10.0))).execute()
			return f, key
		}
		f, key := newFix(9.60, 2.0, true)
		got := f.ledger().roundOf(key)
		if got == nil || got.Status != tLStatusUnwound || !strings.Contains(got.UnwindReason, "加速配平失败") {
			t.Fatalf("加速路径卖腿被拒应收摊（unwound + 加速配平失败），实得 %+v", got)
		}
		f2, key2 := newFix(10.05, 0.2, true)
		got2 := f2.ledger().roundOf(key2)
		if got2 == nil || got2.Status != tLStatusOpen || !strings.Contains(got2.LastBlockStep, "step8") {
			t.Fatalf("非加速路径卖腿被拒应仍 markBlocked(step8) 且保持 open，实得 %+v", got2)
		}
	})

	t.Run("反T专款不足", func(t *testing.T) {
		f, _ := accelFix(t, 10, 30)
		f.seedAccount(100000, 100) // 专款之外的可用现金只剩 100 元
		code := "600000"
		// 底仓 2000、反 T 卖 1000：剩 1000 仍在持仓里（round 构造时按持仓取价，
		// 全卖光会让 r.prices 没有这只有效价，配对链在"无价"分支提前返回，测不到现金腿）。
		f.seedPosition(code, "浦发", 2000, 10, 10)
		key := tLiveKey(code, "sell_first", 1)
		f.legFill(key, code, "卖出", 1000, 10.0, 0)
		f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: tLDirSell, Qty: 1000}, 1000, 10.0)
		p := f.params(func(p *paper.TParams) { p.RoundStopPct = 2.0 })
		f.round(p, f.quotes(f.quote(code, "浦发", 10.40, 10.0, 10.6, 9.5, 10.0))).execute()
		got := f.ledger().roundOf(key)
		if got == nil || got.Status != tLStatusUnwound {
			t.Fatalf("反 T 专款不足应无条件收摊，实得 %+v", got)
		}
		// 反 T 侧文案是「买回被拦…（accel=stop）」：状态语义与 ①② 相同，只是不带
		// 「加速配平失败」前缀（方案要求"状态语义不动"，这里钉住标签仍在，复盘能分组）。
		if !strings.Contains(got.UnwindReason, "accel=stop") {
			t.Fatalf("反 T 加速失败也应带 accel 标签，实得 %q", got.UnwindReason)
		}
	})
}

// ACC-15 源码级：premiseGone 不许出现行情/止损读数（写进来就退化成"结掉不管"，方向反）。
func TestPremiseGoneNeverReadsMarketState(t *testing.T) {
	src := tAccelFnSource(t, "intraday_t_live.go", "premiseGone(")
	for _, bad := range []string{"MarketState", "accelReason", "roundSpreadPctNow"} {
		if strings.Contains(src, bad) {
			t.Fatalf("premiseGone 不该出现 %s：行情/止损的出口动作是**配平**，写在释放路径里就变成结掉 ⇒ 敞口变大", bad)
		}
	}
	for _, need := range []string{"r.force", "hasPendingPair", "tLPremiseGrace", "p.Enabled"} {
		if !strings.Contains(src, need) {
			t.Fatalf("premiseGone 少了守卫 %s（四个守卫一个都不许删）", need)
		}
	}
}
