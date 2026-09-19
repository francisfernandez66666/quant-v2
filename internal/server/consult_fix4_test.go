// consult_fix4_test.go — §FIX-4/§FIX-7(20260919) 咨询入口回归：
// ① 每用户 in-flight=1（并发第二路直接 429，回复落定后释放）；
// ② 预算熔断错误经错误链映射为 429（区别于上游故障 500）；
// ③ 请求 ctx 取消时不写响应（用户已断开）。
// English: per-user in-flight gate → 429; budget sentinel errors.Is → 429 (not 500);
// cancelled request writes nothing.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/llm"
)

type consultCtrl struct {
	EngineController // 只实现咨询路径用到的方法，其余走嵌入的 nil（本用例不触碰）
	mode             string
	entered          chan struct{} // 缓冲 1：进入出呼时非阻塞发信号（多条并发不会 close 二次 panic）
	release          chan struct{}
	gotMsg           chan string
}

// 桩控制器：按 mode 走不同咨询响应路径（超时/错误等）。
func (f *consultCtrl) ConsultLLM(ctx context.Context, userID, userMsg string, proMode bool) (string, error) {
	switch f.mode {
	case "block":
		if userID != "u_a" {
			return "答复", nil // 其他用户的请求不阻塞（用于验证闸门按 uid 隔离）
		}
		select {
		case f.entered <- struct{}{}:
		default:
		}
		select {
		case <-f.release:
			return "答复", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	case "budget":
		// 引擎侧真实形态：fmt.Errorf("咨询调用失败: %w", llm.ErrBudgetExceeded) —— 必须保持 %w 链。
		return "", fmt.Errorf("咨询调用失败: 咨询日调用预算已用尽(3 次): %w", llm.ErrBudgetExceeded)
	case "fail":
		return "", fmt.Errorf("咨询调用失败: %w", errors.New("LLM API 返回 502: bad gateway"))
	}
	f.gotMsg <- userMsg
	return "答复", nil
}

// newConsultServer 组装注入桩控制器的咨询测试服务。
func newConsultServer(t *testing.T, ctrl EngineController) *Server {
	t.Helper()
	am := auth.NewManager(t.TempDir())
	if err := am.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	s := &Server{auth: am}
	s.SetEngineController(ctrl)
	return s
}

// consultRequest 构造带登录态的咨询 POST 请求。
func consultRequest(userID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(`{"message":"这只票能买吗"}`))
	r.RemoteAddr = "10.0.0.1:1234"
	if userID != "" {
		r = r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: userID}))
	}
	return r
}

// doConsult 执行一次请求并返回 recorder 供断言。
func doConsult(s *Server, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleConsult(rec, r)
	return rec
}

// TestConsultInflightGate §FIX-4：同用户第二条并发咨询必须 429；第一条落定后可再发。
func TestConsultInflightGate(t *testing.T) {
	ctrl := &consultCtrl{mode: "block", entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newConsultServer(t, ctrl)

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- doConsult(s, consultRequest("u_a")) }()
	<-ctrl.entered // 确认第一条已进入 LLM 出呼

	rec2 := doConsult(s, consultRequest("u_a"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("在途第二条应 429, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "处理中") {
		t.Fatalf("429 文案应说明上一条仍在处理: %s", rec2.Body.String())
	}
	// 其他用户不受影响（闸门按 uid 维度）
	// 先放掉第一条，避免与 u_b 用例共享 busy（u_b 本就不该被挡）
	recB := doConsult(s, consultRequest("u_b"))
	if recB.Code != http.StatusOK {
		t.Fatalf("不同用户应放行, got %d", recB.Code)
	}

	close(ctrl.release)
	rec1 := <-first
	if rec1.Code != http.StatusOK {
		t.Fatalf("第一条应成功, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	// 闸门随回复落定释放：同一用户可再次发起
	ctrl2 := &consultCtrl{mode: "echo", gotMsg: make(chan string, 1)}
	s2 := newConsultServer(t, ctrl2)
	if rec := doConsult(s2, consultRequest("u_a")); rec.Code != http.StatusOK {
		t.Fatalf("释放后同用户应可再咨询, got %d", rec.Code)
	}
}

// TestConsultBudgetMapsTo429 §FIX-7：ErrBudgetExceeded（经 %w 链）→ 429 + 中文额度文案；
// 普通上游故障仍 500——两者必须在 HTTP 层分流，前端才能区分"明天再来"与"系统坏了"。
func TestConsultBudgetMapsTo429(t *testing.T) {
	rec := doConsult(newConsultServer(t, &consultCtrl{mode: "budget"}), consultRequest("u_a"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("预算熔断应 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(body["error"], "预算") {
		t.Fatalf("429 响应应含额度文案: %s", rec.Body.String())
	}
	// §FIX-9d(20260919)：错误体必须带机读 code，前端免关键字猜测。
	if body["code"] != "consult_budget_exceeded" {
		t.Fatalf("429 应带 code=consult_budget_exceeded, got %q", body["code"])
	}
	// §FIX-9d：上游 5xx 故障不再 500 直出原始串——归类 503（可重试）+ 脱敏文案 + code。
	rec2 := doConsult(newConsultServer(t, &consultCtrl{mode: "fail"}), consultRequest("u_a"))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游 502 类故障应 503（§FIX-9d 分流）, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if b := rec2.Body.String(); !strings.Contains(b, "上游模型服务暂不可用") || strings.Contains(b, "bad gateway") {
		t.Fatalf("503 应为脱敏归类文案且不含上游原始串: %s", b)
	}
}

// TestConsultCancelledWritesNothing §FIX-4：请求 ctx 取消 → 出呼中止且不再写响应。
func TestConsultCancelledWritesNothing(t *testing.T) {
	ctrl := &consultCtrl{mode: "block", entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newConsultServer(t, ctrl)
	ctx, cancel := context.WithCancel(context.Background())
	// 注意：WithContext 是整体替换 ctx——必须把登录用户值一并挂进新 ctx，否则 userID 变空串。
	r := httptest.NewRequest(http.MethodPost, "/api/consult", strings.NewReader(`{"message":"这只票能买吗"}`)).
		WithContext(context.WithValue(ctx, ctxUserKey{}, &auth.User{ID: "u_a"}))
	r.RemoteAddr = "10.0.0.1:1234"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doConsult(s, r) }()
	<-ctrl.entered
	cancel()
	select {
	case rec := <-done:
		if rec.Body.Len() != 0 {
			t.Fatalf("已断开的请求不应写响应体: %q", rec.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 handleConsult 未返回（出呼悬挂）")
	}
}
