// sizing_evo_api.go — §SIZING-EVO 只读观测端点（自动研究页「仓位进化」tab 数据源）。
//
// GET /api/research/sizing-evo 返回：
//   - 开关与阈值：evo.enabled / auto_promote / min_samples / observation_days / 灰度上限；
//   - 影子库规模：总快照 / 已结算 / 交易日（冷启动进度条用）；
//   - 灰度候选：条目 + **实时 L2 判定**（promote/reject/pending + 指标 + 理由）；
//   - 生效配置（nil=出厂默认）、已驳回组合数、降级记录；
//   - 净流入因子当前档位（配置可视化，owner 核对"现在实盘用的是哪档"）；
//   - 最近周度/夜间运行报告原文（runner 落盘的 report_*.json）。
//
// 只读：本端点不跑评估、不写状态（评估在 CLI 进程里跑，避免 HTTP 线程做重活）。
// English: read-only status endpoint for the sizing-evolution tab — switches, shadow-store
// progress, grayscale candidates with live L2 verdicts, applied config, flow bands, last reports.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"quant-trading-v2/internal/sizing_evo"
)

// handleSizingEvoStatus 处理 GET /api/research/sizing-evo。
// English: GET /api/research/sizing-evo — the read-only evolution status payload.
func (s *Server) handleSizingEvoStatus(w http.ResponseWriter, r *http.Request) {
	if s.researchDir == "" {
		writeError(w, 503, "研究库未接入")
		return
	}
	evoDir := filepath.Join(s.researchDir, "sizing_evo")
	snaps := sizing_evo.NewSnapshotStore(evoDir)
	state := sizing_evo.NewEvoStateStore(evoDir)

	base, _ := s.cfg.GetSizingConfigFor("")
	base = base.WithDefaults()
	evo := base.Evo.WithDefaults()

	counts, err := snaps.Counts()
	if err != nil {
		writeError(w, 500, "影子库读取失败: "+err.Error())
		return
	}
	all, err := snaps.Samples()
	if err != nil {
		writeError(w, 500, "影子库读取失败: "+err.Error())
		return
	}
	st, err := state.Load()
	if err != nil {
		writeError(w, 500, "状态库读取失败: "+err.Error())
		return
	}

	// 灰度候选 + 实时 L2 判定（只算不写）。
	type grayItem struct {
		CandID    string                `json:"cand_id"`
		Params    map[string]float64    `json:"params"`
		EnteredAt string                `json:"entered_at"`
		Note      string                `json:"note,omitempty"`
		Verdict   sizing_evo.EvoVerdict `json:"verdict"`
	}
	l2 := sizing_evo.L2Opts{ObservationDays: evo.ObservationDays}
	gray := make([]grayItem, 0, len(st.Grayscale))
	for _, g := range st.Grayscale {
		gray = append(gray, grayItem{
			CandID: g.CandID, Params: g.Params, EnteredAt: g.EnteredAt, Note: g.Note,
			Verdict: sizing_evo.EvaluateGrayscale(all, g.CandID, l2),
		})
	}

	// 组装只读响应：开关/阈值/样本计数/灰度与 L2 统计一次出门，前端不再二次拼口径。
	resp := map[string]any{
		"enabled":          base.EvoOn(),
		"auto_promote":     base.EvoAutoPromoteOn(),
		"min_samples":      evo.MinSamples,
		"observation_days": evo.ObservationDays,
		"grayscale_max":    sizing_evo.GrayscaleMax,
		"counts":           counts,
		"samples":          len(all),
		"grayscale":        gray,
		"applied":          st.Applied,
		"rejected_count":   len(st.Rejected),
		"demoted":          st.Demoted,
		"flow_factor":      base.FlowFactor,
		"sizing_enabled":   base.EnabledOn(),
		"weekly_report":    readEvoReport(evoDir, "weekly"),
		"nightly_report":   readEvoReport(evoDir, "nightly"),
	}
	writeJSON(w, http.StatusOK, resp)
}

// readEvoReport 读 runner 落盘的一次运行报告（缺文件返回 nil，前端按"未跑过"处理）。
// English: reads a persisted run report (nil when the file is missing).
func readEvoReport(dir, kind string) any {
	b, err := os.ReadFile(filepath.Join(dir, "report_"+kind+".json"))
	if err != nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}
