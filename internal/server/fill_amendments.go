// fill_amendments.go — §FILL-AMEND（2026-09-23）历史错账的**人工逐笔勘误** HTTP 通道 +
// 账本守恒自检端点。
//
// 端点清单（全部 adminMiddleware 收口，实盘账本本就是 admin-only，见 server.go §GAP1.8/1.10）：
//
//	GET  /api/qmt/fill-amendments               列出勘误决定（?status=pending|applied|revoked）
//	POST /api/qmt/fill-amendments               提交一条**待批准**勘误 {fill_id,new_side,reason}
//	POST /api/qmt/fill-amendments/{id}/apply    批准（唯一让账目数字变动的动作）
//	POST /api/qmt/fill-amendments/{id}/revoke   撤销（数字立刻回到原始方向）
//	GET  /api/qmt/fills/conservation            守恒自检（只读、只报数）
//
// 三条边界（与 store 层同口径，这里只列 HTTP 侧的要害）：
//  1. 本文件没有任何改写 fills 的路径——勘误是"追加一条决定"，批准/撤销只改勘误自己的状态；
//  2. 提交/批准/撤销三类动作各写一条 opslog 审计 + 一条 opslog.Audit 安全审计行，
//     内容**只有业务字段**（代码、原方向、新方向、成交号/行号、理由、操作者）；
//     理由是人写的自由文本，会原样进审计行，所以本文件绝不把任何请求头、token、口令拼进日志；
//  3. 守恒自检只读：不触发任何写账动作，发现差异也只输出线索。
//
// English: §FILL-AMEND admin-only HTTP surface for human per-fill corrections (submit → approve →
// optionally revoke) plus the read-only conservation self-check. Nothing here rewrites the raw
// fills table; every mutation is an append-only decision row, and each action leaves a business-only
// audit line (never credentials).
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// fillAmendBody 勘误提交体。
// ⚠ 只有 fill_id：锚点（trade_id / 复合键）一律由服务端从原始行读出（见 CreateFillAmendment 注释）——
// 让调用方自报锚点等于允许手写一个匹配不到任何成交的"死勘误"。
type fillAmendBody struct {
	FillID  int64  `json:"fill_id"`
	NewSide string `json:"new_side"`
	Reason  string `json:"reason"`
	UserID  string `json:"user_id,omitempty"` // 预留：多账号时显式指定归属（当前仅 admin 单账号）
}

// handleCreateFillAmendment POST /api/qmt/fill-amendments：提交一条待批准勘误（影子态）。
// 明确回显 status=pending，前端据此显示"已提交勘误，尚未批准"。
func (s *Server) handleCreateFillAmendment(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	var req fillAmendBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: 需要 {\"fill_id\":123,\"new_side\":\"卖出\",\"reason\":\"…\"}")
		return
	}
	uid := userIDFor(r)
	operator := amendmentOperator(r)

	// 越权面收口：先按"本账号可见（含遗留全局行）"取原始行，取不到即 404。
	// 不做这一步，多账号部署下任一管理员可凭自猜的 fills.id 改到他人账上的成交。
	raw, err := db.RawFillForUser(uid, req.FillID)
	if err != nil {
		if errors.Is(err, store.ErrFillAmendmentNoFill) {
			writeError(w, http.StatusNotFound, "待勘误的成交不存在或不属于本账号")
			return
		}
		writeError(w, 500, "read raw fill: "+err.Error())
		return
	}

	am, err := db.CreateFillAmendment(raw.ID, req.NewSide, req.Reason, operator)
	if err != nil {
		s.auditFillAmendment("create", r, raw, nil, "fail: "+err.Error())
		switch {
		case errors.Is(err, store.ErrFillAmendmentSide):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrFillAmendmentConflict):
			writeError(w, http.StatusConflict, "该成交已有待批准或已生效的勘误，请先撤销原条目")
		case errors.Is(err, store.ErrFillAmendmentNoFill):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			// 校验类错误（理由必填/过长）文案本身就是给用户看的，原样回 400 之外的状态码
			// 反而会让前端把"漏填理由"当成服务故障。
			if strings.Contains(err.Error(), "必填") || strings.Contains(err.Error(), "过长") {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, 500, "create amendment: "+err.Error())
		}
		return
	}
	s.auditFillAmendment("create", r, raw, am, "pending")
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"ok":        "1",
		"amendment": am,
		"note":      "勘误已提交，状态=待批准（pending）；批准前不影响任何账目数字",
	})
}

// handleListFillAmendments GET /api/qmt/fill-amendments?status=&limit=：勘误台账（含影子态）。
// 前端用它把"这笔已提交、尚未批准"挂到成交行上（匹配键 amend_key 由 Go 侧单点计算）。
func (s *Server) handleListFillAmendments(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != store.FillAmendPending && status != store.FillAmendApplied && status != store.FillAmendRevoked {
		writeError(w, http.StatusBadRequest, "status 仅接受 pending|applied|revoked 或留空")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit 需为正整数")
			return
		}
		limit = n
	}
	uid := userIDFor(r)
	all, err := db.ListFillAmendments(status, limit)
	if err != nil {
		writeError(w, 500, "list amendments: "+err.Error())
		return
	}
	// 归属过滤与成交簿同一口径：本账号的 + 遗留全局行（user_id=''）。
	out := make([]store.FillAmendment, 0, len(all))
	for _, a := range all {
		if a.UserID == "" || a.UserID == uid {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []store.FillAmendment{} // 固定空数组：前端 .length 不能因 null 崩页（§F-5 同族）
	}
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "amendments": out})
}

// handleApplyFillAmendment POST /api/qmt/fill-amendments/{id}/apply：批准生效。
// 这是整条链路上**唯一**让账目数字变动的动作，故单独成端点、单独留痕，不并入提交流程。
func (s *Server) handleApplyFillAmendment(w http.ResponseWriter, r *http.Request) {
	s.transitionFillAmendment(w, r, "apply")
}

// handleRevokeFillAmendment POST /api/qmt/fill-amendments/{id}/revoke：撤销（数字回到原始方向）。
func (s *Server) handleRevokeFillAmendment(w http.ResponseWriter, r *http.Request) {
	s.transitionFillAmendment(w, r, "revoke")
}

// transitionFillAmendment apply/revoke 的共用执行体：两条路径的校验/审计形态必须一致，
// 分写两份迟早只改其中一份（本仓 §M5「同一保护两处各写一遍」的教训形态）。
func (s *Server) transitionFillAmendment(w http.ResponseWriter, r *http.Request, action string) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "勘误 ID 非法")
		return
	}
	before, err := db.FillAmendmentByID(id)
	if err != nil {
		if errors.Is(err, store.ErrFillAmendmentNotFound) {
			writeError(w, http.StatusNotFound, "勘误记录不存在")
			return
		}
		writeError(w, 500, "read amendment: "+err.Error())
		return
	}
	// 越权面收口（与提交口一致）：只能处置本账号可见的勘误。
	if before.UserID != "" && before.UserID != userIDFor(r) {
		opslog.Logf("quant", "勘误处置越权拒绝 action=%s id=%d 操作者=%s 归属=%s", action, id, userIDFor(r), before.UserID)
		writeError(w, http.StatusForbidden, "无权限：该勘误不属于本账号")
		return
	}
	var after *store.FillAmendment
	operator := amendmentOperator(r)
	if action == "apply" {
		after, err = db.ApplyFillAmendment(id, operator)
	} else {
		after, err = db.RevokeFillAmendment(id)
	}
	if err != nil {
		s.auditFillAmendment(action, r, store.RealFill{Code: before.Code, OrigSide: before.OrigSide,
			Side: before.OrigSide, TradeID: before.TradeID, ID: before.FillID, Amount: before.OrigAmount},
			before, "fail: "+err.Error())
		// 失败分层：并发抢占处置、非 pending 态（已批准/已撤销再动一次）都是"客户端重试即可"的
		// 冲突 → 409；其余是存储层异常 → 500。把库故障伪装成 4xx 会让前端误报成"参数不对"，
		// 掩盖真实故障（§M-8/§N-6：降级却报成功/报错报错了方向，都是失明）。
		switch {
		case strings.Contains(err.Error(), "并发"), strings.Contains(err.Error(), "仅待批准"):
			// 冲突原文回给前端：让运维一眼分清"重复点击/已被他人处置"与参数错误。
			writeError(w, http.StatusConflict, err.Error())
		default:
			// 存储层异常按 500 报，绝不折叠成 4xx 掩盖真实故障。
			writeError(w, 500, action+" amendment: "+err.Error())
		}
		return
	}
	raw := store.RealFill{Code: after.Code, OrigSide: after.OrigSide, Side: after.OrigSide,
		TradeID: after.TradeID, ID: after.FillID, Amount: after.OrigAmount}
	result := after.Status
	if action == "revoke" {
		result = store.FillAmendRevoked
	}
	s.auditFillAmendment(action, r, raw, after, result)
	writeJSON(w, 200, map[string]interface{}{
		"ok": "1", "amendment": after,
		"note": map[string]string{
			"apply":  "勘误已生效：买入笔数/买入金额/卖出回款/成交簿重放同步跟着变（原始 fills 行未改）",
			"revoke": "勘误已撤销：所有账目数字回到原始方向",
		}[action],
	})
}

// amendmentOperator 审计用的操作者标识：优先用户名（人类可读），回落 uid，再回落 "unknown"。
// 为什么不用 token：审计行必须能在没有凭据体系的情况下读，且绝不把凭据值写进日志。
func amendmentOperator(r *http.Request) string {
	if u := userFromContext(r); u != nil {
		if strings.TrimSpace(u.Username) != "" {
			return u.Username
		}
		if u.ID != "" {
			return u.ID
		}
	}
	return "unknown"
}

// auditFillAmendment 勘误动作的双通道留痕：
//   - opslog.Logf("quant") —— 运营日志，含完整业务字段（代码/原方向/新方向/成交号/理由/操作者）；
//   - opslog.Audit —— 安全审计行，只到"谁对哪笔做了什么、结果如何"的粒度（理由可能含自由文本，
//     不进安全审计行，避免把长文本灌进按事件对齐的 audit 文件里）。
//
// ⚠ 这里拼进去的每一个字段都来自成交行或勘误行本身，绝不碰请求头/凭据/配置里的密钥。
func (s *Server) auditFillAmendment(action string, r *http.Request, raw store.RealFill, am *store.FillAmendment, result string) {
	uid := userIDFor(r)
	operator := amendmentOperator(r)
	fillsKey := raw.TradeID
	if fillsKey == "" {
		fillsKey = "row:" + strconv.FormatInt(raw.ID, 10)
	}
	orig, newSide, reason, amendID := raw.OrigSide, "", "", int64(0)
	if am != nil {
		newSide = am.NewSide
		reason = am.Reason
		amendID = am.ID
		if orig == "" {
			orig = am.OrigSide
		}
		if fillsKey == "row:0" {
			fillsKey = "row:" + strconv.FormatInt(am.FillID, 10)
		}
	}
	opslog.Logf("quant", "§FILL-AMEND 勘误动作=%s 操作者=%s 账号=%s 代码=%s 成交号=%s 原方向=%s 新方向=%s 勘误ID=%d 结果=%s 理由=%s",
		action, operator, uid, raw.Code, fillsKey, orig, newSide, amendID, result, reason)
	opslog.Audit("fill_amend_"+action, uid, raw.Code+"|"+fillsKey, result)
}

// handleFillConservation GET /api/qmt/fills/conservation?day=YYYY-MM-DD：§FILL-AMEND 守恒自检
// （只读、只报数，绝不动账）。两条不变量的判据原文见 internal/store/fill_conservation.go。
// 期初资金取本账号 qmt.initial_capital 配置（未配置时现金条如实标"未检查"，不假装通过）。
func (s *Server) handleFillConservation(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	uid := userIDFor(r)
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = cntime.Now().Format("2006-01-02")
	}
	if len(day) != 10 || day[4] != '-' || day[7] != '-' {
		writeError(w, http.StatusBadRequest, "day 需为 YYYY-MM-DD")
		return
	}
	initial := 0.0
	if s.cfg != nil {
		initial = s.cfg.GetQMTConfigFor(uid).InitialCapital
	}
	rep, err := db.CheckBookConservation(uid, day, initial)
	if err != nil {
		writeError(w, 500, "conservation check: "+err.Error())
		return
	}
	// 结论变化才留档（本端点是只读诊断口，前端会周期刷新——每次刷新都写日志会把 opslog 冲成噪音）。
	if !rep.OK {
		opslog.Logf("quant", "§FILL-AMEND 守恒自检未通过 账号=%s 日=%s 持仓差异=%d 现金已检查=%v 现金差=%.2f 生效勘误=%d",
			uid, day, len(rep.PositionLines), rep.Cash.Checked, rep.Cash.Diff, rep.AppliedAmendments)
	}
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "report": rep})
}
