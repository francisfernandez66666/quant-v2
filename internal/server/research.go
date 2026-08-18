// B5 研究候选审批端点：GET 候选列表 / POST 审批(应用) / POST 驳回。
package server

import (
	"log"
	"net/http"
	"strconv"

	"quant-trading-v2/internal/research"
)

// handleResearchProgress 处理 GET /api/research/progress：返回研究库数据加载与研究任务进度。
// 统计：股票总数、行情覆盖天数/行数、财务覆盖、候选数、研究池可用股票数。
// 前端自动研究页据此渲染进度条（数据准备度）与候选产出状态。
// （handleResearchProgress serves GET /api/research/progress — the research DB data-loading and
// research-task progress: stock counts, daily coverage, financial coverage, candidate counts and
// how many stocks are research-ready. The auto-research page renders progress bars from this.）
func (s *Server) handleResearchProgress(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	db := s.researchDB
	stocks, err := db.StockCodes()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	nStocks := len(stocks)

	// 行情覆盖：近一年有日线的股票数（研究可用的有效池）+ daily 总行数
	dailyRows, err := db.Count("daily", "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 研究池：近一年（约 244 个交易日）有行情的股票数
	ready := 0
	if nStocks > 0 {
		ready, err = db.ReadyStockCount()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	// 财务覆盖（近一年有财报的股票数，作为因子可用性代理）
	fin, err := db.Count("fina_indicator", "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 候选产出
	cands, err := db.ListCandidates("")
	if err != nil {
		writeError(w, 500, err.Error())
		return
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
	writeJSON(w, 200, map[string]any{
		"stocks":       nStocks,
		"ready_stocks": ready,
		"ready_pct":    readyPct,
		"daily_rows":   dailyRows,
		"fin_rows":     fin,
		"candidates":   len(cands),
		"applied":      applied,
		"proposed":     proposed,
		"db_attached":  true,
	})
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
	writeJSON(w, 200, map[string]any{"candidates": cands})
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
		// E6：因子战法候选审批 → 写 applied_factors.json，实盘因子 runner 读取生效。
		// English: E6 — approving a factor candidate writes applied_factors.json, consumed by the live
		// factor runner to take effect.
		if c.Kind == "factor" {
			if err := research.ApplyFactorRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用因子规则失败: "+err.Error())
				return
			}
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			log.Printf("[research] 候选 #%d 审批并应用因子规则", id)
		}
		// F3：形态战法候选审批 → 写 applied_patterns.json，实盘形态解释器读取生效。
		// English: F3 — approving a pattern candidate writes applied_patterns.json, consumed by the live
		// pattern interpreter to take effect.
		if c.Kind == "pattern" {
			if err := research.ApplyPatternRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用形态规则失败: "+err.Error())
				return
			}
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			log.Printf("[research] 候选 #%d 审批并应用形态规则", id)
		}
	case "reject":
		if err := s.researchDB.UpdateCandidateStatus(id, "rejected"); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		log.Printf("[research] 候选 #%d 已驳回", id)
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
