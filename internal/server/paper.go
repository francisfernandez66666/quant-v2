package server

import (
	"encoding/json"
	"net/http"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
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
// admin 账户的模拟盘额外支持回测与自动化交易联动）。
// English: returns the paper master state (enabled) plus performance/signal-quality stats, with the
// account role flag (the admin account's paper additionally supports backtest + auto-trade linkage).
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

// handlePaperBuy 手动按实时价买入一只股票（前端/APK 信号页"模拟买入"）。请求体：
// {"code":"600000.SH","name":"浦发银行","strategy":"N形","signal_price":9.8}。
// English: manually buys one stock at the live price (frontend/APK signal-page "paper buy").
// Body: {"code":"600000.SH","name":"浦发银行","strategy":"N形","signal_price":9.8}.
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	quotes := s.liveQuotes(req.Code)
	if err := pe.Buy(req.Code, req.Name, req.Strategy, req.SignalPrice, quotes); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
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

// handlePaperSell 手动按实时价卖出指定模拟持仓（清仓）。请求体 {"code":"600000.SH"}。
// English: manually sells a paper position at the live price. Body: {"code":"600000.SH"}.
func (s *Server) handlePaperSell(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil || !pe.Enabled() {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, 400, "缺少股票代码")
		return
	}
	if err := pe.Sell(req.Code, s.liveQuotes(req.Code)); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handlePaperReset 重置模拟盘。区分两种语义（联动版前端两个按钮）：
//   - 请求体带 {"initial_capital":N}（>0）→ 确认资金：Reconfigure 设置新初始资金/持仓上限，
//     清空当前持仓、净值从新资金重开，**保留成交日志**（历史固化不丢）。
//   - 请求体不带/为 0 → 清盘重置：Reset 只清空重开（持仓/成交/净值），不改自定义资金与上限。
//     可选 {"max_positions":N} 自定义持仓上限（>=0 生效；0=不设限，由资金自然决定）。
//
// English: resets the paper book. Two semantics (matching the two frontend buttons):
//   - body with {"initial_capital":N} (>0) → confirm capital: Reconfigure applies the new starting
//     capital / position cap, clears positions, restarts the equity curve from the new capital, and
//     **keeps the fill log** (history survives a capital change).
//   - body absent / zero → liquidate: Reset just reopens the book (positions/trades/equity cleared)
//     without changing the user's customized capital or cap.
//     Optional {"max_positions":N} customizes the position cap (applies when >= 0; 0 = unlimited).
func (s *Server) handlePaperReset(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(requestUserID(r))
	if pe == nil {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	var req struct {
		InitialCapital float64 `json:"initial_capital"`
		MaxPositions   int     `json:"max_positions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.InitialCapital > 0 {
		// 确认资金：设新资金/上限，保留成交日志，净值从新资金重开
		pe.Reconfigure(req.InitialCapital, req.MaxPositions)
	} else {
		// 清盘重置：不改资金/上限，仅清空重开
		pe.Reset()
		if req.MaxPositions >= 0 && req.InitialCapital == 0 && req.MaxPositions > 0 {
			pe.SetMaxPositions(req.MaxPositions)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":             true,
		"initial_capital": pe.Cfg().InitialCapital,
		"max_positions":  pe.Cfg().MaxPositions,
	})
}