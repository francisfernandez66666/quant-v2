// Package paper — anchor_book_t1.go：纸面账本的首买锚点簿（§ANCHOR-T1，owner 2026-11-19 裁决）。
//
// 为什么持仓对象上已经有 AnchorPrice 还要一张表
//
//	清仓时持仓对象整条从 `e.positions` 里删掉（sellAllLocked / ResetPool），锚点跟着消失。
//	于是同一个交易日内「卖出清仓 → 再买回来」会走 fillLocked 重新建仓、把锚点冻结成第二次
//	买入的成本，止盈/止损的分母就跟着漂了。owner 的口径是"即使清仓了也要 T+1 才清除"。
//
// 判据只有一条：簿里那一行的交易日 == 本笔成交的北京交易日 ⇒ 沿用；否则重新冻结。
//
//	与实盘侧 `internal/store/anchor_book.go` 同一口径（那边是 real_anchor_book 表，这边是
//	随盘面状态一起落盘的一张 map），两本账各自有落盘介质，规则必须一致——否则纸面能开 T
//	而实盘不能（或反之），对照表读起来像"两个系统"。
//
// English: the paper book's first-buy anchor, kept per code together with the trading day it was
// frozen on. Clearing a position deletes the position object, so without this book a same-day
// re-buy would re-freeze the anchor at the new cost. Same day = reuse, new day = re-freeze; the
// live book keeps the same rule in store's real_anchor_book table.
package paper

import (
	"time"

	"quant-trading-v2/internal/cntime"
)

// anchorBookEntry 一条冻结的锚点：价、来源、冻结时刻与它属于哪个北京交易日。
// English: one frozen anchor — price, provenance, freeze time and the Beijing trading day it belongs to.
type anchorBookEntry struct {
	Price  float64   `json:"price"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
	Day    string    `json:"day"`
}

// pruneAnchorBookLocked 丢掉不属于 `day` 的簿行（T+1 判据是"同日才沿用"，更早的日子用不上）。
// English: drops entries not belonging to `day` — the predicate is "same day only".
func (e *Engine) pruneAnchorBookLocked(day string) {
	if e.anchorBook == nil {
		e.anchorBook = make(map[string]anchorBookEntry)
		return
	}
	for code, b := range e.anchorBook {
		if b.Day != day {
			delete(e.anchorBook, code)
		}
	}
}

// rememberAnchorLocked 把某码的锚点登记进簿（须持锁调用）。价格 <=0 视为"没有锚点"，不登记——
// 0 是"未知"，写进去会让当天回买读到一个不存在的首买价。
// English: records a code's anchor; 0 means "unknown" and is never recorded.
func (e *Engine) rememberAnchorLocked(code string, price float64, source string, at, now time.Time) {
	if price <= 0 || code == "" {
		return
	}
	day := cntime.DayOf(now)
	e.pruneAnchorBookLocked(day)
	if source == "" {
		source = anchorSourceFirstLot
	}
	e.anchorBook[code] = anchorBookEntry{Price: price, Source: source, At: at, Day: day}
}

// openAnchorLocked 决定这次建仓该用哪个锚点（须持锁调用）：簿里有同一交易日的就沿用
// （连同来源与冻结时刻一起沿用，这样 AnchorAt 不会凭空前移），否则按本笔冻结并登记。
// English: resolves the anchor for a new position — a same-day book row wins, otherwise this fill
// freezes a new one. Returns (price, source, at).
func (e *Engine) openAnchorLocked(code string, price float64, source string, now time.Time) (float64, string, time.Time) {
	day := cntime.DayOf(now)
	if b, ok := e.anchorBook[code]; ok && b.Day == day && b.Price > 0 && b.Source != "" {
		return b.Price, b.Source, b.At
	}
	e.rememberAnchorLocked(code, price, source, now, now)
	return price, source, now
}

// clearAnchorBookLocked 清盘（全局重置 / 单池清盘）时删除这些码的簿行：清盘是"账本重开"，
// 不是市场行为，沿用旧锚点会让重置后新建的仓带着上一本账的风险线。
// English: a book reset (global or per-pool) also clears those codes' anchors — a reset is an
// accounting restart, not a market event, so an old anchor must not leak into the new book.
func (e *Engine) clearAnchorBookLocked(codes []string) {
	for _, code := range codes {
		delete(e.anchorBook, code)
	}
}
