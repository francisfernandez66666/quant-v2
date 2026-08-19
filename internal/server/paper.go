package server

import (
	"encoding/json"
	"net/http"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
)

// paperEngine 返回注入的模拟盘引擎；未注入时返回 nil。
// English: returns the injected paper engine; nil when not injected.
func (s *Server) paperEngine() *paper.Engine { return s.paper }

// handlePaperState 返回模拟盘开关与绩效/信号质量汇总。
// English: returns the paper master state (enabled flag) plus performance/signal-quality stats.
func (s *Server) handlePaperState(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
	if pe == nil {
		writeJSON(w, 200, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"enabled": pe.Enabled(),
		"stats":   pe.Stats(),
	})
}

// handlePaperPositions 返回模拟盘当前持仓（含实时估值价/浮盈/滑点参照）。
// English: returns paper positions (with live mark price, floating P/L and signal-price reference).
func (s *Server) handlePaperPositions(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
	if pe == nil {
		writeJSON(w, 200, []paper.Position{})
		return
	}
	writeJSON(w, 200, pe.Positions())
}

// handlePaperTrades 返回模拟盘成交记录（最新在前）。
// English: returns paper fills (newest first).
func (s *Server) handlePaperTrades(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
	if pe == nil {
		writeJSON(w, 200, []paper.Trade{})
		return
	}
	writeJSON(w, 200, pe.Trades())
}

// handlePaperEquity 返回模拟盘净值曲线。
// English: returns the paper equity curve.
func (s *Server) handlePaperEquity(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
	if pe == nil {
		writeJSON(w, 200, []paper.EquityPoint{})
		return
	}
	writeJSON(w, 200, pe.Equity())
}

// handlePaperSell 手动按实时价卖出指定模拟持仓（清仓）。请求体 {"code":"600000.SH"}。
// English: manually sells a paper position at the live price. Body: {"code":"600000.SH"}.
func (s *Server) handlePaperSell(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
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
	// 实时行情：优先 5s 快照，缺失降级单票拉取
	quotes := make(map[string]*data.StockInfo)
	if f := s.fetcher; f != nil {
		if snap := f.Snapshot(); snap != nil && snap.Stocks != nil {
			for c, q := range snap.Stocks {
				quotes[c] = q
			}
		}
	}
	if _, ok := quotes[req.Code]; !ok && s.dc != nil {
		if info, err := s.dc.GetQuote(req.Code); err == nil && info != nil {
			quotes[req.Code] = info
		}
	}
	if err := pe.Sell(req.Code, quotes); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handlePaperReset 清盘模拟盘（按最后估值价平仓全部持仓，重置现金/成交/净值）。
// English: liquidates the paper book at the last mark and resets cash/trades/equity.
func (s *Server) handlePaperReset(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngine()
	if pe == nil {
		writeError(w, 400, "模拟盘未启用")
		return
	}
	pe.Reset()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}