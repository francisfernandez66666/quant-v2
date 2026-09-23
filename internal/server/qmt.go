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
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// knownStrategyInfo 实盘战法白名单中的单条战法元信息（供前端分组展示与切换）。
// kind 取值：form=内置形态战法，factor=因子战法（fac_*），pattern=形态自动发现战法（pat_*）。
type knownStrategyInfo struct {
	ID   string `json:"id"`   // 战法 ID（如 dragon/fac_1/pat_2）
	Name string `json:"name"` // 显示名
	Kind string `json:"kind"` // form/factor/pattern（内置形态/因子/形态自动发现）
}

// knownStrategyList 返回实盘战法白名单全集：内置四形态战法 + 动量战法 + 已应用的因子/形态战法（fac_*/pat_*）。
// 因子/形态战法审批注入 applied_factors.json / applied_patterns.json 后即出现在实盘准入列表，
// 可独立开关并参与实盘量化交易（与模拟盘分池口径一致）。
// §20260917：动量战法显式入列——此前它不在白名单全集里、没有用户开关，却在动量分达阈值时
// 自动产 buy 信号并被"空白名单=全部允许"放行实盘下单（误交易根因）。现入列受开关管控，
// 且信号端 rules.strategy.momentum.enabled 默认关，双重严格 opt-in。
// English: the full live-whitelist — four built-in form strategies plus momentum and any approved
// factor/pattern rules (fac_*/pat_*), each toggleable for live trading. §20260917: momentum is now
// explicitly listed — previously it had no toggle yet could auto-fill through the empty-whitelist
// "allow all" default (the mis-trade root cause).
func (s *Server) knownStrategyList() []knownStrategyInfo {
	list := []knownStrategyInfo{
		// 内置形态战法五件套：kind 统一为 form（§20260917 动量战法显式入列受开关管控）。
		{ID: "dragon", Name: "龙头战法 Dragon", Kind: "form"},
		{ID: "double_bump", Name: "双响炮 DoubleBump", Kind: "form"},
		{ID: "n_shape", Name: "N形超短 NShape", Kind: "form"},
		{ID: "dragon_return", Name: "龙回头(中线) DragonReturn", Kind: "form"},
		{ID: "momentum", Name: "动量战法 Momentum", Kind: "form"},
	}
	// 追加战法库已启用的规则进白名单（因子 fac_ 与形态 pat_）。
	// researchDir 为空（研究库未配置）时跳过，白名单退化为仅内置战法。
	if s.researchDir != "" {
		// 因子规则：只透出 Enabled=true 的（停用规则不允许实盘准入）。
		if es, err := research.ListAppliedFactorRules(s.researchDir); err == nil {
			for _, e := range es {
				if e.Enabled {
					list = append(list, knownStrategyInfo{ID: e.ID, Name: e.Name, Kind: "factor"})
				}
			}
		}
		// 形态规则同样按启用状态透出。
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
			hasFactorEntry = true // 已有因子/形态条目则无需再放"波动突破"入口
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
		// 仅允许携带「网关 token」的请求（回报/对账），普通用户 token 一律拒绝。
		// 命中时把解析出的账号写入请求上下文（ctxUserKey），后续 handler 用 userIDFor 取用，
		// 等价于普通用户鉴权中间件的效果，但身份来源是网关 token 而非会话。
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
		return "" // 配置层未接入/请求未带 token：直接无匹配
	}
	// 遍历所有账号：取每个账号 rules.qmt.token 与请求 token 常量时间比对。
	// 账号量级有限（<百），线性扫描可接受；命中即返回（token 与账号一一对应）。
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
// §2026-09-07 多账号实盘：走 liveCtrlFor 按调用方账号自身路由——每个账号读到自己引擎
// 的控制器（其 gateway/token 由该账号 QMT 配置决定），运营账号行为与旧路径一致。
// English: returns an account's QMT controller (nil when the live chain isn't wired) — routed to the
// caller's own engine so each account sees its own gateway/capital.
func (s *Server) qmtCtrlFor(userID string) *trading.Controller {
	c := s.liveCtrlFor(userID)
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
	// §F2 名称兜底：网关对账回报常不带 name（real_positions.name 为空）——用行情快照的
	// Name 补全展示名，避免前端实盘持仓只显示代码不显示名称。
	// English: §F2 name backfill — gateway reconciliation omits name; fill it from the quote
	// snapshot so the real-positions table shows stock names, response-layer only (no persist).
	for i := range positions {
		// 名称兜底优先级：DB 已有名称 → stockName（基础信息表）→ quoteDisplay（行情快照）。
		if positions[i].Name == "" {
			if n := s.stockName(positions[i].TsCode); n != "" {
				positions[i].Name = n
			} else if si := s.quoteDisplay(positions[i].TsCode); si != nil && si.Name != "" {
				positions[i].Name = si.Name
			}
		}
		// 现价兜底：行情快照有价才覆盖（快照缺失时保留 DB 侧 CurPrice，前端至少不显示 0）。
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
		// 兜底估值循环：逐只累加"现价×数量"（现价缺失回退成本价），得到近似市值/总值。
		// 可用资金无法推算（本地无从知道冻结现金），保持 0 由前端按 updated_at 区分展示。
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
		"positions": positions,                     // 持仓列表（已补名称/现价）
		"account":   acc,                           // 账户资产快照（可能经市值兜底回填）
		"enabled":   ctrl != nil && ctrl.Enabled(), // 实盘链路是否启用
		"tripped":   ctrl != nil && ctrl.Tripped(), // 熔断是否触发
		"mode":      ctrlMode(ctrl),                // 执行模式 manual/auto
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
// §2026-09-07 多账号实盘：引擎可用性按调用方账号自身路由（liveCtrlFor），子账号只读自己的实盘建议。
// English: handles GET /api/positions/advice — engine availability is routed to the CALLER's own account.
func (s *Server) handleRealAdvice(w http.ResponseWriter, r *http.Request) {
	c := s.liveCtrlFor(userIDFor(r))
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
	// §F-6（20260917 缺陷修复批）REST 回填：返回 SSE 中心留存的最近一轮 real_advice 广播
	// （断线超出补发缓冲或页面重载后不再丢建议；旧实现恒返空表只能等下一次 5s 广播）。
	// 新鲜度：建议按 5s 循环滚动，超过 10 分钟视为过期回空表（休市陈旧快照不当作现值建议）。
	// English: §F-6 — return the broker's last real_advice broadcast snapshot (reconnect/reload
	// backfill); payloads older than 10 minutes are treated as stale and reported empty.
	// 依次尝试调用方账号 → 运营账号两个缓存键（多账号实盘下子账号可能共享运营账号的引擎轮次）。
	uid := userIDFor(r)
	for _, key := range []string{uid, s.operatorID()} {
		if key == "" {
			continue // 空键跳过（未登录/未配置运营账号）
		}
		// LastRealAdvice 返回最近一次广播的原始 JSON 与时间戳；未广播过（ok=false）或
		// 已过期（>10min）都继续尝试下一个键。
		raw, at, ok := s.sse.LastRealAdvice(key)
		if !ok || time.Since(at) > 10*time.Minute {
			continue
		}
		// 解包广播 payload 中真正给前端的 advices 数组；解包失败视为缓存损坏，继续尝试。
		var payload struct {
			Advices []trading.PositionAdvice `json:"advices"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}
		// 命中：返回缓存建议 + 缓存时间（cached_at 供前端展示新鲜度）。
		writeJSON(w, 200, map[string]interface{}{
			"advices":   payload.Advices,
			"tripped":   ctrlTripped(s, uid),
			"cached_at": at.Format(time.RFC3339),
		})
		return
	}
	// 从未广播过（引擎未跑/无建议轮次）：维持空表形状。
	writeJSON(w, 200, map[string]interface{}{"advices": []trading.PositionAdvice{}, "tripped": ctrlTripped(s, uid)})
}

// ctrlTripped 返回某账号 QMT 控制器熔断状态。
// English: ctrlTripped reports an account's QMT circuit-breaker state.
func ctrlTripped(s *Server, userID string) bool {
	c := s.qmtCtrlFor(userID)
	return c != nil && c.Tripped()
}

// regClientID §P1-7 客户端幂等键格式：UUID/字母数字-下划线，≤64 字符（入 signal_id 前校验，
// 拒绝把任意用户串拼进幂等键）。
// English: §P1-7 client idempotency key pattern (alnum + dash/underscore, ≤64 chars).
var regClientID = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)

// handleExecuteAction 执行 manual 下单（POST /api/positions/execute）。
// 请求体：{code, side(必填，只接受 买入/卖出——§SIDE-AUTH-2 起空串也拒), action(加仓/减仓/止盈/止损/清仓),
// qty, price, strategy, reason}
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
		// §P1-7（2026-09-15）客户端幂等键：旧实现 signalID=manual@code@秒级时间戳，跨秒双击/重试
		// 生成两个不同键 → 两笔真实订单。前端对一次「确认」生成一次 UUID 并随重试复用，
		// 服务端按其幂等（signal_id 唯一键天然拦截）。缺省时回退旧键（兼容旧客户端）。
		ClientID string `json:"client_id"`
		// §P1-7 限价偏离确认：参考价偏离实时价超 ±15% 时必须显式确认才受理（防手滑输错价
		// 真金白银成交）。行情不可用时跳过校验（fail-open，与风控闸哲学一致）。
		ConfirmDeviation bool `json:"confirm_deviation"`
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
	// §SIDEGATE-GO（2026-09-22 修复批，M-1/N-4 Go 层收口）方向白名单：只接受 买入/卖出 两值。
	// 缺陷原文：旧实现 `side := req.Side; if side == "" { side = trading.SideBuy }` 只把**空串**
	// 缺省成买入，其余任意字符串（"buy"/"SELL"/"卖出 "带空格/"买"）都原样透传到控制器→网关→broker，
	// 而 broker 的语义是「side 不等于 '买入' 就下卖单」——用户在前端选了买入、请求体里方向被写成
	// "BUY"，实际落到柜台的是一张**卖单**（方向翻转），同时因 risk.Gate 各闸按精确串匹配而连带
	// 跳过 T+1/涨停/跌停三道方向闸（该族失效已在 §N-4 于闸内 fail-close 兜底）。
	// 为何在 HTTP 入口就拒：方向翻转属于资金安全级错误，必须在最外层以 400 明确告知调用方
	// 「你传的方向我们不认」，而不是猜一个默认值继续往下走。（§SIDE-AUTH-2 起空串同样拒，
	// 方向必填——下面紧邻的缺方向分支。）
	// English: §SIDEGATE-GO — hard whitelist at the manual-order HTTP entry. Anything other than the
	// two canonical sides is rejected with 400 (since §SIDE-AUTH-2 the empty string too — side is
	// mandatory), because downstream the broker treats "not 买入" as a SELL: a non-canonical side
	// would flip the direction and simultaneously skip every directional risk gate.
	// §SIDE-AUTH-2（2026-09-23 夜间批）残余 fail-open 清零：方向**必填**——空串不再缺省成买入。
	// 为什么：上一条注释里保留的"空串缺省买入"本身仍是 fail-open——调用方一旦漏传 side
	// （脚本/旧客户端/序列化丢字段），系统就替用户决定"买"，而这个决定花的是真钱；
	// 603468.SH 事故链证明方向错的连锁污染极深（回款 0→预算占满→纪律闸当日拒 4793 次），
	// 入口宁可多一次 400 也不替任何人猜方向。前端调用点已同步显式传方向（Positions.jsx
	// 手动下单本就按用户点击传 买入/卖出），vitest 锁"发出的请求必带方向"。
	// English: §SIDE-AUTH-2 — side is now mandatory; the old empty-string default of 买入 was
	// itself a fail-open (a dropped field silently became a real buy). Entry rejects with 400
	// instead of guessing, and the audit line keeps the rejected attempt forensible.
	if req.Side == "" {
		log.Printf("[security] 手动下单缺方向被拒（§SIDE-AUTH-2 必填）用户=%s code=%s", uid, req.Code)
		opslog.Audit("live_order_side_missing", uid, req.Code, "side 为空被拒：必须显式传 买入/卖出")
		writeError(w, 400, "缺少下单方向(side)：必须显式传 买入/卖出")
		return
	}
	side := req.Side
	if side != trading.SideBuy && side != trading.SideSell {
		log.Printf("[security] 手动下单方向非法被拒 用户=%s code=%s side=%q", uid, req.Code, req.Side)
		opslog.Audit("live_order_side_reject", uid, req.Code, fmt.Sprintf("side=%q 只接受 %s/%s", req.Side, trading.SideBuy, trading.SideSell))
		writeError(w, 400, fmt.Sprintf("非法下单方向(side=%q)：只接受 %s/%s", req.Side, trading.SideBuy, trading.SideSell))
		return
	}
	// 基础参数校验：代码/数量/价格三者缺一不可（价格是限价参考价，0 价无意义）。
	if req.Code == "" || req.Qty <= 0 || req.Price <= 0 {
		writeError(w, 400, "code/qty/price required")
		return
	}
	qty := req.Qty
	// §D4 修复：卖出侧 qty<100 静默放大到 100 会把「用户手滑输 50」变成「按 100 股下单」，
	// 持仓 80 股时卖 100 会失败（sell exceeds holding）；持仓 500 股时想清 50 结果清 100——
	// 语义与用户输入不一致。卖出直接 400 拒单，清残股请走「清仓」动作；买入侧凑整保留。
	// English: silent round-up on sell is removed; sub-lot sells are rejected 400. Buys keep the
	// <100→100 clamp because A-share 最小申购单位为一手.
	if qty < 100 {
		if side == trading.SideSell {
			writeError(w, 400, "卖出不支持零股（qty<100）；如需清仓请用清仓动作")
			return
		}
		qty = 100 // 不足一手按一手（100 股）
	}
	// 卖出侧校验：减仓数量不得超过当前持仓（超卖会被网关拒，这里前置拦截给用户明确报错）。
	if side == trading.SideSell {
		if db := s.realDB(); db != nil {
			// 只在能查到持仓且持仓 >0 时校验（DB 不可用/无持仓行时放行给引擎/网关裁决）。
			if p, err := db.RealPositionByCodeForUser(uid, normalizeTsCode(req.Code)); err == nil && p.Qty > 0 && qty > p.Qty {
				writeError(w, 400, "sell qty exceeds holding")
				return
			}
		}
	}
	// §P1-7 手动单行情上下文 + 价格合理性：拉一次实时行情（best-effort，失败即 fail-open
	// 不阻断——与风控闸"无行情弃权"哲学一致）。成功时同时修掉三个历史缺口：
	//   - checkLimitPrice 因手动单 PrevClose=0 恒 fail-open；
	//   - 限价偏离市价 ±20% 也直发（手滑输错价 = 真金白银）；
	//   - 陈旧度未提供（StaleQuoteGuard 恒跳过）。
	var q *data.StockInfo
	var staleMs int64 = -1 // -1=无行情（StalenessMs 未知，守卫跳过）；取到行情后置 0（新鲜）
	if s.market != nil {
		// best-effort 拉实时行情：失败/无价不阻断下单（fail-open）。
		if qq, qerr := s.market.GetRealtimeQuote(normalizeTsCode(req.Code)); qerr == nil && qq != nil && qq.Price > 0 {
			q = qq
		}
	}
	if q != nil {
		// §P1-7 限价偏离检查：委托价偏离现价超 ±15% 且未显式确认 → 400 拒绝（防手滑输错价）。
		if dev := (req.Price - q.Price) / q.Price; (dev > 0.15 || dev < -0.15) && !req.ConfirmDeviation {
			writeError(w, 400, fmt.Sprintf("委托价 %.2f 偏离现价 %.2f 超 ±15%%（%.1f%%），请核对价格后确认提交",
				req.Price, q.Price, dev*100))
			return
		}
	}
	// 幂等键构造：客户端 UUID 合法（格式/长度通过）则以 clientID 组键（重试复用同键防重复下单）；
	// 缺省/非法回退秒级时间戳键（兼容旧客户端，双击跨秒会各自成单——旧行为）。
	clientID := ""
	if cid := strings.TrimSpace(req.ClientID); cid != "" && len(cid) <= 64 && regClientID.MatchString(cid) {
		clientID = cid
	}
	signalID := "manual@" + req.Code + "@" + time.Now().Format("20060102150405")
	if clientID != "" {
		signalID = "manual@" + req.Code + "@" + clientID
	}
	oreq := trading.OrderRequest{
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
	}
	if q != nil {
		// 装配行情上下文：现价供风控闸；PrevClose 供涨跌幅校验（§P1-5 显式昨收，
		// 源未提供时回退旧 Close 字段语义）；staleMs=0 表示行情新鲜。
		oreq.CurrentPrice = q.Price
		oreq.PrevClose = q.PrevClose // §P1-5 显式昨收；未装配的源回退旧 Close 语义
		if oreq.PrevClose <= 0 {
			oreq.PrevClose = q.Close
		}
		staleMs = 0
	}
	oreq.StalenessMs = staleMs
	// 提交订单：走控制器全链路风控（风控闸/熔断/预算/白名单），拒绝原因原样回给前端。
	res, err := ctrl.PlaceOrder(oreq)
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
// §P1-6（2026-09-15）：实现收口到 data.ExchangeSuffix——旧内联 switch 把 `9` 前缀一律 .SH，
// 920xxx 北交所新股会被补成 .SH 下发网关（发错交易所/写错账本代码），与 data.LimitUpPct
// 的 92=北交所 30% 涨跌幅口径矛盾。
// English: normalizeTsCode appends the exchange suffix to a bare code, delegating to
// data.ExchangeSuffix (§P1-6) so the 920 BJ segment is no longer misrouted to .SH.
func normalizeTsCode(code string) string {
	if code == "" {
		return code
	}
	upper := strings.ToUpper(strings.TrimSpace(code))
	return data.ExchangeSuffix(upper)
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

// qmtReportEvent 网关回报（POST /api/qmt/report）的载荷结构。
// §F2（2026-09-22 修复批）：由 handleQMTReport 内匿名结构升级为具名类型——回报字段面是
// Go↔网关的对外契约，golden 回归测（report_contract_test.go ↔ qmt_gateway/contract/
// report_fields.json）通过反射本结构锁定字段集，任何一侧加/删字段漏改即测试红。
// English: named report payload (was an anonymous struct) so the golden contract test can
// reflect over its JSON tags; event types trade/order/positions/account share this envelope.
// §M4（2026-09-22 PM 批）双向契约收口：golden 现在同时记录"网关实际发出的字段"，
// 测试要求**发出集 ⊆ 信封集**——旧契约只反射 Go 这一侧，于是网关常年发的
// trade_id / name 在 Go 无 tag、被 encoding/json 静默丢弃（"丢腿"），单看 Go 侧永远不自洽。
// English: §M4 — the golden now also records the fields the gateway actually emits, and the test
// asserts emitted ⊆ envelope. The old one-sided lock let trade_id/name fall off the floor because
// encoding/json drops untagged keys without any error.
type qmtReportEvent struct {
	Type    string `json:"type"`
	OrderID string `json:"order_id"`
	TradeID string `json:"trade_id"` // §M4 券商成交编号（成交判重的精确身份锚）
	Code    string `json:"code"`
	Name    string `json:"name"` // §M4 证券名称（网关成交回报携带，建仓回填）
	Side    string `json:"side"`
	// SideUnverified §SIDE-AUTH-2（2026-09-23 夜间批）：网关未查到派发行（或派发行为空方向）
	// 时置 true——此时 side 只是桥/柜台多枚举空间的**猜测**（本项目已两次踩坑判反，
	// 2026-09-22 603468.SH 真实卖出记成买入即此链条）。为什么 Go 必须认这个字段：
	// 猜错的方向一旦入账就污染持仓/回款/已实现盈亏三本账且事后难发现；网关侧已把该笔
	// 转「待核对」通道，这里对应**不入库不动持仓**，只留痕（opslog+计数）等人工核对。
	// 命中派发行的回报不带该键（网关已用派发项覆盖），false 路径处理完全不变。
	// English: §SIDE-AUTH-2 — set by the gateway when no dispatch row vouches for the side
	// (bridge enum guess only). Such fills are journaled (opslog + counter), never booked:
	// a wrong guess would silently poison positions/cash-out/realized P&L.
	SideUnverified bool    `json:"side_unverified"`
	Status         string  `json:"status"`
	Price          float64 `json:"price"`
	Qty            int     `json:"qty"`
	Amount         float64 `json:"amount"`
	TradedAt       string  `json:"traded_at"`
	// CreatedAt §M4：order 回报的委托创建时间。网关 on_stock_order 一直同时发 at 与
	// created_at（双字段兼容契约），旧信封只有 at → created_at 被静默丢弃，委托行的
	// created_at 实际记成了回报时刻。现在优先取 created_at，缺失才退回 at。
	// English: §M4 — the gateway sends both `at` and `created_at`; the old envelope only had `at`,
	// so created_at was dropped and order rows stored the report time as creation time.
	CreatedAt string               `json:"created_at"`
	SignalID  string               `json:"signal_id"`
	Reason    string               `json:"reason"`    // §FIX-0921 柜台废单/拒单原因（网关尽力透传 status_msg）
	Fee       float64              `json:"fee"`       // §P2-FEE 20260918 经手费/佣金（尽力透传，缺=0）
	StampTax  float64              `json:"stamp_tax"` // 印花税（卖方单边，缺=0）
	Positions []store.RealPosition `json:"positions"`
	Asset     map[string]float64   `json:"asset"` // §可用资金：账户资产（cash/frozen_cash/total_asset/market_value）
	At        string               `json:"at"`
	UserID    string               `json:"user_id"` // §GAP1.10 网关配置的归属账号
	Broker    string               `json:"broker"`  // §QMT-DUAL 通道切换事件：目标通道
	From      string               `json:"from"`    // §QMT-DUAL 通道切换事件：来源通道
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
	var ev qmtReportEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeError(w, 400, "invalid report body")
		return
	}
	ctrl := s.qmtCtrlFor(uid)
	if ctrl != nil {
		// 上行通道新鲜度：任何回报到达都刷新 last_report_at（互通健康展示用）。
		// SetLastReport 同时记录事件类型，/api/qmt/state 据此渲染上行新鲜度与最近事件。
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
		// §AUDIT-PM 2026-09-15 空快照纵深守卫（第三层）：positions 语义是全量替换，而 broker.py
		// 在通道断连时同样返回空列表——若仅靠 网关token(第一层)+网关 clear_guard(第二层)，
		// Go 侧一个畸形/伪造的空数组仍会清空本账号持仓。规则：本地该账号仍有持仓而快照为空 →
		// 不可信，拒收并告警（409=永久拒绝进 outbox_dead 隔离，不无限重推）。合法全平不受影响：
		// 先经 trade 回报逐笔清零持仓行，或由 /state 周期对账（Controller.Reconcile 带 connected 守卫）落账。
		// 注意守卫只对"空快照"生效——非空快照仍按全量 reconcile 正常落账。
		if len(ev.Positions) == 0 {
			// 先查本地该账号当前持仓：仍有持仓却收到空快照 → 判定不可信。
			if held, herr := db.RealPositionsForUser(owner); herr == nil && len(held) > 0 {
				// 三路告警：服务端日志 + 运维日志 + 定向前端 SSE（positions_clear_guard 事件）。
				log.Printf("[trading] ⚠ positions 空快照但本地仍有 %d 持仓(用户=%s)——判定不可信，拒绝全清", len(held), owner)
				opslog.Logf("quant", "持仓全清守卫触发 用户=%s 本地持仓=%d 空快照被拒（若为真实全平请走成交回报/对账通道）", owner, len(held))
				if s.sse != nil {
					s.sse.BroadcastTo(owner, map[string]interface{}{
						"type": "positions_clear_guard", "held": len(held),
						"time": time.Now().Format("15:04:05"),
					})
				}
				writeError(w, 409, "positions 空快照但本地有持仓，拒绝全清（防断连空列表误清账；合法清仓走成交回报）")
				return
			}
		}
		// 守卫通过（空快照且本地确实无持仓，或非空快照）：按用户范围全量对账落库。
		// ReconcilePositionsForUser 以本账号为界做 upsert+删除，绝不触碰其它账号数据。
		if n, err := db.ReconcilePositionsForUser(owner, ev.Positions); err != nil {
			// §F2（2026-09-22 修复批）：store 入口的字段级校验失败（任一行 ts_code 空/非法格式）
			// → 整批拒收 400（留痕已在 store 侧 opslog 完成）。4xx 会被网关 outbox 按永久拒绝
			// 移入死信表，不会无限重推刷屏；其余落库错误仍回 500 触发重推。
			if errors.Is(err, store.ErrInvalidPositionReport) {
				log.Printf("[trading] ⚠ positions 快照字段校验整批拒收(用户=%s): %v", owner, err)
				writeError(w, 400, err.Error())
				return
			}
			writeError(w, 500, "reconcile positions: "+err.Error())
			return
		} else {
			log.Printf("[trading] 网关全量对账(用户=%s): %d 持仓", owner, n)
			// §DAILY_OPSLOG 每日至首次对账记一行（对账每分钟跑，全记会淹没核心记录）。
			// DayOnce 以 "reconcile:<uid>" 为当日去重键，同日重复对账只记首次。
			opslog.DayOnce("reconcile:"+owner, func() {
				opslog.Logf("quant", "首次持仓对账 用户=%s 持仓=%d", owner, n)
			})
		}
	case "order":
		// §安全 T3：委托方向同样归一（仅用于展示，但保持口径一致，避免"BUY"等串污染委托行）。
		// §2026-09-11 生产实录放宽：委托状态回报里 side 属"仅展示"语义，桥侧干跑探测
		// 单（TEST-DRY…）side="???" 会被拒 400 并在 outbox 反复重推刷屏——order 事件
		// 不动账本，未知方向改为原样保留 + 告警日志，不再拒收（trade 仍保持强校验）。
		orderSide, oErr := normalizeReportSide(ev.Side)
		if oErr != nil {
			log.Printf("[trading] 委托回报方向异常(signal=%s status=%s): %v，按原样落库仅展示", ev.SignalID, ev.Status, ev.Side)
			orderSide = ev.Side
		}
		// §M4/§F5（2026-09-22 修复批）委托状态回报接入段重构，同块并修两病：
		//  §M4 旧实现 ApplyOrderReportTx 出错只打日志、末尾仍回 200 ok——网关 outbox 据此
		//     标记投递成功不再重推，状态回报在落库失败时永久丢失（成交腿正确姿势是回 500）。
		//     现落库失败回 500，让 outbox 按可重试路径重推。
		//  §F5 旧实现要求 signal_id/order_id 齐全才进块、缺键回报无 else 无日志静默丢弃——
		//     手工单（无 signal_id）委托状态永久隐身。语义选择：
		//     ① order_id 是 orders 表主键，缺失时既无法定位也无法幂等落行 → 显式拒收 400
		//        （网关 outbox 对 4xx 走死信留痕，不无限重推），并 log+opslog 双留痕；
		//     ② 仅缺 signal_id 时不再丢弃：用 `ext:<order_id>` 占位信号键落一条可查的最小
		//        状态行（orders.signal_id 有 UNIQUE(user_id,signal_id) 约束，空串会互撞，
		//        ext: 前缀按单号天然唯一且不与业务信号前缀 buy:/sell:/pend: 冲突），
		//        同一单号的后续状态回报经秩守卫正常推进，撤单/对账面板可见可查。
		if ev.OrderID == "" {
			log.Printf("[trading] ⚠ order 回报缺主键 order_id(signal=%s code=%s status=%s)——拒收留痕",
				ev.SignalID, ev.Code, ev.Status)
			opslog.Logf("quant", "委托状态回报缺 order_id 被拒收(§F5) signal=%s code=%s status=%s",
				ev.SignalID, ev.Code, ev.Status)
			writeError(w, 400, "order 回报缺 order_id，拒收留痕")
			return
		}
		orderSignalID := ev.SignalID
		if orderSignalID == "" {
			orderSignalID = "ext:" + ev.OrderID // §F5 手工单占位信号键
			log.Printf("[trading] §F5 order 回报缺 signal_id，以占位键 %s 落最小状态行(code=%s status=%s)",
				orderSignalID, ev.Code, ev.Status)
		}
		{
			// §R4-4 委托状态推进：回报的 部成/已成/已撤/部撤/废单 必须写入本地行——
			// 旧实现 UpsertRealOrder 是 INSERT OR IGNORE（signal_id 冲突即忽略），状态回报被
			// 静默吞掉、本地永远停留"已报"，撤单闭环/对账全部失真。现走单调守卫的
			// 秩比较：秩高于本地才更新，乱序/重放/回退绝不覆盖真实进度。
			// §A4（20260918 全栈审计批）：旧实现"AdvanceRealOrderStatus 未命中 → 再 UpsertRealOrder
			// 补插"是两步独立写，崩溃/并发插队会整体丢掉这条回报；现合并为单事务
			// ApplyOrderReportTx（推进/补插/幂等 no-op 三选一，判定共享同一事务快照）。
			// English: §A4 — advance-then-insert-if-absent folded into one atomic transaction call.
			created := ev.CreatedAt // §M4 优先网关的委托创建时间
			if created == "" {
				created = ev.At // 缺省退回回报时刻（旧口径）
			}
			action, err := db.ApplyOrderReportTx(store.RealOrder{
				OrderID: ev.OrderID, SignalID: orderSignalID, Code: ev.Code,
				Side: orderSide, Status: ev.Status, Price: ev.Price, Qty: ev.Qty,
				CreatedAt: created,
				UserID:    uid, // §W2-10 委托行打归属账号
			})
			if err != nil {
				// §M4：状态腿落库失败绝不再吞错回 ok——500 让网关 outbox 重推（成交腿同口径）。
				log.Printf("[trading] apply order report(signal=%s): %v", orderSignalID, err)
				writeError(w, 500, "apply order report: "+err.Error())
				return
			}
			switch action {
			case store.OrderReportAdvanced:
				if ev.Status == "已报" {
					break
				}
				// 状态有实际推进（非停留在"已报"）：打日志留痕；Reason 是柜台废单/拒单原因（尽力透传）。
				log.Printf("[trading] 委托状态推进 %s: %s (order=%s%s)", ev.SignalID, ev.Status, ev.OrderID,
					func() string {
						if ev.Reason != "" {
							return " 拒因=" + ev.Reason
						}
						return ""
					}())
				// §DAILY_OPSLOG 状态推进是委托生命周期的核心节点（已成/已撤/废单…）
				opslog.Logf("quant", "委托状态推进 %s %s %s qty=%d status=%s order=%s%s",
					orderSignalID, orderSide, ev.Code, ev.Qty, ev.Status, ev.OrderID,
					func() string {
						if ev.Reason != "" {
							return " 拒因=" + ev.Reason
						}
						return ""
					}())
			case store.OrderReportInserted:
				// 本地无单（网侧重放/回报先于下单回填到达）：补插留痕，便于次日取证还原时间线
				log.Printf("[trading] 委托回报本地无单，补插 %s %s status=%s order=%s", orderSignalID, ev.Code, ev.Status, ev.OrderID)
			}
		}
	case "trade":
		// 成交回报应用到实盘账本（建仓/加仓加权/减仓/清仓）+ 写 fills
		// §安全 T3：先归一方向，非预期值直接报错，避免误走卖分支清零持仓。
		tradeSide, sErr := normalizeReportSide(ev.Side)
		if sErr != nil {
			// trade 直接拒收（400）：方向不明绝不能默认按卖处理，否则会静默清零持仓。
			writeError(w, 400, sErr.Error())
			return
		}
		// §SIDE-AUTH-2（2026-09-23 夜间批）方向未获派发行证实的成交：**不入库、不动持仓账**，
		// 只留痕（opslog 审计行 + 计数指标）并回 200 幂等吞掉。为什么不入账：
		// 此刻 side 只是桥/柜台枚举猜测（23/24 vs 1101/1102 vs 48/50 多空间反推，
		// 已两次实锤判反），猜错就是把卖出记成买入——持仓/回款/已实现盈亏三本账当场被污
		// 且事后无从分辨；网关已把同笔证据落「待核对」通道（/settlement 可见），人工核对
		// 后再勘误，两侧各管各账、不算双改（比对既有已入库行的处理路径一字未动）。
		// 为什么回 200 而不是 4xx：4xx 会进网关死信表永久隔离，证据链反而更难凑齐；
		// 留痕行带全部可复核字段，运维据此走人工勘误通道。
		// English: §SIDE-AUTH-2 — a side-unverified fill is journaled (opslog + counter) and
		// NOT booked; the gateway already keeps the 待核对 evidence row. ACK 200 so the durable
		// outbox does not dead-letter the event.
		if ev.SideUnverified {
			metrics.FillsSideUnverified()
			log.Printf("[trading] ⚠ §SIDE-AUTH-2 成交方向未证实（网关未命中派发行），留痕不入账: "+
				"用户=%s code=%s inferred_side=%s qty=%d price=%.2f order=%s trade_id=%s signal=%s",
				uid, ev.Code, tradeSide, ev.Qty, ev.Price, ev.OrderID, ev.TradeID, ev.SignalID)
			opslog.Audit("live_fill_side_unverified", uid, ev.Code,
				fmt.Sprintf("方向未获派发行证实-未入账 side=%q qty=%d price=%.2f order=%s trade_id=%s signal=%s amount=%.2f traded_at=%s",
					tradeSide, ev.Qty, ev.Price, ev.OrderID, ev.TradeID, ev.SignalID, ev.Amount, ev.TradedAt))
			opslog.Logf("quant", "成交方向待核对（§SIDE-AUTH-2，未入本地账）%s %s qty=%d price=%.2f order=%s trade_id=%s——请到网关「待核对」流水人工核对后勘误",
				tradeSide, ev.Code, ev.Qty, ev.Price, ev.OrderID, ev.TradeID)
			writeJSON(w, 200, map[string]string{"ok": "1", "side_unverified": "1"})
			return
		}
		// ApplyRealFill 事务内完成：成交流水插入（signal_id 幂等，重复回报整体回滚）
		// + 持仓更新（买入加权成本 / 卖出按加权成本实现盈亏 / 清仓删行）。
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: ev.OrderID, Code: ev.Code, Side: tradeSide, Price: ev.Price,
			Qty: ev.Qty, Amount: ev.Amount, TradedAt: ev.TradedAt, SignalID: ev.SignalID,
			// §M4（2026-09-22 PM 批）两条腿接进账本：name 回填建仓持仓（R8 回填路径一直在等它，
			// 但信封没 tag 所以恒空），trade_id 成交判重精确锚（同委托同秒同价同量的两笔真实
			// 部成不再被复合键误判为重放）。
			// English: §M4 — carry the two legs the old envelope dropped: name feeds the position
			// backfill path, trade_id gives fills an exact replay anchor.
			Name: ev.Name, TradeID: ev.TradeID,
			UserID: uid, // §W2-10 成交流水打归属账号（幂等键冲突时整体回滚，持仓不重复累加）
			// §P2-FEE 20260918：成交费用腿透传入本地 fills（网关回报缺省时为 0，与旧口径一致）。
			Fee: ev.Fee, StampTax: ev.StampTax,
		}); err != nil {
			writeError(w, 500, "apply fill: "+err.Error())
			return
		}
		log.Printf("[trading] 成交回报 %s %s qty=%d price=%.2f trade_id=%s", ev.Side, ev.Code, ev.Qty, ev.Price, ev.TradeID)
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
	case "broker":
		// §QMT-DUAL 通道切换事件（xt↔queued）：仅观察用，不动账本。旧实现没有本 case
		// 导致 outbox 里的切换回报反复 400 重推刷屏（2026-09-11 生产实录）。
		log.Printf("[trading] 网关节点通道切换事件: %s -> %s at=%s", ev.From, ev.Broker, ev.At)
	default:
		writeError(w, 400, "unknown report type")
		return
	}

	// SSE 推前端：无论事件类型，统一以 qmt_report 事件向归属账号定向广播摘要
	// （前端实盘页据此即时刷新；完整明细以 DB/各专用事件为准）。
	// §M13（2026-09-22 修复批）载荷补 tripped：Positions.jsx 用 `!!msg.tripped` 直接覆盖熔断位，
	// 而旧载荷**根本没有 tripped 字段** → 任意一笔成交/委托回报都会把「熔断中」徽标瞬清成
	// 「正常」，要等同账号下一次 loadReal（一个 RTT）才纠回来。资金拦截本身没失守
	// （下单口有权威闸），坏的是呈现层——所以后端必须把权威熔断状态随广播带出去。
	// 只读语义：ctrl.Tripped() 是 RLock 读，不改变任何熔断状态；无控制器（未启用实盘）时为 false，
	// 与 REST 侧 ctrlTripped 的口径完全一致。event/at 两个键保留既有形状（type 已是 event 值、
	// time 仍是 HH:MM:SS），此处只增字段、不改名不删字段，前端旧消费者零影响。
	// English: §M13 — the qmt_report payload now carries the authoritative breaker state (read-only)
	// so the frontend badge can't be silently cleared by an unrelated report; fields are additive only.
	// §UPDLINK（2026-09-22 H-4）：这里用**有界广播**（2s 预算）。账本已经落库、前端刷新只是
	// 尽力而为（轮询/REST 兜底在位），而 2026-09-22 的生产实录证明无界等待会把整条上行入口
	// 陪葬：SSE 广播锁被一次 double-close panic 永久占住后，每个回报 POST 都堵在 BroadcastTo 上，
	// positions/account 冻结 1h45m、资金闸拿着上午的碎钱值把当日买入全拦。宁可丢推送不丢账。
	if s.sse != nil {
		s.sse.BroadcastToWithin(uid, map[string]interface{}{
			"type":    "qmt_report",
			"event":   ev.Type,
			"code":    ev.Code,
			"side":    ev.Side,
			"price":   ev.Price,
			"qty":     ev.Qty,
			"time":    time.Now().Format("15:04:05"),
			"at":      time.Now().Format(time.RFC3339),
			"tripped": ctrl != nil && ctrl.Tripped(),
		}, 2*time.Second)
	}
	// 回报受理成功，返回 ok 让网关 outbox 标记完成（否则会重推）。
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleGetQMTConfig 处理 GET /api/config/qmt：返回当前账号实盘配置。
// token 只回脱敏形态（§GAP2-W2 同口径），提交脱敏哨兵或空串时后端保持原值。
// English: GET /api/config/qmt returns the account's live-trading config with the token masked.
func (s *Server) handleGetQMTConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetQMTConfigFor(userIDFor(r))
	// 诊断日志：排查「开关刷新后变回关闭」——记录每次读取的真实账号与 enabled 值。
	log.Printf("[diag-qmt] GET /api/config/qmt user=%s operator=%s enabled=%v", userIDFor(r), s.operatorID(), cfg.Enabled)
	writeJSON(w, 200, qmtConfigView(cfg, s.knownStrategyList()))
}

// qmtConfigView 把 QMT 实盘配置渲染为对外响应形状（token 脱敏、附已知战法列表）。
// English: renders a QMT live-trading config as the API response shape (token masked).
func qmtConfigView(cfg *config.QMTConfig, known []knownStrategyInfo) map[string]interface{} {
	tokenMasked := ""
	if cfg.Token != "" {
		tokenMasked = maskSecret(cfg.Token)
	}
	return map[string]interface{}{
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
		// §AUDIT-PM 2026-09-15 单笔金额绝对帽（风控闸 risk_gate.max_order_amount，0=关）
		"max_order_amount": cfg.RiskGate.MaxOrderAmount,
		"known_strategies": known,
	}
}

// setQMTConfigReq 局部更新请求：指针字段=「本次要改的」，nil=保持不变。
type setQMTConfigReq struct {
	Enabled           *bool               `json:"enabled"`             // 是否启用量化实盘
	Mode              *string             `json:"mode"`                // 运行模式（实盘/模拟等）
	GatewayURL        *string             `json:"gateway_url"`         // 券商网关地址
	Token             *string             `json:"token"`               // 网关鉴权令牌
	PriceType         *string             `json:"price_type"`          // 委托价格类型（市价/限价）
	FixedAmount       *float64            `json:"fixed_amount"`        // 单笔固定买入金额
	MaxPositions      *int                `json:"max_positions"`       // 最大持仓数
	InitialCapital    *float64            `json:"initial_capital"`     // 初始资金
	Strategies        *[]string           `json:"strategies"`          // 启用战法列表
	StrategyAmounts   *map[string]float64 `json:"strategy_amounts"`    // 各战法分配资金
	DailyMaxBuys      *int                `json:"daily_max_buys"`      // 每日最大买入笔数（按当日已成交计，2026-09-18 口径修正）
	DailyBudgetAmount *float64            `json:"daily_budget_amount"` // 每日买入预算
	AutoSell          *bool               `json:"auto_sell"`           // 是否自动卖出
	MissHeartbeatSec  *int                `json:"miss_heartbeat_sec"`  // 心跳超时秒数
	// §R4-1 kill-switch 与撤单闭环参数
	Halted         *bool `json:"halted"`           // 全局熔断暂停
	CancelStaleSec *int  `json:"cancel_stale_sec"` // 未成交撤单超时秒数
	CloseSweepAt   *int  `json:"close_sweep_at"`   // 尾盘清仓时间（分钟）
	// §AUDIT-PM 2026-09-15 单笔委托金额绝对帽（元；0=关）——安全封顶闸，保存即生效，
	// 不随开关队列滞留到次日（与 §U-3 halted 即时语义同口径）。
	MaxOrderAmount *float64 `json:"max_order_amount"`
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
	s.applySetQMTConfig(w, userIDFor(r), userIDFor(r), req)
}

// applySetQMTConfig 局部合并 QMT 实盘配置并保存到目标账号（§2026-09-07 多账号实盘）：
// 运营账号经 /api/config/qmt、管理员代配经 /api/admin/users/{id}/config/qmt 都走这里，
// 校验与落库语义一致（指针字段=本次要改的，nil=保持原值）。
// English: merges and persists a QMT config patch for the target account — shared by the operator
// endpoint and the admin per-account endpoint so validation/save semantics stay identical.
func (s *Server) applySetQMTConfig(w http.ResponseWriter, actor, target string, req setQMTConfigReq) {
	// §WS-K 维4 保存前快照上一版 config.json（全局无 store 路径；best-effort，失败仅告警不阻断）。
	// beforeBytes 留作之后 diff 审计用；快照文件供回滚/追溯。
	// English: WS-K 维4 — snapshot the previous config.json before saving (best-effort).
	beforeBytes, _ := config.RestoreRulesContentCurrent(s.cfg)
	_, _ = config.SnapshotRules(s.cfg)
	// 以目标账号当前配置为基线做局部合并（值拷贝，改完一次性写回）。
	cfg := *(s.cfg.GetQMTConfigFor(target))

	// ---- 以下逐字段按「指针非 nil 才生效」合并，枚举/范围非法直接 400 拒绝 ----
	if req.Mode != nil {
		m := strings.TrimSpace(*req.Mode)
		if m != "manual" && m != "auto" { // 枚举校验：只允许手动/自动两种执行模式
			writeError(w, 400, "mode 仅允许 manual/auto")
			return
		}
		cfg.Mode = m
	}
	if req.PriceType != nil {
		p := strings.TrimSpace(*req.PriceType)
		if p != "market" && p != "limit" { // 枚举校验：市价/限价
			writeError(w, 400, "price_type 仅允许 market/limit")
			return
		}
		cfg.PriceType = p
	}
	if req.GatewayURL != nil {
		u := strings.TrimSpace(*req.GatewayURL)
		// 仅对"非空且发生变更"的新地址做校验，避免每次保存都外呼。
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
		// token 三重条件：显式携带、非空、非脱敏哨兵（前端回显的掩码串不回写覆盖真值）。
		cfg.Token = strings.TrimSpace(*req.Token)
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled // 实盘总开关（热同步走 §QMT-PENDING 队列，halted 例外见后）
	}
	if req.AutoSell != nil {
		cfg.AutoSell = *req.AutoSell // 自动卖出开关（尾盘清仓等）
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
	// §AUDIT-PM 2026-09-15 单笔金额绝对帽（0=关；上限 1e9 防误输成"天文数字"失去封顶意义）
	if req.MaxOrderAmount != nil {
		v := *req.MaxOrderAmount
		if v < 0 {
			writeError(w, 400, "max_order_amount 不能为负")
			return
		}
		if v > 1000000000 {
			writeError(w, 400, "max_order_amount 超出范围（0-1000000000）")
			return
		}
		cfg.RiskGate.MaxOrderAmount = v
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
		// 战法白名单：逐项去空格、去重、校验必须在已知战法集合内（未知战法 400 拒绝）。
		// out 允许为空数组——空数组语义 = 不设白名单（全部允许），与引擎口径一致。
		seen := map[string]bool{}
		out := make([]string, 0, len(*req.Strategies))
		knownSet := s.knownStrategyIDSet()
		for _, v := range *req.Strategies {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue // 空串/重复项跳过
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
		// 各战法资金覆盖：key 同样必须在白名单集合内；金额范围 0-1000000。
		out := map[string]float64{}
		knownSet := s.knownStrategyIDSet()
		for k, v := range *req.StrategyAmounts {
			k = strings.TrimSpace(k)
			if k == "" {
				continue // 空 key 忽略
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

	// §WS-K 维4 保存前 schema 校验：非法配置返回 400，不再静默排队/落库。
	// English: WS-K 维4 — schema-validate before persisting; invalid configs get an explicit 400.
	if verr := config.Validate(&config.Rules{QMT: cfg}); verr != nil {
		writeError(w, 400, "非法配置: "+verr.Error())
		return
	}
	s.cfg.SetQMTConfigFor(target, &cfg)
	// §U-3（2026-09-14 像素级 UAT）：本端点其余字段走开关队列（休市不翻转实盘行为，§QMT-PENDING），
	// 但请求显式携带 halted 属 kill-switch 语义——必须与 /api/qmt/halt 同口径立即生效，
	// 否则"保存即熔断"会静默滞留到次日开盘（fail-stop 漏洞）。
	if req.Halted != nil {
		s.applyKillSwitchNow(target, &cfg, "config_save")
	}
	// §AUDIT-PM 2026-09-15：单笔金额绝对帽同样"保存即生效"——安全封顶闸若滞留开关队列到
	// 次日，等于收盘设置的上限在次日开盘前形同虚设。UpdateConfig 只换 c.cfg 不动 executor，
	// 引擎热同步下一轮仍会以 QueueConfigUpdate 对齐（同值幂等，无竞争）。
	if req.MaxOrderAmount != nil {
		if ctrl := s.qmtCtrlFor(target); ctrl != nil {
			ctrl.UpdateConfig(cfg)
		}
	}
	// §WS-K 维4 变更 diff → opslog 审计（可下载/前端历史可见）
	if beforeBytes != nil {
		if afterBytes, err := config.RestoreRulesContentCurrent(s.cfg); err == nil {
			if d, derr := config.DiffRules(beforeBytes, afterBytes); derr == nil && d != "(无变更)" {
				opslog.Audit("config_change", actor, "qmt", d)
			}
		}
	}
	// §WS-F C1 审计：QMT 配置变更留痕（enabled 翻转为"上线/下线"，其余为"hot_reload"）。
	// 事件名映射：本次携带 enabled 字段时按落库后的 cfg.Enabled 定为上线(qmt_go_live)/
	// 下线(qmt_shutdown)；未携带则统一记热更新。
	event := "qmt_config_hot_reload"
	if req.Enabled != nil {
		event = map[bool]string{true: "qmt_go_live", false: "qmt_shutdown"}[cfg.Enabled]
	}
	opslog.Audit(event, actor, target, "ok")
	// 诊断日志：记录每次保存的真实账号、目标 enabled 与落盘后回读值，确认是否真正写盘。
	// saved 是落库后回读值（持久化配置，立即生效口径）；ctrlEnabled 是控制器当前 applied 值
	// （§QMT-PENDING 延迟到下一交易时段才翻转）。两者在休市期间本就不同——区分它俩可避免把
	// "延迟生效" 误判为 "开关变回关闭 / 写盘失败"（§FIX-2）。
	saved := s.cfg.GetQMTConfigFor(target)
	ctrlEnabled := false
	if ctrl := s.qmtCtrlFor(target); ctrl != nil {
		ctrlEnabled = ctrl.Enabled()
	}
	log.Printf("[diag-qmt] POST qmt config target=%s operator=%s reqEnabled=%v savedEnabled=%v ctrlEnabled=%v", target, s.operatorID(), cfg.Enabled, saved.Enabled, ctrlEnabled)
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
	// 已接入：直接透出控制器健康快照（下行探测/上行回报新鲜度/熔断详情/资金等）。
	writeJSON(w, 200, ctrl.Snapshot())
}

// handleQMTOrders 处理 GET /api/qmt/orders（admin）：当日实盘委托列表。
// §U-2（2026-09-14 像素级 UAT）：撤单端点 /api/qmt/cancel/{order_id} 一直存在但前端零入口——
// 根因是列表缺口：前端拿不到 order_id 就无从挂"撤单"按钮。本端点把 live.db orders 行
// 按北京时间当日过滤返回（created_at 前缀 yyyy-MM-dd），终态/在途由前端按 status 呈现，
// 仅 已报/部成（含部成待撤等未终结态）允许撤单。遗留全局行（user_id=”）沿用
// RealOrdersForUser 的可见性口径，不额外收权。
// English: §U-2 — today's live-book orders for the admin UI. The cancel endpoint existed with no
// frontend entry precisely because the list carried no order ids to anchor the button; this
// returns same-day rows (Beijing-time prefix filter) so the UI can render per-order cancel
// actions on non-terminal statuses.
func (s *Server) handleQMTOrders(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	orders, err := db.RealOrdersForUser(userIDFor(r))
	if err != nil {
		writeError(w, 500, "list orders: "+err.Error())
		return
	}
	today := cntime.In(time.Now()).Format("2006-01-02") // 北京时间当日（created_at 以北京时间落库）
	out := make([]store.RealOrder, 0, len(orders))
	for _, o := range orders {
		// 按当日前缀过滤：created_at 形如 "2026-09-17 09:31:00"，前缀匹配即当日委托。
		if strings.HasPrefix(o.CreatedAt, today) {
			out = append(out, o)
		}
	}
	writeJSON(w, 200, out)
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
	uid := userIDFor(r)                  // kill-switch 作用于当前登录账号（admin 权限中间件已保证）
	cfg := *(s.cfg.GetQMTConfigFor(uid)) // 值拷贝：基于当前配置做单字段覆盖，避免读到一半被并发改写
	cfg.Halted = *req.Halted             // 本次只翻转 halted 字段，其余保持原值
	s.cfg.SetQMTConfigFor(uid, &cfg)     // 持久化（跨重启保留）
	// 立即执行 kill-switch：绕过 §QMT-PENDING 开关队列（见 applyKillSwitchNow 注释），
	// 返回本次同步撤销的在途未成交委托笔数。
	cancelled := s.applyKillSwitchNow(uid, &cfg, "endpoint")
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "halted": *req.Halted, "cancelled": cancelled})
}

// applyKillSwitchNow §U-3（2026-09-14 像素级 UAT）：kill-switch 置位/解除的"立即生效"公共执行体，
// 供 POST /api/qmt/halt 专用端点与 POST /api/config/qmt 携带 halted 字段两条路径共用。
// 背景：普通配置变更走 §QMT-PENDING 开关队列（交易时段由 scoreCycle 消费），休市时保存的
// halted 会滞留到次日开盘才生效——与 fail-stop 语义冲突（UAT 实录：盘后 config 路径置 halted
// 等待 6s 仍可下单）。kill-switch 属紧急停止，必须绕过队列直接 ctrl.UpdateConfig 同步，
// 置位时同步 HaltAll 撤销在途未成交委托、SSE 广播、opslog 审计留痕（来源 source 区分端点）。
// 返回值：本次同步撤销的委托笔数（无控制器/解除时 0）。
// English: §U-3 — shared immediate-effect body for the kill switch, used by both the dedicated
// /api/qmt/halt endpoint and a halted field carried by /api/config/qmt. Normal config edits ride
// the session-queued hot-sync (so an off-hours halted save would only bite at next open — a
// fail-stop violation caught in UAT); the kill switch bypasses the queue via ctrl.UpdateConfig,
// runs HaltAll on engagement, broadcasts SSE and writes the audit trail. Returns cancelled count.
func (s *Server) applyKillSwitchNow(uid string, cfg *config.QMTConfig, source string) int {
	cancelled := 0
	ctrl := s.qmtCtrlFor(uid)
	if ctrl != nil {
		ctrl.UpdateConfig(*cfg)
		if cfg.Halted {
			cancelled = ctrl.HaltAll()
		}
	}
	log.Printf("[trading] ⚠️ kill-switch %s (用户=%s 来源=%s): 同步撤销未成交委托 %d 笔",
		map[bool]string{true: "置位——紧急停止一切下单", false: "解除"}[cfg.Halted], uid, source, cancelled)
	// §DAILY_OPSLOG kill-switch 属最高优先级留档事件
	opslog.Logf("quant", "kill-switch %s 用户=%s 来源=%s 撤销未成交委托=%d",
		map[bool]string{true: "置位(紧急停止)", false: "解除"}[cfg.Halted], uid, source, cancelled)
	// §WS-F C1 审计：kill-switch 翻转留痕（来源区分专用端点/配置保存）
	result := "clear"
	if cfg.Halted {
		result = "set"
	}
	opslog.Audit("kill_switch", uid, "qmt:"+source, result)
	if s.sse != nil {
		s.sse.BroadcastTo(uid, map[string]interface{}{
			"type":      "qmt_halt",
			"halted":    cfg.Halted,
			"cancelled": cancelled,
			"time":      time.Now().Format("15:04:05"),
		})
	}
	return cancelled
}

// handleQMTSettle §WS-B 交割单三方对账端点（POST /api/qmt/settle，admin 权限）：
// 请求体 {"day":"2026-09-08","mode":"report_only|sync_fills"}（day 缺省今天）。
// 调用 Controller.SettleDay 拉券商交割单 ↔ 本地账本三方比对，差异落 settlement_diff + 告警。
// English: §WS-B settlement endpoint (admin) — triggers a three-way reconciliation for a day,
// persisting diffs to settlement_diff and alerting on discrepancies.
func (s *Server) handleQMTSettle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Day  string `json:"day"`
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Day == "" {
		req.Day = cntime.In(time.Now()).Format("2006-01-02")
	}
	if req.Mode == "" {
		req.Mode = trading.SettleModeReportOnly
	}
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		writeError(w, 503, "real book not available")
		return
	}
	diff, err := ctrl.SettleDay(req.Day, req.Mode)
	if err != nil {
		opslog.Audit("settle", userIDFor(r), req.Day, "fail: "+err.Error())
		writeError(w, 502, "settle failed: "+err.Error())
		return
	}
	// 差异告警（与调度路径一致）
	if diff != nil && (len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch) > 0) {
		s.notifySettleDiff(userIDFor(r), diff)
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok": "1", "day": req.Day, "mode": req.Mode,
		"diff": diff,
	})
}

// notifySettleDiff §WS-B 对账差异告警（P1 强提醒 + opslog）。
// English: alerts a settlement diff via the notify/opslog channels.
func (s *Server) notifySettleDiff(uid string, diff *store.SettlementDiff) {
	opslog.Logf("quant", "交割单对账差异 用户=%s 日=%s 缺失=%d 多余=%d 不符=%d 费用差=%.2f 现金差=%.2f",
		uid, diff.Day, len(diff.MissingInLocal), len(diff.ExtraInLocal), len(diff.Mismatch), diff.FeeDiff, diff.CashDiff)
	if s.sse != nil {
		s.sse.BroadcastTo(uid, map[string]interface{}{
			"type": "settlement_diff",
			"diff": diff,
			"time": time.Now().Format("15:04:05"),
		})
	}
}

// handleQMTSettleHistory §WS-B 对账历史（GET /api/qmt/settle/history，admin）。
// English: settlement history (admin).
func (s *Server) handleQMTSettleHistory(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, 503, "real book not available")
		return
	}
	diffs, err := db.ListSettlementDiffs(20)
	if err != nil {
		writeError(w, 500, "list settlement diffs: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "diffs": diffs})
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
		// 失败分类：报文含 not connected/circuit 属网关不可达或熔断 → 502（可稍后重试）；
		// 其余（已成交/已撤/不可撤等状态冲突）→ 409（重试无意义）。
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

// handleQMTBroker 返回网关 active 通道与双路径状态（GET /api/qmt/broker，登录可见）。
// §QMT-DUAL：供 admin 面板读取当前执行路径（miniqmt=xt / qmt=queued）与兜底在线态。
// 未接入实盘或网关不可达时返回 200 + {ok:false, err:...}（前端展示"未接入/不可达"）。
// English: returns the gateway's active broker and dual-path liveness (GET /api/qmt/broker).
func (s *Server) handleQMTBroker(w http.ResponseWriter, r *http.Request) {
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		// 未接入实盘：不报错（保持 200），由前端按 ok:false 展示"未接入"。
		writeJSON(w, 200, map[string]interface{}{"ok": false, "broker": "", "err": "not enabled"})
		return
	}
	// 查询网关 active 通道状态：err=网关探测失败（200+ok:false，不当作 HTTP 错误）。
	st, err := ctrl.GatewayBrokerStatus()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "broker": "", "err": err.Error()})
		return
	}
	writeJSON(w, 200, st)
}

// handleQMTBrokerSwitch 切换网关 active 通道（POST /api/qmt/broker，仅 admin）。
// 请求体 {"broker":"xt"|"queued"}；xt=miniqmt 兼容主路径，queued=QMT 内置桥兜底。
// English: admin-only gateway broker switch (POST /api/qmt/broker).
func (s *Server) handleQMTBrokerSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Broker string `json:"broker"` // 目标通道：xt=miniqmt 兼容主路径 / queued=内置桥兜底
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Broker) == "" {
		writeError(w, 400, "invalid request body: 需要 {\"broker\":\"xt\"|\"queued\"}")
		return
	}
	ctrl := s.qmtCtrlFor(userIDFor(r))
	if ctrl == nil {
		writeError(w, 503, "real book not available")
		return
	}
	// 切换校验与执行在控制器侧：非法通道名/切换中失败都会带原因返回。
	if err := ctrl.SwitchGatewayBroker(req.Broker); err != nil {
		writeError(w, 502, "切换失败: "+err.Error())
		return
	}
	opslog.Logf("quant", "admin 切换网关通道 -> %s 用户=%s", req.Broker, userIDFor(r))
	writeJSON(w, 200, map[string]interface{}{"ok": "1", "broker": req.Broker})
}

// qmtStrategyOf 从 signal_id 解析战法归属：buy:<码>:<战法>:<日> → 战法名；其余（sell:/manual@）→ manual。
// 卖出盈亏按持仓当前的入场战法归因（重放状态维护），卖出自身 signal_id 里的类目是退出原因而非来源战法。
// English: derives the strategy tag from a buy signal_id; sells are attributed to the position's
// entry strategy tracked during replay (the sell key encodes exit class, not origin strategy).
func qmtStrategyOf(signalID string) string {
	// 买入信号键形如 buy:600519.SH:龙抬头:2026-09-17，第 3 段即战法名。
	if strings.HasPrefix(signalID, "buy:") {
		parts := strings.Split(signalID, ":")
		if len(parts) >= 3 && parts[2] != "" { // 战法段缺失/为空 → 回退 manual
			return parts[2]
		}
	}
	// sell:*/manual@… 等非买入键统一归 manual（卖出按持仓入场战法归因，见调用方）。
	return "manual"
}

// handleQMTTrades 处理 GET /api/qmt/trades：交易流水 + 整体盈亏 + 按战法归因统计。
// 盈亏口径：
//   - 已实现：按时间升序重放全部成交（加权成本法），买入成本摊入佣金、卖出 pnl 扣
//     fee+stamp_tax（§F1，2026-09-22 起与 paper 含费口径一致）；
//   - 浮动：real_positions 的 市值-数量×成本（市值为最近一次网关对账快照）；
//   - 总盈亏 = 已实现 + 浮动；胜/亏按单笔卖出 pnl 正负计数。
//   - 金额统计优先取落库 Amount（§F12 单口径），旧格式回报回退 Price×Qty。
//
// 飞轮数据面：by_strategy 即「research 出战法 → 信号 → 实盘结果」回流评估的输入源，
// research 侧可直接读同一 researchDB 的 fills/orders 表或消费本端点。
//
// §FILL-AMEND（2026-09-23）：本端点的成交来源 RealFills 已改走 fills_effective 视图，即
// **按生效方向重放**（人工勘误批准后自动跟着变）。刻意不另写一套"勘误后重放"逻辑：
// 加权的成本基准、超卖钳制（sellQty 钳到重放量、超出部分按持仓账定价）这两条防线都建立在
// 同一条按时间升序的成交流上，任何旁路都会让"改判方向"绕过钳制而凭空造出已实现盈亏。
// 勘误只改方向，金额沿用落库 Amount（§F12 单口径），所以 amt 腿与钳制量都不需要特判。
// English: §FILL-AMEND — the replay consumes the amendment-resolved direction (via the
// fills_effective view) through the very same time-ordered stream, so the weighted cost basis and the
// oversell clamp keep applying to a re-booked fill; only the side changes, the amount stays the
// stored turnover.
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
	// 归属过滤：空 user_id = 遗留全局行，对所有人可见（§GAP1.10 口径）；
	// 非空行只保留本账号的，避免跨账号流水泄漏。
	fills := make([]store.RealFill, 0, len(allFills))
	for _, f := range allFills {
		if f.UserID == "" || f.UserID == uid {
			fills = append(fills, f)
		}
	}
	// 按成交时间升序排序：盈亏重放（加权成本法）必须按时间序计算才准确。
	sort.Slice(fills, func(i, j int) bool { return fills[i].TradedAt < fills[j].TradedAt })

	// 当前实盘账本持仓：①计算浮动盈亏（unrealized）；②为"对账来源持仓"（成交簿无买入记录）的
	// 卖出提供成本基准（§2026-09-08 验证②）。
	positions, err := db.RealPositionsForUser(uid)
	if err != nil {
		writeError(w, 500, "read positions: "+err.Error())
		return
	}
	// posCost：当前实盘持仓的每股成本（代码→成本价），供"成交簿无买入口"的卖出定价。
	posCost := make(map[string]float64, len(positions))
	for _, p := range positions {
		posCost[p.TsCode] = p.CostPrice
	}

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

	realized := 0.0 // 累计已实现盈亏
	wins, losses := 0, 0
	for _, f := range fills {
		ps := stratState[f.Code] // 取该代码的重放状态（无则初始化空状态）
		if ps == nil {
			ps = &posState{}
			stratState[f.Code] = ps
		}
		amt := f.Price * float64(f.Qty) // 本笔成交金额（买卖同式，直接进战法统计）
		// §F12（2026-09-22 修复批）：统计金额优先取**落库 Amount**（柜台成交额口径），
		// 消除「重算 Price×Qty vs 库内 Amount」双口径漂移；旧格式回报 Amount<=0 回退重算值
		// （与 SumBuyFilledAmountByDay 的回退姿势一致）。
		if f.Amount > 0 {
			amt = f.Amount
		}
		// §F1 费用腿：买入佣金摊入重放成本（与 ApplyRealFill 同式），卖出已实现盈亏扣
		// fee+stamp_tax——与 paper 口径（buy: cost+fee；sell: net=gross-fee）对齐，
		// 实盘/模拟两账盈亏从此同基准，不再系统性偏乐观。
		switch f.Side {
		case "买入":
			// 买入：新数量摊薄加权成本 =（旧成本×旧量+本笔含费金额）/ 新量。
			newQty := ps.qty + f.Qty
			costAmt := f.Price*float64(f.Qty) + f.Fee
			if newQty > 0 {
				ps.cost = (ps.cost*float64(ps.qty) + costAmt) / float64(newQty)
			}
			ps.qty = newQty
			ps.strategy = qmtStrategyOf(f.SignalID) // 记录该持仓的入场战法（卖出据此归因）
			buyStat := statFor(ps.strategy)
			buyStat.Buys += amt
			buyStat.Count++
		case "卖出":
			// 卖出：按持仓剩余量钳制成交，计已实现盈亏（扣卖出费用腿）与胜负次数。
			sellQty := f.Qty
			if sellQty > ps.qty {
				sellQty = ps.qty // 超卖钳制（与 ApplyRealFill 同口径）
			}
			sellPnl := (f.Price-ps.cost)*float64(sellQty) - (f.Fee + f.StampTax)
			// §2026-09-08 验证②修复：超出成交簿买量的部分（对账来源持仓）无重放成本基准——
			// 旧实现 sellQty=0 → pnl=0 → 一律 wins++，把亏损退出伪造成"胜"并吞掉已实现盈亏。
			// 改用当前实盘账本成本 pricing；账本亦无该持仓（已全仓卖光）则放弃定价，不计胜/负
			// 与已实现盈亏——无基准的退出不应被当成结果计数。
			extra := f.Qty - sellQty
			pricable := sellQty > 0
			if extra > 0 {
				if pc, ok := posCost[f.Code]; ok {
					sellPnl += (f.Price - pc) * float64(extra)
					pricable = true
				}
			}
			if pricable {
				realized += sellPnl
				if sellPnl >= 0 {
					wins++
				} else {
					losses++
				}
			}
			// 战法标签：卖出单无内嵌战法时沿用买入时状态，空回退 manual。
			k := ps.strategy
			if k == "" {
				k = "manual"
			}
			sellStat := statFor(k)
			sellStat.Sells += amt
			if pricable {
				sellStat.Realized += sellPnl // 无成本基准的退出不计入战法盈亏
			}
			sellStat.Count++
			ps.qty -= sellQty // 重放扣减持仓量（可为 0，不删除状态以便后续同码成交）
		}
	}

	// 浮动盈亏 = Σ（最新市值 − 持仓量×成本价）；市值取最近一次网关对账快照的 Amount。
	unrealized := 0.0
	for _, p := range positions {
		unrealized += p.Amount - float64(p.Qty)*p.CostPrice
	}

	// 流水倒序输出最近 100 笔并附战法标签。
	// start 为窗口起点：全量超过 100 笔时只展示末尾 100 笔（新的在前）。
	outFills := make([]map[string]interface{}, 0, len(fills))
	start := 0
	if len(fills) > 100 {
		start = len(fills) - 100
	}
	for i := len(fills) - 1; i >= start; i-- {
		f := fills[i]
		// 标签策略：买入直接从 signal_id 解析；卖出沿用重放状态里的入场战法；兜底 manual。
		strat := stratState[f.Code]
		tag := "manual"
		if f.Side == "买入" {
			tag = qmtStrategyOf(f.SignalID)
		} else if strat != nil && strat.strategy != "" {
			tag = strat.strategy
		}
		outFills = append(outFills, map[string]interface{}{
			// §FILL-AMEND（2026-09-23）id 是勘误入口的定位锚：前端提交勘误只带 fill_id，
			// 锚点（trade_id/复合键）由服务端从原始行读出——不给 id 就没有逐笔改判入口。
			"id":       f.ID,
			"order_id": f.OrderID, "code": f.Code, "side": f.Side, "price": f.Price,
			"qty": f.Qty, "amount": f.Amount, "traded_at": f.TradedAt,
			"signal_id": f.SignalID, "strategy": tag,
			// §F1：费用腿回显——此前 trades 含费重算但流水不展示 fee/stamp_tax，
			// 前端对不上账时无从核对；RealFills 现已带回两列。
			"fee": f.Fee, "stamp_tax": f.StampTax,
			// §FILL-AMEND 勘误回显：side 已是**生效方向**（RealFills 走 fills_effective 视图），
			// orig_side 保留柜台原始方向；amended=true 时前端把这一行标成"人工改判"，
			// 避免下一个读账的人按原始方向去核对柜台回单却找不到差异来源。
			"orig_side":    f.OrigSide,
			"amended":      f.AmendID > 0,
			"amend_id":     f.AmendID,
			"amend_reason": f.AmendReason,
			"amend_key":    f.AmendKey,
			"trade_id":     f.TradeID,
		})
	}

	// 按战法名排序输出统计列表（含胜率与已实现盈亏）。
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
