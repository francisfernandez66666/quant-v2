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
	"reflect"
	"strconv"

	"quant-trading-v2/internal/paper"
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
// §B 每行附加「模拟盘实测」：按 战法→池 映射取池级真实绩效（胜率/期望/成交笔数），
// 前端与回测最优并排对比——回测冠军是否在模拟盘复现一眼可见。
func (s *Server) handleOptimizationList(w http.ResponseWriter, r *http.Request) {
	list, err := s.researchDB.ListOptimizations(20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pe := s.paperEngineFor(requestUserIDSafe(r))
	for _, task := range list {
		results, _ := task["results"].([]*store.OptimizationResult)
		for _, row := range results {
			poolKey := paper.PoolKeyForStrategy(row.Strategy, row.StrategyKind)
			if poolKey == "" || pe == nil {
				continue // 其他池（手动/兜底）不下发也不回显
			}
			st := pe.PoolStats(poolKey)
			row.PoolStats = &store.PoolLiveStats{
				WinRatePct: st.WinRatePct,
				Expectancy: st.Expectancy,
				FilledBuys: st.FilledBuys,
				// §Phase3 A/B 对照组：回显池组标签（A=回测最优/B=灰度观察）
				// English: Phase-3 A/B group tag echoed for the pool.
				ABGroup: pe.PoolABGroup(poolKey),
			}
		}
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
		// §内置战法一键应用：四内置均写统一出场旋钮（trailing_drawback_pct/max_hold_days）
		if err := s.applyBuiltinOptParams(row); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		// §A2 寻优门槛下发池纪律（用户拍板）：内置信号 Confidence 即 D1 分，
		// 与寻优的分位门槛同口径——写入对应战法池 MinScore，模拟盘入场按最优门槛过滤。
		s.applyPoolMinScore(requestUserID(r), row)
		_ = s.researchDB.UpdateOptimizationStatus(id, "approved")
		writeJSON(w, 200, map[string]any{"status": "approved", "id": id})
		return
	}
	if err := research.ApplyOptimizationParams(s.researchDir, row.StrategyKind,
		p.TakeProfitPct, p.StopLossPct, p.HoldDays, p.MinScore); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_ = s.researchDB.UpdateOptimizationStatus(id, "approved")
	s.reloadRulesByKind(row.StrategyKind) // 热重载对应库文件，实盘即时生效
	// §A2 库规则行同样把门槛下发 factor/pattern 池纪律（模拟盘入场同步过滤）
	s.applyPoolMinScore(requestUserID(r), row)
	writeJSON(w, 200, map[string]any{"status": "approved", "id": id})
}

// applyPoolMinScore 把寻优排名行的门槛分数写入对应模拟盘战法池的买入纪律（§A2）。
// 映射失败（未知战法→"" 其他池）静默跳过——其他池是手动/兜底池，不自动改纪律。
// §Phase4 同时下发参考 IR（信息比率）供动态仓位缩放。
// English: pushes an optimization row's min_score into the matching paper pool's buy discipline,
// and (Phase 4) also pushes its reference IR for dynamic position sizing. Silently skips
// unmappable strategies — the "other" pool is manual/fallback and never auto-tuned.
func (s *Server) applyPoolMinScore(userID string, row *store.OptimizationResult) {
	poolKey := paper.PoolKeyForStrategy(row.Strategy, row.StrategyKind)
	if poolKey == "" {
		return
	}
	if pe := s.paperEngineFor(userID); pe != nil {
		pe.ApplyPoolMinScore(poolKey, row.Params.MinScore)
		// §Phase4 IR 动态仓位基准：排名行无独立 IR 字段，用 Sharpe 作为风险调整收益代理
		// （年均化夏普与 IR 同为风险调整口径，动态仓位语义一致）。
		// English: Phase-4 IR-scale basis — the sweep row has no IR column, so Sharpe stands in as the
		// risk-adjusted-return proxy (same spirit as IR for dynamic sizing).
		pe.SetPoolIR(poolKey, row.Sharpe)
	}
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
	// 取第一个可用引擎（注册表模式或单引擎模式均可）。
	// 注意：ctrlFor("") 在注册表模式下会返回带 nil 值的接口（Go 经典陷阱），
	// 所以改用 AllControllers 取第一个非 nil 引擎。
	var c EngineController
	if s.registry != nil {
		for _, cc := range s.registry.AllControllers() {
			if cc != nil {
				c = cc
				break
			}
		}
	} else {
		c = s.ctrl
	}
	if c == nil {
		return
	}
	// 二次防护：反射检查底层值是否非 nil，避免 *Engine 转 interface 的非 nil 陷阱。
	if reflect.ValueOf(c).Kind() == reflect.Ptr && reflect.ValueOf(c).IsNil() {
		return
	}
	if len(kind) >= 4 && kind[:4] == "fac_" {
		c.ReloadFactorRules(s.researchDir)
	} else if len(kind) >= 4 && kind[:4] == "pat_" {
		c.ReloadPatternRules(s.researchDir)
	}
}

// applyBuiltinOptParams 内置战法的一键应用（§P2 反馈升级版）：四个手写战法全部支持——
// 把扫参冠军的 移动止盈回撤% + 最长持仓天 写进各自 config 段的统一出场旋钮
// （trailing_drawback_pct/max_hold_days，CheckExit 最前执行），落盘后引擎主循环每轮
// HotReload 自动生效；语义与扫参排名的统一出场引擎完全同口径。原有个股规则（破板/
// 止盈止损等）保持不动、仍在其后生效。min_score 不应用于内置（其入场门槛是内部评分结构）。
func (s *Server) applyBuiltinOptParams(row *store.OptimizationResult) error {
	cfg := s.cfg.GetStrategyConfig()
	if cfg == nil {
		return fmt.Errorf("策略配置未初始化")
	}
	p := row.Params
	apply := func(trail *float64, hold *int) {
		if p.TakeProfitPct > 0 {
			*trail = p.TakeProfitPct
		}
		if p.HoldDays > 0 {
			*hold = p.HoldDays
		}
	}
	switch row.Strategy {
	case "双响炮":
		apply(&cfg.DoubleBump.TrailingDrawbackPct, &cfg.DoubleBump.MaxHoldDays)
		if p.StopLossPct > 0 {
			cfg.DoubleBump.DoubleBumpTakeProfitPct = p.StopLossPct
		}
	case "龙头":
		apply(&cfg.Dragon.TrailingDrawbackPct, &cfg.Dragon.MaxHoldDays)
	case "龙回头":
		apply(&cfg.DragonReturn.TrailingDrawback, &cfg.DragonReturn.MaxHoldDays)
		if p.StopLossPct > 0 {
			cfg.DragonReturn.StopLossPct = p.StopLossPct
		}
		if p.TakeProfitPct > 0 {
			cfg.DragonReturn.TakeProfitPct = p.TakeProfitPct
		}
	case "N形":
		apply(&cfg.NShape.TrailingDrawbackPct, &cfg.NShape.MaxHoldDays)
		if p.StopLossPct > 0 {
			cfg.NShape.HardStopLoss = p.StopLossPct / 100
		}
	default:
		return fmt.Errorf("未知内置战法：%s", row.Strategy)
	}
	s.cfg.SetStrategyConfig(cfg)
	return nil
}
