package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// §N-05：ACAO 回显必须带 Vary: Origin；预检头只挂在 OPTIONS 上。
func TestCorsVaryAndPreflightScope(t *testing.T) {
	s := &Server{}
	h := s.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// 同源 GET：ACAO 回显 + Vary: Origin，且不得带 Allow-Methods/Headers
	req := httptest.NewRequest("GET", "http://a.com/api/status", nil)
	req.Host = "a.com"
	req.Header.Set("Origin", "https://a.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.com" {
		t.Fatalf("ACAO=%q", got)
	}
	if got := rec.Header().Values("Vary"); len(got) != 1 || !strings.EqualFold(got[0], "Origin") {
		t.Fatalf("GET Vary=%v", got)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") != "" || rec.Header().Get("Access-Control-Allow-Headers") != "" {
		t.Fatalf("预检头不得挂在普通 GET 响应上")
	}

	// OPTIONS：204 + 三组 Vary + 方法/请求头
	req = httptest.NewRequest("OPTIONS", "http://a.com/api/status", nil)
	req.Host = "a.com"
	req.Header.Set("Origin", "https://a.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("OPTIONS code=%d", rec.Code)
	}
	vary := strings.Join(rec.Header().Values("Vary"), ",")
	for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		if !strings.Contains(vary, want) {
			t.Fatalf("OPTIONS Vary 缺 %s: %s", want, vary)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("OPTIONS 缺 Allow-Methods")
	}

	// 白名单外 Origin：不写 ACAO（浏览器自行拦截）
	req = httptest.NewRequest("GET", "http://a.com/api/status", nil)
	req.Host = "a.com"
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("白名单外 Origin 不得回显 ACAO")
	}
}
