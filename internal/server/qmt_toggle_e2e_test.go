package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestQMTTogglePersistE2E 端到端验证：POST 打开总开关 → GET 应为 true；
// 模拟刷新（新请求同一账号）→ 仍为 true；模拟 Watch/Load 重置全局后 → 仍为 true。
// English: E2E test that the QMT master switch persists across requests and config reloads.
func TestQMTTogglePersistE2E(t *testing.T) {
	s, admin := newAdminTestServer(t)

	// 1) 打开总开关
	post := adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"enabled":true}`)
	rr := adminDo(s, post)
	if rr.Code != 200 {
		t.Fatalf("POST /api/config/qmt 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 2) 立即 GET
	get := adminReq(s, admin, http.MethodGet, "/api/config/qmt", "")
	rr = adminDo(s, get)
	if rr.Code != 200 {
		t.Fatalf("GET 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatalf("打开后 GET 期望 enabled=true, got %v", resp["enabled"])
	}

	// 3) 模拟刷新：用同一账号重新构造请求（等价页面刷新后重新拉取）
	refresh := adminReq(s, admin, http.MethodGet, "/api/config/qmt", "")
	rr = adminDo(s, refresh)
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatalf("刷新后 GET 期望 enabled=true（开关应持久）, got %v", resp["enabled"])
	}

	// 4) 模拟 Watch 触发 Load（重置全局 rules，不应影响 per-user 覆盖）
	s.cfg.Load()
	afterLoad := adminReq(s, admin, http.MethodGet, "/api/config/qmt", "")
	rr = adminDo(s, afterLoad)
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatalf("Load 后 GET 期望 enabled=true, got %v", resp["enabled"])
	}
}
