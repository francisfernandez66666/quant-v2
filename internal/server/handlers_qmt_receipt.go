// Package server — §4.2 人工回执（2026-11-04，外脑化 B1 第二条腿）。
// handlers_qmt_receipt.go：POST /api/qmt/order-receipt 的处理。
//
// 背景（一句话）：券商 11-05 起关闭 QMT 内置策略的本地文件读写与任何外部网络
// （AGENTS「通道前提变更」），柜台成交/撤单不再自动回报；owner 在券商 App 手工下的单、
// 以及机器侧策略已发但没有回执的单，靠这条路由把"成交/部成/废单/已撤/未成交"人肉回填，
// 量仔据此推进委托与成交两本账（docs/IMPL_QMT_INTERNAL_LOOP_20261103.md §4.2）。
//
// 三条不许被"顺手优化"掉的边界（§4.2 原文，逐条有测试）：
//  1. **账号级 authMiddleware，刻意不落严格位**（与 manual-positions 同取向）：
//     这是成员回填自己账号的回执，不是实盘动作；挂 quant_trade/trading_admin 才能录，
//     等于把"桥断后唯一能把账收口的通道"锁进实盘权限面。归属严格按 requestUserID，
//     别人的 signal_id 一律拒（403），查无此单如实 404——绝不静默 200（§FIX-20261028-F01
//     读面纪律的反面：写面同样按请求者）。
//  2. **幂等**：signal_id 是唯一锚点。同键第二次回执一律 409（抄网关 §CLAIMRELEASE 的
//     「待核对」取向：宁可拒第二次，也不重复落账）；409 之后只允许"差异字段"里唯一
//     结构安全的那一腿——委托状态，且必须过 store 的单调秩守卫（只升不降），
//     成交行/数量在任何重放下都不再写第二次。
//  3. **金额红线**：回执**不碰账户资金两列**（总资产/可用资金归柜台回报所有，11-05 后
//     由 §2.12 手动账人工维护）。本 handler 的写面只有三处：orders 状态推进（秩守卫）、
//     fills 成交行（经 ApplyRealFill 自身幂等：合成成交号 + 判重）、撤单留痕三列（只补空）。
//     这条同时有行为锁（回执前后账户资产逐值不变）与静态负锁（函数体内不得出现
//     账户资产写标识符）——见 handlers_qmt_receipt_test.go。
//
// 可辨识性（§7 ④"人工回执"读数要求）：经本路由推进的行，orders 行的撤单留痕来源列写
// `manual_receipt`、fills 行的券商成交号写 `manual_receipt:<signal_id>`（前缀不可能与
// 真实成交编号相撞）；不加任何新列（schemaVersion 由既有测试钉死）。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// manualReceiptSource 人工回执在委托行留痕来源列上的标记值（读面据此区分"人敲的"与"柜台报的"）。
const manualReceiptSource = "manual_receipt"

// orderReceiptStatusMap 回执状态词 → 本仓委托状态机的态名。
// not_placed 映射「发送失败」而非「废单」：它的语义是"根本没到券商"，
// 与 §GAP2-W1 同口径（该态允许同键重放，秩守卫也只为 已报→发送失败 留了这条例外）；
// 废单是券商受理后又作废，语义与额度占用完全不同，混用会把没发生的事记成发生过。
var orderReceiptStatusMap = map[string]string{
	"filled":     "已成",
	"partial":    "部成",
	"rejected":   "废单",
	"cancelled":  "已撤",
	"not_placed": "发送失败",
}

// orderReceiptReq POST /api/qmt/order-receipt 的请求体（§4.2 形状；signal_id 必填且是幂等键）。
type orderReceiptReq struct {
	SignalID string  `json:"signal_id"`
	Status   string  `json:"status"`
	Qty      int     `json:"qty"`
	Price    float64 `json:"price"`
	Note     string  `json:"note"`
	// UserID 收下但永不采信（§安全 T1 同口径）：归属只由登录态决定。
	// 不报错也不采信，只在两者不一致时留一行日志——完全静默会让后来人以为这字段有用。
	UserID string `json:"user_id,omitempty"`
}

// handleQMTOrderReceipt POST /api/qmt/order-receipt（账号级 authMiddleware，不落严格位）。
func (s *Server) handleQMTOrderReceipt(w http.ResponseWriter, r *http.Request) {
	// 空身份直接拒（403，不是 500 也不是"落到运营账号"）。authMiddleware 正常不会放
	// 未登录请求进来；这一层是"中间件被摘掉时最后一道归属闸"。
	uid := requestUserID(r)
	if uid == "" {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	var req orderReceiptReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	// 拼错键名（singal_id / statuss）必须当场失败：静默丢幂等锚＝下一次同键回执变成"新单"。
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.UserID != "" && req.UserID != uid {
		log.Printf("[qmt-receipt] 请求体 user_id=%s 与登录账号 %s 不一致，按登录账号归属（§安全 T1：请求体自报身份一律不信）", req.UserID, uid)
	}
	sid := strings.TrimSpace(req.SignalID)
	if sid == "" {
		writeError(w, http.StatusBadRequest, "signal_id 必填（它是幂等与审计的唯一锚点，§G2 同口径）")
		return
	}
	target, ok := orderReceiptStatusMap[strings.TrimSpace(req.Status)]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("status 只认 filled/partial/rejected/cancelled/not_placed，收到 %q", req.Status))
		return
	}
	if req.Qty < 0 || req.Price < 0 {
		writeError(w, http.StatusBadRequest, "qty/price 不许为负")
		return
	}
	switch req.Status {
	case "filled", "partial":
		// 成交回执必须带量价：没有量价的"成交"只能推进状态不能落成交行，
		// 那种行会让 VWAP/已实现盈亏两条账对不上，宁可在门口拒。
		if req.Qty <= 0 || req.Price <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("status=%s 必须带 qty>0 与 price>0（成交回执缺量价无法落成交行）", req.Status))
			return
		}
	default:
		// 非成交类回执带上量价：忽略但不拒——幂等锚是状态，多余数字不该挡路。
	}

	// ── 归属：只动本账号的行；别人的行如实拒绝；查无此单如实 404 ──
	own, found, other, err := s.lookupReceiptOrder(db, uid, sid)
	if err != nil {
		// 账本读不出＝"归属未知"，503 而不是把读失败报成 404 查无（那会诱导调用方重试/重敲）。
		writeError(w, http.StatusServiceUnavailable, "read orders book failed: "+err.Error())
		return
	}
	if !found {
		if other {
			// 别的账号有同键单：拒。回执不暴露对方账号 ID（存在性已经够答复调用方了）。
			writeError(w, http.StatusForbidden, "该 signal_id 不属于当前账号（回执只能推进自己的委托行）")
			return
		}
		writeError(w, http.StatusNotFound, fmt.Sprintf("本账号当日账里没有 signal_id=%s 的委托行——回执不补插新单，查无就是查无", sid))
		return
	}

	// ── 幂等：同 signal_id 的第二次回执一律 409（网关 §CLAIMRELEASE「待核对」取向）──
	if s.receiptAlreadyApplied(db, uid, sid, own) {
		// 差异字段里唯一允许动的是委托状态，且必须过 store 的单调秩守卫（只升不降）；
		// 成交行、数量、留痕在重放下一个字都不写——"宁可拒第二次也不重复落账"。
		advScope := uid
		if own.UserID == "" {
			advScope = "" // 遗留全局行的作用域口径，见下方首次回执处的注释
		}
		advanced, err := db.AdvanceRealOrderStatus(advScope, sid, target)
		if err != nil {
			writeError(w, 500, "advance receipt status failed: "+err.Error())
			return
		}
		// 落痕 + 响应：重放必须**留下痕迹**（谁在何时重复回执了什么、状态有没有真的推进），
		// 但响应只回事实不回动作——409 而不是 200，调用方据此知道"这一笔没有再落账"，
		// 不会把同一条委托的回执数当成两笔（成交行/数量/留痕在重放下一个字都不写）。
		opslog.Audit("qmt_order_receipt_replay", uid, sid,
			fmt.Sprintf("dup status=%s cur=%s status_advanced=%v note=%s", target, own.Status, advanced, req.Note))
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"status":            "rejected_replay",
			"signal_id":         sid,
			"current_status":    own.Status,
			"received_status":   target,
			"status_advanced":   advanced,
			"fill_written":      false,
			"note_for_operator": "该 signal_id 已有回执在账；重放只允许单调状态补进，成交行不重复落账——数量纠错请走待核对勘误通道",
		})
		return
	}

	// ── 首次回执 ──
	// 推进作用域：遗留全局行（user_id=''）只有运营账号能走到这里（归属闸在上面），
	// 而 store 的作用域口径是"userID 为空时仅操作遗留全局行"——不换成 ''，
	// 按运营 ID 查这一行永远 miss，旧部署那本账的单子就永远收不了口。
	advScope := uid
	if own.UserID == "" {
		advScope = ""
	}
	// 先推进状态（秩守卫）：推不动的行绝不落成交行——"已成但没有单"或
	// "把已撤改回已报再记一笔成交"都比拒单贵得多。
	advanced, err := db.AdvanceRealOrderStatus(advScope, sid, target)
	if err != nil {
		writeError(w, 500, "advance receipt status failed: "+err.Error())
		return
	}
	if !advanced {
		// 本地态与回执态冲突（等秩/回退）：如实 409，一条都不写，绝不静默 200。
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"本地委托行状态为 %q，回执 %q(%q) 不构成合法推进——未写入任何数据，请先核对这单是不是已被别的通道推进过",
			own.Status, req.Status, target))
		return
	}

	fillWritten := false
	if req.Status == "filled" || req.Status == "partial" {
		// 成交行走 ApplyRealFill 本尊（与网关回报同一条腿）：持仓量价由它推进、
		// 判重由它的合成成交号兜底。它**不写账户资产两列**（资金红线由它的实现保证，
		// 加上本函数体的静态负锁与行为锁）。
		now := time.Now().Format("2006-01-02 15:04:05")
		f := store.RealFill{
			OrderID:  own.OrderID,
			Code:     own.Code,
			Side:     own.Side,
			Price:    req.Price,
			Qty:      req.Qty,
			Amount:   req.Price * float64(req.Qty),
			TradedAt: now,
			SignalID: own.SignalID,
			TradeID:  manualReceiptFillKey(sid),
			UserID:   uid,
		}
		if err := db.ApplyRealFill(f); err != nil {
			// 状态已推进而成交行没落：如实报错并留日志，不假装整单成功（半态比失败难查）。
			log.Printf("[qmt-receipt] 状态已推进但成交行落库失败 uid=%s sid=%s err=%v", uid, sid, err)
			writeError(w, 500, "fill row write failed after status advance: "+err.Error())
			return
		}
		fillWritten = true
	}

	// 人工回执标记：只补空、不覆盖（SetCancelTrace 的既有语义）——
	// 若这行的撤单留痕已被网关占住，标记退化为 fills 成交号前缀 + 审计行，不硬抢别人的留痕。
	trace := fmt.Sprintf("人工回执 status=%s qty=%d price=%.2f note=%s", req.Status, req.Qty, req.Price, req.Note)
	if _, err := db.SetCancelTrace(uid, own.OrderID, manualReceiptSource, trace); err != nil {
		// 标记写不进不影响账的正确性（状态与成交已落），但必须留痕：
		// "哪些回执行将来按来源区分不出来"全靠这行日志。
		log.Printf("[qmt-receipt] 人工回执标记写入失败 uid=%s order=%s err=%v", uid, own.OrderID, err)
	}

	opslog.Audit("qmt_order_receipt", uid, sid,
		fmt.Sprintf("before=%s after=%s qty=%d price=%.2f fill=%v note=%s", own.Status, target, req.Qty, req.Price, fillWritten, req.Note))
	writeJSON(w, 200, map[string]interface{}{
		"status":        "ok",
		"signal_id":     sid,
		"order_id":      own.OrderID,
		"before_status": own.Status,
		"after_status":  target,
		"fill_written":  fillWritten,
		"source":        manualReceiptSource,
	})
}

// lookupReceiptOrder 在本账号的委托账里按 signal_id 找回执目标行。
// 返回 (行, 本账号命中, 别的账号存在同键行)。
//
// 口径三条：
//  1. 严格本账号（o.UserID == uid）——RealOrdersForUser 会把遗留全局行（user_id=”）也带回来，
//     那类行按 §FIX-20261028-F01 归运营账号，普通成员对它的回执算越权（other=true → 403）；
//  2. 运营账号可以回执遗留全局行（uid == operator 时 ” 行视为自己的账）；
//  3. 本账号没有时再查全集判断"是不是别人的单"——403 与 404 必须分开：
//     把越权报成查无，调用方会换个账号重试；把查无报成越权，是撒谎。
func (s *Server) lookupReceiptOrder(db *store.DB, uid, sid string) (store.RealOrder, bool, bool, error) {
	ownRows, err := db.RealOrdersForUser(uid)
	if err != nil {
		return store.RealOrder{}, false, false, err
	}
	for _, o := range ownRows {
		if o.SignalID != sid {
			continue
		}
		if o.UserID == uid || (o.UserID == "" && uid == s.operatorID()) {
			return o, true, false, nil
		}
		// 遗留全局行且请求者不是运营账号：视同别人的行。
		return store.RealOrder{}, false, true, nil
	}
	all, err := db.RealOrders()
	if err != nil {
		return store.RealOrder{}, false, false, err
	}
	for _, o := range all {
		if o.SignalID == sid {
			return store.RealOrder{}, false, true, nil
		}
	}
	return store.RealOrder{}, false, false, nil
}

// receiptAlreadyApplied 本账号对该 signal_id 是否已经收过人工回执。
// 两个锚合判（任一成立即算已回执）：
//   - 委托行留痕来源列 == manual_receipt（SetCancelTrace 写入；被网关占住的行会漏这个锚）；
//   - 存在成交行的券商成交号 == 合成回执号（ApplyRealFill 的 trade_id 判重锚，
//     它兜住"留痕列被占、标记没落下"的那半，重放成交从此无处二次落账）。
func (s *Server) receiptAlreadyApplied(db *store.DB, uid, sid string, own store.RealOrder) bool {
	if own.CancelSource == manualReceiptSource {
		return true
	}
	key := manualReceiptFillKey(sid)
	fills, err := db.RealFills()
	if err != nil {
		// 读不出成交账＝"已回执与否"未知。这里选**按已回执处理**（409）：
		// 未知时放行第二次回执＝重复落账的方向；未知时拒绝只是让 owner 稍后再试，成本低一档。
		log.Printf("[qmt-receipt] 成交账读取失败，按已回执保守处理 uid=%s sid=%s err=%v", uid, sid, err)
		return true
	}
	for _, f := range fills {
		if f.TradeID == key && (f.UserID == uid || (f.UserID == "" && uid == s.operatorID())) {
			return true
		}
	}
	return false
}

// manualReceiptFillKey 人工回执成交行的合成券商号：前缀带来源标记，
// 真实成交编号是柜台短号，不会与 `manual_receipt:` 相撞（判重锚因此也是"是不是回执"的锚）。
func manualReceiptFillKey(sid string) string {
	return manualReceiptSource + ":" + sid
}
