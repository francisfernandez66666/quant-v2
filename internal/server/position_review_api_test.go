// §DAILY_REVIEW HTTP API 层测试：POST /api/review/positions 的三条分支——
// 注册表缺失（503）、复盘成功（200 + reviewed 计数透传）、引擎报错（502 + 错误文案透传）。
// 用假注册表隔离真实引擎/LLM，只验证 handler 契约（前端按钮逻辑依赖该响应形状）。
// English: API-layer tests for the manual position-review endpoint (503/200/502 branches)
// with a fake registry stub; asserts the handler contract the frontend relies on.
package server

import (
	"encoding/json"
	"strings"
	"testing"

	"quant-trading-v2/internal/paper"
)

// fakeReviewRegistry EngineRegistry 最小假实现：只关心 TriggerPositionReview 行为，其余方法为接口占位。
// 通过 reviewN/reviewErr 控制成功/失败分支。
type fakeReviewRegistry struct {
	reviewN   int    // TriggerPositionReview 返回的复盘数量
	reviewErr error  // TriggerPositionReview 返回的错误（非空则 handler 应回 502）
	gotUser   string // 记录传入的 userID，验证上下文透传
}

func (f *fakeReviewRegistry) GetController(string) EngineController        { return nil }
func (f *fakeReviewRegistry) InitStatusJSON(string) map[string]interface{} { return nil }
func (f *fakeReviewRegistry) AllControllers() []EngineController           { return nil }
func (f *fakeReviewRegistry) PaperForUser(string) *paper.Engine            { return nil }
func (f *fakeReviewRegistry) Len() int                                     { return 0 }
func (f *fakeReviewRegistry) SetPaperPools([]string)                       {}
func (f *fakeReviewRegistry) SetPaperLabelResolver(func(string) string)    {}
func (f *fakeReviewRegistry) TriggerPositionReview(uid string) (int, error) {
	f.gotUser = uid
	return f.reviewN, f.reviewErr
}

// TestTriggerPositionReviewHandler §DAILY_REVIEW 手动复盘端点的分支覆盖。
func TestTriggerPositionReviewHandler(t *testing.T) {
	s, admin := newAdminTestServer(t)

	// 1) 注册表未注入 → 503（服务尚未就绪的降级保护）
	req := adminReq(s, admin, "POST", "/api/review/positions", "")
	rr := adminDo(s, req)
	if rr.Code != 503 {
		t.Fatalf("registry=nil 期望 503, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 2) 正常复盘 3 只 → 200 {"reviewed":3}，且 userID 来自认证上下文
	fake := &fakeReviewRegistry{reviewN: 3}
	s.SetEngineRegistry(fake)
	req = adminReq(s, admin, "POST", "/api/review/positions", "")
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("成功复盘期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %v body=%s", err, rr.Body.String())
	}
	if body["reviewed"].(float64) != 3 {
		t.Fatalf("期望 reviewed=3, got %v", body["reviewed"])
	}
	if fake.gotUser != admin.ID {
		t.Fatalf("期望透传 userID=%s, got %s", admin.ID, fake.gotUser)
	}

	// 3) 引擎返回错误 → 502 + 错误文案（前端按钮据此弹"复盘失败"）
	s.SetEngineRegistry(&fakeReviewRegistry{reviewErr: &stubErr{}})
	req = adminReq(s, admin, "POST", "/api/review/positions", "")
	rr = adminDo(s, req)
	if rr.Code != 502 {
		t.Fatalf("引擎错误期望 502, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "复盘开关已关闭") {
		t.Fatalf("502 应透传错误文案, got %s", rr.Body.String())
	}
}

// stubErr 可控错误文案桩。
type stubErr struct{}

func (e *stubErr) Error() string { return "复盘开关已关闭" }
