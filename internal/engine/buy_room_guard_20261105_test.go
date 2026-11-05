// buy_room_guard_20261105_test.go — §BUY-ROOM-GUARD（2026-11-05）发送前预算复核的算术锁。
//
// 起因是 owner 的现场：「买入信号要连续排队并占用预算，不然同时两笔买入会击穿仓位控制」。
// 读码定到的因（不是猜）：
//   - 同轮预占的账本在 buy_reserve.go，定量处的扣减（room −= reserved）一直存在；
//   - 但**发送前**那道兜底复核写的是 `limit = room + 他人预占` ——
//     `reservedAmountExcept(uid, 自己)` 剩下的全是别人的额度，本应把上限压低，原式却把它加回去：
//     同轮每多一笔预占，后面那笔反而多出等额空间 ⇒ 两笔各花全额，合计穿透目标。
//   - 而且定权缓存拿不到时它直接 `return true`（放行），legacy 支路等于完全没有这道尺。
//
// 本文件锁四件事：符号方向、退到可用资金那把尺、两把尺都没有时的今天形态、以及退路开关。
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// TestSendRoomLimitSubtractsOthers 核心回归：目标剩余 5 万、他人已预占 4 万 ⇒ 本笔只有 1 万可花。
// 旧式（room + others）会回 9 万，正好放行今天那种"两笔各 2 万、空间只有 3 万"的穿透。
func TestSendRoomLimitSubtractsOthers(t *testing.T) {
	limit, ok := sendRoomLimit(50_000, true, 40_000, 0, false)
	if !ok {
		t.Fatal("目标空间可得时必须给出限")
	}
	if limit != 10_000 {
		t.Fatalf("限必须是 5万−4万=1万（加回去就是今天那颗雷）, got %.0f", limit)
	}
	// 他人预占超过剩余 ⇒ 限归零，不许回负数（负数会让"amount > limit"变成恒真但日志里是个怪数）
	if l, _ := sendRoomLimit(10_000, true, 30_000, 0, false); l != 0 {
		t.Fatalf("超占时下限必须是 0, got %.0f", l)
	}
	// 负的他人预占只会来自上游算错，钳掉——不许它反过来抬高上限
	if l, _ := sendRoomLimit(10_000, true, -5_000, 0, false); l != 10_000 {
		t.Fatalf("others<0 必须按 0 处理, got %.0f", l)
	}
}

// TestSendRoomLimitCashFallback 动态定权没跑（缓存缺/未开）⇒ 退到"可用资金 − 他人预占"。
// 这一把尺的存在就是为了消灭"拿不到目标就当没事发生"那个漏口。
func TestSendRoomLimitCashFallback(t *testing.T) {
	limit, ok := sendRoomLimit(0, false, 8_000_000, 10_000_000, true)
	if !ok || limit != 2_000_000 {
		t.Fatalf("应按可用资金给限 2万, got (%.0f,%v)", limit, ok)
	}
	// 两把尺都拿不到 ⇒ ok=false（调用方回今天的放行形态，并打一条明说的日志）
	if _, ok := sendRoomLimit(0, false, 100, 0, false); ok {
		t.Fatal("两把尺都缺时不许伪造一个限")
	}
}

// guardFixtures 装配一个能走 sendRoomGuard 的引擎：资金 1000 万、空持仓、当日无在途买单。
func guardFixFixture(t *testing.T, dyn bool, guard *bool) (*Engine, *store.DB) {
	t.Helper()
	e, db, _, _ := newQMTEngine(t, nil)
	e.SetCfgMgr(config.NewManagerWithRules(&config.Rules{
		Sizing: config.SizingConfig{
			Enabled:             &dyn,
			BudgetMode:          "dynamic",
			MaxTotalPositionPct: 0.5, // 目标仓位天花板 50% ⇒ 1000 万净值的目标是 500 万
			BuyRoomGuard:        guard,
		},
	}))
	e.mu.Lock()
	e.realStore = db
	e.mu.Unlock()
	return e, db
}

// TestSendRoomGuardUsesCashWhenNoSizingCtx 没有定权缓存（legacy/未定权）时不再一路放行：
// 可用资金 1000 万、他人预占 800 万 ⇒ 本笔 300 万必须被拒（旧实现在这里连尺都不拿）。
func TestSendRoomGuardUsesCashWhenNoSizingCtx(t *testing.T) {
	e, _ := guardFixFixture(t, true, nil)
	e.reserveBuyBudget("u_1", "sig-other", 8_000_000)
	ok, limit := e.sendRoomGuard("u_1", 3_000_000, "sig-self")
	if ok {
		t.Fatalf("现金只剩 200 万却放行 300 万，就是今天那笔穿透, limit=%.0f", limit)
	}
	if limit != 2_000_000 {
		t.Fatalf("限应为可用资金−他人预占=200万, got %.0f", limit)
	}
	// 本笔恰好卡在线上：等于剩余额度时放行（限是"可花"而不是"必须留"）
	if allowed, _ := e.sendRoomGuard("u_1", 2_000_000, "sig-self"); !allowed {
		t.Fatal("等于剩余额度的一笔不该被拒")
	}
}

// TestSendRoomGuardSubtractsOthersFromTargetRoom 定权缓存在场时按目标空间给尺，并且**减**他人预占：
// 目标 500 万、当前仓 0 ⇒ room=500 万；他人预占 400 万 ⇒ 本笔只能花 100 万。
// 旧式会把限算成 900 万，两笔 400/300 万都会放行 ⇒ 合计 700 万穿透 500 万目标。
func TestSendRoomGuardSubtractsOthersFromTargetRoom(t *testing.T) {
	e, _ := guardFixFixture(t, true, nil)
	e.mu.Lock()
	e.sizingCtxCache = map[string]sizingCtx{"u_1": {MS: 1.0, At: time.Now()}}
	e.mu.Unlock()
	e.reserveBuyBudget("u_1", "sig-other", 4_000_000)

	if allowed, limit := e.sendRoomGuard("u_1", 3_000_000, "sig-self"); allowed {
		t.Fatalf("目标剩余 500 万、他人已占 400 万时不该放行第三笔 300 万, limit=%.0f", limit)
	}
	if _, limit := e.sendRoomGuard("u_1", 1, "sig-self"); limit != 1_000_000 {
		t.Fatalf("限应为 500万−400万=100万, got %.0f", limit)
	}
	// 自己的预占不能被扣两次（这就是 reservedAmountExcept 排除自己的意义）
	e.reserveBuyBudget("u_1", "sig-self", 900_000)
	if allowed, limit := e.sendRoomGuard("u_1", 1_000_000, "sig-self"); !allowed {
		t.Fatalf("排除自己预占后限仍是 100 万，本笔 100 万应放行, limit=%.0f", limit)
	}
}

// TestSendRoomGuardKillSwitchIsToday 退路开关显式关掉＝回到今天不复核的形态（放行）。
// 缺省（nil）必须是开——这道闸修的是钱路径上的算术反了，不是新功能。
func TestSendRoomGuardKillSwitchIsToday(t *testing.T) {
	off := false
	e, _ := guardFixFixture(t, true, &off)
	e.reserveBuyBudget("u_1", "sig-other", 9_500_000)
	if allowed, limit := e.sendRoomGuard("u_1", 9_000_000, "sig-self"); !allowed || limit != 0 {
		t.Fatalf("关掉后应一路放行且不给限, got (%v,%.0f)", allowed, limit)
	}
	on := true
	e2, _ := guardFixFixture(t, true, &on)
	e2.reserveBuyBudget("u_1", "sig-other", 9_500_000)
	if allowed, _ := e2.sendRoomGuard("u_1", 9_000_000, "sig-self"); allowed {
		t.Fatal("显式打开时必须照常复核")
	}
}
