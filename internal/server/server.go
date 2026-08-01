// Package server HTTP 服务端：提供看板数据、策略配置、持仓管理、做空开关等 REST API。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
)

// EngineController 引擎对外暴露的控制面：利好/利空开关 + 流水线调试数据 + 消息中心 + 热点记录。
// 由顶层编排引擎实现，server 不直接依赖 engine 包（避免导入环）。
type EngineController interface {
	LongEnabled() bool
	SetLongEnabled(v bool)
	ShortEnabled() bool
	SetShortEnabled(v bool)
	GetDebugInfo() *newsagent.DebugInfo
	GetStageRecords() []newsagent.DebugInfo
	GetHotRecords() []data.HotRecord
	GetMessages() []data.MessageItem
	ClearMessages()
	DeleteMessage(id string)
}

// Server HTTP 服务端，聚合所有依赖组件并注册 REST/SSE 路由。
type Server struct {
	auth        *auth.Manager                      // 认证管理器：注册/登录/临时账号/token 校验
	agg         *display.Aggregator                // 看板数据聚合器（读取实时看板快照）
	cfg         *config.Manager                    // 配置管理器（策略/D1/LLM 参数）
	rpt         *report.Report                     // 交易持仓报告（开仓/平仓/统计）
	mux         *http.ServeMux                     // 路由注册表
	market      *data.MarketAPI                    // 行情数据 API（实时报价/板块/IPO 等）
	ths         *data.THSClient                    // 同花顺客户端（板块行情表）
	watchlist   *data.WatchlistManager             // 自选股管理器
	sse         *SSEBroker                         // SSE 事件广播器（向前端实时推送）
	startTime   time.Time                          // 服务启动时间（用于 uptime 统计）
	llmRecreate func(apiKey, apiURL, model string) // 热重建 LLM 客户端
	ctrl        EngineController                   // 引擎控制面（做多/做空开关、流水线调试数据等）

	llmMu      sync.Mutex // 保护 runtimeLLM/runtimeURL 的互斥锁
	runtimeLLM string     // 运行时实际使用的 model（与文件配置可能不同）
	runtimeURL string     // 运行时实际使用的 API 地址

	calMu         sync.Mutex // 保护日历缓存的互斥锁
	macroCache    []data.MacroEvent
	macroCacheDay string // 宏观日历缓存对应的日期（用于按天失效）
	ipoCache      []data.IPOEvent
	ipoCacheDay   string // IPO 日历缓存对应的日期（用于按天失效）
}

// SetLLMRecreate 设置 LLM 客户端热重建回调。
func (s *Server) SetLLMRecreate(fn func(apiKey, apiURL, model string)) { s.llmRecreate = fn }

// SetEngineController 设置引擎控制器。
func (s *Server) SetEngineController(c EngineController) { s.ctrl = c }

// SetRuntimeLLM 记录启动时实际生效的 LLM 模型与地址（供 /api/config/llm 返回真实值）。
func (s *Server) SetRuntimeLLM(url, model string) {
	s.llmMu.Lock()
	s.runtimeURL = url
	s.runtimeLLM = model
	s.llmMu.Unlock()
}

// runtimeModel 返回运行时 LLM 模型；未记录时回退配置文件中的 model。
func (s *Server) runtimeModel() string {
	s.llmMu.Lock()
	defer s.llmMu.Unlock()
	if s.runtimeLLM != "" {
		return s.runtimeLLM
	}
	return s.cfg.GetLLMConfig().Model
}

// macroEvents 返回当日宏观日历事件（按天缓存，每天首次调用时生成）。
func (s *Server) macroEvents(now time.Time) []data.MacroEvent {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	day := now.Format("2006-01-02")
	if s.macroCacheDay == day && s.macroCache != nil {
		return s.macroCache
	}
	s.macroCache = data.GenMacroEvents(now.Year(), nil)
	s.macroCacheDay = day
	return s.macroCache
}

// ipoCalendar 返回当日 IPO 日历（按天缓存，每天首次调用时远程拉取）。
func (s *Server) ipoCalendar(now time.Time) ([]data.IPOEvent, error) {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	day := now.Format("2006-01-02")
	if s.ipoCacheDay == day && s.ipoCache != nil {
		return s.ipoCache, nil
	}
	list, err := s.market.GetEastMoneyIPOCalendar()
	if err != nil {
		return nil, err
	}
	s.ipoCache = list
	s.ipoCacheDay = day
	return list, nil
}

// longOn / shortOn 读取引擎开关；未接入引擎时回退默认值（做多开 / 做空关）。
func (s *Server) longOn() bool {
	if s.ctrl != nil {
		return s.ctrl.LongEnabled()
	}
	return true
}

func (s *Server) shortOn() bool {
	if s.ctrl != nil {
		return s.ctrl.ShortEnabled()
	}
	return false
}

// New 创建 HTTP 服务端实例。
func New(authMgr *auth.Manager, agg *display.Aggregator, cfg *config.Manager, rpt *report.Report, market *data.MarketAPI, wl *data.WatchlistManager, ths *data.THSClient) *Server {
	s := &Server{
		auth:      authMgr,
		agg:       agg,
		cfg:       cfg,
		rpt:       rpt,
		mux:       http.NewServeMux(),
		market:    market,
		ths:       ths,
		watchlist: wl,
		sse:       NewSSEBroker(),
		startTime: time.Now(),
	}
	s.registerRoutes()
	return s
}

// GetSSE 返回 SSE 事件推送器。
func (s *Server) GetSSE() *SSEBroker { return s.sse }

// registerRoutes 注册全部 HTTP 路由：
// 认证/初始化（register/temp/login/setup）无需鉴权；业务 API 统一包一层 authMiddleware。
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/temp", s.handleTemp)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /setup", s.handleSetupStatus)
	s.mux.HandleFunc("POST /setup", s.handleSetupSubmit)

	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
	s.mux.HandleFunc("POST /api/long/toggle", s.authMiddleware(s.handleLongToggle))
	s.mux.HandleFunc("GET /api/long/status", s.authMiddleware(s.handleLongStatus))
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
	s.mux.HandleFunc("DELETE /api/alerts", s.authMiddleware(s.handleClearAlerts))
	s.mux.HandleFunc("DELETE /api/alerts/{id}", s.authMiddleware(s.handleDeleteAlert))
	s.mux.HandleFunc("GET /api/holdings", s.authMiddleware(s.handleFixGetHoldings))
	s.mux.HandleFunc("POST /api/holdings", s.authMiddleware(s.handleFixSetHoldings))
	s.mux.HandleFunc("GET /api/sector/hot", s.authMiddleware(s.handleFixSectorHot))
	s.mux.HandleFunc("GET /api/sector/hot/records", s.authMiddleware(s.handleSectorHotRecords))
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
	s.mux.HandleFunc("GET /api/stage-records", s.authMiddleware(s.handleStageRecords))
	s.mux.HandleFunc("GET /api/events", s.handleFixSSE)
}

// Serve 启动 HTTP 服务监听指定地址。
func (s *Server) Serve(addr string) error {
	log.Printf("HTTP server starting on %s", addr)
	return http.ListenAndServe(addr, s.corsMiddleware(s.mux))
}

// corsMiddleware 跨域中间件：为所有响应添加 CORS 头，并直接终结 OPTIONS 预检请求。
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

// GetServeMux 返回路由注册表。
func (s *Server) GetServeMux() *http.ServeMux { return s.mux }

// GetAuthManager 返回认证管理器。
func (s *Server) GetAuthManager() *auth.Manager { return s.auth }

// writeJSON 以 JSON 格式写入响应：设置 Content-Type、状态码并编码序列化 v。
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 以标准错误结构 {"error": msg} 写入响应。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ── Auth handlers ──

// registerReq 注册请求体：用户名 + 密码。
type registerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleRegister 处理 POST /auth/register：创建用户并返回 token 与用户 ID。
// 用户名/密码缺失返回 400；用户名已存在返回 409。
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

// handleTemp 处理 POST /auth/temp：创建有效期 14 天的临时演示账号，返回 token/ID/过期时间。
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

// loginReq 登录请求体：用户名 + 密码。
type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 处理 POST /auth/login（/api/auth/login 同路由）：校验凭据并返回 token/ID/账号名。
// 凭据错误返回 401。
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
		"token":   user.Token,
		"id":      user.ID,
		"account": user.Username,
	})
}

// ── Setup handlers ──

// handleSetupStatus 处理 GET /setup：返回系统是否已完成初始化（用于前端引导首次配置）。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	initialized := s.auth.IsInitialized()
	writeJSON(w, 200, map[string]bool{"initialized": initialized})
}

// setupReq 首次初始化配置请求体：管理员账号 + LLM 参数 + Tushare token。
type setupReq struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	LLMApiURL    string `json:"llm_api_url"`
	LLMApiKey    string `json:"llm_api_key"`
	TushareToken string `json:"tushare_token"`
}

// handleSetupSubmit 处理 POST /setup：完成首次初始化。
// 创建管理员账号，将非空的 LLM/Tushare 配置写入用户配置，并标记系统已初始化。
// 已初始化时返回 400 拒绝重复配置。
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

// authMiddleware 认证中间件：从 Authorization 头提取 token（兼容 Bearer 前缀），
// 校验通过后放行请求，否则返回 401。
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

// handleHealth 处理 GET /api/health：健康检查，恒返回 {"status":"ok"}。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDashboard 处理 GET /api/dashboard：返回看板聚合快照。
// 包括新闻事件、热门/利空板块、多空信号、最终信号、L1 评分与做多/做空开关状态；
// 若报表存在则附带统计指标与持仓日志。无数据时返回 waiting_for_data。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := s.agg.Current()
	if data == nil {
		writeJSON(w, 200, map[string]string{"status": "waiting_for_data"})
		return
	}
	resp := map[string]interface{}{
		"news_events":   data.NewsEvents,
		"hot_sectors":   data.HotSectors,
		"bear_sectors":  data.BearSectors,
		"bear_stocks":   data.BearStocks,
		"verified_bull": data.VerifiedBull,
		"verified_bear": data.VerifiedBear,
		"bull_signals":  data.BullSignals,
		"bear_signals":  data.BearSignals,
		"final_signals": data.FinalSignals,
		"l1_score":      data.L1Score,
		"l1_blocked":    data.L1Blocked,
		"long_enabled":  s.longOn(),
		"short_enabled": s.shortOn(),
	}
	if data.Report != nil {
		total, holding, win, wr, avgW, avgL := data.Report.Stats()
		resp["report_stats"] = map[string]interface{}{
			"total":        total,
			"holding":      holding,
			"win":          win,
			"win_rate":     wr,
			"avg_win_pct":  avgW,
			"avg_loss_pct": avgL,
		}
		resp["report_logs"] = data.Report.List()
	}
	writeJSON(w, 200, resp)
}

// longToggleReq 做多开关请求体。
type longToggleReq struct {
	Enabled bool `json:"enabled"`
}

// handleLongToggle 处理 POST /api/long/toggle：切换做多开关（经引擎控制面生效）并返回最新状态。
func (s *Server) handleLongToggle(w http.ResponseWriter, r *http.Request) {
	var req longToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if s.ctrl != nil {
		s.ctrl.SetLongEnabled(req.Enabled)
	}
	log.Printf("[server] 做多开关: %v", req.Enabled)
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOn()})
}

// handleLongStatus 处理 GET /api/long/status：返回当前做多开关状态。
func (s *Server) handleLongStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"long_enabled": s.longOn()})
}

// shortToggleReq 做空开关请求体。
type shortToggleReq struct {
	Enabled bool `json:"enabled"`
}

// handleShortToggle 处理 POST /api/short/toggle：切换做空开关并返回最新状态。
func (s *Server) handleShortToggle(w http.ResponseWriter, r *http.Request) {
	var req shortToggleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if s.ctrl != nil {
		s.ctrl.SetShortEnabled(req.Enabled)
	}
	log.Printf("[server] 做空开关: %v", req.Enabled)
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOn()})
}

// handleShortStatus 处理 GET /api/short/status：返回当前做空开关状态。
func (s *Server) handleShortStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"short_enabled": s.shortOn()})
}

// ── 持仓管理 ──

// createPositionReq 新建持仓请求体：股票代码/名称、方向、策略、开仓价及止盈止损百分比。
type createPositionReq struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Direction     string  `json:"direction"`
	Strategy      string  `json:"strategy"`
	EntryPrice    float64 `json:"entry_price"`
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   float64 `json:"stop_loss_pct,omitempty"`
}

// handleCreatePosition 处理 POST /api/positions：记录一条开仓信号到报表，返回生成的信号 ID。
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

// updatePositionReq 更新持仓请求体：止盈/止损百分比与名称均可选，指针为 nil 表示不修改。
type updatePositionReq struct {
	TakeProfitPct *float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   *float64 `json:"stop_loss_pct,omitempty"`
	Name          *string  `json:"name,omitempty"`
}

// handleUpdatePosition 处理 PUT /api/positions/{id}：按 ID 更新持仓的止盈/止损/名称字段。
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

// handleDeletePosition 处理 DELETE /api/positions/{id}：软删除指定持仓记录。
func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.rpt.Delete(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// exitPositionReq 平仓请求体：平仓价格。
type exitPositionReq struct {
	ExitPrice float64 `json:"exit_price"`
}

// handleExitPosition 处理 POST /api/positions/{id}/exit：按平仓价计算盈亏并标记止盈/止损。
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

// handleListPositions 处理 GET /api/positions：返回全部持仓记录与交易统计指标。
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

// handleGetStrategyConfig 处理 GET /api/config/strategy：返回当前策略参数配置。
func (s *Server) handleGetStrategyConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetStrategyConfig())
}

// handleSetStrategyConfig 处理 POST /api/config/strategy：保存新的策略参数配置。
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

// handleGetD1Config 处理 GET /api/config/d1：返回 D1 规则配置。
func (s *Server) handleGetD1Config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.GetD1Config())
}

// handleSetD1Config 处理 POST /api/config/d1：保存新的 D1 规则配置。
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

// setLLMConfigReq LLM 配置请求体：APIKey 可选（不修改时留空），APIURL 与 Model 必填。
type setLLMConfigReq struct {
	APIKey string `json:"api_key,omitempty"`
	APIURL string `json:"api_url"`
	Model  string `json:"model"`
}

// handleGetLLMConfig 处理 GET /api/config/llm：返回 API 地址与运行时实际生效的模型名。
func (s *Server) handleGetLLMConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetLLMConfig()
	writeJSON(w, 200, map[string]interface{}{
		"api_url": cfg.APIURL,
		"model":   s.runtimeModel(),
	})
}

// handleSetLLMConfig 处理 POST /api/config/llm：保存 LLM 配置并热重建客户端。
// 依次执行：APIURL+Model 写入 config.json → APIKey 写入 auth 配置 → 触发 llmRecreate
// 回调重建客户端 → 记录运行时实际生效的 model（空值兜底为默认模型）。
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

	// 记录运行时实际生效的 model（空值会被 llm 客户端按默认模型兜底）
	model := req.Model
	if model == "" {
		model = llm.DefaultModel
	}
	s.SetRuntimeLLM(req.APIURL, model)

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleLLMDebug 处理 GET /api/llm-debug：返回引擎的 LLM 流水线调试信息。
// 未接入引擎或无数据时分别返回 no_engine / no_data 状态。
func (s *Server) handleLLMDebug(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	di := s.ctrl.GetDebugInfo()
	if di == nil {
		writeJSON(w, 200, map[string]string{"status": "no_data"})
		return
	}
	writeJSON(w, 200, di)
}

// handleStageRecords 返回当日全量 Stage 流水线轮次记录（用于复盘/策略引擎实时调取）。
func (s *Server) handleStageRecords(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	recs := s.ctrl.GetStageRecords()
	if recs == nil {
		recs = []newsagent.DebugInfo{}
	}
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}
