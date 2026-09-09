// risk_gates.go — §WS-C 风控闸口状态端点：当日 risk_gates 命中明细 + 配置开关状态，
// 供前端「风控闸口状态」卡片与审计。English: §WS-C risk-gate status endpoint — today's gate hit
// details for the front-end gate card and audits.
package server

import (
	"net/http"
	"time"

	"quant-trading-v2/internal/cntime"
)

// handleRiskGates §WS-C 风控闸口状态（GET /api/risk/gates?day=YYYY-MM-DD，admin 权限）：
// 返回当日每闸命中计数与最近原因（risk_gates 表），以及各闸当前开关状态（qmt.risk_gate 生效配置）。
// English: §WS-C risk-gate status endpoint — per-gate hit counts/reasons for a day plus the enabled
// switch states read from the effective qmt.risk_gate config.
func (s *Server) handleRiskGates(w http.ResponseWriter, r *http.Request) {
	day := r.URL.Query().Get("day")
	if day == "" {
		day = cntime.In(time.Now()).Format("2006-01-02")
	}
	db := s.realDB()
	if db == nil {
		writeError(w, 503, "real book not available")
		return
	}
	rows, err := db.RiskGateDay(day, 200)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	switches := map[string]bool{}
	if u := s.operatorID(); u != "" {
		if ctrl := s.qmtCtrlFor(u); ctrl != nil {
			rg := ctrl.Config().RiskGate
			switches["any_enabled"] = rg.AnyEnabled()
			switches["day_loss"] = rg.DayLossLimitPct > 0
			switches["concentration"] = rg.SingleStockValuePct > 0
			switches["stale_quote"] = rg.StaleQuoteMs > 0
			switches["limit_up_block_buy"] = rg.LimitUpBlockBuy
			switches["limit_down_block_sell"] = rg.LimitDownBlockSell
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"day": day, "gates": rows, "switches": switches,
		"time": time.Now().Format("15:04:05"),
	})
}
