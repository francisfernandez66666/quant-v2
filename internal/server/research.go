// B5 研究候选审批端点：GET 候选列表 / POST 审批(应用) / POST 驳回。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"quant-trading-v2/internal/store"
	"strconv"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"sync"
	"time"
)

// handleResearchFactors 处理 GET /api/research/factors：返回全部已注册因子的元数据
// （ID / 中文名 / 大类 / 一句话解释），供前端自动研究页把因子规则渲染成可读文案。
// 由后端 `factor` 库直接提供，前端无需硬编码维护两份映射。
// （handleResearchFactors serves GET /api/research/factors — metadata for every registered factor
// (ID / Chinese name / category / one-line description), so the auto-research page can render factor
// rules as readable text. Sourced directly from the factor registry; the frontend keeps no duplicate map.）
func (s *Server) handleResearchFactors(w http.ResponseWriter, r *http.Request) {
	defs := factor.All()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"id":   d.ID,
			"name": d.Name,
			"cat":  d.Cat.CategoryName(),
			"desc": d.Desc,
		})
	}
	writeJSON(w, 200, map[string]any{"factors": out})
}

// handleResearchProgress 处理 GET /api/research/progress：返回研究库数据加载与研究任务进度。
// 统计：股票总数、行情覆盖天数/行数、财务覆盖、候选数、研究池可用股票数。
// 前端自动研究页据此渲染进度条（数据准备度）与候选产出状态。
// （handleResearchProgress serves GET /api/research/progress — the research DB data-loading and
// research-task progress: stock counts, daily coverage, financial coverage, candidate counts and
// how many stocks are research-ready. The auto-research page renders progress bars from this.）
// progressCache 进度接口结果缓存：该接口含三次全表统计（daily 765万行 COUNT/GROUP BY），
// 浏览器 30s 轮询 × 全表扫描曾参与把整机挤进内存枯竭（2026-08-23 实录）。
// 60s 缓存对进度条展示完全够用。English: 60s cache — three full-table stats per poll was a wedge ingredient.
var progressCache struct {
	mu   sync.Mutex
	at   time.Time
	body map[string]any
}

// handleSchedulerStatus 处理 GET /api/scheduler/status：读取 researchd 每 30s 写入的
// scheduler_status.json 可见性快照，让前端直接回答"为何卡排队"（禁用/交易时段/内存闸门/槽位占用/队列空）。
// 文件不存在时回退为"未知"，避免前端误判。English: returns researchd's visibility snapshot so the
// UI can explain why tasks are queued without server logs.
func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.researchDir, "scheduler_status.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"ok":     false,
			"reason": "尚未获取到调度状态：researchd 未运行或未到首次写入（请确认 quant-research 服务已启动，且与 quant 共用同一 QUANT_DATA_DIR）",
		})
		return
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "reason": "调度状态解析失败"})
		return
	}
	st["ok"] = true
	writeJSON(w, 200, st)
}

// handleResearchTaskLog 处理 GET /api/research/task/{id}/log：读取 researchd 为每条研究任务
// 落盘的 task_logs/task_<id>.log（子进程 stdout/stderr），让前端直接查看回测/研究日志，
// 无需 SSH 翻服务器。文件不存在时返回 exists=false。English: serves the per-task log file so the
// UI can show backtest/research output without server access.
func (s *Server) handleResearchTaskLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	logPath := filepath.Join(s.researchDir, "task_logs", "task_"+id+".log")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		writeJSON(w, 200, map[string]any{"exists": false, "log": ""})
		return
	}
	// 限制返回体积：超过 200KB 只取末尾，避免一次性拖垮前端/接口。
	const maxBytes = 200 * 1024
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
	}
	writeJSON(w, 200, map[string]any{"exists": true, "log": string(raw)})
}

// handleResearchProgress 处理 GET /api/research/progress：返回自动研究流水尾部日志
// （截断至 maxBytes 防止超大响应），配合前端轮询展示研究执行进度。
func (s *Server) handleResearchProgress(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	progressCache.mu.Lock()
	if progressCache.body != nil && time.Since(progressCache.at) < 60*time.Second {
		body := progressCache.body
		progressCache.mu.Unlock()
		writeJSON(w, 200, body)
		return
	}
	progressCache.mu.Unlock()
	body := s.computeResearchProgress(w)
	if body == nil {
		return
	}
	progressCache.mu.Lock()
	progressCache.at, progressCache.body = time.Now(), body
	progressCache.mu.Unlock()
	writeJSON(w, 200, body)
}

// computeResearchProgress 真正的统计逻辑（原 handler 体，签名化以便缓存包装）。
func (s *Server) computeResearchProgress(w http.ResponseWriter) map[string]any {
	db := s.researchDB
	stocks, err := db.StockCodes()
	if err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	nStocks := len(stocks)

	// 行情覆盖：近一年有日线的股票数（研究可用的有效池）+ daily 总行数
	dailyRows, err := db.Count("daily", "")
	if err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	// 研究池：近一年（约 244 个交易日）有行情的股票数
	ready := 0
	if nStocks > 0 {
		ready, err = db.ReadyStockCount()
		if err != nil {
			writeError(w, 500, err.Error())
			return nil
		}
	}
	// 财务覆盖（近一年有财报的股票数，作为因子可用性代理）
	fin, err := db.Count("fina_indicator", "")
	if err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	// 候选产出
	cands, err := db.ListCandidates("")
	if err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	applied, proposed := 0, 0
	for _, c := range cands {
		switch c.Status {
		case "applied":
			applied++
		case "proposed":
			proposed++
		}
	}

	// 覆盖比例：研究池就绪 = 近一年有行情的股票 / 总股票
	readyPct := 0.0
	if nStocks > 0 {
		readyPct = float64(ready) / float64(nStocks)
	}
	log.Printf("[research] progress: stocks=%d ready=%d daily_rows=%d fin=%d cands=%d", nStocks, ready, dailyRows, fin, len(cands))
	// §数据源标识：前端徽标展示「同花顺（新）/ baostock」
	ds := "baostock"
	if s.cfg != nil {
		if ps := s.cfg.Rules.Data.PrimarySource; ps == "hithink" {
			ds = "同花顺（新）"
		}
	}
	return map[string]any{
		"stocks":       nStocks,
		"ready_stocks": ready,
		"ready_pct":    readyPct,
		"daily_rows":   dailyRows,
		"fin_rows":     fin,
		"candidates":   len(cands),
		"applied":      applied,
		"proposed":     proposed,
		"db_attached":  true,
		"data_source":  ds,
		"scheduler":    s.schedulerState(),
	}
}

// schedulerState 读取 research_state.json 的最近步骤结果（阶段2.4 状态上报）：
// 返回 {day, step_index, done, last_step, last_status, last_error, last_at}；文件缺失返回 null。
// 前端自动研究页据此展示「夜间作业最近一步：discover_factors / done|error|timeout」，
// 杜绝"dataload 卡 21h、step_index 停在 0"这类静默故障无感知。
// English: reads the latest step outcome from research_state.json ({day, step_index, done, last_step,
// last_status, last_error, last_at}); null when absent. The auto-research page shows it so silent
// stalls (a dataload once hung 21h with step_index stuck at 0) become visible.
func (s *Server) schedulerState() map[string]any {
	if s.researchDir == "" {
		return nil // 未配置研究目录
	}
	b, err := os.ReadFile(filepath.Join(s.researchDir, "research_state.json"))
	if err != nil {
		return nil // 无状态文件
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil {
		return nil // 状态文件损坏按缺失处理
	}
	return st
}

// handleResearchCandidates 处理 GET /api/research/candidates：列出候选（默认全部）。
// （handleResearchCandidates lists research candidates, optionally filtered by status.）
func (s *Server) handleResearchCandidates(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	status := r.URL.Query().Get("status")
	cands, err := s.researchDB.ListCandidates(status)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 通用回测证据（§8.6-B）：factor 候选看 B4 回填的 avg_excess；pattern 候选的
	// 回放结果在任务行 result_text——统一按"两类型最新任务是否 done"判定已测，
	// 并把回放汇总文本附带给前端展示（否则形态候选永远显示"未测"）。
	type btEvidence struct {
		Done bool   `json:"backtest_done"`
		Text string `json:"backtest_result_text,omitempty"`
	}
	evid := map[int64]btEvidence{}
	for _, c := range cands {
		var best *store.ResearchTask
		for _, typ := range []string{store.TaskBacktestCandidate, store.TaskBacktestStrategy} {
			t, err := s.researchDB.LatestTaskByRef(typ, c.ID)
			if err == nil && t != nil && (best == nil || t.ID > best.ID) {
				best = t
			}
		}
		if best != nil && best.Status == store.TaskDone {
			evid[c.ID] = btEvidence{Done: true, Text: best.ResultText}
		}
	}
	out := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		m := map[string]any{
			"id": c.ID, "kind": c.Kind, "status": c.Status,
			"factors": c.Factors, "weights": c.Weights, "metric": c.Metric,
			"ic_mean": c.ICMean, "ir": c.IR, "avg_excess": c.AvgExcess,
			"horizon": c.Horizon, "reason": c.Reason, "created_at": c.CreatedAt,
		}
		if ev, ok := evid[c.ID]; ok {
			m["backtest_done"] = true
			if ev.Text != "" {
				m["backtest_result_text"] = ev.Text
			}
		}
		out = append(out, m)
	}
	writeJSON(w, 200, map[string]any{"candidates": out})
}

// handleResearchApprove 处理 POST /api/research/candidates/{id}/approve：审批通过并应用权重。
// （handleResearchApprove approves a candidate; for weight candidates the set is written
// to applied_rules.json so live strategies pick it up.）
func (s *Server) handleResearchApprove(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "approve")
}

// handleResearchReject 处理 POST /api/research/candidates/{id}/reject：驳回候选。
func (s *Server) handleResearchReject(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "reject")
}

// handleResearchGrayscale 处理 POST /api/research/candidates/{id}/grayscale：
// §Phase2 自动灰度分级——候选进入灰度观察（写 grayscale_rules.json，仅 paper 盘消费，不上实盘）。
func (s *Server) handleResearchGrayscale(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "grayscale")
}

// approveCandidate 审批/驳回候选的公共实现：更新候选状态；审批时按候选类型
// （weights/factor/pattern）写应用配置并热重载引擎，驳回仅改状态。
func (s *Server) approveCandidate(w http.ResponseWriter, r *http.Request, action string) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	c, err := s.researchDB.CandidateByID(id)
	if err != nil {
		writeError(w, 404, "候选不存在")
		return
	}
	switch action {
	case "approve":
		if err := s.researchDB.UpdateCandidateStatus(id, "approved"); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if c.Kind == "weights" {
			if err := research.ApplyWeights(s.researchDir, c); err != nil {
				writeError(w, 500, "应用权重失败: "+err.Error())
				return
			}
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			log.Printf("[research] 候选 #%d 审批并应用权重", id)
		}
		// E6：因子战法候选审批 → 追加到战法库 applied_factors.json 并热重载引擎（多战法同时实盘）。
		// English: E6 — approving a factor candidate appends it to the library applied_factors.json and
		// hot-reloads the engine (multiple strategies run concurrently).
		if c.Kind == "factor" {
			if err := research.ApplyFactorRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用因子规则失败: "+err.Error())
				return
			}
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			s.reloadLibraries() // 立即注入 8a/8b，无需重启
			log.Printf("[research] 候选 #%d 审批并应用因子规则", id)
		}
		// F3：形态战法候选审批 → 追加到战法库 applied_patterns.json 并热重载引擎（多形态同时实盘）。
		// English: F3 — approving a pattern candidate appends it to the library and hot-reloads the engine.
		if c.Kind == "pattern" {
			if err := research.ApplyPatternRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用形态规则失败: "+err.Error())
				return
			}
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			s.reloadLibraries()
			log.Printf("[research] 候选 #%d 审批并应用形态规则", id)
		}
	case "reject":
		if err := s.researchDB.UpdateCandidateStatus(id, "rejected"); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		log.Printf("[research] 候选 #%d 已驳回", id)
	case "grayscale":
		// §Phase2 自动灰度分级：仅 factor/pattern 支持；写入灰度库（paper 观察），不改实盘。
		// English: Phase-2 grayscale — factor/pattern only; enters the grayscale library (paper observation),
		// the live 8a/8b injection is untouched.
		if c.Kind != "factor" && c.Kind != "pattern" {
			writeError(w, 400, "灰度仅支持 factor/pattern 候选（kind="+c.Kind+"）")
			return
		}
		if err := research.ApplyGrayscale(s.researchDir, c); err != nil {
			writeError(w, 500, "写入灰度库失败: "+err.Error())
			return
		}
		if err := s.researchDB.UpdateCandidateStatus(id, research.StatusGrayscale); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		// §Phase3 paper A/B 对照组：为灰度候选开立独立规则池并标记 B 组，
		// 与回测最优（A 组）并行实测。池 key 与库规则同规则（fac_<id>/pat_<id>）。
		// English: Phase-3 paper A/B — carve a dedicated rule pool for the grayscale candidate and tag
		// it group B, running head-to-head against the backtest champion (group A). Pool key follows
		// the library-rule convention (fac_<id>/pat_<id>).
		if pe := s.paperEngineFor(requestUserID(r)); pe != nil {
			poolKey := "fac_" + strconv.FormatInt(id, 10)
			if c.Kind == "pattern" {
				poolKey = "pat_" + strconv.FormatInt(id, 10)
			}
			pe.EnsurePool(poolKey)
			pe.SetPoolABGroup(poolKey, "B")
		}
		log.Printf("[research] 候选 #%d 进入灰度观察（paper 盘，B 组对照）", id)
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
