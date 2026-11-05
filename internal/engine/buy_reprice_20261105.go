// buy_reprice_20261105.go — §BUY-REPRICE（2026-11-05）买入挂单跟着 VWAP 走：偏离够大就撤掉重挂。
//
// owner 的原话是两条一起提的：「能不能根据 vwap 动态更新买入价格，并且信号消失后就撤下来」。
// 后一半已经落在 trading.Controller.SweepOrders 的 signal_gone 档；这一半是前一半。
//
// 形态为什么是"撤单 + 同键重放"而不是"改单"：
//   - QMT/柜台没有改价通道，已报出去的单只能撤（这是通道事实，不是设计选择）；
//   - 而"撤了当天就补不回来"这件事在 §H1-MG 里已经被解决过一轮——控制器对
//     **零成交已撤**的同键放行（见 trading/controller.go 的 existed 分支），
//     所以下一轮同一枚还在的买入信号会按当时的价自己补上。
//     再造一套"待重挂清单"就是第二本挂单账，而那本账没人对账。
//
// 三条硬边界，缺一条都会往钱的方向错：
//  1. **只碰战略买单**（幂等键 buy: 前缀）。做 T 的腿（t0:）有自己的让价/配对节奏，手动单
//     （ext:/manual）是用户自己的动作——把别人的单撤掉属于越权误杀；
//  2. **部成不撤**：撤掉的只是未成交那部分，但本地幂等键会连带把已成交部分的重挂机会一起关掉，
//     于是"改了个价"变成"少买了一批"；
//  3. **一码当日最多撤两次**（repriceMaxPerDay）：改价是会被行情抖动触发的，不限次就是
//     拿柜台的撤单额度做高频交易，而且每次都吃掉一次"同键重放"的机会。
//     计数读的是 orders 表里今天这批撤单留痕（cancel_source=vwap_reprice），
//     所以重启之后限制仍然成立——它不是内存里的计数。
//
// 缺省关（`rules.qmt.vwap_reprice_pct=0`＝今天一笔都不因改价被撤）：撤单吃柜台额度、
// 阈值要盘中真实读数才定得准，所以出厂关、由 owner 按下。
//
// English: cancel a resting STRATEGIC BUY whose limit has drifted from market VWAP by more than
// the configured percent, so the still-live signal re-places at the current price through the
// existing zero-fill same-key replay. Partial fills and T/manual legs are never touched; max two
// reprices per code per day, counted from the orders ledger so a restart cannot reset it.
package engine

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// repriceMaxPerDay 一只票一天最多因改价撤几次。两次是"上午一次、下午一次"的量级；
// 第三次基本意味着行情在来回抽，这时候该由人看，而不是继续消耗撤单额度与重放机会。
const repriceMaxPerDay = 2

// repriceDeviationPct 挂单价相对 VWAP 的偏离（%，正＝挂得比市场参考价高）。
// vwap 不可得（0/负）时 ok=false —— 没有参考价就不许撤，这是 missing≠zero 的另一面。
func repriceDeviationPct(orderPrice, vwap float64) (float64, bool) {
	if !(orderPrice > 0) || !(vwap > 0) {
		return 0, false
	}
	return (orderPrice - vwap) / vwap * 100, true
}

// repriceWorthCancelling 是否到了该撤的价值。两个方向都撤：挂高了会多付，挂低了根本不会成交，
// 两者都是"价已经跟不上市场参考价"的同一个事实。
func repriceWorthCancelling(devPct, thresholdPct float64) bool {
	if !(thresholdPct > 0) {
		return false // 关着（0）或写坏（负/NaN）：一律不撤
	}
	return math.Abs(devPct) >= thresholdPct
}

// isStrategicBuyTicket 这一行是不是"战略买入挂单"（本条腿唯一的可撤对象）。
// 判据是幂等键的形状而不是 side：手动单/占位行/做 T 腿都落在 buy: 之外，
// 把它们一并排除是结构性的，不靠逐条 if 记住。
func isStrategicBuyTicket(o store.RealOrder) bool {
	if o.Side != trading.SideBuy {
		return false
	}
	if o.Status != "已报" { // 部成/已成/撤单中/终态都不在本条腿的射程（部成见文件头边界 2）
		return false
	}
	if strings.HasPrefix(o.OrderID, "pend:") || o.IntradayT {
		return false
	}
	if strings.HasPrefix(o.SignalID, "ext:") || strings.HasPrefix(o.SignalID, "manual:") {
		return false
	}
	return strings.HasPrefix(o.SignalID, "buy:")
}

// orderDay 委托行属于哪个交易日（YYYYMMDD，北京时）。
// CreatedAt 解析不了就回空串——调用方按"不是今天"处理，宁可不计数少撤一次，
// 也不把一条时间读不懂的行当成本日额度（那是拿假账限制真账）。
func orderDay(createdAt string) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return ""
	}
	return cntime.In(t).Format("20060102")
}

// repriceStaleBuys 扫一遍当日委托，把偏离 VWAP 足够的战略买单撤掉（下一轮由同一信号按新价重挂）。
//
// 调用点在打分轮里（pushRealAdvice，与 SweepOrders 同一处）：那里同时有实时快照与控制器，
// 而且**每个交易周期都到**——放在下单分支里就只覆盖新单，放在展示层就变成"看得见但不做"。
// 行情快照按入参传进来而不是函数内自取：这条腿的可测性依赖于"能不能给它一份受控参考价"，
// 而 snapshotQuotes 背后是 fetcher（用例里没有 fetcher，只能拿到 nil ⇒ 断言会恒真）。
func (e *Engine) repriceStaleBuys(ctrl *trading.Controller, quotes map[string]*data.StockInfo, now time.Time) {
	if ctrl == nil {
		return
	}
	cfg := ctrl.Config()
	if !(cfg.VWAPRepricePct > 0) {
		return // 关着＝零查询零动作，今天的形态一个字节都不变
	}
	db := e.realStoreForReprice()
	if db == nil {
		log.Printf("[qmt] §BUY-REPRICE 账本未就绪（本轮不改价）")
		return
	}
	uid := e.budgetUID()
	day := data.TradingDayDate(now)
	orders, err := db.RealOrdersForUser(uid)
	if err != nil {
		log.Printf("[qmt] §BUY-REPRICE 读当日委托失败（本轮不改价）uid=%s: %v", uid, err)
		return
	}
	// 限次计数按**账本**算而不是按内存：今天已经为改价撤过几笔，就写在那张表的 cancel_source 上，
	// 重启后限制依然成立；用进程内计数器会给出"重启就又能撤两次"的假额度。
	repriced := map[string]int{}
	candidates := 0
	for _, o := range orders {
		if o.CancelSource == trading.SweepSourceVWAPReprice && orderDay(o.CreatedAt) == day {
			repriced[pureTsCode(o.Code)]++
		}
		if isStrategicBuyTicket(o) {
			candidates++
		}
	}
	if candidates == 0 {
		return
	}
	cancelled, blocked, skippedNoRef := 0, 0, 0
	for _, o := range orders {
		if !isStrategicBuyTicket(o) {
			continue
		}
		code := pureTsCode(o.Code)
		vwap, ok := data.VWAPOf(quotes[code])
		dev, ok2 := repriceDeviationPct(o.Price, vwap)
		if !ok || !ok2 {
			skippedNoRef++ // 没有参考价就不撤：把"拿不到 VWAP"当成"价没问题"是另一种穿透
			continue
		}
		if !repriceWorthCancelling(dev, cfg.VWAPRepricePct) {
			continue
		}
		if repriced[code] >= repriceMaxPerDay {
			blocked++
			opslog.DayOnce("buy-reprice-cap:"+code+":"+day, func() {
				opslog.Logf("quant", "§BUY-REPRICE %s 已达当日改价撤单上限 %d 次，本轮不再撤（偏离 %.2f%%，VWAP=%.2f 挂价=%.2f）——继续撤就是拿撤单额度做高频",
					code, repriceMaxPerDay, dev, vwap, o.Price)
			})
			continue
		}
		reason := fmt.Sprintf("买入挂单跟不上市场参考价：挂价 %.2f 对 VWAP %.2f 偏离 %+.2f%%（阈值 %.2f%%），"+
			"撤掉这一笔，由同一信号按新价重挂（零成交已撤同键可重放）",
			o.Price, vwap, dev, cfg.VWAPRepricePct)
		switch ctrl.RequestCancel(o, trading.SweepSourceVWAPReprice, reason, now, day) {
		case trading.CancelRequested:
			cancelled++
			repriced[code]++
			log.Printf("[qmt] §BUY-REPRICE 撤单请求已发出 %s %s 挂价=%.2f VWAP=%.2f 偏离=%+.2f%%（阈值=%.2f%%）",
				o.OrderID, o.Code, o.Price, vwap, dev, cfg.VWAPRepricePct)
		case trading.CancelWindowBlocked:
			blocked++
		default:
			// 被拒/状态未推进：本轮没撤成，下一轮再评（不计入限次——限次管的是"真的撤过"）
		}
	}
	log.Printf("[qmt] §BUY-REPRICE 本轮 阈值=%.2f%% 候选=%d 撤=%d 限次/窗口挡下=%d 无参考价=%d",
		cfg.VWAPRepricePct, candidates, cancelled, blocked, skippedNoRef)
}

// realStoreForReprice 锁内取账本引用（不在持锁期间做 IO）。为 nil 时返回 nil，
// 调用方的读路径会自然失败并留下"本轮不改价"的日志——那是一条**有名字**的静默，
// 不是把观察失败说成没有偏离。
func (e *Engine) realStoreForReprice() *store.DB {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.realStore
}
