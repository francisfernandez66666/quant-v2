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
	"fmt"
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
	p := row.Params
	if row.StrategyKind == "" {
		// §内置战法一键应用：仅龙回头具备语义一致的出场旋钮
		// （trailing_drawback=移动止盈回撤%、max_hold_days=超期天数）；
		// 其余内置的出场结构是固定止盈/硬止损，硬填会把回撤止盈错配成固定止盈，明确拒绝。
		if err := s.applyBuiltinOptParams(row); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		_ = s.researchDB.UpdateOptimizationStatus(id, "approved")
		writeJSON(w, 200, map[string]any{"status": "approved", "id": id})
		return
	}
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

// applyBuiltinOptParams 内置战法的一键应用（§P2 用户反馈）：把扫参冠军参数写进 config.json。
// 仅龙回头完整映射（移动止盈回撤 + 最长持仓）；落盘后引擎主循环每轮 HotReload 自动生效。
// English: applies sweep params onto a builtin strategy's config (only DragonReturn maps
// semantically today); persisted via the manager and hot-applied by the engine's per-cycle reload.
func (s *Server) applyBuiltinOptParams(row *store.OptimizationResult) error {
	if row.Strategy != "龙回头" {
		return fmt.Errorf("「%s」暂无可一键应用的出场参数（仅龙回头支持移动止盈/持仓天数映射）——请在设置页调整", row.Strategy)
	}
	cfg := s.cfg.GetStrategyConfig()
	if cfg == nil {
		return fmt.Errorf("策略配置未初始化")
	}
	p := row.Params
	if p.TrailPct > 0 {
		cfg.DragonReturn.TrailingDrawback = p.TrailPct
	}
	if p.HoldDays > 0 {
		cfg.DragonReturn.MaxHoldDays = p.HoldDays
	}
	s.cfg.SetStrategyConfig(cfg)
	return nil
}
