// signal_decisions_api.go — §DECISION-TODAY（2026-10-28）「今日动态仓位」只读接口。
//
// 一条信号一行：定权拆分（MS/SS/单笔占比）→ 账户目标层（总资产/现金/目标/当前/剩余）→
// 三道额度帽（行业/单票/簇）→ 结果（数量/受限项/拒因）。被拦/未下单同样返回（stage=skipped），
// 因为"没买"也必须能回答为什么。
//
// 口径：day 为北京时间交易日（YYYYMMDD），缺省取今天；跨日旧行不参与（每日重新定权）。
// 该接口只读、不改任何账本。
package server

import (
	"net/http"

	"quant-trading-v2/internal/cntime"
)

// handleSignalDecisions 处理 GET /api/signals/decisions?day=YYYYMMDD（admin）。
func (s *Server) handleSignalDecisions(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	day := r.URL.Query().Get("day")
	if len(day) != 8 {
		day = cntime.DayCompactOf(cntime.Now())
	}
	items, err := db.SignalDecisionsForDay(userIDFor(r), day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list signal decisions: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"day": day, "count": len(items), "items": items,
	})
}
