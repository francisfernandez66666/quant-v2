package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// paperEngine 返回注入的全局模拟盘引擎（旧单引擎回退；未注入时返回 nil）。
// English: returns the injected global paper engine (legacy single-engine fallback; nil when not set).
func (s *Server) paperEngine() *paper.Engine { return s.paper }

// paperEngineFor 返回指定账号的独立模拟盘引擎（账户级）：优先走注册表按账号懒加载，
// 未接入注册表时回退全局引擎。
// English: returns an account's independent paper engine (account-level): prefers the registry's
// per-account lazy-load, falling back to the global engine without a registry.
func (s *Server) paperEngineFor(userID string) *paper.Engine {
	if s.registry != nil {
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
		writeJSON(w, 200, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"enabled":         pe.Enabled(),
		"is_admin":        s.auth.IsAdmin(uid),
		"stats":           pe.Stats(),
		"initial_capital": pe.Cfg().InitialCapital,
		"max_positions":   pe.Cfg().MaxPositions,
		"strategy_pools":  pe.StrategyPools(),
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
	writeJSON(w, 200, pe.Positions())
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
// English: returns paper order-lifecycle records (signal→order→outcome audit, newest first).
func (s *Server) handlePaperOrders(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeJSON(w, 200, []paper.Order{})
		return
	}
	writeJSON(w, 200, pe.Orders())
}

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

// handlePaperBuy 手动买入一只股票（前端信号页/持仓页"模拟买入/加仓"）。请求体：
// {"code":"600000.SH","name":"浦发银行","strategy":"N形","signal_price":9.8,"price":9.5,"qty":10}。
//   - qty > 0：按用户输入的买入手数（10=10 手=1000 股）撮合，price > 0 时按用户输入价成交（静态记账），
//     price = 0 时回退实时价——普通用户"搬运持仓"记账场景。
//   - qty <= 0：回退固定金额（FixedAmount）整手买入（旧行为，实时价成交）。
//
// English: manually buys one stock (frontend/APK signal page or positions page "paper buy/add").
// Body: {"code":"600000.SH","name":"浦发银行","strategy":"N形","signal_price":9.8,"price":9.5,"qty":10}.
//   - qty > 0: fills the typed lot count (10 = 10 lots = 1000 shares); price > 0 fills at the typed
//     price (static bookkeeping), price = 0 falls back to the live quote — the "copy real positions"
//     scenario for normal users.
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
		SignalPrice float64 `json:"signal_price"`
		Price       float64 `json:"price"` // 用户输入的买入价（>0 生效）
		Qty         int     `json:"qty"`   // 用户输入的买入手数（>0 生效；<=0 回退固定金额）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	quotes := s.liveQuotes(req.Code)
	if req.Qty > 0 {
		// 输入价格+手数：按用户指定记账（price=0 时用实时价，仍按指定手数）
		// English: typed price + lots: fills as specified (price=0 falls back to the live quote but
		// still respects the typed lot count).
		if err := pe.BuyEx(req.Code, req.Name, req.Strategy, req.SignalPrice, req.Price, req.Qty, quotes); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	} else {
		// 旧行为：固定金额整手，实时价成交
		if err := pe.Buy(req.Code, req.Name, req.Strategy, req.SignalPrice, quotes); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// ExportPaperToResearch 盘后把某账号模拟盘的当日成交与每日快照导出到研究库（供自动研究消费）。
// 由 main 注入 registry.SetDayCloseExport，注册表每日盘后触发一次；幂等由 store 唯一键保证。
// 普通用户（非自动撮合账号）不参与（isAutoPaper 过滤已在上游完成）。
// English: exports an account's paper fills + daily snapshot into the research DB after the close
// (for auto-research). Wired by main into registry.SetDayCloseExport and fired once per day by the
// registry; idempotency is guaranteed by store unique keys. Normal (non-auto) accounts are filtered
// upstream.
func (s *Server) ExportPaperToResearch(userID string, pe *paper.Engine) {
	if s.researchDB == nil {
		return
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
		return
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
		return
	}
	log.Printf("[paper] 盘后导出研究库 user=%s 成交=%d 快照现金=%.2f 净值=%.2f",
		userID, len(recs), st.Cash, st.TotalValue)
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

// handlePaperSell 手动卖出指定模拟持仓。请求体 {"code":"600000.SH","price":9.5,"qty":5}：
//   - qty > 0：按用户输入数量减仓（price > 0 用输入价，price = 0 回退实时价）；数量 >= 持仓=清仓。
//   - qty <= 0：清仓（实时价，旧行为）。
//
// English: manually sells a paper position. Body {"code":"600000.SH","price":9.5,"qty":5}:
//   - qty > 0: trims the typed lot count (price > 0 uses the typed price, price = 0 falls back to the
//     live quote); qty >= the position closes it.
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
		Qty   int     `json:"qty"`   // 用户输入的减仓手数（>0 生效；<=0 清仓）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	if req.Qty > 0 {
		if err := pe.SellEx(req.Code, req.Price, req.Qty, s.liveQuotes(req.Code)); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	} else {
		if err := pe.Sell(req.Code, s.liveQuotes(req.Code)); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
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
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	pe.ResetPool(req.Pool)
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
		var rules map[string]struct {
			MaxDailyBuys    int     `json:"max_daily_buys"`
			CooldownMinutes int     `json:"cooldown_minutes"`
			MinScore        float64 `json:"min_score"`
			BudgetPctPerDay float64 `json:"budget_pct_per_day"`
		}
		if json.Unmarshal(v, &rules) == nil {
			for pk, r := range rules {
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
		InitialCapital float64 `json:"initial_capital"`
		MaxPositions   int     `json:"max_positions"`
		ResetTo        float64 `json:"reset_to"` // §反馈修复：清盘时显式指定重置后的初始资金
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.InitialCapital > 0 {
		// 注入资金：增量加现金，保留持仓/净值/成交
		pe.Deposit(req.InitialCapital)
		if req.MaxPositions >= 0 {
			pe.SetMaxPositions(req.MaxPositions)
		}
	} else {
		// §反馈修复 v3：普通清盘把 InitialCapital 重置为干净默认 10 万——
		// 多次 Deposit 会累加污染 cfg.InitialCapital（10→300万），导致清盘后从
		// 错误基数起跳。用户可通过 reset_to 显式指定其他金额。
		resetAmount := 100000.0
		if req.ResetTo > 0 {
			resetAmount = req.ResetTo
		}
		pe.SetInitialCapital(resetAmount)
		pe.Reset()
		if req.MaxPositions > 0 {
			pe.SetMaxPositions(req.MaxPositions)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":              true,
		"initial_capital": pe.Cfg().InitialCapital,
		"max_positions":   pe.Cfg().MaxPositions,
	})
}
