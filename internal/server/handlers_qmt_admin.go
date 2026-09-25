// handlers_qmt_admin.go — §0925EVE-W3-G（FIX_PLAN ⑫ C3）：第三态「待核对」人工收敛的
// 产品化出口（两个 admin 权限端点）：
//
//	GET  /api/qmt/pending-review   透传网关 /admin/status 的 unresolved_orders 清单；
//	POST /api/qmt/order-confirm    人工改判单条待核对委托（released|settled），转发网关
//	                               /admin/order-confirm 并如实回传结论。
//
// 设计口径（对齐本仓既有 admin 端点体例）：
//   - 读失败绝不空数组冒充：网关不可达/未接实盘一律非 200 + error，让前端能把
//     「查询失败」与「确实没有待核对单」分成两种可见状态渲染（观察/低危档 D3 同款教训）；
//   - 确认动作是**特权人工改判**（删占位/改写委托终态），无论成败都写 opslog.Audit 行；
//   - 本端点不重发任何订单（网关 §CLAIMRELEASE 语义原样转发，Go 侧也不做自动决策）。
//
// English: admin endpoints wiring the gateway's third-state reconciliation exit —
// the pending-review list (honest errors, never an empty array masking a read failure)
// and the manual order-confirm passthrough with an opslog audit line on every attempt.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/trading"
)

// handleQMTPendingReview 处理 GET /api/qmt/pending-review（admin）：
// 拉取网关「待核对」清单。未接实盘 → 503；网关读失败 → 502 带原因；成功 → 200 +
// {ok, orders(非 null 数组), unresolved_count, truncated, active, gateway_ts, failover_enable}。
// English: relays the gateway unresolved list; read failures return explicit 502/503.
func (s *Server) handleQMTPendingReview(w http.ResponseWriter, r *http.Request) {
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		// 注意与 handleQMTBroker 的 200+ok:false 不同：本面板的「空清单」是有资金安全含义的
		// 断言（没有待核对单），未接入/读失败若冒充 200 空清单，前端就把"没在看"渲染成
		// "没问题"。失败必须走非 200。
		writeError(w, 503, "未接入 QMT 实盘：无法查询待核对清单（空清单不代表无风险）")
		return
	}
	pv, err := ctrl.GatewayPendingReview()
	if err != nil {
		writeError(w, 502, "网关待核对清单查询失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":               true,
		"orders":           pv.Orders, // 客户端构造时已保证非 nil（JSON 序列化为 []）
		"unresolved_count": pv.TotalCount,
		"truncated":        pv.Truncated,
		"active":           pv.Active,
		"gateway_ts":       pv.GatewayTS,
		"failover_enable":  pv.FailoverEnable,
	})
}

// qmtOrderConfirmReq POST /api/qmt/order-confirm 请求体。
// wire_ref 为前端契约里的「待核对单锚点」，即网关下单幂等键 signal_id
// （兼容直传 signal_id 的调用方，两键取先非空者）。
// English: confirm request — wire_ref (== gateway signal_id anchor) + human decision.
type qmtOrderConfirmReq struct {
	WireRef  string `json:"wire_ref"`
	SignalID string `json:"signal_id"`
	Decision string `json:"decision"` // released=柜台确无此单，删占位解锁 | settled=柜台有此单，改写正常终态
	OrderID  string `json:"order_id"` // settled 可选：回填真实委托号
	Status   string `json:"status"`   // settled 可选：指定终态（缺省「已撤」由网关决定）
}

// handleQMTOrderConfirm 处理 POST /api/qmt/order-confirm（admin）：
// 人工改判一条待核对委托。参数非法 400；未接入 503；网关传输失败 502；
// 网关业务拒绝（404 无此单 / 409 非待核对态等）按网关状态码如实映射 4xx；
// 成功 200 回传网关结论（released/status）。**每次尝试都落 opslog.Audit**。
// English: forwards one manual confirm, mapping gateway verdicts honestly and auditing
// every attempt (privileged human re-judgement of an order's fate).
func (s *Server) handleQMTOrderConfirm(w http.ResponseWriter, r *http.Request) {
	var req qmtOrderConfirmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body: 需要 {\"wire_ref\":\"<signal_id>\",\"decision\":\"released|settled\"}")
		return
	}
	ref := strings.TrimSpace(req.WireRef)
	if ref == "" {
		ref = strings.TrimSpace(req.SignalID) // 兼容别名：直传 signal_id 的调用方
	}
	if ref == "" {
		writeError(w, 400, "wire_ref（待核对单锚点 signal_id）必填")
		return
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision != "released" && decision != "settled" {
		writeError(w, 400, `decision 必须为 "released"（柜台无此单，删占位解锁）或 "settled"（柜台有此单，转正常终态）`)
		return
	}
	uid := userIDFor(r)
	ctrl := s.qmtCtrlFor(uid)
	if ctrl == nil {
		writeError(w, 503, "未接入 QMT 实盘：无法转发人工确认")
		return
	}
	res, err := ctrl.ConfirmGatewayOrder(trading.OrderConfirmRequest{
		SignalID: ref, Decision: decision,
		OrderID: strings.TrimSpace(req.OrderID), Status: strings.TrimSpace(req.Status),
	})
	if err != nil {
		// 传输/协议失败：改判**没有发生**，审计行如实记 fail，响应 502（可稍后重试）。
		opslog.Audit("qmt_order_confirm", uid, ref, "fail transport: "+truncate1line(err.Error()))
		writeError(w, 502, "网关人工确认调用失败: "+err.Error())
		return
	}
	// 审计先行：特权人工改判无论成/败都要留行（事件=谁把哪条待核对单判成了什么结论）。
	opslog.Audit("qmt_order_confirm", uid, ref, confirmAuditResult(decision, res))
	if !res.OK {
		// 网关业务拒绝：4xx 语义域内如实映射，其余（意外的 200-with-ok:false 或 5xx）
		// 统一 409——重试与否交给人，不假装成功。
		// §0925EVE-W3-G 特判：网关自己的 401/403（token 漂移）一律映射 502——本 API 的
		// 401/403 保留给首尔侧会话语义，否则前端会把「网关不认我们的 token」误判成
		// 「管理员登录过期」而把操作员踢下线。
		code := res.HTTPStatus
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			code = http.StatusBadGateway
		} else if code < 400 || code > 499 {
			code = http.StatusConflict
		}
		writeError(w, code, "网关拒绝人工确认: "+res.Err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"wire_ref": ref,
		"decision": decision,
		"released": res.Released, // released 分支：占位是否已删除解锁
		"status":   res.Status,   // settled 分支：改写后的委托终态
	})
}

// confirmAuditResult 把网关确认结论压成审计 result 单行串（避免换行污染日志格式）。
// English: flattens the gateway verdict into a single-line audit result.
func confirmAuditResult(decision string, res *trading.OrderConfirmResult) string {
	if !res.OK {
		return "decision=" + decision + " rejected http=" + httpStatusStr(res.HTTPStatus) + " err=" + truncate1line(res.Err)
	}
	return "decision=" + decision + " ok released=" + boolStr(res.Released) + " status=" + res.Status
}

// truncate1line 折叠空白并截断（审计行为单行格式，error 里可能带响应体换行）。
// 注意：trading.truncate 不导出，本包内联一份同语义实现。
func truncate1line(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const keep = 180
	if len(s) <= keep {
		return s
	}
	return s[:keep] + "..."
}

// httpStatusStr / boolStr 审计串化的极简格式化（错误/状态码原样入审计行）。
// （tiny stringifiers for audit lines.）
func httpStatusStr(n int) string { return strconv.Itoa(n) }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
