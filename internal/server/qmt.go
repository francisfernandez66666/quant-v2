// qmt.go — 实盘交易（AUTO_TRADING_PLAN M1）HTTP 端点。
// 持仓页实盘 tab：拉取真实持仓（real_positions）、持仓建议（advice）、执行 manual 下单；
// 网关回报接收（POST /api/qmt/report，token 鉴权）：成交/委托/持仓/断线 → 落库 → SSE → 告警；
// 网关状态查询（/api/qmt/state）。
// English: live-trading (AUTO_TRADING_PLAN M1) HTTP endpoints — the live tab pulls real positions and
// position advice, executes manual orders; POST /api/qmt/report receives gateway reports (fills/orders/
// positions/disconnect) → persist → SSE → alert; GET /api/qmt/state reads gateway status.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// qmtReportMiddleware 认证网关回报（POST /api/qmt/report）：
// 优先接受合法用户 token；否则接受 QMT 网关 Bearer token（qmt.token，配置在账号 QMT 配置里），
// 并解析为持有该 token 的账号（供 SSE 定向推送 / 熔断控制器使用）。
// English: authenticates gateway reports (POST /api/qmt/report). Accepts a valid user token first;
// otherwise accepts the QMT gateway Bearer token (qmt.token, stored in an account's QMT config) and
// resolves it to the owning account (for SSE routing / breaker controller).
func (s *Server) qmtReportMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		token = strings.TrimSpace(token)
		if token == "" {
			writeError(w, 401, "missing authorization token")
			return
		}
		if u := s.auth.ValidateToken(token); u != nil {
			next(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, u)))
			return
		}
		if uid := s.userForQMTToken(token); uid != "" {
			u := &auth.User{ID: uid}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, u)))
			return
		}
		writeError(w, 401, "invalid or expired token")
	}
}

// userForQMTToken 返回持有给定 QMT 网关 token 的账号 ID（遍历账号的 QMT 配置）；无匹配返回空串。
// English: returns the account ID whose QMT config carries the given gateway token; "" when none.
func (s *Server) userForQMTToken(token string) string {
	if s.cfg == nil || token == "" {
		return ""
	}
	for _, u := range s.auth.ListUsers() {
		if s.cfg.GetRulesFor(u.ID).QMT.Token == token {
			return u.ID
		}
	}
	return ""
}

// userIDFor 返回当前请求账号 ID（鉴权中间件未置用户时返回空串，网关回报等直连场景安全）。
// English: userIDFor returns the account ID for the request ("" when the auth middleware hasn't set a
// user — safe for direct gateway-report calls in tests).
func userIDFor(r *http.Request) string {
	if u := userFromContext(r); u != nil {
		return u.ID
	}
	return ""
}

// qmtCtrlFor 返回指定账号的 QMT 执行控制器（可空=未接入实盘）。
// English: returns an account's QMT controller (nil when the live chain isn't wired).
func (s *Server) qmtCtrlFor(userID string) *trading.Controller {
	c := s.ctrlFor(userID)
	if c == nil {
		return nil
	}
	return c.QMTController()
}

// handleRealPositions 返回实盘持仓（real_positions，含建议徽标由前端按 advice 叠加）。
// GET /api/positions/real
func (s *Server) handleRealPositions(w http.ResponseWriter, r *http.Request) {
	db := s.researchDB
	if db == nil {
		writeError(w, 200, "real book not available")
		return
	}
	positions, err := db.RealPositions()
	if err != nil {
		writeError(w, 500, "read real positions: "+err.Error())
		return
	}
	ctrl := s.qmtCtrlFor(userIDFor(r))
	writeJSON(w, 200, map[string]interface{}{
		"positions": positions,
		"enabled":   ctrl != nil && ctrl.Enabled(),
		"tripped":   ctrl != nil && ctrl.Tripped(),
		"mode":      ctrlMode(ctrl),
	})
}

// ctrlMode 返回控制器执行模式（manual/auto，nil → manual）。
// （ctrlMode returns the controller execution mode; nil → manual.）
func ctrlMode(c *trading.Controller) string {
	if c == nil {
		return "manual"
	}
	return c.Mode()
}

// handleRealAdvice 返回实盘持仓处理建议（实时计算，供持仓页实盘 tab 展示）。
// GET /api/positions/advice
func (s *Server) handleRealAdvice(w http.ResponseWriter, r *http.Request) {
	c := s.ctrlFor(userIDFor(r))
	if c == nil {
		writeError(w, 200, "engine not available")
		return
	}
	db := s.researchDB
	if db == nil {
		writeError(w, 200, "real book not available")
		return
	}
	positions, err := db.RealPositions()
	if err != nil {
		writeError(w, 500, "read real positions: "+err.Error())
		return
	}
	if len(positions) == 0 {
		writeJSON(w, 200, map[string]interface{}{"advices": []trading.PositionAdvice{}, "tripped": ctrlTripped(s, userIDFor(r))})
		return
	}
	// 复用引擎分析能力：通过引擎暴露的行情/分数上下文不可得（分析在 5s 循环内实时推送），
	// HTTP 端点仅返回最近一轮 SSE 已广播的建议（由前端 SSE 持续更新），此处返回空表由前端展示 SSE 数据。
	// English: the advice is computed live inside the 5s scoring loop and pushed via SSE; this endpoint
	// only acknowledges the source of truth (SSE). An empty list here is fine — the frontend consumes the
	// SSE stream.
	writeJSON(w, 200, map[string]interface{}{"advices": []trading.PositionAdvice{}, "tripped": ctrlTripped(s, userIDFor(r))})
}

// ctrlTripped 返回某账号 QMT 控制器熔断状态。
// English: ctrlTripped reports an account's QMT circuit-breaker state.
func ctrlTripped(s *Server, userID string) bool {
	c := s.qmtCtrlFor(userID)
	return c != nil && c.Tripped()
}

// handleExecuteAction 执行 manual 下单（POST /api/positions/execute）。
// 请求体：{code, side(买入/卖出), action(加仓/减仓/止盈/止损/清仓), qty, price, strategy, reason}
// 熔断中/未启用 → 拒绝；写入 orders 表（signal_id 幂等）。
// English: manual order execution (POST /api/positions/execute). Rejects while tripped/disabled; persists
// to the orders table (signal_id idempotency).
func (s *Server) handleExecuteAction(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	var req struct {
		Code     string  `json:"code"`
		Side     string  `json:"side"`     // 买入/卖出
		Action   string  `json:"action"`   // 加仓/减仓/止盈/止损/清仓
		Qty      int     `json:"qty"`      // 手数→股数由前端换算（或直接股数）
		Price    float64 `json:"price"`    // 参考价
		Strategy string  `json:"strategy"` // 战法（白名单过滤用）
		Reason   string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	ctrl := s.qmtCtrlFor(uid)
	if ctrl == nil || !ctrl.Enabled() {
		writeError(w, 400, "qmt not enabled")
		return
	}
	side := req.Side
	if side == "" {
		side = trading.SideBuy
	}
	if req.Code == "" || req.Qty <= 0 || req.Price <= 0 {
		writeError(w, 400, "code/qty/price required")
		return
	}
	qty := req.Qty
	if qty < 100 {
		qty = 100 // 不足一手按一手（100 股）
	}
	// 卖出侧校验：减仓数量不得超过当前持仓
	if side == trading.SideSell {
		if db := s.researchDB; db != nil {
			if p, err := db.RealPositionByCode(normalizeTsCode(req.Code)); err == nil && p.Qty > 0 && qty > p.Qty {
				writeError(w, 400, "sell qty exceeds holding")
				return
			}
		}
	}
	signalID := "manual@" + req.Code + "@" + time.Now().Format("20060102150405")
	res, err := ctrl.PlaceOrder(trading.OrderRequest{
		SignalID:  signalID,
		Code:      normalizeTsCode(req.Code),
		Name:      s.stockName(req.Code),
		Strategy:  req.Strategy,
		Side:      side,
		PriceType: ctrl.Config().PriceType,
		Price:     req.Price,
		Qty:       qty,
		Amount:    float64(qty) * req.Price,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		writeError(w, 400, "order rejected: "+err.Error())
		return
	}
	log.Printf("[trading] manual %s %s(%s) qty=%d price=%.2f → %+v", side, req.Code, s.stockName(req.Code), qty, req.Price, res)
	// SSE 通知前端
	if s.sse != nil {
		s.sse.BroadcastTo(uid, map[string]interface{}{
			"type":     "real_order",
			"code":     req.Code,
			"side":     side,
			"order_id": res.OrderID,
			"ok":       res.OK,
			"time":     time.Now().Format("15:04:05"),
		})
	}
	writeJSON(w, 200, res)
}

// normalizeTsCode 把前端传入的股票代码补成带后缀形式（600000 → 600000.SH）。
// English: normalizeTsCode appends the exchange suffix to a bare code.
func normalizeTsCode(code string) string {
	if code == "" {
		return code
	}
	code = strings.TrimSpace(code)
	upper := strings.ToUpper(code)
	if strings.HasSuffix(upper, ".SH") || strings.HasSuffix(upper, ".SZ") || strings.HasSuffix(upper, ".BJ") {
		return upper
	}
	switch {
	case upper[0] == '6', upper[0] == '9':
		return upper + ".SH"
	case upper[0] == '4', upper[0] == '8':
		return upper + ".BJ"
	default:
		return upper + ".SZ"
	}
}

// handleQMTReport 接收网关回报（POST /api/qmt/report，Bearer token 鉴权）。
// 事件类型：trade（成交）/ order（委托）/ positions（全量对账）/ disconnect（断线）。
// 落库 → SSE 推前端 → 断线触发熔断并告警。
// English: receives gateway reports (POST /api/qmt/report, Bearer auth). Event types: trade/order/
// positions/disconnect. Persists → SSE to frontend; disconnect trips the breaker and alerts.
func (s *Server) handleQMTReport(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	db := s.researchDB
	if db == nil {
		writeError(w, 500, "real book not available")
		return
	}
	var ev struct {
		Type      string               `json:"type"`
		OrderID   string               `json:"order_id"`
		Code      string               `json:"code"`
		Side      string               `json:"side"`
		Status    string               `json:"status"`
		Price     float64              `json:"price"`
		Qty       int                  `json:"qty"`
		Amount    float64              `json:"amount"`
		TradedAt  string               `json:"traded_at"`
		SignalID  string               `json:"signal_id"`
		Positions []store.RealPosition `json:"positions"`
		At        string               `json:"at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeError(w, 400, "invalid report body")
		return
	}
	ctrl := s.qmtCtrlFor(uid)

	switch ev.Type {
	case "positions":
		// 全量对账：upsert 覆盖 + 移除已不在集合内的持仓
		if n, err := db.UpsertRealPositions(ev.Positions); err != nil {
			writeError(w, 500, "reconcile positions: "+err.Error())
			return
		} else {
			log.Printf("[trading] 网关全量对账: %d 持仓", n)
		}
	case "order":
		if ev.OrderID != "" && ev.SignalID != "" {
			if _, err := db.UpsertRealOrder(store.RealOrder{
				OrderID: ev.OrderID, SignalID: ev.SignalID, Code: ev.Code,
				Side: ev.Side, Status: ev.Status, Price: ev.Price, Qty: ev.Qty,
				CreatedAt: ev.At,
			}); err != nil {
				log.Printf("[trading] upsert order: %v", err)
			}
		}
	case "trade":
		// 成交回报应用到实盘账本（建仓/加仓加权/减仓/清仓）+ 写 fills
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: ev.OrderID, Code: ev.Code, Side: ev.Side, Price: ev.Price,
			Qty: ev.Qty, Amount: ev.Amount, TradedAt: ev.TradedAt, SignalID: ev.SignalID,
		}); err != nil {
			writeError(w, 500, "apply fill: "+err.Error())
			return
		}
		log.Printf("[trading] 成交回报 %s %s qty=%d price=%.2f", ev.Side, ev.Code, ev.Qty, ev.Price)
	case "disconnect":
		// 断线回报 → 熔断暂停下单并告警
		if ctrl != nil {
			// 直接置熔断（回报即事实，不等心跳超时）
			ctrl.SetTripped("网关断线回报（disconnect）")
		}
		log.Printf("[trading] 网关断线回报，实盘下单已熔断")
	default:
		writeError(w, 400, "unknown report type")
		return
	}

	// SSE 推前端
	if s.sse != nil {
		s.sse.BroadcastTo(uid, map[string]interface{}{
			"type":  "qmt_report",
			"event": ev.Type,
			"code":  ev.Code,
			"side":  ev.Side,
			"price": ev.Price,
			"qty":   ev.Qty,
			"time":  time.Now().Format("15:04:05"),
		})
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleQMTState 返回网关连接/熔断/持仓状态（GET /api/qmt/state）。
// English: returns gateway connectivity / breaker / holdings (GET /api/qmt/state).
func (s *Server) handleQMTState(w http.ResponseWriter, r *http.Request) {
	ctrl := s.qmtCtrlFor(userIDFor(r))
	writeJSON(w, 200, map[string]interface{}{
		"enabled": ctrl != nil && ctrl.Enabled(),
		"mode":    ctrlMode(ctrl),
		"tripped": ctrl != nil && ctrl.Tripped(),
		"gateway_url": func() string {
			if ctrl == nil {
				return ""
			}
			return ctrl.Config().GatewayURL
		}(),
	})
}
