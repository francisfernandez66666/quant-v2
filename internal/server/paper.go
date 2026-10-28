// Package server HTTP API 服务器：为前端/网关提供 REST 接口、SSE 推送、量化研究、模拟盘、QMT 回报等路由。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// paperEngine 返回注入的全局模拟盘引擎（旧单引擎回退；未注入时返回 nil）。
// English: returns the injected global paper engine (legacy single-engine fallback; nil when not set).
func (s *Server) paperEngine() *paper.Engine { return s.paper }

// paperEngineFor 返回**请求者账号**的模拟盘引擎。
//
// §FIX-20261028-F01（2026-10-29）：此函数此前忽略入参、恒取 `s.operatorID()` 的纸面账，
// 导致任何普通账号打开模拟盘页看到的都是运营账号的持仓与成交（跨账号读面泄漏，UAT 实弹实录：
// 新建成品号 GET /api/paper/positions 拿到运营账号 163 行纸面仓）。
// 现按入参取；只有 uid 为空（内部调用/未登录装配）才回落到运营账号，保证"没有身份"的老调用点行为不变。
// 旧注释"模拟盘账本为系统级共享（运营账本）"已随本条修复废弃——账本本就按账号隔离
// （registry.PaperForUser 每账号独立 paper.json），是这一行把路由写死成了运营账号。
// English: returns the requester's own paper engine; empty uid falls back to the operator book.
// The former "shared operator-scoped book" wording is obsolete: books were always per-account
// (registry.PaperForUser), and this single line used to hard-wire them to the operator.
func (s *Server) paperEngineFor(userID string) *paper.Engine {
	if s.registry != nil {
		if strings.TrimSpace(userID) == "" {
			userID = s.operatorID()
		}
		return s.registry.PaperForUser(userID)
	}
	return s.paperEngine()
}

// handlePaperState 返回模拟盘开关与绩效/信号质量汇总（含账号角色标记：
// admin 账户的模拟盘额外支持回测与自动化交易联动），并附带战法资金池快照（分仓余量）。
// English: returns the paper master state (enabled) plus performance/signal-quality stats, with the
// account role flag (the admin account's paper additionally supports backtest + auto-trade linkage),
// plus the strategy pool snapshot (allocation balances).
func (s *Server) handlePaperState(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	pe := s.paperEngineFor(uid)
	if pe == nil {
		writeJSON(w, 200, map[string]interface{}{"enabled": false}) // 未接入模拟盘引擎
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"enabled":         pe.Enabled(),            // 模拟盘总开关
		"is_admin":        s.auth.IsAdmin(uid),     // 账号角色（admin 额外支持回测/自动交易联动）
		"stats":           pe.Stats(),              // 绩效/信号质量汇总（现金/净值/胜率等）
		"initial_capital": pe.Cfg().InitialCapital, // 初始资金（收益基准）
		"max_positions":   pe.Cfg().MaxPositions,   // 全局持仓上限
		"strategy_pools":  pe.StrategyPools(),      // 战法资金池快照（各池现金/持仓/上限）
		// §SHORT-3/决策⑤ 融券做空卡（负持仓/担保/利息/做空权益），enabled=false 时前端整卡隐藏。
		// English: §SHORT-3 margin-short card payload; the frontend hides it entirely when disabled.
		"short_book": pe.ShortBook(),
	})
}

// handlePaperPositions 返回模拟盘当前持仓（含实时估值价/浮盈/滑点参照）。
// English: returns paper positions (with live mark price, floating P/L and signal-price reference).
func (s *Server) handlePaperPositions(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeJSON(w, 200, []paper.Position{})
		return
	}
	// §纸面估值修复：持仓不在 5s 快照池（非跟踪股/盘后/重启后）时 Mark 可能为 0，前端现价显示 0.00、
	// 浮亏 -100% 失真。展示层兜底：Mark<=0 时先用实时行情（快照→单查），再无则回退成本价，
	// 保证 PnL 不虚报 -100%。不改持久化，仅响应层装配。
	// English: display-time mark fallback — a held code missing from the live snapshot (non-pool / after
	// hours / after restart) may carry Mark=0, which the frontend renders as 0.00 and -100% (false). Here
	// we backfill: try the live quote first (snapshot → single fetch), else fall back to the cost price
	// so PnL is never misreported as -100%. Response-layer only; persistence untouched.
	positions := pe.Positions()
	for i := range positions {
		if positions[i].Mark > 0 {
			continue
		}
		if si := s.quoteDisplay(positions[i].Code); si != nil && si.Price > 0 {
			positions[i].Mark = si.Price
		} else if positions[i].CostPrice > 0 {
			positions[i].Mark = positions[i].CostPrice
		}
	}
	writeJSON(w, 200, positions)
}

// handlePaperTrades 返回模拟盘成交记录（最新在前）。
// English: returns paper fills (newest first).
func (s *Server) handlePaperTrades(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeJSON(w, 200, []paper.Trade{})
		return
	}
	writeJSON(w, 200, pe.Trades())
}

// handlePaperOrders 返回模拟盘订单生命周期记录（阶段1.3：信号→订单→成交/拒绝 全留痕，最新在前）。
//
// §FIX-20261028-F13（2026-10-29）：单账号实测 616,493B / 2000 行且 `?limit` 被忽略
// （浏览器与 APK 每次打开模拟盘页都要搬 600KB）。这里做**传输层**分页：
//
//	· 响应体**保持裸数组**（前端 fetchPaperOrders → setOrders 直接按数组用，改形状会当场崩）；
//	· 元信息走响应头 X-Total-Count（跨域时由 CORS 的 Expose-Headers 放行）；
//	· limit 缺省 500、上限 2000；limit<=0 或 offset<0 一律 400（不静默改语义）。
//
// 引擎内存侧不动（paper.Engine.Orders 仍是全量复制+排序）——见 FIX_PLAN §十一 Q4。
// English: transport-layer paging that keeps the bare-array body and reports X-Total-Count.
func (s *Server) handlePaperOrders(w http.ResponseWriter, r *http.Request) {
	// 先把请求形态校验完再判断引擎：非法分页参数是**请求错**（400），
	// 不该因为"这台机器没接纸面引擎"就变成 200（那会让客户端以为参数被接受）。
	q := r.URL.Query()
	limit, offset := paperOrdersPageDefault, 0
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > paperOrdersPageMax {
			writeError(w, http.StatusBadRequest, "limit 必须是 1..2000 的整数")
			return
		}
		limit = n
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset 必须是非负整数")
			return
		}
		offset = n
	}
	// 总数始终照实回（即便本次切片为空）：前端据 X-Total-Count 判断"还有更早的记录"，
	// 缺了它分页就退化成"翻不到底"。
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		// 没有纸面引擎 ⇒ 空账本（合法），总数 0；形状仍是裸数组。
		w.Header().Set("X-Total-Count", "0")
		writeJSON(w, 200, []paper.Order{})
		return
	}
	all := pe.Orders()
	total := len(all)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	start, end := pageWindow(total, limit, offset)
	if start >= end {
		// offset 越过末尾 ⇒ 空数组（不是 404、也不是 400）：合法的"翻过了最后一页"。
		writeJSON(w, 200, []paper.Order{})
		return
	}
	writeJSON(w, 200, all[start:end])
}

// pageWindow 把 (total, limit, offset) 归一成可安全切片的 [start,end)。
//
// 抽成纯函数是为了让边界（末页收口、offset 越界）能被单测直接钉住——
// 写在 handler 里就只能靠"造 2000 条订单"来测，成本高且容易假绿。
func pageWindow(total, limit, offset int) (start, end int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = paperOrdersPageDefault
	}
	start = offset
	if start > total {
		start = total
	}
	end = start + limit
	if end > total {
		end = total
	}
	return start, end
}

// 分页缺省值与上限（§FIX-20261028-F13）。
// 取值理由：2000 是现有单账号的实测上限（约 600KB），缺省 500 把首屏压到 <200KB；
// 上限与"limit 必须 1..2000"的报错文案必须同值，改一个就要改另一个。
const (
	paperOrdersPageDefault = 500
	paperOrdersPageMax     = 2000
)

// handlePaperEquity 返回模拟盘净值曲线。
// English: returns the paper equity curve.
func (s *Server) handlePaperEquity(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeJSON(w, 200, []paper.EquityPoint{})
		return
	}
	writeJSON(w, 200, pe.Equity())
}

// handlePaperSelfCheck 模拟盘自检接口：汇总"为什么持仓/成交/订单/净值可能为空"的诊断信息，
// 便于前端一键排查与后端日志对齐。GET /api/paper/selfcheck。
// English: paper self-check — aggregates diagnostics for why positions/trades/orders/equity may be
// empty, so the frontend can one-click troubleshoot and align with backend logs. GET /api/paper/selfcheck.
func (s *Server) handlePaperSelfCheck(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	pe := s.paperEngineFor(uid)
	if pe == nil {
		writeJSON(w, 200, map[string]interface{}{
			"enabled":       false,
			"engine":        "nil",
			"is_admin":      s.auth.IsAdmin(uid),
			"has_filled":    false,
			"positions":     0,
			"trades":        0,
			"orders":        0,
			"equity_points": 0,
			"pools":         0,
			"engine_path":   "",
			"file_exists":   false,
			"pool_detail":   []map[string]interface{}{},
			"note":          "模拟盘引擎未初始化（注册表/全局引擎均为空），所有持仓/成交/订单/净值均为空属正常",
		})
		return
	}
	path := pe.Path()
	// 引擎健康信息：开关/权限/持仓数/资金池数等概览。
	info := map[string]interface{}{
		"enabled":       pe.Enabled(),
		"is_admin":      s.auth.IsAdmin(uid),
		"engine_path":   path,
		"has_filled":    pe.HasFilled(),
		"positions":     len(pe.Positions()),
		"trades":        len(pe.Trades()),
		"orders":        len(pe.Orders()),
		"equity_points": len(pe.Equity()),
		"pools":         len(pe.StrategyPools()),
	}
	// paper.json 物理文件状态
	if path != "" {
		if st, err := os.Stat(path); err == nil {
			info["file_exists"] = true
			info["file_size"] = st.Size()
			info["file_mtime"] = st.ModTime().Format(time.RFC3339)
		} else {
			info["file_exists"] = false
			info["file_error"] = err.Error()
		}
	}
	// 汇总各资金池持仓数（用于核对"全部"tab 是否漏算）
	poolSummary := make([]map[string]interface{}, 0)
	for _, p := range pe.StrategyPools() {
		poolSummary = append(poolSummary, map[string]interface{}{
			"strategy":  p.Key,
			"name":      p.Label,
			"positions": p.Positions,
			"cash":      p.Cash,
		})
	}
	info["pool_detail"] = poolSummary
	log.Printf("[paper-selfcheck] uid=%s admin=%v enabled=%v has_filled=%v positions=%d trades=%d orders=%d equity=%d file=%s exists=%v",
		uid, s.auth.IsAdmin(uid), pe.Enabled(), pe.HasFilled(), len(pe.Positions()), len(pe.Trades()), len(pe.Orders()), len(pe.Equity()), path, info["file_exists"])
	writeJSON(w, 200, info)
}

// handlePaperBuy 手动买入一只股票（前端信号页/持仓页"模拟买入/加仓"）。请求体：
// {"code":"600000.SH","name":"浦发银行","strategy":"N形","signal_price":9.8,"price":9.5,"qty":1000}。
// §FIX-1(20260919) 单位收敛：qty 对外唯一口径=股数（引擎按股记账，见 paper_test.go:682 契约注释），
// 手数→股数的换算只发生在前端提交前（Paper.jsx/Signals.jsx 各一处 ×100）。
//   - qty > 0：按用户输入的买入股数撮合（须为 100 股整数倍，对齐实盘整手纪律），price > 0 时按用户
//     输入价成交（静态记账），price = 0 时回退实时价——普通用户"搬运持仓"记账场景。
//   - qty <= 0：回退固定金额（FixedAmount）整手买入（旧行为，实时价成交）。
//
// English: manually buys one stock (frontend/APK signal page or positions page "paper buy/add").
// Body: {"code":"600000.SH",...,"price":9.5,"qty":1000}. §FIX-1: qty is in SHARES (the engine books
// shares; lots→shares conversion happens once in the frontend before submit).
//   - qty > 0: fills the typed share count (must be a whole multiple of 100); price > 0 fills at the
//     typed price (static bookkeeping), price = 0 falls back to the live quote — the "copy real
//     positions" scenario for normal users.
//   - qty <= 0: legacy fixed-amount whole-lot buy at the live price.
func (s *Server) handlePaperBuy(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Strategy    string  `json:"strategy"`
		SignalPrice float64 `json:"signal_price"` // 原信号触发价（供信号质量/滑点统计）
		Price       float64 `json:"price"`        // 用户输入的买入价（>0 生效）
		Qty         int     `json:"qty"`          // 用户输入的买入股数（>0 生效且须 100 整数倍；<=0 回退固定金额）
		// §C 归属字段：信号页模拟买入携带原信号的战法池/库规则 ID，
		// 买入归入对应资金池（非空且池存在时）；纯手动不传 → 其他池（旧行为）。
		StrategyType string `json:"strategy_type,omitempty"`
		StrategyID   string `json:"strategy_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	// §FIX-1(20260919) 整手纪律闸：显式股数必须是 100 的整数倍，防止把"手数"当"股数"提交后
	// 以 1/100 规模建仓（历史 P0：UI 传手数、引擎按股记账导致账本 100 倍错位）。
	// English: explicit share counts must be whole lots; this blocks the historic lots-vs-shares P0.
	if req.Qty > 0 && req.Qty%100 != 0 && !strings.EqualFold(strings.TrimSpace(pe.Cfg().BudgetMode), "dynamic") {
		writeError(w, 400, "买入数量需为 100 股整数倍（1 手 = 100 股）")
		return
	}
	// 归属解析：规则 ID（fac_/pat_ 前缀）优先即池 key；否则用类型字段；
	// 都为空 = 纯手动 → 其他池。引擎侧对不存在的池还会二次回退兜底。
	// 前缀切片判断安全前提：len>=4 已先检查，避免对短 ID 越界。
	poolKey := req.StrategyType
	if len(req.StrategyID) >= 4 && (req.StrategyID[:4] == "fac_" || req.StrategyID[:4] == "pat_") {
		poolKey = req.StrategyID
	}
	quotes := s.liveQuotes(req.Code) // 行情快照（撮合价基准），一次构造供买入使用
	// §DYN-SIZE-v3：dynamic 模式下手动买入不再接受手填股数，统一走动态仓位（qty 字段忽略）。
	// English: in v3 dynamic mode manual buys ignore the typed qty and always use dynamic sizing.
	if strings.EqualFold(strings.TrimSpace(pe.Cfg().BudgetMode), "dynamic") {
		if err := pe.BuyInPool(req.Code, req.Name, req.Strategy, poolKey, req.SignalPrice, quotes); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	} else if req.Qty > 0 {
		// 输入价格+股数：按用户指定记账（price=0 时用实时价，仍按指定股数）
		// English: typed price + share count: fills as specified (price=0 falls back to the live quote
		// but still respects the typed share count).
		if err := pe.BuyExInPool(req.Code, req.Name, req.Strategy, poolKey, req.SignalPrice, req.Price, req.Qty, quotes); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	} else {
		// 旧行为：固定金额整手，实时价成交
		if err := pe.BuyInPool(req.Code, req.Name, req.Strategy, poolKey, req.SignalPrice, quotes); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// ExportPaperToResearch 盘后把某账号模拟盘的当日成交与每日快照导出到研究库（供自动研究消费）。
// 由 main 注入 registry.SetDayCloseExport，注册表每日盘后触发；幂等由 store 唯一键保证。
// 普通用户（非自动撮合账号）不参与（isAutoPaper 过滤已在上游完成）。
// 返回值 = 两笔写入是否都真正落库：false 时注册表不记"今日已导出"，下一轮维护分支会重试
// （§N6：此前无返回值，写库失败也被当成成功，一次抖动就丢一整天数据）。
// English: exports an account's paper fills + daily snapshot into the research DB after the close
// (for auto-research). Wired by main into registry.SetDayCloseExport and fired by the registry;
// idempotency is guaranteed by store unique keys. Returns true only when both writes landed — the
// registry keeps retrying on failure instead of marking the day done.
func (s *Server) ExportPaperToResearch(userID string, pe *paper.Engine) bool {
	if s.researchDB == nil {
		return false
	}
	// 当日成交 → paper_trades（INSERT OR IGNORE，同一笔不重复入库）
	// English: the day's fills → paper_trades (INSERT OR IGNORE, never duplicated).
	trades := pe.Trades()
	recs := make([]store.PaperTradeRecord, 0, len(trades))
	for _, t := range trades {
		recs = append(recs, store.PaperTradeRecord{
			UserID:       userID,
			Code:         t.Code,
			Name:         t.Name,
			Strategy:     t.Strategy,
			StrategyType: t.StrategyType,
			Side:         t.Side,
			Price:        t.Price,
			SignalPrice:  t.SignalPrice,
			LatencySec:   float64(t.LatencySec),
			Qty:          t.Qty,
			Amount:       t.Amount,
			FilledAt:     t.Time.Format("2006-01-02 15:04:05"),
			Reason:       t.Reason,
		})
	}
	if err := s.researchDB.SavePaperTrades(recs); err != nil {
		log.Printf("[paper] 盘后导出成交失败 user=%s: %v", userID, err)
		return false
	}
	// 当日快照 → paper_daily（现金/市值/净值/已实现/持仓数）
	// English: the daily snapshot → paper_daily (cash/market value/equity/realized/positions).
	st := pe.Stats()
	now := time.Now()
	if err := s.researchDB.SavePaperDaily(store.PaperDailyRecord{
		UserID:      userID,
		Date:        now.Format("2006-01-02"),
		Cash:        st.Cash,
		MarketValue: st.MarketValue,
		TotalValue:  st.TotalValue,
		Realized:    st.RealizedPnl,
		Positions:   st.OpenPositions,
	}); err != nil {
		log.Printf("[paper] 盘后导出快照失败 user=%s: %v", userID, err)
		return false
	}
	log.Printf("[paper] 盘后导出研究库 user=%s 成交=%d 快照现金=%.2f 净值=%.2f",
		userID, len(recs), st.Cash, st.TotalValue)
	return true
}

// liveQuotes 构造实时行情表：优先 5s 快照，缺失时对指定代码降级单票拉取。
// English: builds a live quote map — the 5s snapshot first, falling back to a one-off pull for the code.
func (s *Server) liveQuotes(code string) map[string]*data.StockInfo {
	quotes := make(map[string]*data.StockInfo)
	if f := s.fetcher; f != nil {
		if snap := f.Snapshot(); snap != nil && snap.Stocks != nil {
			for c, q := range snap.Stocks {
				quotes[c] = q
			}
		}
	}
	if _, ok := quotes[code]; !ok && s.dc != nil {
		if info, err := s.dc.GetQuote(code); err == nil && info != nil {
			quotes[code] = info
		}
	}
	return quotes
}

// handlePaperSell 手动卖出指定模拟持仓。请求体 {"code":"600000.SH","price":9.5,"qty":500}：
// §FIX-1(20260919) 单位收敛：qty 对外唯一口径=股数（同 handlePaperBuy）。
//   - qty > 0：按用户输入股数减仓（price > 0 用输入价，price = 0 回退实时价）；数量 >= 持仓=清仓。
//     部分减仓（qty < 持仓）须为 100 股整数倍，否则拒绝（不留零股）；清仓允许零股（尾仓随仓平掉）。
//   - qty <= 0：清仓（实时价，旧行为）。
//
// English: manually sells a paper position. Body {"code":"600000.SH","price":9.5,"qty":500}:
//   - qty > 0: trims the typed share count (price > 0 uses the typed price, price = 0 falls back to
//     the live quote); qty >= the position closes it. A partial trim must be whole lots; zero-lot
//     remainders are only allowed on a full close.
//   - qty <= 0: closes the position at the live price (legacy behavior).
func (s *Server) handlePaperSell(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Code  string  `json:"code"`
		Price float64 `json:"price"` // 用户输入的卖出价（>0 生效）
		Qty   int     `json:"qty"`   // 用户输入的减仓股数（>0 生效；<=0 清仓）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	// §DYN-TRIM（2026-10-24 owner 裁决）：**任何预算口径下，qty>0 都是真实减仓，不再退化成清仓**。
	// 改前的两处坑（UAT U-2/U-4 原案）：dynamic 口径下这一支直接调 Sell() 把 qty 丢掉（点"减仓"
	// 结果整仓清掉），而零股守卫在 dynamic 下又被跳过——于是"想减仓"既不可控也不设防。现在：
	// qty>0 ⇒ 按股数减仓（SellEx），qty<=0 ⇒ 清仓（Sell）；零股纪律两种口径都生效。
	// 持仓量在卖出前取一次：SellEx 会把请求量封顶到可卖量，事后再读就分不清"成交了多少"。
	// English: §DYN-TRIM — under every budget mode a positive qty is a real partial trim (SellEx), only
	// qty<=0 closes. The pre-fix code dropped qty entirely in dynamic mode (a "trim" click closed the
	// whole position) and skipped the odd-lot guard there too.
	held, sellable := 0, 0
	for _, p := range pe.Positions() {
		if p.Code == req.Code {
			held = p.Qty
		}
	}
	if held <= 0 {
		writeError(w, 400, "未持有该标的")
		return
	}
	sellable = pe.SellableQty(req.Code)
	if req.Qty > 0 && req.Qty%100 != 0 && req.Qty < held {
		writeError(w, 400, "减仓数量需为 100 股整数倍（1 手 = 100 股），零股请清仓卖出")
		return
	}
	// 请求量超过持仓＝清仓形态（允许零股尾仓随仓平掉，与 §FIX-1 口径一致）。
	trimQty := req.Qty
	if trimQty > held {
		trimQty = held
	}
	var err error
	if req.Qty > 0 {
		err = pe.SellEx(req.Code, req.Price, trimQty, s.liveQuotes(req.Code))
	} else {
		err = pe.Sell(req.Code, s.liveQuotes(req.Code))
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// 成交后重读：sold = 卖出前持仓 − 卖出后持仓（整仓清掉时为全量，不依赖 SellEx 的返回值）。
	left := 0
	for _, p := range pe.Positions() {
		if p.Code == req.Code {
			left = p.Qty
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":        true,
		"sold":      held - left,
		"remaining": left,
		// sellable 是**卖出前**的可卖量：前端据此说明"填的量为什么被截断"（T+1 锁着当日买入的批次）。
		"sellable": sellable,
	})
}

// handlePaperShortOpen §SHORT-4 手动融券开仓：{"code","name","strategy","price"}；price>0 用指定价，
// 0=实时价。做空池未开设/已持空/同日重复均返回中文错误。
// English: manual short-open (margin sell). Body code/name/strategy/price (0 = live quote).
func (s *Server) handlePaperShortOpen(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Code     string  `json:"code"`
		Name     string  `json:"name"`
		Strategy string  `json:"strategy"`
		Price    float64 `json:"price"` // 用户指定开仓价（>0 生效；0=实时价）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	q := s.liveQuotes(req.Code) // 行情快照（做空预算/开仓 proxy 依赖实时价）
	if req.Price > 0 && q[req.Code] != nil {
		// 用户指定开仓价：直接改写快照里该票的价格供引擎按指定价撮合；
		// 快照缺失该票时不伪造，交由引擎侧拒绝（缺实时价时引擎自行处理）。
		q[req.Code].Price = req.Price // 用户指定开仓价（缺实时价时引擎侧自行拒绝）
	}
	// ShortOpenManual 返回实际开仓手数（负持仓量）；失败（池未开/已持空/同日重复）返回中文错误。
	qty, err := pe.ShortOpenManual(req.Code, req.Name, req.Strategy, "", req.Price, q)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "qty": qty}) // qty=开仓手数回显
}

// handlePaperShortCover §SHORT-4 融券买回平仓：{"code","price"}；price>0 用指定价（缺实时价也可），
// 0=按实时价。T+1 未解禁/无空头均返回中文错误。
// English: manual buy-to-cover. Body code/price (0 = live quote); T+1 and no-short return errors.
func (s *Server) handlePaperShortCover(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Code  string  `json:"code"`
		Price float64 `json:"price"` // 回买价（>0 生效；0=按实时价撮合）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	// ShortCover 执行买回平仓：触发 T+1 解禁校验，按剩余空头结算已实现盈亏。
	pnl, err := pe.ShortCover(req.Code, req.Price, s.liveQuotes(req.Code))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "realized": pnl}) // realized=本次平仓已实现盈亏
}

// handlePaperPoolReset 单池清盘：只清指定战法资金池的持仓与持久化表现（平仓回池现金），
// 其余池与全局净值/成交日志不受影响。请求体 {"pool":"n_shape"}（空 pool 清"其他池"）。
// 对应前端分仓 tab 上的"清盘本池"按钮。
// English: resets a single strategy pool — closes that pool's positions (proceeds return to the pool)
// and zeroes its persisted cost/realized, leaving other pools and the global equity/fill log untouched.
// Body {"pool":"n_shape"} (empty pool = the "other" pool). Backs the "清盘本池" button on a pool tab.
func (s *Server) handlePaperPoolReset(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Pool string `json:"pool"` // 池 key（n_shape 等）；空串=清"其他池"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	pe.ResetPool(req.Pool) // 引擎侧只清该池：平仓回池现金、清池级持久化，其余池不动
	writeJSON(w, 200, map[string]interface{}{"ok": true, "pool": req.Pool})
}

// handlePaperPoolConfig 设置分仓池级自定义（资金分配 + 每池持仓上限），与全局持仓上限/资金解耦。
// 请求体 {"max_positions":N, "pool_caps":{"n_shape":10,...}, "pool_allocs":{"n_shape":50000,...}}：
//   - max_positions ≥0：更新全局持仓上限（0=不设限）。
//   - pool_caps：每池持仓上限（n<=0=该池不单独设限）；Σ池上限 ≤ 全局上限由前端守恒校验。
//   - pool_allocs：每池目标资金额（>0 生效）；SetPoolAllocs 保证 Σ池现金=总现金（守恒）。
//
// English: sets pool-level customization (per-pool cash allocation + per-pool position caps), decoupled
// from the global cap/capital. Body {"max_positions":N, "pool_caps":{...}, "pool_allocs":{...}}:
//   - max_positions ≥0 updates the global position cap (0 = unlimited).
//   - pool_caps set per-pool position caps (n<=0 = no per-pool limit); Σpool caps ≤ the global cap is
//     conserved by the frontend.
//   - pool_allocs set per-pool target cash (>0 applies); SetPoolAllocs keeps Σpool cash = total cash.
func (s *Server) handlePaperPoolConfig(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	// §反馈解耦：三个字段按**是否出现在请求体**独立生效——资金分配与仓位上限
	// 拆成两个前端弹窗后互不牵连（旧实现"未传 allocs 就恢复均分"会把只改上限
	// 的操作误清自定义资金，2026-08-24 用户实录）。
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	if v, ok := raw["max_positions"]; ok {
		var mp int
		if json.Unmarshal(v, &mp) == nil && mp >= 0 {
			pe.SetMaxPositions(mp)
		}
	}
	if v, ok := raw["pool_caps"]; ok {
		var caps map[string]int
		if json.Unmarshal(v, &caps) == nil && len(caps) > 0 {
			pe.SetPoolCaps(caps)
		}
	}
	if v, ok := raw["pool_rules"]; ok {
		// 池级买入规则：每池日内次数/冷却分钟/最低分/每日预算百分比。
		// 四项全空视为"清除该池规则"（SetPoolBuyRule(pk, nil)），否则逐池写入。
		var rules map[string]struct {
			MaxDailyBuys    int     `json:"max_daily_buys"`     // 该池每日最大买入笔数
			CooldownMinutes int     `json:"cooldown_minutes"`   // 同票冷却分钟数
			MinScore        float64 `json:"min_score"`          // 触发买入的最低评分
			BudgetPctPerDay float64 `json:"budget_pct_per_day"` // 每日预算占池现金百分比
		}
		if json.Unmarshal(v, &rules) == nil {
			for pk, r := range rules {
				// 任意一项 >0 即视为有效规则；全 0 = 清除该池自定义规则（回落全局口径）。
				if r.MaxDailyBuys > 0 || r.CooldownMinutes > 0 || r.MinScore > 0 || r.BudgetPctPerDay > 0 {
					pe.SetPoolBuyRule(pk, &paper.PoolBuyRule{
						MaxDailyBuys:    r.MaxDailyBuys,
						CooldownMinutes: r.CooldownMinutes,
						MinScore:        r.MinScore,
						BudgetPctPerDay: r.BudgetPctPerDay,
					})
				} else {
					pe.SetPoolBuyRule(pk, nil) // 清除规则
				}
			}
		}
	}
	if v, ok := raw["pool_allocs"]; ok {
		var allocs map[string]float64
		if json.Unmarshal(v, &allocs) == nil {
			if len(allocs) > 0 {
				pe.SetPoolAllocs(allocs)
			} else {
				pe.ResetPoolAllocs() // 显式传空对象=清除自定义恢复均分
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handlePaperReset 重置/注入模拟盘。区分两种语义（联动版前端两个按钮）：
//   - 请求体带 {"initial_capital":N}（>0）→ 注入资金：Deposit 增量加现金，按池占比分配，
//     **保留现有持仓/净值/成交日志**，收益基准（累计投入）同步增加——与真实持仓一致，不清仓。
//     可选 {"max_positions":N} 自定义持仓上限（>=0 生效；0=不设限）。
//   - 请求体不带/为 0 → 清盘重置：Reset 只清空重开（持仓/成交/净值），不改自定义资金与上限。
//
// English: deposits into / resets the paper book. Two semantics (matching the two frontend buttons):
//   - body with {"initial_capital":N} (>0) → deposit: Deposit adds cash incrementally, distributed to the
//     pools by their share; **positions / equity / fill log are all kept** and the return basis
//     (cumulative investment) grows — just like the real book, nothing is cleared.
//     Optional {"max_positions":N} customizes the position cap (applies when >= 0; 0 = unlimited).
//   - body absent / zero → liquidate: Reset just reopens the book (positions/trades/equity cleared)
//     without changing the user's customized capital or cap.
func (s *Server) handlePaperReset(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		InitialCapital float64 `json:"initial_capital"` // >0 = 注入资金模式
		MaxPositions   int     `json:"max_positions"`   // 可选持仓上限（注入模式 >=0 生效；清盘模式 >0 生效）
		ResetTo        float64 `json:"reset_to"`        // §反馈修复：清盘时显式指定重置后的初始资金
	}
	// §0926E2E-W1D：清盘/注入是破坏性动作——畸形 JSON 不再吞成零值走默认清盘分支，400 中止。
	// 空体仍合法（三种语义见下方分支：缺省/注入/清盘）。
	if err := decodeOptJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body: "+err.Error())
		return
	}
	if req.InitialCapital > 0 {
		// 注入资金：增量加现金，保留持仓/净值/成交
		pe.Deposit(req.InitialCapital)
		// MaxPositions >=0 都生效（0=不设限）；负值视为未携带，不改上限。
		if req.MaxPositions >= 0 {
			pe.SetMaxPositions(req.MaxPositions)
		}
	} else {
		// §反馈修复 v3：普通清盘把 InitialCapital 重置为干净默认 10 万——
		// 多次 Deposit 会累加污染 cfg.InitialCapital（10→300万），导致清盘后从
		// 错误基数起跳。用户可通过 reset_to 显式指定其他金额。
		resetAmount := 100000.0
		if req.ResetTo > 0 {
			resetAmount = req.ResetTo // 用户显式指定的重置金额优先
		}
		pe.SetInitialCapital(resetAmount) // 先定新基准，再清盘（Reset 只清持仓/成交/净值）
		pe.Reset()
		// 清盘模式仅 >0 才改上限（0 会把上限清成"不设限"，与注入模式口径不同）。
		if req.MaxPositions > 0 {
			pe.SetMaxPositions(req.MaxPositions)
		}
	}
	// 回显落库后的最终配置，前端据此刷新资金/上限显示。
	writeJSON(w, 200, map[string]interface{}{
		"ok":              true,
		"initial_capital": pe.Cfg().InitialCapital,
		"max_positions":   pe.Cfg().MaxPositions,
	})
}
