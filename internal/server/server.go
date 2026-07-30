// Package server HTTP 服务端：提供看板数据、策略配置、持仓管理、做空开关等 REST API。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
)

type Server struct {
	auth         *auth.Manager
	agg          *display.Aggregator
	cfg          *config.Manager
	rpt          *report.Report
	mux          *http.ServeMux
	shortEnabled bool
	market       *data.MarketAPI
	watchlist    *data.WatchlistManager
	sse          *SSEBroker
	startTime    time.Time
	llmRecreate  func(apiKey, apiURL, model string) // 热重建 LLM 客户端
	newsAgent    *newsagent.Agent
}

func (s *Server) ShortEnabled() bool { return s.shortEnabled }
func (s *Server) SetLLMRecreate(fn func(apiKey, apiURL, model string)) { s.llmRecreate = fn }
func (s *Server) SetNewsAgent(a *newsagent.Agent) { s.newsAgent = a }

func New(authMgr *auth.Manager, agg *display.Aggregator, cfg *config.Manager, rpt *report.Report, market *data.MarketAPI, wl *data.WatchlistManager) *Server {
	s := &Server{
		auth:      authMgr,
		agg:       agg,
		cfg:       cfg,
		rpt:       rpt,
		mux:       http.NewServeMux(),
		market:    market,
		watchlist: wl,
		sse:       NewSSEBroker(),
		startTime: time.Now(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) GetSSE() *SSEBroker { return s.sse }

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/temp", s.handleTemp)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /setup", s.handleSetupStatus)
	s.mux.HandleFunc("POST /setup", s.handleSetupSubmit)

	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
	s.mux.HandleFunc("POST /api/short/toggle", s.authMiddleware(s.handleShortToggle))
	s.mux.HandleFunc("GET /api/short/status", s.authMiddleware(s.handleShortStatus))
	s.mux.HandleFunc("GET /api/config/strategy", s.authMiddleware(s.handleGetStrategyConfig))
	s.mux.HandleFunc("POST /api/config/strategy", s.authMiddleware(s.handleSetStrategyConfig))
	s.mux.HandleFunc("GET /api/config/d1", s.authMiddleware(s.handleGetD1Config))
	s.mux.HandleFunc("POST /api/config/d1", s.authMiddleware(s.handleSetD1Config))
	s.mux.HandleFunc("GET /api/config/llm", s.authMiddleware(s.handleGetLLMConfig))
	s.mux.HandleFunc("POST /api/config/llm", s.authMiddleware(s.handleSetLLMConfig))
	s.mux.HandleFunc("POST /api/positions", s.authMiddleware(s.handleCreatePosition))
	s.mux.HandleFunc("PUT /api/positions/{id}", s.authMiddleware(s.handleUpdatePosition))
	s.mux.HandleFunc("DELETE /api/positions/{id}", s.authMiddleware(s.handleDeletePosition))
	s.mux.HandleFunc("POST /api/positions/{id}/exit", s.authMiddleware(s.handleExitPosition))
	s.mux.HandleFunc("GET /api/positions", s.authMiddleware(s.handleListPositions))

	// fix 兼容端点
	s.mux.HandleFunc("GET /api/signals", s.authMiddleware(s.handleFixSignals))
	s.mux.HandleFunc("GET /api/status", s.authMiddleware(s.handleFixStatus))
	s.mux.HandleFunc("GET /api/alerts", s.authMiddleware(s.handleFixAlerts))
	s.mux.HandleFunc("GET /api/holdings", s.authMiddleware(s.handleFixGetHoldings))
	s.mux.HandleFunc("POST /api/holdings", s.authMiddleware(s.handleFixSetHoldings))
	s.mux.HandleFunc("GET /api/sector/hot", s.authMiddleware(s.handleFixSectorHot))
	s.mux.HandleFunc("GET /api/snapshot", s.authMiddleware(s.handleFixSnapshot))
	s.mux.HandleFunc("GET /api/snapshot/hot", s.authMiddleware(s.handleFixHotSnapshot))
	s.mux.HandleFunc("GET /api/evaluations", s.authMiddleware(s.handleFixEvaluations))
	s.mux.HandleFunc("GET /api/ipo/calendar", s.authMiddleware(s.handleFixIPOCalendar))
	s.mux.HandleFunc("GET /api/stock/lookup", s.authMiddleware(s.handleFixStockLookup))
	s.mux.HandleFunc("GET /api/news", s.authMiddleware(s.handleFixNews))
	s.mux.HandleFunc("GET /api/watchlist", s.authMiddleware(s.handleFixGetWatchlist))
	s.mux.HandleFunc("POST /api/watchlist", s.authMiddleware(s.handleFixAddWatchlist))
	s.mux.HandleFunc("DELETE /api/watchlist", s.authMiddleware(s.handleFixRemoveWatchlist))
	s.mux.HandleFunc("POST /api/action", s.authMiddleware(s.handleFixAction))
	s.mux.HandleFunc("POST /api/notify-test", s.authMiddleware(s.handleFixNotifyTest))
	s.mux.HandleFunc("GET /api/llm-debug", s.authMiddleware(s.handleLLMDebug))
	s.mux.HandleFunc("GET /api/events", s.handleFixSSE)
}

func (s *Server) Serve(addr string) error {
	log.Printf("HTTP server starting on %s", addr)
	return http.ListenAndServe(addr, s.corsMiddleware(s.mux))
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) GetServeMux() *http.ServeMux  { return s.mux }
func (s *Server) GetAuthManager() *auth.Manager { return s.auth }

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ── Auth handlers ──

type registerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Register(req.Username, req.Password)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"token": user.Token,
		"id":    user.ID,
	})
}

func (s *Server) handleTemp(w http.ResponseWriter, r *http.Request) {
	user, err := s.auth.CreateTemp(14 * 24 * time.Hour)
	if err != nil {
		writeError(w, 500, "create temp account failed")
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"token":      user.Token,
		"id":         user.ID,
		"expires_at": user.TokenExp,
	})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"token":    user.Token,
		"id":       user.ID,
		"account":  user.Username,
	})
}

// ── Setup handlers ──

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	initialized := s.auth.IsInitialized()
	writeJSON(w, 200, map[string]bool{"initialized": initialized})
}

type setupReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	LLMApiURL   string `json:"llm_api_url"`
	LLMApiKey   string `json:"llm_api_key"`
	TushareToken string `json:"tushare_token"`
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if s.auth.IsInitialized() {
		writeError(w, 400, "already initialized")
		return
	}

	var req setupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, 400, "username and password required")
		return
	}

	user, err := s.auth.Register(req.Username, req.Password)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}

	if req.LLMApiURL != "" {
		s.auth.SetConfig(user.ID, "llm_api_url", req.LLMApiURL)
	}
	if req.LLMApiKey != "" {
		s.auth.SetConfig(user.ID, "llm_api_key", req.LLMApiKey)
	}
	if req.TushareToken != "" {
		s.auth.SetConfig(user.ID, "tushare_token", req.TushareToken)
	}
	s.auth.MarkInitialized()

	writeJSON(w, 200, map[string]interface{}{
		"token": user.Token,
		"id":    user.ID,
	})
}

// ── API middleware ──

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			writeError(w, 401, "missing authorization token")
			return
		}
		if strings.HasPrefix(token, "Bearer ") {
			token = strings.TrimPrefix(token, "Bearer ")
		}
		user := s.auth.ValidateToken(token)
		if user == nil {
			writeError(w, 401, "invalid or expired token")
			return
		}
		next(w, r)
	}
}

// ── API handlers ──

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := s.agg.Current()
	if data == nil {
		writeJSON(w, 200, map[string]string{"status": "waiting_for_data"})
		return
	}
	resp := map[string]interface{}{
		"news_events":    data.NewsEvents,
		"hot_sectors":    data.HotSectors,
		"bear_sectors":   data.BearSectors,
		"bear_stocks":    data.BearStocks,
		"verified_bull":  data.VerifiedBull,
		"verified_bear":  data.VerifiedBear,
		"bull_signals":   data.BullSignals,
		"bear_signals":   data.BearSignals,
		"final_signals":  data.FinalSignals,
		"l1_score":       data.L1Score,
		"l1_blocked":     data.L1Blocked,
		"short_enabled":  s.shortEnabled,
	}
	if data.Report != nil {
		total, holding, win, wr, avgW, avgL := data.Report.Stats()
		resp["report_stats"] = map[string]interface{}{
			"total":   total,
			"holding": holding,
			"win":     win,
			"win_rate":       wr,
			"avg_win_pct":    avgW,
			"avg_loss_pct":   avgL,
		}
		resp["report_logs"] = data.Report.List()
	}
	writeJSON(w, 200, resp)
}

type shortToggleReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleShortToggle(w http.ResponseWriter, r *http.Request) {
	var req shortToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.shortEnabled = req.Enabled
	log.Printf("[server] 做空开关: %v", req.Enabled)
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortEnabled})
}

func (s *Server) handleShortStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortEnabled})
}

// ── 持仓管理 ──

type createPositionReq struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Direction     string  `json:"direction"`
	Strategy      string  `json:"strategy"`
	EntryPrice    float64 `json:"entry_price"`
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   float64 `json:"stop_loss_pct,omitempty"`
}

func (s *Server) handleCreatePosition(w http.ResponseWriter, r *http.Request) {
	var req createPositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.Code == "" || req.EntryPrice <= 0 {
		writeError(w, 400, "code and entry_price required")
		return
	}
	id := fmt.Sprintf("POS%d", time.Now().UnixNano())
	s.rpt.LogSignal(id, req.Code, req.Name, req.Direction, req.Strategy, req.EntryPrice, req.TakeProfitPct, req.StopLossPct)
	writeJSON(w, 201, map[string]string{"id": id})
}

type updatePositionReq struct {
	TakeProfitPct *float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   *float64 `json:"stop_loss_pct,omitempty"`
	Name          *string  `json:"name,omitempty"`
}

func (s *Server) handleUpdatePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updatePositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.rpt.Update(id, func(log *report.ExecLog) {
		if req.TakeProfitPct != nil {
			log.TakeProfitPct = *req.TakeProfitPct
		}
		if req.StopLossPct != nil {
			log.StopLossPct = *req.StopLossPct
		}
		if req.Name != nil {
			log.Name = *req.Name
		}
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.rpt.Delete(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type exitPositionReq struct {
	ExitPrice float64 `json:"exit_price"`
}

func (s *Server) handleExitPosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req exitPositionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if req.ExitPrice <= 0 {
		writeError(w, 400, "exit_price required")
		return
	}
	s.rpt.LogExit(id, req.ExitPrice)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleListPositions(w http.ResponseWriter, r *http.Request) {
	logs := s.rpt.List()
	reportStats := map[string]interface{}{}
	total, holding, win, wr, avgW, avgL := s.rpt.Stats()
	reportStats["total"] = total
	reportStats["holding"] = holding
	reportStats["win"] = win
	reportStats["win_rate"] = wr
	reportStats["avg_win_pct"] = avgW
	reportStats["avg_loss_pct"] = avgL
	writeJSON(w, 200, map[string]interface{}{
		"positions": logs,
		"stats":     reportStats,
	})
}

// ── 策略参数配置 ──

func (s *Server) handleGetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetStrategyConfig())
}

func (s *Server) handleSetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.StrategyConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetStrategyConfig(&cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── D1 规则配置 ──

func (s *Server) handleGetD1Config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetD1Config())
}

func (s *Server) handleSetD1Config(w http.ResponseWriter, r *http.Request) {
	var cfg config.D1Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.cfg.SetD1Config(&cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ── LLM 配置 ──

type setLLMConfigReq struct {
	APIKey string `json:"api_key,omitempty"`
	APIURL string `json:"api_url"`
	Model  string `json:"model"`
}

func (s *Server) handleGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetLLMConfig()
	writeJSON(w, 200, map[string]interface{}{
		"api_url": cfg.APIURL,
		"model":   cfg.Model,
	})
}

func (s *Server) handleSetLLMConfig(w http.ResponseWriter, r *http.Request) {
	var req setLLMConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	// 保存 APIURL + Model 到 config.json
	s.cfg.SetLLMConfig(&config.LLMConfig{
		APIURL: req.APIURL,
		Model:  req.Model,
	})

	// 保存 APIKey 到 auth config
	if req.APIKey != "" {
		s.auth.SetConfig("", "llm_api_key", req.APIKey)
	}

	// 热重建 LLM 客户端（如果提供了回调）
	if s.llmRecreate != nil {
		// 从 auth config 读取 key（可能刚保存的 key 还没刷新，用请求里的值）
		key := req.APIKey
		if key == "" {
			if v, ok := s.auth.GetConfig("", "llm_api_key"); ok {
				key = v
			}
		}
		s.llmRecreate(key, req.APIURL, req.Model)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleLLMDebug(w http.ResponseWriter, r *http.Request) {
	if s.newsAgent == nil {
		writeJSON(w, 200, map[string]string{"status": "no_agent"})
		return
	}
	di := s.newsAgent.GetDebugInfo()
	if di == nil {
		writeJSON(w, 200, map[string]string{"status": "no_data"})
		return
	}
	writeJSON(w, 200, di)
}
