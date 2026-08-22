// optimizations.go 参数优化 API（§P2-f STRATEGY_OPTIMIZE_PLAN）。
//
// POST /api/backtest/optimize                          入队全库扫参任务（high 优先级）
// GET  /api/research/optimizations                     扫参任务列表（按任务倒序分组）
// POST /api/research/optimizations/{id}/approve        审批：参数覆盖写库规则 + 热重载
// POST /api/research/optimizations/{id}/reject         淘汰该排名行
//
// English: sweep-optimizer endpoints — enqueue, list, approve (persist rule-level overrides and
// hot-reload the engine), reject.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// handleOptimizeEnqueue 处理 POST /api/backtest/optimize：
// payload {kind:"optimize", objective, start?, end?, top_n?}，ref_id 固定 0（全库一次一个）。
func (s *Server) handleOptimizeEnqueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Objective string `json:"objective"`
		Start     string `json:"start"`
		End       string `json:"end"`
		TopN      int    `json:"top_n"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	payload := map[string]any{"kind": "optimize"}
	if body.Objective != "" {
		payload["objective"] = body.Objective
	}
	if body.Start != "" {
		payload["start"] = body.Start
	}
	if body.End != "" {
		payload["end"] = body.End
	}
	if body.TopN > 0 {
		payload["top_n"] = body.TopN
	}
	id, _, err := s.enqueueBacktestTask(store.TaskBacktestStrategy, optTaskRefID, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": id, "status": "queued"})
}

// optTaskRefID 扫参任务的 ref_id（同 ref 幂等：已有排队/运行中扫参则不重复入队）。
const optTaskRefID int64 = 990

// handleOptimizationList 处理 GET /api/research/optimizations。
func (s *Server) handleOptimizationList(w http.ResponseWriter, r *http.Request) {
	list, err := s.researchDB.ListOptimizations(20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"optimizations": list})
}

// handleOptimizationApprove 处理 POST /api/research/optimizations/{id}/approve：
// 规则级参数覆盖写入 applied_*.json（因子含 buy_threshold；形态仅出场），随后热重载引擎；
// 内置战法（无 strategy_kind）不支持入库，返回 400 提示走设置页。
func (s *Server) handleOptimizationApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	row, err := s.researchDB.GetOptimization(id)
	if err != nil || row == nil {
		writeError(w, 404, "排名记录不存在")
		return
	}
	if row.StrategyKind == "" {
		writeError(w, 400, "内置战法参数暂不支持一键入库——请在设置页手动调整对应战法配置")
		return
	}
	p := row.Params
	if err := research.ApplyOptimizationParams(s.researchDir, row.StrategyKind,
		p.TrailPct, p.HoldDays, p.MinScore); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_ = s.researchDB.UpdateOptimizationStatus(id, "approved")
	s.reloadRulesByKind(row.StrategyKind) // 热重载对应库文件，实盘即时生效
	writeJSON(w, 200, map[string]any{"status": "approved", "id": id})
}

// handleOptimizationReject 处理 POST /api/research/optimizations/{id}/reject。
func (s *Server) handleOptimizationReject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	if err := s.researchDB.UpdateOptimizationStatus(id, "rejected"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "rejected", "id": id})
}

// reloadRulesByKind 按规则 ID 前缀热重载对应的战法库（审批后实盘即时生效）。
func (s *Server) reloadRulesByKind(kind string) {
	c := s.ctrlFor("") // 注册表模式取默认控制器；单引擎模式即全局 ctrl
	if c == nil {
		return
	}
	if len(kind) >= 4 && kind[:4] == "fac_" {
		c.ReloadFactorRules(s.researchDir)
	} else if len(kind) >= 4 && kind[:4] == "pat_" {
		c.ReloadPatternRules(s.researchDir)
	}
}
