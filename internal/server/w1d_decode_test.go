// w1d_decode_test.go — §0926E2E-W1D（2026-09-26 全量审计批）"请求体可选≠请求体可坏"行为锁。
// 缺陷原文：清理账号（POST /api/admin/users/cleanup）与交割对账（POST /api/qmt/settle）等
// 端点写 `_ = json.NewDecoder(r.Body).Decode(&req)`——畸形 JSON 被解析成零值后**继续执行
// 破坏性动作**（想 dry_run 预览的调用坏一个括号=直接真删；想 report_only 的 settle 变全默认）。
// 现口径：空体合法（旧缺省语义不变）；非法 JSON 一律 400 中止。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestW1dCleanupMalformedBodyRejected 清理账号：坏 JSON → 400 且绝不影响任何账号；
// 空体与合法 dry_run 维持旧语义（防"收口把端点收死"的反向误伤）。
func TestW1dCleanupMalformedBodyRejected(t *testing.T) {
	s, admin := newAdminTestServer(t)
	before := len(s.auth.ListUsers())
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":tru`))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid request body") {
		t.Fatalf("畸形 JSON 应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(s.auth.ListUsers()) != before {
		t.Fatal("被 400 拒的畸形请求不得产生任何删除效果")
	}
	// 合法 dry_run 预览仍 200 且 dry_run=true
	rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":true}`))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"dry_run":true`) {
		t.Fatalf("合法 dry_run 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(s.auth.ListUsers()) != before {
		t.Fatal("dry_run 预览不得真删")
	}
	// 空体维持旧语义（=不携带 dry_run，正常执行清理流程）：本盘无僵尸号 → 200 count=0
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", ``)); rr.Code != 200 {
		t.Fatalf("空体应维持旧缺省语义 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 类型错（dry_run 传字符串）同属"带坏了"，必须 400
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/admin/users/cleanup", `{"dry_run":"yes"}`)); rr.Code != 400 {
		t.Fatalf("类型错 body 应 400, got %d", rr.Code)
	}
}

// TestW1dSettleMalformedBodyRejected settle 端点：坏 JSON → 400（在触达控制器/默认日期分支之前）。
func TestW1dSettleMalformedBodyRejected(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/qmt/settle", `{"day":"2026-09-`))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid request body") {
		t.Fatalf("settle 畸形 JSON 应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestW1dPaperResetMalformedBodyRejected 模拟盘清盘/注入：坏 JSON 不得静默走"清盘"默认分支。
func TestW1dPaperResetMalformedBodyRejected(t *testing.T) {
	s := newPaperConfigServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/paper/reset", strings.NewReader(`{"initial_capital":1e9,`))
	s.handlePaperReset(rr, req)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid request body") {
		t.Fatalf("paper reset 畸形 JSON 应 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestW1dDecodeOptJSONSemantics 统一解码口径本体的三态钉：空体=合法零值；坏体=报错；
// 合法体=正常填充。
func TestW1dDecodeOptJSONSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int // 0=合法 1=报错
	}{
		{"空体", "", 0},
		{"坏 JSON", `{"a":`, 1},
		{"类型错", `{"a":"str"}`, 1},
		{"合法", `{"a":1}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v struct {
				A int `json:"a"`
			}
			err := decodeOptJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)), &v)
			if tc.want == 0 && err != nil {
				t.Fatalf("应合法, got %v", err)
			}
			if tc.want == 1 && err == nil {
				t.Fatal("应报错")
			}
		})
	}
}
