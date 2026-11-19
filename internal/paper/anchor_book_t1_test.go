// anchor_book_t1_test.go — §ANCHOR-T1（owner 2026-11-19 裁决）纸面账本行为锁。
//
// 事故形态：清仓时持仓对象整条被删，锚点跟着消失 ⇒ 同一个交易日内「卖出清仓 → 再买回来」
// 会把锚点冻结成第二次买入的成本，止盈/止损的分母随之漂移。owner 的口径是
// "即使清仓了也要 T+1 才清除"。
//
// 这一组锁三条：
//
//	P1 同日清仓后再建仓 ⇒ 锚点、来源仍是当天第一次建仓那一档（成本照新一笔算）；
//	P2 跨交易日后再建仓 ⇒ 重新冻结（T+1 语义）；
//	P3 锚点簿随状态落盘、清盘（Reset）时清空。
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// openAnchorForTest 直接走 fillLocked 建一笔仓（显式股数，夹具为非 dynamic 口径）。
// 用 fillLocked 而不是 OnSignals：本组测的是建仓写锚点的位置，不测信号链上的闸门。
func openAnchorForTest(t *testing.T, e *Engine, price float64, qty int, now time.Time) *Position {
	t.Helper()
	if err := e.fillLocked("dragon", "300001", "测试股", "龙头", price, now, now,
		price, qty, "anchor-book-test", 1, 0, true, 0); err != nil {
		t.Fatalf("建仓 %.2f×%d: %v", price, qty, err)
	}
	p, ok := e.positions["300001"]
	if !ok {
		t.Fatalf("建仓后持仓不存在")
	}
	return p
}

// TestAnchorBookSameDayRebuyKeepsFirstAnchor P1：同日清仓后回买，锚点不漂。
func TestAnchorBookSameDayRebuyKeepsFirstAnchor(t *testing.T) {
	e := partialTestEngine(t)
	// 时间钉在**当日 10:30**：从 `time.Now()` 起步再 +1h，在 23:00 之后跑会把「同日回买」
	// 跑成跨日（锚点理应重冻），于是同一条用例白天绿、深夜红。同类钉法见 paper_test.go 的固定锚点。
	// English: pinned to 10:30 today — `time.Now()` + 1h crosses midnight after 23:00 and turns this
	// same-day rebuy into a cross-day one, which would legitimately re-freeze the anchor.
	now := time.Now()
	now = time.Date(now.Year(), now.Month(), now.Day(), 10, 30, 0, 0, now.Location())
	p := openAnchorForTest(t, e, 10, 1000, now)
	firstAnchor := p.AnchorPrice
	if firstAnchor <= 0 || firstAnchor != p.CostPrice {
		t.Fatalf("建仓锚点应为含费成本同值, got anchor=%v cost=%v", firstAnchor, p.CostPrice)
	}
	// 昨日底仓 → 今天可以整笔卖出
	t1Ready(e)
	if _, err := e.sellAllLocked(p, 11, "anchor-book-test"); err != nil {
		t.Fatalf("清仓: %v", err)
	}
	if _, ok := e.positions["300001"]; ok {
		t.Fatalf("清仓后持仓应已删除")
	}
	if b, ok := e.anchorBook["300001"]; !ok || b.Price != firstAnchor {
		t.Fatalf("清仓不得删锚点簿, got %+v ok=%v", b, ok)
	}
	// 当天以完全不同的价格买回来
	p2 := openAnchorForTest(t, e, 12.6, 1000, now.Add(time.Hour))
	if p2.AnchorPrice != firstAnchor {
		t.Fatalf("§ANCHOR-T1 同日回买锚点必须仍是 %v, got %v", firstAnchor, p2.AnchorPrice)
	}
	if p2.AnchorSource != anchorSourceFirstLot {
		t.Fatalf("锚点来源应保持 first_lot, got %q", p2.AnchorSource)
	}
	if p2.CostPrice < 12.6 {
		t.Fatalf("成本必须按新一笔重算（盈亏口径与风险线口径分开）, got %v", p2.CostPrice)
	}
}

// TestAnchorBookCrossDayHoldClearedTodayKeepsAnchor P1b：前几个交易日建仓的仓，今天清掉、
// 今天再买回来，沿用的仍是当初的首买价（owner 原话"即使清仓了也要 T+1 才清除"的另一种形态）。
// 与实盘 real_anchor_book 同口径：清仓那一笔把簿行按今天重登记（价不动），日键才不会误判过期。
func TestAnchorBookCrossDayHoldClearedTodayKeepsAnchor(t *testing.T) {
	e := partialTestEngine(t)
	openedAt := time.Now().AddDate(0, 0, -2)
	p := openAnchorForTest(t, e, 8, 1000, openedAt)
	firstAnchor := p.AnchorPrice
	t1Ready(e)
	if _, err := e.sellAllLocked(p, 9, "anchor-book-test"); err != nil {
		t.Fatalf("清仓: %v", err)
	}
	today := time.Now()
	if b := e.anchorBook["300001"]; b.Day != cntime.DayOf(today) || b.Price != firstAnchor {
		t.Fatalf("清仓应把锚点按今天重登记且价保持 %v, got %+v", firstAnchor, b)
	}
	p2 := openAnchorForTest(t, e, 9.6, 1000, today)
	if p2.AnchorPrice != firstAnchor {
		t.Fatalf("§ANCHOR-T1 跨日持有后当天清仓再回买，锚点应沿用首买 %v, got %v", firstAnchor, p2.AnchorPrice)
	}
}

// TestAnchorBookNextTradingDayRefreezes P2：跨交易日回买 ⇒ 重新冻结（T+1 清除）。
func TestAnchorBookNextTradingDayRefreezes(t *testing.T) {
	e := partialTestEngine(t)
	now := time.Now()
	p := openAnchorForTest(t, e, 10, 1000, now)
	t1Ready(e)
	if _, err := e.sellAllLocked(p, 11, "anchor-book-test"); err != nil {
		t.Fatalf("清仓: %v", err)
	}
	tomorrow := now.AddDate(0, 0, 1)
	p2 := openAnchorForTest(t, e, 13, 1000, tomorrow)
	if p2.AnchorPrice != 13 {
		t.Fatalf("§ANCHOR-T1 次日应重新冻结锚点（13）, got %v", p2.AnchorPrice)
	}
	if b := e.anchorBook["300001"]; b.Day != cntime.DayOf(tomorrow) {
		t.Fatalf("锚点簿应记次日, got %+v", b)
	}
}

// TestAnchorBookPersistsAcrossReload P3a：锚点簿必须随状态落盘（中午重启不能丢当天首买价）。
func TestAnchorBookPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, dir+"/book.json")
	e.SetStrategyPools([]string{"dragon"})
	now := time.Now()
	openAnchorForTest(t, e, 10, 1000, now)
	want := e.positions["300001"].AnchorPrice
	e.mu.Lock()
	e.persist()
	e.mu.Unlock()

	e2 := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, dir+"/book.json")
	e2.SetStrategyPools([]string{"dragon"})
	b, ok := e2.anchorBook["300001"]
	if !ok || b.Price != want || b.Day != cntime.DayOf(now) {
		t.Fatalf("重载后锚点簿应保留 %v（当日）, got %+v ok=%v", want, b, ok)
	}
}

// TestAnchorBookClearedByReset P3b：清盘是账本重开，锚点簿一并清空。
func TestAnchorBookClearedByReset(t *testing.T) {
	e := partialTestEngine(t)
	openAnchorForTest(t, e, 10, 1000, time.Now())
	if len(e.anchorBook) == 0 {
		t.Fatalf("建仓后锚点簿应有行")
	}
	e.Reset()
	if len(e.anchorBook) != 0 {
		t.Fatalf("清盘后锚点簿必须清空, got %+v", e.anchorBook)
	}
}
