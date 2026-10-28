// funds_today.go — §FUNDS-TODAY（2026-10-28）当日资金帧的「信号仓侧」发布点与定权上下文。
//
// 背景（方案 §7.1 首日实测暴露的发布点缺陷）：
// 资金帧原先只在**实盘 T 循环**里写，于是「做 T 没开（sizing.t.enabled=false）/ 当天没做一笔 T」
// 的日子，卡片上永远是「今日尚未定权」——而信号仓的定权（MS / 目标 / 剩余）与 T 开不开毫无关系。
// 盘中引擎明明算出了 MS（stderr 有 MS=0.765 的日志），卡片却看不到。
//
// 本文件把两件事收口：
//  1. 定权上下文（MS / 盲区 / 指数涨跌）走**唯一计算出口** sizingContextFor：
//     同一轮里「资金帧发布」与「T 参数解析」都要用 MS，两处各算一次就有"同轮两个 MS"的漂移风险
//     （目标制一旦拿到两个不同的 MS，卡片上的目标与实际开腿用的额度就对不上）。短窗缓存解决这件事。
//  2. 目标/当前/剩余的**唯一公式** fundsTargetCurRoom：
//     信号仓资金帧与 T 额度共用同一套式子，杜绝"卡片显示剩余 3 万、T 腿按剩余 2 万算"的双账。
//
// 帧的写入分工（两侧各有写者，字段互不覆盖）：
//   - 信号仓侧（账户资金 + MS + 目标/当前/剩余）：**打分循环**每轮发布（去重 10s）——本文件。
//   - T 侧（额度/可用 T 现金/在途腿/当日 T 收益）：**T 循环**发布（见 intraday_t_live.go）。
//
// T 没跑时 T 侧保持上一帧并由 t_at 标出取数时刻（前端据此判"已过期"），**绝不伪造 0**。
package engine

import (
	"math"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// sizingCtxTTL 定权上下文的缓存窗口。
//
// 取 10s：两个消费点（打分轮 5s 一跳 + 落盘）之间的最大间隔就是一轮打分周期，
// 窗口内复用同一份读数 ⇒ 同一轮里的资金帧与 T 额度必然拿到同一个 MS。
// 上界 10s 也意味着市场态变化后最多 10s 才反映到卡片上——MS 是慢变量，这个延迟可接受。
const sizingCtxTTL = 10 * time.Second

// sizingCtx 一份定权上下文：市场缩放、关键读数缺失标记、大盘指数涨跌。
type sizingCtx struct {
	MS       float64 // 市场缩放（目标仓位的乘数；<=0 视为尚未定权，回落 1.0）
	Missing  bool    // 关键市场读数缺失（数据盲区）：目标封顶半仓且当天不做 T
	IndexPct float64 // 大盘指数涨跌（%），T 的环境门用它
	At       time.Time
}

// sizingContextFor 取该账号的定权上下文（短窗缓存）。
//
// 为什么缓存而不是每处现算：MS 是"目标仓位"的唯一乘数，同一轮里算两次就可能拿到两个值
// （主循环并发刷新情绪相位/市场态时两次采样点不同）——卡片上的目标与实际开腿用的额度会对不上，
// 而这两者一旦漂移，操作者看到的"剩余空间"就是假的。
//
// 缓存失效只靠时间窗（10s），不靠"配置变了"这类事件：sizing 配置盘中热改的下一轮即生效
// （窗口到期后重算），而 MS 本身是慢变量，10s 内的复用不会掩盖真实变化。
func (e *Engine) sizingContextFor(uid string) sizingCtx {
	if uid == "" {
		uid = e.primaryMember()
	}
	e.mu.RLock()
	c, ok := e.sizingCtxCache[uid]
	e.mu.RUnlock()
	if ok && !c.At.IsZero() && time.Since(c.At) < sizingCtxTTL {
		return c
	}
	r := e.regimeForAccount(uid)
	res := signalctl.ComputeSizing(r, combat_agent.Signal{})
	ms := res.MarketScale
	if ms <= 0 {
		ms = 1.0 // 未定权时的安全回落：与 T 参数解析旧行为逐字一致（目标按满额算，不凭空缩仓）
	}
	c = sizingCtx{MS: ms, Missing: res.MissingKeyInput, IndexPct: r.IndexChangePct, At: time.Now()}
	e.mu.Lock()
	if e.sizingCtxCache == nil {
		e.sizingCtxCache = map[string]sizingCtx{}
	}
	e.sizingCtxCache[uid] = c
	e.mu.Unlock()
	return c
}

// fundsTargetCurRoom 目标仓位 / 战略当前仓位 / 战略剩余空间 的**唯一公式**。
//
//   - target = 总资产 × min(MS, 天花板)
//   - cur    = Σ(持仓现价×数量) + 战略在途买单 − 正T买腿市值 + 反T卖腿市值
//     （T 腿按净额剔除：T 腿不占战略空间，反 T 卖掉的那部分底仓还欠着买回）
//   - room   = max(0, target − cur)
//
// 返回 ok=false 表示在途委托读数不可得（此时 cur 只按"只算持仓"给保守值，room 仍给保守值）。
// 资金帧与 T 可用现金共用这一个出口：同一份读数、同一套式子，面板与开腿不会各算一套。
func fundsTargetCurRoom(total, ms, ceiling float64,
	positions map[string]store.RealPosition, prices map[string]float64,
	pending []store.PendingBuy, book tLiveBook) (target, cur, room float64, ok bool) {
	if ceiling <= 0 || ceiling > 1 {
		ceiling = 1.0
	}
	ratio := math.Min(ms, ceiling)
	target = total * ratio
	var strat float64
	for code, pos := range positions {
		px := prices[code]
		if px <= 0 {
			px = pos.CostPrice
		}
		strat += px * float64(pos.Qty)
	}
	// 在途战略买单（非 t0: 前缀）同样占用目标空间；T 腿由账本按净额口径给。
	for _, pb := range pending {
		if pb.IntradayT {
			continue
		}
		strat += pb.Price * float64(pb.Remain)
	}
	strat = strat - book.BuyLegValue + book.SellLegValue
	if strat < 0 {
		strat = 0
	}
	if target-strat > 0 {
		room = target - strat
	}
	return target, strat, room, true
}

// publishSignalFunds 发布当日资金帧的**信号仓侧**。
//
// 谁在写：打分循环（每轮调用，去重 10s）。为什么不是 T 循环：信号仓的定权与做 T 开不开无关——
// sizing.t.enabled=false 或当天没做一笔 T 的日子，信号仓的 MS/目标/剩余照样有值（方案 §7.1）。
//
// 写什么：账户资金快照（现金/冻结/总资产/市值 + 快照时刻）+ 定权三件套（MS/目标/当前/剩余）。
// T 侧字段**不在这里动**：T 没跑时保持上一帧并由 t_at 标出取数时刻，不伪造 0。
func (e *Engine) publishSignalFunds(uid string, quotes map[string]*data.StockInfo) {
	if e == nil || uid == "" {
		return
	}
	st := e.tLiveFor(uid)
	if st == nil {
		return // 无 T 账本目录＝无资金帧落点（纯内存/测试环境），不硬造
	}
	now := e.nowTime()
	// 去重 10s：打分轮 5s 一跳，而资金帧落盘是整份 T 账本的原子写，不需要 5s 一帧。
	if !e.fundsPublishDue(uid, now) {
		return
	}
	db := e.realStoreForFunds()
	if db == nil {
		return // 实盘账本不可得：没有持仓/资金读数就不写帧（宁可无帧，不要假帧）
	}

	// 账户级读数：可用资金走控制器（与 T 循环同一份券商快照 + 30min 新鲜度口径），
	// 冻结/市值/总资产取账本快照（updated_at 一并透出，前端据此区分"金额没变"与"快照没更新"）。
	var acc store.RealAccount
	if a, err := db.GetRealAccount(uid); err == nil {
		acc = a
	}
	cash, cashOK := 0.0, false
	if ctrl := e.QMTController(); ctrl != nil {
		cash, cashOK = ctrl.AvailableCash()
	} else {
		cash = acc.AvailableCash // 无控制器＝没有新鲜度口径（cash_ok=false），金额仍按快照给
	}
	total := acc.TotalAsset
	if total <= 0 {
		if v, err := db.TotalAssets(uid); err == nil && v > 0 {
			total = v
		}
	}

	// 持仓与现价：与 T 回合同源（缺价按成本价兜底，缺价持仓**不进 prices**，
	// 让 T 侧判定链据此 fail-close；市值仍按成本价计入，不漏仓）。
	positions := map[string]store.RealPosition{}
	prices := map[string]float64{}
	poses, err := db.RealPositionsForUser(uid)
	if err != nil {
		return // 持仓读不到 ≠ 空仓：宁可不写帧，也不要写一份"看起来空仓"的假帧
	}
	for _, pos := range poses {
		if pos.Qty <= 0 {
			continue
		}
		code := pureTsCode(pos.TsCode)
		positions[code] = pos
		if q := quotes[code]; q != nil && q.Price > 0 {
			prices[code] = q.Price
		} else if pos.CurPrice > 0 {
			prices[code] = pos.CurPrice
		}
	}

	// T 腿净额（T 账本）：正 T 买腿从战略仓位里剔掉、反 T 卖腿加回——与 T 回合同一份账本读数。
	book := st.bookValue(prices)

	// 战略在途买单：只取非 T 腿（T 腿由账本按净额口径给）。
	var pending []store.PendingBuy
	if pb, perr := db.PendingBuys(uid, data.TradingDayDate(now)); perr == nil {
		pending = pb
	}

	ctx := e.sizingContextFor(uid)
	sc := e.sizingConfigFor(uid)
	target, cur, room, _ := fundsTargetCurRoom(total, ctx.MS, sc.MaxTotalPositionPct, positions, prices, pending, book)

	st.updateFunds(func(f *tLiveFunds) {
		f.At = now.Format(time.RFC3339)
		f.Cash = cash
		f.FrozenCash = acc.FrozenCash
		f.TotalAsset = total
		f.MarketValue = acc.MarketValue
		f.CashAt = acc.UpdatedAt
		f.CashOK = cashOK
		f.Target = target
		f.CurStrat = cur
		f.Room = room
		f.MarketScale = ctx.MS
		f.MissingKey = ctx.Missing
	})
}

// fundsPublishDue 判断该账号是否到了再发一帧的时刻（去重 10s）。
func (e *Engine) fundsPublishDue(uid string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fundsPublishedAt == nil {
		e.fundsPublishedAt = map[string]time.Time{}
	}
	last, ok := e.fundsPublishedAt[uid]
	e.fundsPublishedAt[uid] = now
	return !ok || now.Sub(last) >= 10*time.Second
}

// realStoreForFunds 取实盘账本（资金帧的持仓/资金读数源）。
func (e *Engine) realStoreForFunds() *store.DB {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.realStore
}
