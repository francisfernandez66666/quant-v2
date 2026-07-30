package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/display"
)

type Server struct {
	auth   *auth.Manager
	agg    *display.Aggregator
	mux    *http.ServeMux
}

func New(authMgr *auth.Manager, agg *display.Aggregator) *Server {
	s := &Server{
		auth: authMgr,
		agg:  agg,
		mux:  http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /auth/temp", s.handleTemp)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /setup", s.handleSetupStatus)
	s.mux.HandleFunc("POST /setup", s.handleSetupSubmit)

	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))
	s.mux.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))
}

func (s *Server) Serve(addr string) error {
	log.Printf("HTTP server starting on %s", addr)
	return http.ListenAndServe(addr, s.mux)
}

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
		"token": user.Token,
		"id":    user.ID,
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
	writeJSON(w, 200, data)
}
