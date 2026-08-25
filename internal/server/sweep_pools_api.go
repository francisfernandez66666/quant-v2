// sweep_pools_api.go 各战法独立寻优参数池 API（§OPTIMIZE_POOL_INTEGRATION_PLAN D1）。
//
// GET /api/research/sweep-pools            列出全部自定义配置（未配置战法由引擎走默认池）
// PUT  /api/research/sweep-pools           保存单战法四维步进搜索空间（服务端护栏校验）
//
// 护栏语义（用户确认）：组合数不设硬拒绝上限——引擎按批(≤5000)全量模拟后批冠军 PK；
// 仅拦截明显误填的超天量空间（单战法 >10 万组合），提示放宽步长。
//
// English: per-strategy sweep search-space endpoints (list/upsert with server-side guardrails).
package server

import (
	"encoding/json"
	"net/http"

	"quant-trading-v2/internal/store"
)

// handleSweepPoolList 处理 GET /api/research/sweep-pools。
func (s *Server) handleSweepPoolList(w http.ResponseWriter, r *http.Request) {
	list, err := s.researchDB.ListSweepPoolConfigs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]*store.SweepPoolConfig, 0, len(list))
	for _, c := range list {
		out = append(out, c)
	}
	writeJSON(w, 200, map[string]any{"pools": out})
}

// handleSweepPoolUpsert 处理 PUT /api/research/sweep-pools。
// body: {strategy, tp_from/tp_to/tp_step, sl_*, hold_from/hold_to/hold_step(int), score_*}
func (s *Server) handleSweepPoolUpsert(w http.ResponseWriter, r *http.Request) {
	var c store.SweepPoolConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, 400, "请求体非法")
		return
	}
	if c.Strategy == "" {
		writeError(w, 400, "strategy 不能为空")
		return
	}
	if err := c.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := s.researchDB.UpsertSweepPoolConfig(&c); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "saved", "combos": c.ComboCount()})
}
