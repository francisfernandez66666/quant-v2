// observability_api_test.go — §OBSERVABILITY（2026-10-26）：
// 前端错误上报的去重/脱敏/鉴权，以及 /api/usage 的 LLM 用量与错误聚合。
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClientErrorsAndUsage(t *testing.T) {
	s, admin := newAdminTestServer(t)

	post := func(body string) map[string]any {
		t.Helper()
		rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/client-errors", body))
		if rr.Code != http.StatusOK {
			t.Fatalf("上报应 200，got %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}

	// ① 首次上报：不重复；含凭据的文本必须脱敏后才落聚合。
	out := post(`{"message":"boom Bearer abc123 token=deadbeef","stack":"at App","source":"app.js","line":10,"col":2,"route":"/paper","build":"0d46551"}`)
	if out["deduped"] == true {
		t.Fatalf("首次上报不应被去重: %+v", out)
	}
	// ② 同指纹 5 分钟内再报：deduped=true，计数累加。
	out = post(`{"message":"boom Bearer abc123 token=deadbeef","stack":"at App","source":"app.js","line":10,"col":2}`)
	if out["deduped"] != true {
		t.Fatalf("同指纹应去重: %+v", out)
	}

	// ③ /api/usage：错误总数=2、唯一=1，且消息脱敏。
	s.SetLLMUsageFn(func() map[string]int64 {
		return map[string]int64{"calls": 3, "tokens": 120, "call_budget": 10, "token_budget": 1000}
	})
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/usage", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("usage 应 200，got %d: %s", rr.Code, rr.Body.String())
	}
	var usage struct {
		LLM map[string]int64 `json:"llm"`
		Err struct {
			Total  int64            `json:"total"`
			Unique int              `json:"unique"`
			Top    []map[string]any `json:"top"`
		} `json:"client_errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.LLM["calls"] != 3 || usage.LLM["tokens"] != 120 {
		t.Fatalf("LLM 用量未透传: %+v", usage.LLM)
	}
	if usage.Err.Total != 2 || usage.Err.Unique != 1 {
		t.Fatalf("错误聚合应为 total=2 unique=1，got %+v", usage.Err)
	}
	topMsg, _ := usage.Err.Top[0]["message"].(string)
	if strings.Contains(topMsg, "abc123") || strings.Contains(topMsg, "deadbeef") {
		t.Fatalf("错误文本未脱敏: %q", topMsg)
	}
	if !strings.Contains(topMsg, "<redacted>") {
		t.Fatalf("脱敏哨兵缺失: %q", topMsg)
	}

	// ④ 鉴权：普通成员可上报（authMiddleware），但不能读 admin 的 usage 看板。
	member, err := s.auth.CreateUser("member_obs", "memberpw", "user", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr = adminDo(s, adminReq(s, member, http.MethodGet, "/api/usage", ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("成员读 usage 应 403，got %d", rr.Code)
	}
	rr = adminDo(s, adminReq(s, member, http.MethodPost, "/api/client-errors", `{"message":"member error"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("成员上报应 200，got %d: %s", rr.Code, rr.Body.String())
	}
}
