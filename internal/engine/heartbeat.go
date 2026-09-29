// heartbeat.go — 「应有值缺失」型业务心跳的引擎侧喂数腿（§0929HB-1，09-29 全量审计批 ⑪-4）。
//
// 为什么需要这条腿（因果链，照 docs/FIX_PLAN_20260929.md ⑪-4）：
//   - 外部监控（deploy/mac/kuma_seed.js）三条探针回答的是"有没有进程在应答"：站点 200、
//     /api/health 接受 200 **或 401**、emergency 口接受 200/302/404。它看不见业务事实；
//   - 系统内部原有告警（alerter.go）全是"越界才报"型：熔断、下单失败率、行情陈旧、
//     对账差异、LLM 冷却。**没有任何一条是"该有数却没有数"型**；
//   - 实录形态：战法库在夜里被降级、行情源在盘前静默降级、扫描分支条件短路——这些情况下
//     引擎每 30s 照常打分、日志完全正常、监控全绿，而**当日一条信号都没有**，
//     决策面整天空转。反向事故同样存在过（data/trade_time.go:80：旧数据在休市日持续出信号，
//     44 分钟内"当日信号"从 210 涨到 460），说明"信号数"本身必须是可读数、可告警的量。
//
// 判据口径（三处刻意，逐条都有反面理由）：
//  1. **只在盘中计时**：盘后本就停扫描，全天候计时会变成每晚一条 p1 噪音
//     （与 quote_staleness_sec「只在盘中采集」同一条纪律，scoring_loop.go refreshStalenessGauges）；
//  2. **只在实盘腿计时**：模拟/影子账号零信号是常见且合法的状态，把它告警出来只会淹掉真告警
//     （§CB 防误熔同一方向：宁可少报噪音，不可制造误动作）；
//  3. **锚=当日首轮「盘中且实盘」的评估时刻**，不是 09:30 固定点：引擎可能在盘中重启
//     （重启后当日固化信号从 signals_today.json 回填，读数立刻>0 ⇒ 告警自动销案），
//     用固定点会把"重启后 5 分钟"误报成"今天 4 小时没信号"。跨日按 TradingDayDate 重开锚，
//     与 signalStore 自己的跨日清空（RolloverDayStores）同一把日界，两者不会出现
//     "库已清空、锚还在昨天"的错位。
//  4. 计时还要求 **IsTradingDay**（见下面 signalHeartbeatAge 的 tradingDay 参数）：
//     只看钟点会让每个周末/长假都造一条 p1 假故障。
//
// 本文件同时喂第二条相反方向的心跳：signal_closed_day_pinned（休市日仍有当日新固化信号）。
// 它不是"锦上添花"——09-25 中秋当天固化数 470→481 就是这条的真实成因现场，
// 而当时没有任何一条告警或读数能区分"休市日安静是对的"与"休市日还在出信号"。
//
// 多账号口径：与仓内既有量规一致（registry.go gateLiveStrategyLibrary 注释明文："多账号先后
// 装配是进程级最后写者口径，与 trading_calendar_loaded 等既有量规一致"）。刻意**不让非实盘
// 引擎写 0**——那会让影子账号的正常状态覆盖掉实盘账号的异常读数（掩蔽），所以本函数只在
// 本引擎确认为实盘时落笔；盘中收案与盘后归零都由实盘引擎自己的 7×24 节拍负责。
//
// 锁纪律：e.QMTController() 内部取 e.mu（非重入），故调用它时不得持有 e.mu；本函数按
// 「无锁判会话 → 取控制器 → 短临界区读/写心跳字段 → 锁外调 signalStore.List()」的顺序走
// （signalStore 自带互斥，与 e.mu 无交叉；§0924 死锁教训里那类"持锁调加锁 accessor"的形态
// 在此明确避开）。
//
// English: engine-side feed for the absence-type heartbeat "no pinned signal all session".
// Counts seconds since the first in-session live cycle while today's pinned-signal store is empty;
// writes nothing for non-live engines (a paper account writing 0 would mask the live account's
// reading). Day anchor uses the same trading-day boundary the signal store itself rolls over on.
package engine

import (
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
)

// 量规键名**在写入点写死字面量** "signal_zero_session_sec"，不另立 const 别名：
// §DEADGAUGE 老坑（2026-09-23 锤实三条死规则，09-29 全量门禁又抓一次本批自撞）——
// 通用守卫是按「SetGauge("<键名>"」的字面形态扫赋值点的，别名写法扫不到 ⇒ 刚接的心跳会被
// 重新判成"有规则无赋值点、永不触发"。本仓 settlement/llm/order_rate 三处赋值点同样是字面量，
// 这里跟同一口径；heartbeat_test.go 的"读写同源"锁直接拿规则表的 Metric 比这里的字面量。

// signalHeartbeatAge 纯函数判据（单测直接喂参数，不需要真引擎、不需要真时钟）。
// 返回"当日盘中零固化信号的持续秒数"；不属于计时场景一律返回 0（0 恒不触发 gt 型规则，
// 与 refreshStalenessGauges 的"未知/不适用写 0"统一口径）。
// tradingDay 是**第二个独立门**，和 inSession 不是一回事：IsActiveSession 只看钟点
//
//	（§CAL-GATE 把这类"只看钟点"的判据统一改走 IsTradingDay 时，本函数是当天新写的、
//	当场把同一坑又踩了一遍：周六 10:00 钟点在盘中、日历上不是交易日，零信号会一路计到
//	30 分钟然后推 p1「交易日盘中零固化信号」——每条周末/长假都在造假故障。
//	造假告警的代价不是多一条通知，而是两周后这条通知被关掉，真事故跟着一起被关掉。）
//
// English: pure predicate for the zero-signal session age; 0 outside the metered scope, which
// requires BOTH an active clock session and a real trading day.
func signalHeartbeatAge(now, anchor time.Time, inSession, tradingDay, live bool, pinnedSignals int) int64 {
	if !inSession || !tradingDay || !live || pinnedSignals > 0 || anchor.IsZero() {
		return 0
	}
	s := int64(now.Sub(anchor).Seconds())
	if s < 0 {
		// 时钟回拨（NTP 校正）不得倒着计数：负值与"不适用"同归 0。
		return 0
	}
	return s
}

// closedDayPinnedCount 纯函数判据：**休市日里"今天才生成"的固化信号条数**。
// 与上面那条互为反面：signal_zero 管"该有数却没有"，这条管"不该有数却有了"。
// 为什么"休市日出信号"是可判红的事实（不是理论风险）：2026-09-25 中秋休市当天，
// 当日固化数从 470 涨到 481（§CAL-GATE 复看记录）——TradingDayDate 在休市日回退到上一交易日，
// 所以这些新增全都落进**昨天那一桶**，界面上看像"昨天的信号还在"，实际是行情源/日历
// 任一环节 fail-open 后引擎照旧在出信号。那种时候"没有新信号"才是正确行为。
// 判据只看 GeneratedAt 落在 now 这一**日历日**的条数：跨日回填的老信号（重启后从
// signals_today.json 读回来的上一交易日批次）天然不计，避免"休市日一开引擎就红"。
// tradingDay=true 时恒 0：交易日出信号是本分，不是异常。
// English: counts pinned signals generated **today** while today is not a trading day —
// the fail-open direction of the same calendar problem (09-25 Mid-Autumn grew 470→481).
func closedDayPinnedCount(now time.Time, tradingDay bool, sigs []combat_agent.Signal) int {
	if tradingDay {
		return 0
	}
	n := 0
	for _, s := range sigs {
		if s.GeneratedAt.IsZero() {
			continue // 无生成时刻＝无法判定是不是今天新增，宁可不计也不伪造一次红
		}
		g := s.GeneratedAt.In(now.Location())
		if g.Year() == now.Year() && g.YearDay() == now.YearDay() {
			n++
		}
	}
	return n
}

// feedClosedDayPinnedGauge 喂 signal_closed_day_pinned（休市日增量型心跳）。
// 与 signal_zero 的**唯一**口径差：这里不区分实盘/影子账号，任何引擎都落笔。
// 理由：休市日还在出信号是"链路 fail-open"，与哪个账号在跑无关；而按实盘门控会让
// 只在影子/研究模式下跑的部署**永远不喂这个键**，§DEADGAUGE 当场判它"有规则无赋值点"。
// 多账号先后装配沿用本仓既有的进程级最后写者口径（registry.go gateLiveStrategyLibrary 注释）。
func (e *Engine) feedClosedDayPinnedGauge(now time.Time, tradingDay bool) {
	if tradingDay {
		metrics.SetGauge("signal_closed_day_pinned", 0)
		return
	}
	e.mu.Lock()
	sb := e.signalStore
	e.mu.Unlock()
	var sigs []combat_agent.Signal
	if sb != nil {
		sigs = sb.List() // signalStore 自带互斥，必须在 e.mu 之外调用（同上）
	}
	metrics.SetGauge("signal_closed_day_pinned", int64(closedDayPinnedCount(now, tradingDay, sigs)))
}

// feedSignalHeartbeatGauge 按当前引擎状态喂两条信号面心跳：
//   - signal_zero_session_sec（盘中该有却没有）
//   - signal_closed_day_pinned（休市日不该有却有）
//
// 实盘判定取控制器快照的 Enabled（与 uplink_staleness_sec 同一读数点），不另造"是否在跑实盘"
// 的第二判据——两处判据迟早漂移，漂移后告警读的是没人维护的那一个。
// English: feeds both signal-side heartbeats; the zero-session one is live-gated, the closed-day
// growth one is fed by every engine (see feedClosedDayPinnedGauge's why).
func (e *Engine) feedSignalHeartbeatGauge(now time.Time) {
	tradingDay := data.IsTradingDay(now)
	e.feedClosedDayPinnedGauge(now, tradingDay)
	if !data.IsActiveSession(now) || !tradingDay {
		// 盘后/盘前/休市日：写 0 收案。本函数由 scoreCycle 顶部每轮调用（会话门禁之前），
		// 所以这条收案一定会发生，不会把上午的读数留到晚上冒充故障。
		metrics.SetGauge("signal_zero_session_sec", 0)
		return
	}
	c := e.QMTController()
	if c == nil {
		return // 没接实盘控制器＝这台引擎不参与实盘决策，不写（见文件头"多账号口径"）
	}
	if snap := c.Snapshot(); !snap.Enabled {
		return // 实盘开关未开：影子/模拟态的零信号是合法事实，不计时也不写 0 掩蔽别人
	}
	e.mu.Lock()
	day := data.TradingDayDate(now)
	if e.hbSignalDay != day {
		e.hbSignalDay = day
		e.hbSignalAt = now // 本周期锚刚建立，时长 0 ⇒ 首轮恒不触发（防"刚重启就报"）
	}
	anchor := e.hbSignalAt
	sb := e.signalStore
	e.mu.Unlock()
	pinned := 0
	if sb != nil {
		pinned = len(sb.List()) // signalStore 自带互斥，必须在 e.mu 之外调用
	}
	metrics.SetGauge("signal_zero_session_sec", signalHeartbeatAge(now, anchor, true, tradingDay, true, pinned))
}
