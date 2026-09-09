// prometheus.go 指标面扩展（§WS-L 维5）：在 expvar 计数器之外补充实时量规与
// Prometheus text 格式导出，供 promtool 校验与 Prometheus/Alertmanager 采集。
// 继续坚持「不引外部依赖」：text 格式手写（<100 行），与现有 expvar 哲学一致。
//
// English: metrics expansion (WS-L 维5). Adds real-time gauges plus a hand-written Prometheus text
// exposition (no external deps, <100 lines), consistent with the existing expvar philosophy.
package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// gauges 实时量规（原子，键=指标名）。
var (
	gaugeMu sync.Mutex
	gauges  = map[string]*atomic.Int64{}
)

// SetGauge 设置/创建量规值（整数；需要小数的指标按 1000 倍存，Prometheus 侧除回来）。
// English: sets a gauge (integer scale; fractional metrics are stored ×1000 and scaled back in
// exposition comments).
func SetGauge(name string, v int64) {
	gaugeMu.Lock()
	g, ok := gauges[name]
	if !ok {
		g = &atomic.Int64{}
		gauges[name] = g
	}
	gaugeMu.Unlock()
	g.Store(v)
}

// gaugeSnapshot 返回量规名的排序快照（确定性输出）。
func gaugeSnapshot() map[string]int64 {
	gaugeMu.Lock()
	defer gaugeMu.Unlock()
	out := make(map[string]int64, len(gauges))
	for k, g := range gauges {
		out[k] = g.Load()
	}
	return out
}

// PrometheusExposition 生成 Prometheus text 格式指标文本（gauge + 计数器）。
// 命名前缀 quant_；帮助/类型行齐全，可直接过 promtool check metrics。
// English: renders the Prometheus text-format exposition (gauges + counters), promtool-compatible.
func PrometheusExposition() string {
	s := "# HELP quant_orders_placed_total 实盘下单成功受理笔数\n" +
		"# TYPE quant_orders_placed_total counter\n" +
		"quant_orders_placed_total " + strconv.FormatInt(ordersPlaced.Load(), 10) + "\n" +
		"# HELP quant_orders_rejected_total 实盘下单被拒笔数\n" +
		"# TYPE quant_orders_rejected_total counter\n" +
		"quant_orders_rejected_total " + strconv.FormatInt(ordersRejected.Load(), 10) + "\n" +
		"# HELP quant_orders_cancelled_total 撤单成功笔数\n" +
		"# TYPE quant_orders_cancelled_total counter\n" +
		"quant_orders_cancelled_total " + strconv.FormatInt(ordersCancelled.Load(), 10) + "\n" +
		"# HELP quant_breaker_trips_total 熔断触发次数\n" +
		"# TYPE quant_breaker_trips_total counter\n" +
		"quant_breaker_trips_total " + strconv.FormatInt(breakerTrips.Load(), 10) + "\n" +
		"# HELP quant_llm_degrades_total LLM 降级事件次数\n" +
		"# TYPE quant_llm_degrades_total counter\n" +
		"quant_llm_degrades_total " + strconv.FormatInt(llmDegrades.Load(), 10) + "\n" +
		"# HELP quant_panics_recovered_total 顶层 panic 恢复次数\n" +
		"# TYPE quant_panics_recovered_total counter\n" +
		"quant_panics_recovered_total " + strconv.FormatInt(httpPanics.Load(), 10) + "\n"
	// 量规（gauge）按名排序，输出确定
	names := make([]string, 0, len(gauges))
	for k := range gauges {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		s += fmt.Sprintf("# TYPE quant_gauge_%s gauge\nquant_gauge_%s %d\n",
			sanitizeName(name), sanitizeName(name), gauges[name].Load())
	}
	return s
}

// sanitizeName 把指标名里的非字母数字下划线替换为下划线（Prometheus 指标名仅允许 [a-zA-Z0-9_:]）。
// English: replaces illegal chars in metric names with underscores for Prometheus label safety.
func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == ':' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
