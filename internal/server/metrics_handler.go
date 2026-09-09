// metrics_handler.go §WS-L 维5 指标/告警端点：Prometheus text 导出 + 阈值告警状态。
// English: WS-L 维5 metrics/alert endpoints — Prometheus text exposition and alert state.
package server

import (
	"net/http"

	"quant-trading-v2/internal/metrics"
)

// handlePrometheusMetrics 处理 GET /api/metrics/prometheus（admin）：
// 返回 Prometheus text 格式指标文本（promtool check metrics 可校验）。
// English: serves the Prometheus text exposition (admin; promtool-compatible).
func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(metrics.PrometheusExposition()))
}

// handleAlertState 处理 GET /api/metrics/alerts（admin）：返回当前告警规则与触发状态。
// English: returns the current alert rules and firing state (admin).
func (s *Server) handleAlertState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"rules": metrics.DefaultAlertRules(),
	})
}
