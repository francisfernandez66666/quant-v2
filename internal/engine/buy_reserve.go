// buy_reserve.go — §ORDER-STATE-PULL / P0-5（2026-10-30）同一轮并发下单的预算预留。
//
// 背景（当天现网实证）：14:03 释放 40,915 剩余空间后，同一轮的三笔信号在 3 秒内先后定量，
// 每一笔都读到"上一笔还没落库"的旧余额 ⇒ 三笔合计 52,497，把目标仓位打穿 11,616（+18.4%）。
// 根因不是公式错，而是**定量与落库之间的可见性窗口**：下单是异步的（auto order queued），
// PendingBuys 只能看见已经写进 orders 表的单。
//
// 本文件用进程内"预占"把这个窗口补上：
//   - 定量的那一刻就把本单金额记进 reservedBuy（同 goroutine，天然先于下一笔信号）；
//   - 下一笔信号计算剩余空间时先扣掉未过期的预占；
//   - 下单 worker 完成（成功/失败）即释放；进程崩溃留下的预占最多 TTL 后自动过期。
//
// 为什么不用"串行化定量+发送"：那会把网关 RTT 串进打分轮，今天主循环已经被拖到 4~15 分钟，
// 再串行只会更糟。预占只碰内存，代价是微秒级。
//
// 边界：预占只影响**战略买入**的剩余空间；它不落盘、不参与对账，重启即清空——
// 重启后真实在途单已经在 orders 表里（PendingBuys 负责），不会重复扣。
package engine

import (
	"log"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// buyReserveTTL 预占的最长存活时间：覆盖"定量 → 入队 → worker 调网关 → 落库"的正常链路
// （实测 1~3 秒），留足余量；worker 异常退出时靠它自愈，不让幽灵预占锁住当日额度。
const buyReserveTTL = 2 * time.Minute

// buyReserveEntry 一笔未落库的买入预占。
type buyReserveEntry struct {
	Amount float64   // 预占金额（元）
	At     time.Time // 预占时刻（TTL 判据）
}

// budgetUID 预算归属账号：优先引擎绑定的 userID，缺省回退管理员成员。
// 单独成函数是为了让"取账号"与"加锁"分开——primaryMember 自己会 RLock，
// 不能在任何持锁区间里调用（自锁）。
func (e *Engine) budgetUID() string {
	if e == nil {
		return ""
	}
	e.mu.RLock()
	u := e.userID
	e.mu.RUnlock()
	if u != "" {
		return u
	}
	return e.primaryMember()
}

// reserveBuyBudget 记一笔预占。signalID 与账号一起作键：共享引擎可能同时服务多个成员，
// 不能把 A 账号的在途金额算到 B 账号头上。
func (e *Engine) reserveBuyBudget(uid, signalID string, amount float64) {
	if e == nil || signalID == "" || amount <= 0 {
		return
	}
	if uid == "" {
		uid = e.primaryMember()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.buyReserve == nil {
		e.buyReserve = map[string]buyReserveEntry{}
	}
	e.buyReserve[uid+"|"+signalID] = buyReserveEntry{Amount: amount, At: time.Now()}
}

// releaseBuyBudget 释放预占（worker 完成或明确失败时调用，幂等）。
func (e *Engine) releaseBuyBudget(uid, signalID string) {
	if e == nil || signalID == "" {
		return
	}
	if uid == "" {
		uid = e.primaryMember()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.buyReserve, uid+"|"+signalID)
}

// reservedBuyAmount 该账号当前有效的预占总额（过期条目顺手清掉）。
func (e *Engine) reservedBuyAmount(uid string) float64 {
	return e.reservedAmountExcept(uid, "")
}

// reservedAmountExcept 同 reservedBuyAmount，但排除指定 signal 自己的预占
// （发送前复核时要算"别人占了多少"）。
func (e *Engine) reservedAmountExcept(uid, exceptSignalID string) float64 {
	if e == nil {
		return 0
	}
	if uid == "" {
		uid = e.primaryMember()
	}
	now := time.Now()
	prefix := uid + "|"
	skip := ""
	if exceptSignalID != "" {
		skip = prefix + exceptSignalID
	}
	var sum float64
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range e.buyReserve {
		if now.Sub(v.At) >= buyReserveTTL {
			delete(e.buyReserve, k)
			continue
		}
		if k == skip {
			continue
		}
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			sum += v.Amount
		}
	}
	return sum
}

// sendRoomLimit 发送前复核的那道算术（纯函数——本轮的缺陷正好藏在原来那行算术里，
// 混在取数链条里就没法被单独锁住）：
//
//   - roomOK=true：限 = 目标剩余空间 − 他人已预占（下限 0）。
//   - roomOK=false：目标空间这个读数拿不到（未开动态定权/定权缓存缺失）⇒ 退到**现金**这一把尺：
//     限 = 可用资金 − 他人已预占。拿不到目标不等于可以不看预算：同轮两笔各按全额定量，
//     2026-11-05 就是这么把仓位控制打穿的。
//   - 两者都拿不到（cashOK=false）：返回 limit=math.MaxFloat64 的"不限"形态由调用方决定，
//     这里只回 (false) 表示"没有可信的限"。
//
// 为什么原来的 `limit = room + others` 是反的：`reservedAmountExcept(uid, signalID)` 排除的是
// **自己这一笔**，剩下的全是别人已经占掉的额度——别人占的应当把本笔的上限**压低**，
// 原式却把它加回去，等于"每多一笔同轮预占，本笔反而多获得一份空间"。两笔各 2 万、剩余 3 万时，
// 第二笔的上限被算成 5 万 ⇒ 两笔都放行，合计 4 万穿透 3 万空间。
//
// English: the arithmetic of the pre-send backstop, extracted so it can be pinned by a test.
// Others' reservations must SUBTRACT from the room (the old code added them, which let a
// same-round pair each spend the full remaining room). When the target room is unavailable
// (legacy sizing) fall back to available cash minus others.
func sendRoomLimit(room float64, roomOK bool, others float64, cash float64, cashOK bool) (limit float64, ok bool) {
	if others < 0 {
		others = 0 // 预占合计不会为负；负值只可能来自上游算错，钳掉免得把上限抬高
	}
	if roomOK {
		limit = room - others
		if limit < 0 {
			limit = 0
		}
		return limit, true
	}
	if cashOK {
		limit = cash - others
		if limit < 0 {
			limit = 0
		}
		return limit, true
	}
	return 0, false
}

// sendRoomGuard §P0-5 兜底：发送前用**最新读数**复核一次剩余空间。
//
// 预占解决的是"同轮看不到彼此"；这一道解决"定量到发送之间市场/仓位变了"（例如同轮别的
// 信号成交、或定权窗口刚好翻档）。它只是兜底闸，读数不可得时**放行**——观测失败不能变成
// 不买（主闸仍在 dynamicBuyQty，那里是 fail-closed）。
//
// 返回 (allowed, roomNow)：roomNow 已扣除他人预占（自己的那份从金额里抵掉）。
//
// §BUY-ROOM-GUARD（2026-11-05）两处修：① 符号（见 sendRoomLimit 的注释，别人的预占是压低上限
// 而不是抬高上限）；② 动态定权没开/缓存缺失时不再直接放行，退到"可用资金−他人预占"这把尺。
// 这条腿对**两条定量支路都生效**——它在 placeOrderNow 里，所有买单都过这一处，
// 这正是 owner 要的"买入信号连续排队并占用预算"的最小落点（异步队列与 4 worker 不动，
// 不把网关 RTT 串进打分轮）。要回到今天的行为：`qmt.buy_send_room_guard=false`（缺省开）。
func (e *Engine) sendRoomGuard(uid string, amount float64, signalID string) (bool, float64) {
	if e == nil || amount <= 0 {
		return true, 0
	}
	e.mu.RLock()
	st := e.realStore
	e.mu.RUnlock()
	if st == nil {
		return true, 0
	}
	// 关到今天的形态（缺省不关）：这是一次缺陷修复的退路开关，不是新功能。
	if !e.buyRoomGuardOn(uid) {
		return true, 0
	}
	total := 0.0
	if acc, err := e.cashAccount(uid); err == nil && acc.TotalAsset > 0 {
		total = acc.TotalAsset
	} else if v := e.totalAssetsFor(uid); v > 0 {
		total = v
	}
	// 可用资金这把退路尺：与 total 同源取账号现金（拿不到就是没有这把尺，不许猜 0）。
	cash, cashOK := e.availableCashFor(uid)
	poses, err := st.RealPositionsForUser(uid)
	if err != nil {
		return true, 0
	}
	quotes := e.snapshotQuotes()
	positions := map[string]store.RealPosition{}
	prices := map[string]float64{}
	for _, p := range poses {
		if p.Qty <= 0 {
			continue
		}
		code := pureTsCode(p.TsCode)
		positions[code] = p
		if q := quotes[code]; q != nil && q.Price > 0 {
			prices[code] = q.Price
		} else if p.CurPrice > 0 {
			prices[code] = p.CurPrice
		}
	}
	pending, err := st.PendingBuys(uid, data.TradingDayDate(time.Now()))
	if err != nil {
		return true, 0
	}
	// 只用**已缓存的**定权读数：兜底闸不能反过来触发 regime 计算（那会在下单热路径上打外网，
	// 测试环境还会因无行情源直接炸）。缓存缺失不再等于放行——改走"可用资金−他人预占"这把尺
	// （§BUY-ROOM-GUARD 2026-11-05：同轮两笔各按全额定量把仓位控制打穿，就是从"拿不到目标空间
	// 就当没事"这一步走漏的）。两把尺都拿不到时才回今天的放行形态。
	// English: a missing sizing context no longer means pass-through; fall back to available cash.
	e.mu.RLock()
	ctx, ctxOK := e.sizingCtxCache[uid]
	e.mu.RUnlock()
	roomOK := ctxOK && !ctx.At.IsZero() && total > 0
	room := 0.0
	if roomOK {
		sc := e.sizingConfigFor(uid)
		book := tLiveBook{}
		if stt := e.tLiveFor(uid); stt != nil {
			book = stt.bookValue(prices)
		}
		_, _, room, _ = fundsTargetCurRoom(total, ctx.MS, sc.MaxTotalPositionPct, positions, prices, pending, book)
	}
	others := e.reservedAmountExcept(uid, signalID)
	limit, ok := sendRoomLimit(room, roomOK, others, cash, cashOK)
	if !ok {
		log.Printf("[qmt] §BUY-ROOM-GUARD %s 目标空间与可用资金都拿不到读数 ⇒ 本笔不复核（今天的形态）", signalID)
		return true, 0
	}
	if amount > limit {
		log.Printf("[qmt] §BUY-ROOM-GUARD %s 本单 %.0f 超可支配 %.0f（目标剩余=%.0f 可用资金=%.0f 他人预占=%.0f 尺=%s）",
			signalID, amount, limit, room, cash, others, roomScaleName(roomOK))
		return false, limit
	}
	return true, limit
}

// roomScaleName 复核用的是哪把尺（进日志，别让"限"看起来像凭空出现的数）。
func roomScaleName(roomOK bool) string {
	if roomOK {
		return "目标剩余"
	}
	return "可用资金"
}

// availableCashFor 该账号的可用资金（退路尺）。读不到或为 0 都回 cashOK=false：
// 0 元既可能是真没钱、也可能是账户读数缺失，把任何一种当成"本笔不许发"都不是这把尺的用途，
// 而把它当"有 0 元可花"又会把买入全灭——两件事都用 cashOK 分开。
func (e *Engine) availableCashFor(uid string) (float64, bool) {
	acc, err := e.cashAccount(uid)
	if err != nil {
		return 0, false
	}
	if acc.AvailableCash <= 0 {
		return 0, false
	}
	return acc.AvailableCash, true
}

// buyRoomGuardOn 发送前复核的开关（缺省开）。
// 为什么这条**不**守"缺省＝今天"的惯例：今天的行为是 `limit = room + 他人预占`——
// 别人的额度把自己的上限抬高，同轮两笔各花全额，这是缺陷而不是行为。
// 关掉它（rules.qmt.buy_send_room_guard=false）只用于现网误伤时的即时退路。
func (e *Engine) buyRoomGuardOn(uid string) bool {
	sc := e.sizingConfigFor(uid)
	if sc.BuyRoomGuard != nil {
		return *sc.BuyRoomGuard
	}
	return true
}
