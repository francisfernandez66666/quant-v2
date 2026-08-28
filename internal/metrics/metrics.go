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
)

// countersVar expvar 发布用的可序列化快照。
var countersVar = expvar.NewString("quant.metrics")

func init() { publish() }

// publish 把全部计数器序列化进 expvar（JSON 字符串，采集端直接解析）。
func publish() {
	countersVar.Set(`{"orders_placed":` + itoa(ordersPlaced.Load()) +
		`,"orders_rejected":` + itoa(ordersRejected.Load()) +
		`,"orders_cancelled":` + itoa(ordersCancelled.Load()) +
		`,"breaker_trips":` + itoa(breakerTrips.Load()) +
		`,"llm_degrades":` + itoa(llmDegrades.Load()) +
		`,"panics_recovered":` + itoa(httpPanics.Load()) + `}`)
}

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
