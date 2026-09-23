// Package metrics §R4-9 轻量指标面：基于标准库 expvar 的进程内计数器，
// 经 /api/metrics（鉴权后）导出，供运维观察下单/熔断/撤单/LLM 降级等关键事件频率。
// 刻意不引入 Prometheus 依赖：单进程单机部署下 expvar 足够（可在采集侧转成任意格式）。
// English: §R4-9 lightweight metrics via stdlib expvar — exported through the authenticated
// /api/metrics endpoint; deliberately dependency-free for this single-process deployment.
package metrics

import (
	"expvar"
	"sync/atomic"
)

// counters 关键事件计数器（原子，无锁；expvar 发布时一次性快照）。
var (
	ordersPlaced    atomic.Int64 // 实盘下单成功受理笔数
	ordersRejected  atomic.Int64 // 实盘下单被拒笔数（守卫/kill-switch/熔断/业务拒单）
	ordersCancelled atomic.Int64 // 撤单闭环成功撤销笔数（自动+手动）
	breakerTrips    atomic.Int64 // 熔断触发次数（状态变化时计一次）
	llmDegrades     atomic.Int64 // LLM 降级事件次数（评分失败/解析失败占位等）
	httpPanics      atomic.Int64 // panic 恢复次数（引擎/HTTP 顶层异常保护命中）
	settleFailures  atomic.Int64 // §H1 交割单三方对账失败次数（旧实现只打一行日志，不可观测）
	fillsUnverified atomic.Int64 // §SIDE-AUTH-2 方向未证实（网关未命中派发行）被留痕拒入账本的成交笔数
)

// countersVar expvar 发布用的可序列化快照。
var countersVar = expvar.NewString("quant.metrics")

// init 包加载时立即发布一次全零快照，保证 /api/metrics 未发生任何事件也可读到结构。
func init() { publish() }

// publish 把全部计数器序列化进 expvar（JSON 字符串，采集端直接解析）。
func publish() {
	countersVar.Set(`{"orders_placed":` + itoa(ordersPlaced.Load()) +
		`,"orders_rejected":` + itoa(ordersRejected.Load()) +
		`,"orders_cancelled":` + itoa(ordersCancelled.Load()) +
		`,"breaker_trips":` + itoa(breakerTrips.Load()) +
		`,"llm_degrades":` + itoa(llmDegrades.Load()) +
		`,"panics_recovered":` + itoa(httpPanics.Load()) +
		`,"settle_failures":` + itoa(settleFailures.Load()) +
		`,"fills_side_unverified":` + itoa(fillsUnverified.Load()) + `}`)
}

// itoa 手写 int64→十进制字符串（无符号分支处理），避免为 6 个计数器引入 strconv 别名噪音。
func itoa(v int64) string {
	// 小工具：避免为 6 个数字引入 strconv 别名噪音
	b := [20]byte{}
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	if v == 0 {
		return "0"
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// OrdersPlaced 实盘下单成功 +1。
func OrdersPlaced() { ordersPlaced.Add(1); publish() }

// OrdersRejected 实盘下单被拒 +1。
func OrdersRejected() { ordersRejected.Add(1); publish() }

// OrdersCancelled 撤单成功 +1。
func OrdersCancelled() { ordersCancelled.Add(1); publish() }

// BreakerTripped 熔断触发 +1。
func BreakerTripped() { breakerTrips.Add(1); publish() }

// LLMDegraded LLM 降级 +1。
func LLMDegraded() { llmDegrades.Add(1); publish() }

// PanicRecovered 引擎/HTTP 顶层 panic 恢复 +1（观测未预期异常频率）。
func PanicRecovered() { httpPanics.Add(1); publish() }

// SettleFailed §H1（2026-09-22 修复批）三方对账失败 +1——自动调度路的对账失败旧实现只
// log 一行即吞，网关 400/网络故障均不可观测；现计入指标面并同步 opslog。
func SettleFailed() { settleFailures.Add(1); publish() }

// FillsSideUnverified §SIDE-AUTH-2（2026-09-23 夜间批）方向未证实的成交 +1——网关回报
// side_unverified=true（未命中派发行、方向仅为桥/柜台枚举猜测）的成交被留痕拒入账本。
// 为什么必须可观测：这是"账本可能被动错方向"的唯一实时信号，计数持续增长说明派发落盘或
// 桥回报归因链路在漏（取证线索），人工核对窗口的压力全看这条指标。
// English: counts fills the gateway could not vouch for (no dispatch row) — the only live
// signal that direction may be wrong; growth means the dispatch/attribution chain is leaking.
func FillsSideUnverified() { fillsUnverified.Add(1); publish() }
