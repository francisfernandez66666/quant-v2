// consult_fix9_test.go — §FIX-9(20260919 批五) 咨询出口清理回归：
// ① consultErrorResponse 全分支分流（9d：状态码语义化 + 机读 code + 脱敏截断）；
// ② 成功响应出口统一追加免责尾注（9h）；
// ③ 清空历史在引擎缺失时如实 503，不再假 ok（9g）。
// English: batch-5 regressions — error classification with machine-readable codes,
// server-side disclaimer suffix, and honest 503 on clear-history without an engine.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/llm"
)

// TestConsultErrorResponseClassification §FIX-9d：错误→(状态, code, 文案) 表驱动全分支。
// English: table-driven coverage of every consultErrorResponse branch.
func TestConsultErrorResponseClassification(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantIn     string // 外发文案必须包含
		wantNot    string // 外发文案必须不包含（脱敏面）
	}{
		{"预算", fmt.Errorf("咨询调用失败: %w", llm.ErrBudgetExceeded), 429, "consult_budget_exceeded", "预算", ""},
		{"超时-空闲", fmt.Errorf("咨询调用失败: %w", fmt.Errorf("流式响应空闲超时(60s): 模型疑似卡死")), 504, "llm_timeout", "模型响应超时", "60s"},
		{"超时-总时长", fmt.Errorf("流式响应总时长超限(5m0s): 分片持续但未在限内读完，已掐断"), 504, "llm_timeout", "模型响应超时", ""},
		{"未配置-中文", fmt.Errorf("未配置 LLM_API_KEY，请先在股票咨询页配置 API Key"), 503, "llm_not_configured", "未配置 API Key", "sk-"},
		{"未配置-哨兵", fmt.Errorf("咨询调用失败: %w", llm.ErrNoAPIKey), 503, "llm_not_configured", "未配置 API Key", ""},
		{"上游503机读", &llm.UpstreamError{Status: 503, Detail: "Service Unavailable at https://api.vendor.internal/v1?key=sk-secret"}, 503, "llm_upstream_unavailable", "上游模型服务暂不可用", "sk-secret"},
		{"上游429机读", &llm.UpstreamError{Status: 429, Detail: "rate limited"}, 503, "llm_upstream_unavailable", "HTTP 429", "rate limited"},
		{"上游401机读", &llm.UpstreamError{Status: 401, Detail: "invalid api key sk-abcdef"}, 502, "llm_upstream_error", "核查配置", "sk-abcdef"},
		{"上游500旧式串", fmt.Errorf("LLM API 返回 500: internal server error url=https://private.vendor"), 503, "llm_upstream_unavailable", "暂不可用", "private.vendor"},
		{"未知截断", fmt.Errorf("some unknown failure %s", strings.Repeat("未", 300)), 500, "consult_failed", "详情见服务端日志", ""},
	}
	for _, c := range cases {
		status, code, msg := consultErrorResponse(c.err)
		if status != c.wantStatus || code != c.wantCode {
			t.Errorf("%s: 期望 (%d,%s), got (%d,%s)", c.name, c.wantStatus, c.wantCode, status, code)
		}
		if c.wantIn != "" && !strings.Contains(msg, c.wantIn) {
			t.Errorf("%s: 文案应含 %q, got %q", c.name, c.wantIn, msg)
		}
		if c.wantNot != "" && strings.Contains(msg, c.wantNot) {
			t.Errorf("%s: 文案不得含敏感串 %q, got %q", c.name, c.wantNot, msg)
		}
	}
	// 未知错误外发必须截断到 200 字符 + 留痕提示（旧实现 200 字外直出全量堆栈）。
	_, _, msg := consultErrorResponse(fmt.Errorf("x%s", strings.Repeat("长", 250)))
	if n := len([]rune(msg)); n > 220 {
		t.Errorf("未知错误文案应截断至 ~200 字, got %d 字", n)
	}
}

// TestConsultReplyCarriesDisclaimer §FIX-9h：HTTP 出口回复自带免责尾注（非浏览器客户端同样覆盖）。
// English: successful consult replies now carry the disclaimer appended at the server edge.
func TestConsultReplyCarriesDisclaimer(t *testing.T) {
	s := newConsultServer(t, &fix6Ctrl{})
	rec := doConsult(s, consultRequest("u_h"))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.HasSuffix(body["reply"], "不构成投资建议。）") {
		t.Fatalf("reply 应含出口免责尾注: %s", rec.Body.String())
	}
}

// TestClearConsultHistoryHonest503 §FIX-9g：无引擎时 DELETE 历史不再假 ok。
// English: clear-history without an engine returns 503+code instead of a fake ok.
func TestClearConsultHistoryHonest503(t *testing.T) {
	// 故意不 SetEngineController：registry 为 nil 时 ctrlFor 返回 nil 接口 → 503 分支。
	am := newConsultServer(t, &fix6Ctrl{}).auth
	s := &Server{auth: am}
	r := httptest.NewRequest(http.MethodDelete, "/api/consult/history", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: "u_g"}))
	rec := httptest.NewRecorder()
	s.handleClearConsultHistory(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("无引擎应 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "engine_unavailable" || !strings.Contains(body["error"], "历史未清空") {
		t.Fatalf("503 应带 engine_unavailable 且说明历史未清空: %s", rec.Body.String())
	}
	// 反向护栏：有引擎时仍是 200 ok（本次改动不能把正常清空一并拦掉）。
	s2 := newConsultServer(t, &fix6Ctrl{})
	r2 := httptest.NewRequest(http.MethodDelete, "/api/consult/history", nil)
	r2 = r2.WithContext(context.WithValue(r2.Context(), ctxUserKey{}, &auth.User{ID: "u_g"}))
	rec2 := httptest.NewRecorder()
	s2.handleClearConsultHistory(rec2, r2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "ok") {
		t.Fatalf("有引擎应 200 ok, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}
