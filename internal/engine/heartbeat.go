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
// English: pure predicate returning the zero-signal session age; 0 outside the metered scope.
func signalHeartbeatAge(now, anchor time.Time, inSession, live bool, pinnedSignals int) int64 {
	if !inSession || !live || pinnedSignals > 0 || anchor.IsZero() {
		return 0
	}
	s := int64(now.Sub(anchor).Seconds())
	if s < 0 {
		// 时钟回拨（NTP 校正）不得倒着计数：负值与"不适用"同归 0。
		return 0
	}
	return s
}

// feedSignalHeartbeatGauge 按当前引擎状态喂 signal_zero_session_sec。
// 实盘判定取控制器快照的 Enabled（与 uplink_staleness_sec 同一读数点），不另造"是否在跑实盘"
// 的第二判据——两处判据迟早漂移，漂移后告警读的是没人维护的那一个。
// English: feeds the gauge from live-controller state; non-live engines intentionally do not write.
func (e *Engine) feedSignalHeartbeatGauge(now time.Time) {
	if !data.IsActiveSession(now) {
		// 盘后/盘前/休市：写 0 收案。本函数由 scoreCycle 顶部每轮调用（会话门禁之前），
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
	metrics.SetGauge("signal_zero_session_sec", signalHeartbeatAge(now, anchor, true, true, pinned))
}
