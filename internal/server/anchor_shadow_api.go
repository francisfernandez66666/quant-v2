// anchor_shadow_api.go — §ANCHOR-FIRST W2 影子对照表的只读 HTTP 面（2026-10-26）。
//
// 数据源：<dataDir>/accounts/<userID>/anchor_shadow.json（引擎每账本每交易日首轮写一次）。
// 本接口只读、不触发取样、不写文件；管理员可用它把"成本口径 vs 首买价锚"的线位移动
// 在页面上签认（W2 只对照不切硬，W3 切硬必须先看这张表）。
//
// English: read-only HTTP surface for the W2 anchor-shadow table. It never samples or writes;
// admins use it to review cost-vs-anchor line movement before the W3 hard cutover.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	anchorShadowDefaultDays = 20
	anchorShadowMaxDays     = 60
)

// anchorShadowDayView 一日的账本读数；books 用 RawMessage 透传，避免 server 反向依赖引擎类型。
// English: one day of book readings; books are passed through as raw JSON.
type anchorShadowDayView struct {
	Day   string                     `json:"day"`
	Books map[string]json.RawMessage `json:"books"`
}

// anchorShadowView 接口响应：state 显式区分 ok/missing/corrupt，前端据 state 渲染空态/损坏态。
type anchorShadowView struct {
	Account  string                `json:"account"`
	KeepDays int                   `json:"keep_days"`
	State    string                `json:"state"`
	Reason   string                `json:"reason,omitempty"`
	Days     []anchorShadowDayView `json:"days"`
}

// handleAnchorShadow 处理 GET /api/anchor-shadow?days=N（admin）：
// 读取当前管理员账号的影子对照文件，按日期倒序返回最近 N 个交易日。
// English: GET /api/anchor-shadow?days=N (admin) — newest-first shadow readings for the caller.
func (s *Server) handleAnchorShadow(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	if uid == "" || filepath.Base(uid) != uid || strings.ContainsAny(uid, `/\`) {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	if s.cacheDir == "" {
		writeError(w, http.StatusServiceUnavailable, "数据目录未接入")
		return
	}
	days := anchorShadowDefaultDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			days = n
		}
	}
	if days <= 0 {
		days = 1
	}
	if days > anchorShadowMaxDays {
		days = anchorShadowMaxDays
	}

	path := filepath.Join(s.cacheDir, "accounts", uid, "anchor_shadow.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		state := "corrupt"
		reason := "影子对照文件读取失败（原文件未改动）"
		if os.IsNotExist(err) {
			state = "missing"
			reason = "该账号还没有影子对照数据：引擎在当日首轮裁决后生成，至少需要 1 个交易日"
		}
		writeJSON(w, http.StatusOK, anchorShadowView{
			Account: uid, KeepDays: anchorShadowMaxDays, State: state, Reason: reason,
			Days: []anchorShadowDayView{},
		})
		return
	}

	var view anchorShadowView
	if err := json.Unmarshal(raw, &view); err != nil {
		writeJSON(w, http.StatusOK, anchorShadowView{
			Account: uid, KeepDays: anchorShadowMaxDays, State: "corrupt",
			Reason: "影子对照文件解析失败（原文件未改动，等待下一轮重写或人工检查）",
			Days:   []anchorShadowDayView{},
		})
		return
	}
	if view.Account == "" {
		view.Account = uid
	}
	if view.KeepDays <= 0 {
		view.KeepDays = anchorShadowMaxDays
	}
	// 文件按追加顺序存日；接口给页面按倒序（最新的先看）。日期是 YYYY-MM-DD，字典序即时间序。
	sort.Slice(view.Days, func(i, j int) bool { return view.Days[i].Day > view.Days[j].Day })
	if len(view.Days) > days {
		view.Days = view.Days[:days]
	}
	if view.Days == nil {
		view.Days = []anchorShadowDayView{}
	}
	view.State = "ok"
	view.Reason = ""
	writeJSON(w, http.StatusOK, view)
}
