// config_history.go §WS-K 维4 配置历史/回滚端点：admin 查看 config.json 历史快照与字段级 diff，
// 以及从快照回滚上一版配置（恢复后立即落盘 + 审计）。
// English: WS-K 维4 config history & rollback endpoints (admin): list snapshots with field-level
// diff, and restore a previous version (persists immediately and audits).
package server

import (
	"encoding/json"
	"log"
	"net/http"

	"quant-trading-v2/internal/config"
)

// handleConfigHistory 处理 GET /api/config/history（admin）：返回 config.json 历史快照列表
// （倒序），并支持 ?diff=<ts> 对比「当前 vs 指定快照」的字段级变更。
// English: lists config.json history snapshots (newest first); ?diff=<ts> returns the field-level
// diff between the current file and that snapshot.
func (s *Server) handleConfigHistory(w http.ResponseWriter, r *http.Request) {
	diffTS := r.URL.Query().Get("diff")
	if diffTS != "" {
		before, err := config.RestoreRulesContent(s.cfg, diffTS)
		if err != nil {
			writeError(w, 404, err.Error())
			return
		}
		after, err := config.RestoreRulesContentCurrent(s.cfg)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		d, err := config.DiffRules(before, after)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]interface{}{"snapshot_ts": diffTS, "diff": d})
		return
	}
	list, err := config.ListRuleSnapshots(s.cfg)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"snapshots": list})
}

// handleConfigRollback 处理 POST /api/config/rollback {snapshot_ts}（admin）：
// 从快照恢复 config.json 原子落盘并记 opslog 审计（走 config.Watch 热重载生效）。
// English: restores config.json from a snapshot (atomic write) and audits; the config.Watch
// hot-reload picks it up.
func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotTS string `json:"snapshot_ts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SnapshotTS == "" {
		writeError(w, 400, "需要 snapshot_ts")
		return
	}
	b, err := config.RestoreRulesContent(s.cfg, req.SnapshotTS)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	if err := config.WriteConfigFile(s.cfg, b); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[config] 配置回滚到快照 %s（操作者=%s）", req.SnapshotTS, userIDFor(r))
	writeJSON(w, 200, map[string]string{"status": "ok", "snapshot_ts": req.SnapshotTS})
}
