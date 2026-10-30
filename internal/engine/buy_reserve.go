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

// sendRoomGuard §P0-5 兜底：发送前用**最新读数**复核一次剩余空间。
//
// 预占解决的是"同轮看不到彼此"；这一道解决"定量到发送之间市场/仓位变了"（例如同轮别的
// 信号成交、或定权窗口刚好翻档）。它只是兜底闸，读数不可得时**放行**——观测失败不能变成
// 不买（主闸仍在 dynamicBuyQty，那里是 fail-closed）。
//
// 返回 (allowed, roomNow)：roomNow 已扣除他人预占（自己的那份从金额里抵掉）。
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
	total := 0.0
	if acc, err := st.GetRealAccount(uid); err == nil && acc.TotalAsset > 0 {
		total = acc.TotalAsset
	} else if v, err := st.TotalAssets(uid); err == nil && v > 0 {
		total = v
	}
	if total <= 0 {
		return true, 0
	}
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
	// 测试环境还会因无行情源直接炸）。缓存缺失就放行——主闸在 dynamicBuyQty。
	// English: use the cached sizing context only; never compute the regime on this backstop path.
	e.mu.RLock()
	ctx, ctxOK := e.sizingCtxCache[uid]
	e.mu.RUnlock()
	if !ctxOK || ctx.At.IsZero() {
		return true, 0
	}
	sc := e.sizingConfigFor(uid)
	book := tLiveBook{}
	if stt := e.tLiveFor(uid); stt != nil {
		book = stt.bookValue(prices)
	}
	_, _, room, _ := fundsTargetCurRoom(total, ctx.MS, sc.MaxTotalPositionPct, positions, prices, pending, book)
	self := e.reservedAmountExcept(uid, signalID)
	limit := room + self
	if amount > limit {
		return false, limit
	}
	return true, limit
}
