// §UI-AUTHORITATIVE（2026-09-14 生产事故定位）LLM 配置保存链路回归：
// 设置页 POST /api/config/llm 必须同时完成三件事——① 多密钥落 auth 配置（按账号）；
// ② URL/模型落运营 userRules（重启恢复的权威来源，优先级高于环境变量）；
// ③ 触发 llmRecreate 热重建回调（归因/引擎立即换客户端）。
// 此前热重建只刷"当时存活"引擎且重启后被 NSSM 环境变量顶掉，表现为"前端保存后端不用"。
// English: regression for the settings-page LLM save path — keys persisted per account,
// URL/model stored as the authoritative operator rules, and the hot-recreate callback fired.
package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSetLLMConfigSavesAndRecreates 验证保存→落盘→热重建→回读脱敏的完整闭环。
func TestSetLLMConfigSavesAndRecreates(t *testing.T) {
	s, admin := newAdminTestServer(t)
	// 注入假 recreate 捕获参数（生产由 main 换成真客户端重建）
	var gotURL, gotModel string
	var gotKeys []string
	called := 0
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batch int, classifier string) {
		called++
		gotKeys = keys
		gotURL = url
		gotModel = model
	})
	// example.com 为公网可解析域名，满足出呼 SSRF 校验（拒绝内网/回环）
	body := `{"api_keys":["kira_aaa111","kira_bbb222"],"api_url":"https://example.com/v1/chat/completions","model":"qwen-test-free"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", body)); rr.Code != 200 {
		t.Fatalf("保存应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// ③ 热重建回调被触发且带全参数
	if called != 1 || gotURL != "https://example.com/v1/chat/completions" || gotModel != "qwen-test-free" || len(gotKeys) != 2 {
		t.Fatalf("llmRecreate 参数不符: called=%d url=%s model=%s keys=%d", called, gotURL, gotModel, len(gotKeys))
	}
	// ① 多密钥按账号落 auth 配置（启动装配 StoredLLMConfig 路径的密钥来源）
	if v, ok := s.auth.GetConfig(admin.ID, "llm_api_keys"); !ok || v != "kira_aaa111,kira_bbb222" {
		t.Fatalf("密钥未按账号落盘: %q", v)
	}
	// ② 回读：URL/模型即保存值，密钥脱敏（GAP2-W2 不外泄明文）
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/llm", ""))
	var echo struct {
		APIURL  string   `json:"api_url"`
		Model   string   `json:"model"`
		APIKeys []string `json:"api_keys"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &echo); err != nil {
		t.Fatalf("回读解析: %v", err)
	}
	if echo.APIURL != "https://example.com/v1/chat/completions" || echo.Model != "qwen-test-free" {
		t.Fatalf("回读值不符: %+v", echo)
	}
	// 短密钥（<12 位）全掩为 ***、长密钥前4后4——两种形态都不得露出原文
	if len(echo.APIKeys) != 2 || echo.APIKeys[0] == "kira_aaa111" || echo.APIKeys[1] == "kira_bbb222" {
		t.Fatalf("回读密钥必须脱敏: %+v", echo.APIKeys)
	}
}

// TestSetLLMConfigMaskedKeysPreserved 验证脱敏哨兵回提交不覆盖库中真钥
// （前端 GET 回填 → 用户只改模型再保存时，密钥槽位原样保留）。
func TestSetLLMConfigMaskedKeysPreserved(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batch int, classifier string) {})
	first := `{"api_keys":["kira_real_secret"],"api_url":"https://example.com/v1/chat/completions","model":"m1"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", first)); rr.Code != 200 {
		t.Fatalf("首存应 200, got %d", rr.Code)
	}
	// 第二次提交：密钥是脱敏形态（模拟前端回填后原样再存），只改模型
	second := `{"api_keys":["kira…cret"],"api_url":"https://example.com/v1/chat/completions","model":"m2"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", second)); rr.Code != 200 {
		t.Fatalf("二存应 200, got %d", rr.Code)
	}
	if v, _ := s.auth.GetConfig(admin.ID, "llm_api_keys"); v != "kira_real_secret" {
		t.Fatalf("脱敏哨兵覆盖了真钥: %q", v)
	}
}
