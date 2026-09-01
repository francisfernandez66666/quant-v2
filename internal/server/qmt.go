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
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// knownStrategyInfo 实盘战法白名单中的单条战法元信息（供前端分组展示与切换）。
// kind 取值：form=内置形态战法，factor=因子战法（fac_*），pattern=形态自动发现战法（pat_*）。
type knownStrategyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// knownStrategyList 返回实盘战法白名单全集：内置四形态战法 + 已应用的因子/形态战法（fac_*/pat_*）。
// 因子/形态战法审批注入 applied_factors.json / applied_patterns.json 后即出现在实盘准入列表，
// 可独立开关并参与实盘量化交易（与模拟盘分池口径一致）。
// English: the full live-whitelist — the four built-in form strategies plus any approved
// factor/pattern rules (fac_*/pat_*) loaded from applied_*.json, each toggleable for live trading.
func (s *Server) knownStrategyList() []knownStrategyInfo {
	list := []knownStrategyInfo{
		{ID: "dragon", Name: "龙头战法 Dragon", Kind: "form"},
		{ID: "double_bump", Name: "双响炮 DoubleBump", Kind: "form"},
		{ID: "n_shape", Name: "N形超短 NShape", Kind: "form"},
		{ID: "dragon_return", Name: "龙回头(中线) DragonReturn", Kind: "form"},
	}
	if s.researchDir != "" {
		if es, err := research.ListAppliedFactorRules(s.researchDir); err == nil {
			for _, e := range es {
				if e.Enabled {
					list = append(list, knownStrategyInfo{ID: e.ID, Name: e.Name, Kind: "factor"})
				}
			}
		}
		if ps, err := research.ListAppliedPatternRules(s.researchDir); err == nil {
			for _, p := range ps {
				if p.Enabled {
					list = append(list, knownStrategyInfo{ID: p.ID, Name: p.Name, Kind: "pattern"})
				}
			}
		}
	}
	// 波动突破战法（因子战法）是系统内置的因子策略入口：即便战法库尚未审批出启用规则，
	// 也把它作为可选入口展示在实盘战法白名单，便于用户开启因子实盘；
	// 真实规则经研究审批注入 applied_factors.json 后会以各自 fac_*/pat_* ID 接管并自动出现。
	// 避免与已存在的因子/形态条目重复添加。
	hasFactorEntry := false
	for _, k := range list {
		if k.Kind == "factor" || k.Kind == "pattern" {
			hasFactorEntry = true
			break
		}
	}
	if !hasFactorEntry {
		list = append(list, knownStrategyInfo{ID: "factor", Name: "波动突破战法", Kind: "factor"})
	}
	return list
}

// knownStrategyIDSet 返回白名单战法 ID 集合，供保存时校验未知战法。
func (s *Server) knownStrategyIDSet() map[string]bool {
	set := map[string]bool{}
	for _, k := range s.knownStrategyList() {
		set[k.ID] = true
	}
	return set
}

// qmtReportMiddleware 认证网关回报（POST /api/qmt/report）：
// §GAP2-W1 收权修复（P0）：只接受 QMT 网关 Bearer token（qmt.token，配置在账号 QMT 配置里），
// 并解析为持有该 token 的账号（供 SSE 定向推送 / 熔断控制器使用）。
// 旧实现"优先接受任意合法用户 token"——而 /auth/temp 匿名即可领取 14 天有效 token，
// 等于公网任何人都能伪造 trade/positions 回报：空数组 positions 直接清空 real_positions 全表、
// 伪造成交可改写他人账本（资损级数据面）。现一律 401，仅网关 token 放行。
// English: §GAP2-W1 (P0) authz fix for POST /api/qmt/report: ONLY the QMT gateway Bearer token
// (qmt.token in an account's QMT config) is accepted, resolved to the owning account. The old
// "accept any valid user token first" behavior — combined with the anonymous /auth/temp endpoint —
// let anyone on the internet forge fills or wipe the whole real_positions table with an empty
// positions array. Everything else gets 401.
func (s *Server) qmtReportMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		token = strings.TrimSpace(token)
		if token == "" {
			writeError(w, 401, "missing authorization token")
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
// §GAP2-W1 比对方式改为 subtle.ConstantTimeCompare（常量时间），与 auth.go 的 token 校验同口径，
// 消除字节级时序侧信道；同时跳过未配置 token 的账号（空串配置不应匹配空请求头之外的任何值）。
// English: returns the account ID whose QMT config carries the given gateway token; "" when none.
// §GAP2-W1: comparison switched to subtle.ConstantTimeCompare (constant-time, same as auth.go),
// removing the byte-level timing side channel; accounts without a configured token are skipped.
func (s *Server) userForQMTToken(token string) string {
	if s.cfg == nil || token == "" {
		return ""
	}
	for _, u := range s.auth.ListUsers() {
		cfgToken := s.cfg.GetRulesFor(u.ID).QMT.Token
		if cfgToken == "" {
			continue // 未配置网关 token 的账号不参与比对（避免空值误配）
		}
		if subtle.ConstantTimeCompare([]byte(cfgToken), []byte(token)) == 1 {
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
	db := s.realDB()
	if db == nil {
		// §白板修复：此前 writeError(w, 200, …) 返回 HTTP 200 + {"error":…}——前端 request()
		// 只把非 2xx 当失败，成功路径把 {error} 塞给模板渲染 undefined 属性直接白屏。
		// 错误必须配错误状态码，让前端 try/catch 生效。
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	// §GAP1.10 按账号过滤（遗留全局行 user_id='' 对所有人可见，兼容存量部署）
	positions, err := db.RealPositionsForUser(userIDFor(r))
	if err != nil {
		writeError(w, 500, "read real positions: "+err.Error())
		return
	}
	// §联调修复：装配实时现价（CurPrice）供前端实盘持仓"现价/浮动盈亏"列展示。
	// 网关回报仅含 cost_price，实时价取自 fetcher 5s 快照（缺则按 TS 代码变换重试一次）。
	// 不影响持久化（real_positions 仍按网关为准 upsert），仅响应层补充展示字段。
	for i := range positions {
		if si := s.quoteDisplay(positions[i].TsCode); si != nil && si.Price > 0 {
			positions[i].CurPrice = si.Price
		}
	}
	ctrl := s.qmtCtrlFor(userIDFor(r))
	// §可用资金：广州实盘账户资产（可用/冻结/总值/市值），随持仓接口一并返回供前端展示。
	acc, _ := db.GetRealAccount(userIDFor(r))
	// §实盘账户兜底：网关未上报 account 事件（或上报异常）时 GetRealAccount 返回零值行，
	// 前端"可用资金 ¥0.00/总值 ¥0.00"失真。此时用持仓市值（实时价优先、缺则成本价）补总值/
	// 市值，可用资金未知则保持 0（无法凭空造现金，前端按 updated_at 区分展示）。
	// English: account fallback — when the gateway hasn't reported an account event, GetRealAccount
	// returns a zero row and the frontend shows ¥0.00. We backfill total_asset/market_value from the
	// positions (live price first, cost price as fallback); available cash stays 0 (unknowable here —
	// the frontend distinguishes by updated_at).
	if acc.UpdatedAt == "" && len(positions) > 0 {
		var mv float64
		for i := range positions {
			price := positions[i].CurPrice
			if price <= 0 {
				price = positions[i].CostPrice
			}
			mv += price * float64(positions[i].Qty)
		}
		acc.MarketValue = mv
		acc.TotalAsset = mv
	}
	writeJSON(w, 200, map[string]interface{}{
		"positions": positions,
		"account":   acc,
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
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
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
		if db := s.realDB(); db != nil {
			if p, err := db.RealPositionByCodeForUser(uid, normalizeTsCode(req.Code)); err == nil && p.Qty > 0 && qty > p.Qty {
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

// normalizeReportSide 将网关回报的 side 字段归一为账本内部标准串（"买入"/"卖出"）。
// §安全 T3（2026-08-29）：网关若回报 buy/BUY/买入（含首尾空格）等非精确串，ApplyRealFill 仅认
// "买入"/"卖出"，非精确串会全部走 else（卖）分支 → 持仓被静默清零。此处先归一，非预期值直接报错，
// 绝不默认走卖。
func normalizeReportSide(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "buy", "b", "买入", "买":
		return "买入", nil
	case "sell", "s", "卖出", "卖":
		return "卖出", nil
	}
	return "", fmt.Errorf("未知回报方向(side=%q)", raw)
}

// handleQMTReport 接收网关回报（POST /api/qmt/report，Bearer token 鉴权）。
// 事件类型：trade（成交）/ order（委托）/ positions（全量对账）/ disconnect（断线）。
// 落库 → SSE 推前端 → 断线触发熔断并告警。
// English: receives gateway reports (POST /api/qmt/report, Bearer auth). Event types: trade/order/
// positions/disconnect. Persists → SSE to frontend; disconnect trips the breaker and alerts.
func (s *Server) handleQMTReport(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	db := s.realDB()
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
		Reason    string               `json:"reason"` // §FIX-0921 柜台废单/拒单原因（网关尽力透传 status_msg）
		Positions []store.RealPosition `json:"positions"`
		Asset     map[string]float64   `json:"asset"` // §可用资金：账户资产（cash/frozen_cash/total_asset/market_value）
		At        string               `json:"at"`
		UserID    string               `json:"user_id"` // §GAP1.10 网关配置的归属账号
	}
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeError(w, 400, "invalid report body")
		return
	}
	ctrl := s.qmtCtrlFor(uid)
	if ctrl != nil {
		// 上行通道新鲜度：任何回报到达都刷新 last_report_at（互通健康展示用）
		ctrl.SetLastReport(ev.Type)
	}

	switch ev.Type {
	case "positions":
		// 全量对账（按用户 reconcile）：以本次快照为准写入该账号持仓，并移除该账号范围内已不在
		// 快照中的旧持仓。使用 ReconcilePositionsForUser 而非 UpsertRealPositions —— 后者是
		// 全表覆盖（空快照会 DELETE FROM real_positions 清全表），多账号下会误删他人持仓、造成
		// 跨账号持仓泄漏甚至误卖。按用户 reconcile 后，空快照只清空本账号（含遗留全局行），
		// 绝不波及任何其它账号的数据。
		// §安全 T1（2026-08-29）：归属账号一律以网关 token 解析出的 uid 为准，
		// 忽略 body 携带的 user_id——防止持有 A 账号网关 token 者借 ev.UserID 越权写入/清空任意账号持仓。
		// 此前"网关 user_id > token uid"的优先级是越权写面。
		owner := uid
		if n, err := db.ReconcilePositionsForUser(owner, ev.Positions); err != nil {
			writeError(w, 500, "reconcile positions: "+err.Error())
			return
		} else {
			log.Printf("[trading] 网关全量对账(用户=%s): %d 持仓", owner, n)
			// §DAILY_OPSLOG 每日至首次对账记一行（对账每分钟跑，全记会淹没核心记录）
			opslog.DayOnce("reconcile:"+owner, func() {
				opslog.Logf("quant", "首次持仓对账 用户=%s 持仓=%d", owner, n)
			})
		}
	case "order":
		// §安全 T3：委托方向同样归一（仅用于展示，但保持口径一致，避免"BUY"等串污染委托行）。
		orderSide, oErr := normalizeReportSide(ev.Side)
		if oErr != nil {
			writeError(w, 400, oErr.Error())
			return
		}
		if ev.OrderID != "" && ev.SignalID != "" {
			// §R4-4 委托状态推进：回报的 部成/已成/已撤/部撤/废单 必须写入本地行——
			// 旧实现 UpsertRealOrder 是 INSERT OR IGNORE（signal_id 冲突即忽略），状态回报被
			// 静默吞掉、本地永远停留"已报"，撤单闭环/对账全部失真。现走单调守卫的
			// AdvanceRealOrderStatus：秩高于本地才更新，乱序/重放/回退绝不覆盖真实进度。
			// 本地无此单时（网侧重放等）回落原 INSERT OR IGNORE 行为补插。
			advanced, err := db.AdvanceRealOrderStatus(uid, ev.SignalID, ev.Status)
			if err != nil {
				log.Printf("[trading] advance order status: %v", err)
			}
			if !advanced {
				if _, err := db.UpsertRealOrder(store.RealOrder{
					OrderID: ev.OrderID, SignalID: ev.SignalID, Code: ev.Code,
					Side: orderSide, Status: ev.Status, Price: ev.Price, Qty: ev.Qty,
					CreatedAt: ev.At,
					UserID:    uid, // §W2-10 委托行打归属账号
				}); err != nil {
					log.Printf("[trading] upsert order: %v", err)
				}
			} else if ev.Status != "已报" {
				log.Printf("[trading] 委托状态推进 %s: %s (order=%s%s)", ev.SignalID, ev.Status, ev.OrderID,
					func() string {
						if ev.Reason != "" {
							return " 拒因=" + ev.Reason
						}
						return ""
					}())
				// §DAILY_OPSLOG 状态推进是委托生命周期的核心节点（已成/已撤/废单…）
				opslog.Logf("quant", "委托状态推进 %s %s %s qty=%d status=%s order=%s%s",
					ev.SignalID, orderSide, ev.Code, ev.Qty, ev.Status, ev.OrderID,
					func() string {
						if ev.Reason != "" {
							return " 拒因=" + ev.Reason
						}
						return ""
					}())
			}
		}
	case "trade":
		// 成交回报应用到实盘账本（建仓/加仓加权/减仓/清仓）+ 写 fills
		// §安全 T3：先归一方向，非预期值直接报错，避免误走卖分支清零持仓。
		tradeSide, sErr := normalizeReportSide(ev.Side)
		if sErr != nil {
			writeError(w, 400, sErr.Error())
			return
		}
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: ev.OrderID, Code: ev.Code, Side: tradeSide, Price: ev.Price,
			Qty: ev.Qty, Amount: ev.Amount, TradedAt: ev.TradedAt, SignalID: ev.SignalID,
			UserID: uid, // §W2-10 成交流水打归属账号（幂等键冲突时整体回滚，持仓不重复累加）
		}); err != nil {
			writeError(w, 500, "apply fill: "+err.Error())
			return
		}
		log.Printf("[trading] 成交回报 %s %s qty=%d price=%.2f", ev.Side, ev.Code, ev.Qty, ev.Price)
		// §DAILY_OPSLOG 成交是每日核心记录的第一等事件（信号归因一并落档）
		opslog.Logf("quant", "成交 %s %s qty=%d price=%.2f 金额=%.2f signal=%s order=%s",
			tradeSide, ev.Code, ev.Qty, ev.Price, ev.Amount, ev.SignalID, ev.OrderID)
	case "disconnect":
		// 断线回报 → 熔断暂停下单并告警
		if ctrl != nil {
			// 直接置熔断（回报即事实，不等心跳超时）
			ctrl.SetTripped("网关断线回报（disconnect）")
		}
		log.Printf("[trading] 网关断线回报，实盘下单已熔断")
		opslog.Logf("quant", "网关断线回报，已熔断暂停全部下单")
	case "account":
		// 账户资产回报（可用资金等）：归属账号同 positions，一律以网关 token 解析出的 uid 为准（§安全 T1）。
		owner := uid
		if len(ev.Asset) == 0 {
			writeError(w, 400, "empty account asset")
			return
		}
		if err := db.UpsertRealAccount(store.RealAccount{
			UserID:        owner,
			AvailableCash: ev.Asset["cash"],
			FrozenCash:    ev.Asset["frozen_cash"],
			TotalAsset:    ev.Asset["total_asset"],
			MarketValue:   ev.Asset["market_value"],
			UpdatedAt:     time.Now().Format("2006-01-02 15:04:05"),
		}); err != nil {
			writeError(w, 500, "upsert account: "+err.Error())
			return
		}
		log.Printf("[trading] 账户资产上报(用户=%s): 可用=%.2f 冻结=%.2f 总值=%.2f 市值=%.2f",
			owner, ev.Asset["cash"], ev.Asset["frozen_cash"], ev.Asset["total_asset"], ev.Asset["market_value"])
		// §DAILY_OPSLOG 每日至首次资产上报=开盘快照（每分钟全记会淹没核心记录）
		opslog.DayOnce("asset:"+owner, func() {
			opslog.Logf("quant", "开盘资产快照 用户=%s 可用=%.2f 冻结=%.2f 总值=%.2f 市值=%.2f",
				owner, ev.Asset["cash"], ev.Asset["frozen_cash"], ev.Asset["total_asset"], ev.Asset["market_value"])
		})
	case "heartbeat":
		// §ROBUST 上行心跳：last_report_at 已在 switch 前统一刷新——它就是心跳的全部意义
		// （无交易时段证明 广州→首尔 回程连通），无任何账本副作用。
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

// handleGetQMTConfig 处理 GET /api/config/qmt：返回当前账号实盘配置。
// token 只回脱敏形态（§GAP2-W2 同口径），提交脱敏哨兵或空串时后端保持原值。
// English: GET /api/config/qmt returns the account's live-trading config with the token masked.
func (s *Server) handleGetQMTConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetQMTConfigFor(userIDFor(r))
	// 诊断日志：排查「开关刷新后变回关闭」——记录每次读取的真实账号与 enabled 值。
	log.Printf("[diag-qmt] GET /api/config/qmt user=%s operator=%s enabled=%v", userIDFor(r), s.operatorID(), cfg.Enabled)
	tokenMasked := ""
	if cfg.Token != "" {
		tokenMasked = maskSecret(cfg.Token)
	}
	writeJSON(w, 200, map[string]interface{}{
		"enabled":             cfg.Enabled,
		"mode":                cfg.Mode,
		"gateway_url":         cfg.GatewayURL,
		"token_masked":        tokenMasked,
		"price_type":          cfg.PriceType,
		"fixed_amount":        cfg.FixedAmount,
		"max_positions":       cfg.MaxPositions,
		"initial_capital":     cfg.InitialCapital,
		"strategies":          cfg.Strategies,
		"strategy_amounts":    cfg.StrategyAmounts,
		"daily_max_buys":      cfg.DailyMaxBuys,
		"daily_budget_amount": cfg.DailyBudgetAmount,
		"auto_sell":           cfg.AutoSell,
		"miss_heartbeat_sec":  cfg.MissHeartbeatSec,
		// §R4-1 kill-switch 与撤单闭环参数
		"halted":           cfg.Halted,
		"cancel_stale_sec": cfg.CancelStaleSec,
		"close_sweep_at":   cfg.CloseSweepAt,
		"known_strategies": s.knownStrategyList(),
	})
}

// setQMTConfigReq 局部更新请求：指针字段=「本次要改的」，nil=保持不变。
type setQMTConfigReq struct {
	Enabled           *bool               `json:"enabled"`
	Mode              *string             `json:"mode"`
	GatewayURL        *string             `json:"gateway_url"`
	Token             *string             `json:"token"`
	PriceType         *string             `json:"price_type"`
	FixedAmount       *float64            `json:"fixed_amount"`
	MaxPositions      *int                `json:"max_positions"`
	InitialCapital    *float64            `json:"initial_capital"`
	Strategies        *[]string           `json:"strategies"`
	StrategyAmounts   *map[string]float64 `json:"strategy_amounts"`
	DailyMaxBuys      *int                `json:"daily_max_buys"`
	DailyBudgetAmount *float64            `json:"daily_budget_amount"`
	AutoSell          *bool               `json:"auto_sell"`
	MissHeartbeatSec  *int                `json:"miss_heartbeat_sec"`
	// §R4-1 kill-switch 与撤单闭环参数
	Halted         *bool `json:"halted"`
	CancelStaleSec *int  `json:"cancel_stale_sec"`
	CloseSweepAt   *int  `json:"close_sweep_at"`
}

// handleSetQMTConfig 处理 POST /api/config/qmt：局部合并保存当前账号实盘配置并热加载生效。
// 校验：mode/price_type 枚举；gateway_url 走 §GAP2-W2 外呼校验；白名单过滤到已知战法
// （空数组=全部允许，与引擎语义一致）；数值参数做范围钳制。token 提交脱敏哨兵/空串则不变。
func (s *Server) handleSetQMTConfig(w http.ResponseWriter, r *http.Request) {
	var req setQMTConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	cfg := *(s.cfg.GetQMTConfigFor(userIDFor(r)))

	if req.Mode != nil {
		m := strings.TrimSpace(*req.Mode)
		if m != "manual" && m != "auto" {
			writeError(w, 400, "mode 仅允许 manual/auto")
			return
		}
		cfg.Mode = m
	}
	if req.PriceType != nil {
		p := strings.TrimSpace(*req.PriceType)
		if p != "market" && p != "limit" {
			writeError(w, 400, "price_type 仅允许 market/limit")
			return
		}
		cfg.PriceType = p
	}
	if req.GatewayURL != nil {
		u := strings.TrimSpace(*req.GatewayURL)
		if u != "" && u != cfg.GatewayURL {
			// 网关为内部可信端点（本机/局域网），用宽松校验（允许环回/私网），
			// 不能用公网外呼的 validatePublicURL（会拒绝 127.0.0.1/内网地址）。
			if err := validateGatewayURL(u); err != nil {
				writeError(w, 400, "gateway_url "+err.Error())
				return
			}
		}
		cfg.GatewayURL = u
	}
	if req.Token != nil && *req.Token != "" && !isMaskedSecret(*req.Token) {
		cfg.Token = strings.TrimSpace(*req.Token)
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.AutoSell != nil {
		cfg.AutoSell = *req.AutoSell
	}
	if req.FixedAmount != nil {
		if *req.FixedAmount < 0 {
			writeError(w, 400, "fixed_amount 不能为负")
			return
		}
		cfg.FixedAmount = *req.FixedAmount
	}
	if req.InitialCapital != nil {
		if *req.InitialCapital < 0 {
			writeError(w, 400, "initial_capital 不能为负")
			return
		}
		cfg.InitialCapital = *req.InitialCapital
	}
	if req.MaxPositions != nil {
		if *req.MaxPositions < 1 || *req.MaxPositions > 50 {
			writeError(w, 400, "max_positions 超出范围（1-50）")
			return
		}
		cfg.MaxPositions = *req.MaxPositions
	}
	if req.DailyMaxBuys != nil {
		if *req.DailyMaxBuys < 0 {
			writeError(w, 400, "daily_max_buys 不能为负")
			return
		}
		cfg.DailyMaxBuys = *req.DailyMaxBuys
	}
	if req.DailyBudgetAmount != nil {
		if *req.DailyBudgetAmount < 0 {
			writeError(w, 400, "daily_budget_amount 不能为负")
			return
		}
		cfg.DailyBudgetAmount = *req.DailyBudgetAmount
	}
	if req.MissHeartbeatSec != nil {
		if *req.MissHeartbeatSec < 30 || *req.MissHeartbeatSec > 3600 {
			writeError(w, 400, "miss_heartbeat_sec 超出范围（30-3600 秒）")
			return
		}
		cfg.MissHeartbeatSec = *req.MissHeartbeatSec
	}
	// §R4-1 kill-switch 与撤单闭环参数（范围校验：cancel_stale_sec -1/0/30-3600；close_sweep_at -1/0/1300-1500）
	if req.Halted != nil {
		cfg.Halted = *req.Halted
	}
	if req.CancelStaleSec != nil {
		v := *req.CancelStaleSec
		if v != -1 && v != 0 && (v < 30 || v > 3600) {
			writeError(w, 400, "cancel_stale_sec 仅允许 -1(关闭)/0(默认120)/30-3600")
			return
		}
		cfg.CancelStaleSec = v
	}
	if req.CloseSweepAt != nil {
		v := *req.CloseSweepAt
		if v != -1 && v != 0 && (v < 1300 || v > 1500) {
			writeError(w, 400, "close_sweep_at 仅允许 -1(关闭)/0(默认1452)/1300-1500（北京时 HHMM）")
			return
		}
		cfg.CloseSweepAt = v
	}
	if req.Strategies != nil {
		seen := map[string]bool{}
		out := make([]string, 0, len(*req.Strategies))
		knownSet := s.knownStrategyIDSet()
		for _, v := range *req.Strategies {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			if !knownSet[v] {
				writeError(w, 400, "未知战法: "+v)
				return
			}
			seen[v] = true
			out = append(out, v)
		}
		cfg.Strategies = out // 空数组 = 不设白名单（全部允许）
	}
	if req.StrategyAmounts != nil {
		out := map[string]float64{}
		knownSet := s.knownStrategyIDSet()
		for k, v := range *req.StrategyAmounts {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if !knownSet[k] {
				writeError(w, 400, "未知战法: "+k)
				return
			}
			if v < 0 || v > 1000000 {
				writeError(w, 400, "战法仓位大小超出范围（0-1000000）: "+k)
				return
			}
			if v > 0 { // 0/负数=清除该战法覆盖，回落全局 fixed_amount
				out[k] = v
			}
		}
		cfg.StrategyAmounts = out
	}

	s.cfg.SetQMTConfigFor(userIDFor(r), &cfg)
	// 诊断日志：记录每次保存的真实账号、目标 enabled 与落盘后回读值，确认是否真正写盘。
	saved := s.cfg.GetQMTConfigFor(userIDFor(r))
	log.Printf("[diag-qmt] POST /api/config/qmt user=%s operator=%s reqEnabled=%v savedEnabled=%v", userIDFor(r), s.operatorID(), cfg.Enabled, saved.Enabled)
	log.Printf("[trading] qmt 配置已更新: enabled=%v mode=%s price=%s max_pos=%d fixed=%.0f strategies=%v",
		cfg.Enabled, cfg.Mode, cfg.PriceType, cfg.MaxPositions, cfg.FixedAmount, cfg.Strategies)
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleQMTState 返回网关互通健康快照（GET /api/qmt/state）。
// 下行=首尔探测广州网关（时延/最近探测），上行=网关回报到首尔的新鲜度；含熔断详情。
// English: returns the connectivity snapshot (downlink probe latency/state, uplink report
// freshness, breaker details) for the dashboard system row and the quant page.
func (s *Server) handleQMTState(w http.ResponseWriter, r *http.Request) {
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		// 未接入实盘：保留旧字段形状，前端据此隐藏实盘区块。
		writeJSON(w, 200, map[string]interface{}{
			"enabled": false, "mode": "manual", "tripped": false, "gateway_url": "",
		})
		return
	}
	writeJSON(w, 200, ctrl.Snapshot())
}

// handleQMTHalt §R4-1 kill-switch 端点（POST /api/qmt/halt，admin 权限）：
//   - 请求体 {"halted": true}：置位人工紧急停止——立即拒绝一切新下单（auto/manual 双路径），
//     并同步撤销本地账本全部"已报"未成交委托（HaltAll），SSE 告警广播；
//   - {"halted": false}：解除停止，恢复正常下单（熔断仍按健康探测独立生效）。
//
// 持久化走 per-user QMT 配置（SetQMTConfigFor），跨重启保留。
// English: §R4-1 kill-switch endpoint (admin) — halted=true rejects every new order and cancels
// all unfilled tickets immediately (HaltAll) with an SSE alert; halted=false releases the stop.
// Persisted per-user, survives restarts.
func (s *Server) handleQMTHalt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Halted *bool `json:"halted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Halted == nil {
		writeError(w, 400, "invalid request body: 需要 {\"halted\": true|false}")
		return
	}
	uid := userIDFor(r)
	cfg := *(s.cfg.GetQMTConfigFor(uid))
	cfg.Halted = *req.Halted
	s.cfg.SetQMTConfigFor(uid, &cfg)
	ctrl := s.qmtCtrlFor(uid)
	cancelled := 0
	if ctrl != nil {
		// §QMT-PENDING kill-switch 属紧急停止：必须立即生效（不入开关队列）——
		// placeOrder 读 ctrl.cfg.Halted 拦截新单，若只靠引擎热同步（现为盘中队列消费）会推迟到
		// 交易时段，置位瞬间仍有新单流入，违背 fail-stop 语义。此处直接 UpdateConfig 立即同步。
		// English: kill-switch must take effect immediately (bypasses the switch queue) — placeOrder reads
		// ctrl.cfg.Halted; delaying via the session-queued hot-sync would let new orders flow in the
		// meantime, violating fail-stop semantics.
		ctrl.UpdateConfig(cfg)
		if *req.Halted {
			cancelled = ctrl.HaltAll()
		}
	}
	log.Printf("[trading] ⚠️ kill-switch %s (用户=%s): 同步撤销未成交委托 %d 笔",
		map[bool]string{true: "置位——紧急停止一切下单", false: "解除"}[*req.Halted], uid, cancelled)
	// §DAILY_OPSLOG kill-switch 属最高优先级留档事件
	opslog.Logf("quant", "kill-switch %s 用户=%s 撤销未成交委托=%d",
		map[bool]string{true: "置位(紧急停止)", false: "解除"}[*req.Halted], uid, cancelled)
	if s.sse != nil {
		s.sse.BroadcastTo(uid, map[string]interface{}{
			"type":      "qmt_halt",
			"halted":    *req.Halted,
			"cancelled": cancelled,
			"time":      time.Now().Format("15:04:05"),
		})
	}
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "halted": *req.Halted, "cancelled": cancelled})
}

// handleQMTCancel §R4-1 手动撤单端点（POST /api/qmt/cancel/{order_id}，admin 权限）：
// 撤销指定网关委托并把本地行推进为"已撤"；失败（已成交/已撤/网关不可达）如实返回 409/502。
// English: §R4-1 manual cancel endpoint (admin) — cancels one gateway order; failures surface
// honestly (409 filled/cancelled, 502 gateway unreachable).
func (s *Server) handleQMTCancel(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("order_id")
	if strings.TrimSpace(orderID) == "" {
		writeError(w, 400, "order_id required")
		return
	}
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		writeError(w, 503, "real book not available")
		return
	}
	if err := ctrl.CancelOrder(orderID); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not connected") || strings.Contains(msg, "circuit") {
			writeError(w, 502, "撤单失败: "+msg)
			return
		}
		writeError(w, 409, "撤单失败(已成交/已撤/不可撤): "+msg)
		return
	}
	log.Printf("[trading] 手动撤单成功 %s (用户=%s)", orderID, userIDFor(r))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// qmtStrategyOf 从 signal_id 解析战法归属：buy:<码>:<战法>:<日> → 战法名；其余（sell:/manual@）→ manual。
// 卖出盈亏按持仓当前的入场战法归因（重放状态维护），卖出自身 signal_id 里的类目是退出原因而非来源战法。
// English: derives the strategy tag from a buy signal_id; sells are attributed to the position's
// entry strategy tracked during replay (the sell key encodes exit class, not origin strategy).
func qmtStrategyOf(signalID string) string {
	if strings.HasPrefix(signalID, "buy:") {
		parts := strings.Split(signalID, ":")
		if len(parts) >= 3 && parts[2] != "" {
			return parts[2]
		}
	}
	return "manual"
}

// handleQMTTrades 处理 GET /api/qmt/trades：交易流水 + 整体盈亏 + 按战法归因统计。
// 盈亏口径：
//   - 已实现：按时间升序重放全部成交（加权成本法），卖出对 (卖价-加权成本)×数量 累计；
//   - 浮动：real_positions 的 市值-数量×成本（市值为最近一次网关对账快照）；
//   - 总盈亏 = 已实现 + 浮动；胜/亏按单笔卖出 pnl 正负计数。
//
// 飞轮数据面：by_strategy 即「research 出战法 → 信号 → 实盘结果」回流评估的输入源，
// research 侧可直接读同一 researchDB 的 fills/orders 表或消费本端点。
// English: GET /api/qmt/trades — fill ledger, overall PnL (realized via weighted-cost replay,
// unrealized from the live book) and per-strategy attribution feeding the research flywheel.
func (s *Server) handleQMTTrades(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	db := s.realDB()
	if db == nil {
		// §白板修复：同 handleRealPositions——200+{"error"} 会让前端把错误体当成功数据
		// 渲染（trades.summary 为 undefined → TypeError → 整页白屏）。改 503 走前端失败分支。
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	allFills, err := db.RealFills()
	if err != nil {
		writeError(w, 500, "read fills: "+err.Error())
		return
	}
	// 归属过滤：空 user_id = 遗留全局行，对所有人可见（§GAP1.10 口径）
	fills := make([]store.RealFill, 0, len(allFills))
	for _, f := range allFills {
		if f.UserID == "" || f.UserID == uid {
			fills = append(fills, f)
		}
	}
	sort.Slice(fills, func(i, j int) bool { return fills[i].TradedAt < fills[j].TradedAt })

	// posState 持仓累计状态：数量 + 成本 + 归属战法。
	type posState struct {
		qty      int
		cost     float64
		strategy string
	}
	stratState := map[string]*posState{}
	// stratStat 按战法汇总的成交统计（JSON 输出给前端）。
	type stratStat struct {
		Buys     float64 `json:"buys"`
		Sells    float64 `json:"sells"`
		Realized float64 `json:"realized_pnl"`
		Count    int     `json:"trade_count"`
	}
	byStrat := map[string]*stratStat{}
	statFor := func(k string) *stratStat {
		v := byStrat[k]
		if v == nil {
			v = &stratStat{}
			byStrat[k] = v
		}
		return v
	}

	realized := 0.0
	wins, losses := 0, 0
	for _, f := range fills {
		ps := stratState[f.Code]
		if ps == nil {
			ps = &posState{}
			stratState[f.Code] = ps
		}
		amt := f.Price * float64(f.Qty)
		switch f.Side {
		case "买入":
			newQty := ps.qty + f.Qty
			if newQty > 0 {
				ps.cost = (ps.cost*float64(ps.qty) + amt) / float64(newQty)
			}
			ps.qty = newQty
			ps.strategy = qmtStrategyOf(f.SignalID)
			buyStat := statFor(ps.strategy)
			buyStat.Buys += amt
			buyStat.Count++
		case "卖出":
			sellQty := f.Qty
			if sellQty > ps.qty {
				sellQty = ps.qty // 超卖钳制（与 ApplyRealFill 同口径）
			}
			pnl := (f.Price - ps.cost) * float64(sellQty)
			realized += pnl
			if pnl >= 0 {
				wins++
			} else {
				losses++
			}
			k := ps.strategy
			if k == "" {
				k = "manual"
			}
			sellStat := statFor(k)
			sellStat.Sells += amt
			sellStat.Realized += pnl
			sellStat.Count++
			ps.qty -= sellQty
		}
	}

	positions, err := db.RealPositionsForUser(uid)
	if err != nil {
		writeError(w, 500, "read positions: "+err.Error())
		return
	}
	unrealized := 0.0
	for _, p := range positions {
		unrealized += p.Amount - float64(p.Qty)*p.CostPrice
	}

	// 流水倒序输出最近 100 笔并附战法标签
	outFills := make([]map[string]interface{}, 0, len(fills))
	start := 0
	if len(fills) > 100 {
		start = len(fills) - 100
	}
	for i := len(fills) - 1; i >= start; i-- {
		f := fills[i]
		strat := stratState[f.Code]
		tag := "manual"
		if f.Side == "买入" {
			tag = qmtStrategyOf(f.SignalID)
		} else if strat != nil && strat.strategy != "" {
			tag = strat.strategy
		}
		outFills = append(outFills, map[string]interface{}{
			"order_id": f.OrderID, "code": f.Code, "side": f.Side, "price": f.Price,
			"qty": f.Qty, "amount": f.Amount, "traded_at": f.TradedAt,
			"signal_id": f.SignalID, "strategy": tag,
		})
	}

	stratList := make([]map[string]interface{}, 0, len(byStrat))
	names := make([]string, 0, len(byStrat))
	for k := range byStrat {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		st := byStrat[k]
		stratList = append(stratList, map[string]interface{}{
			"strategy":     k,
			"buys":         math.Round(st.Buys*100) / 100,
			"sells":        math.Round(st.Sells*100) / 100,
			"realized_pnl": math.Round(st.Realized*100) / 100,
			"trade_count":  st.Count,
		})
	}
	writeJSON(w, 200, map[string]interface{}{
		"summary": map[string]interface{}{
			"realized_pnl":   math.Round(realized*100) / 100,
			"unrealized_pnl": math.Round(unrealized*100) / 100,
			"total_pnl":      math.Round((realized+unrealized)*100) / 100,
			"trade_count":    len(fills),
			"wins":           wins,
			"losses":         losses,
		},
		"by_strategy": stratList,
		"fills":       outFills,
	})
}
