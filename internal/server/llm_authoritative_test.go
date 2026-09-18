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
	"fmt"
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

// TestSetLLMConfigRecreateUsesRealKeys §P0 2026-09-18「前端改了 LLM 的 api/key 不生效」回归：
// 热重建回调必须拿到**真钥**，绝不能是 GET 回显的脱敏哨兵。
//
// 事故机理：设置页 GET 拿到的是脱敏值（kira…1234），回填进输入框后用户只改地址/模型再保存，
// 提交回来的就是哨兵串。落库侧有「哨兵 → 原值」映射（上面那条用例覆盖了），但热重建侧此前
// 直接用 `req.APIKeys` 原样重建客户端 —— 于是**线上客户端的密钥变成了字面量 "kira…1234"**：
// llm.New 取 keys[0] 作为主钥且没有第二把可轮询，全部请求 401 → 整条 LLM 链路（归因/D1/咨询）
// 立刻哑掉，而库里那把好钥还在、页面回读也还是老样子 —— 用户看到的就是"改了没生效、改不了"。
//
// English: the hot-recreate callback must receive the *resolved* real keys, never the masked
// sentinels echoed back by GET — otherwise the live client is rebuilt with the mask as its key.
func TestSetLLMConfigRecreateUsesRealKeys(t *testing.T) {
	s, admin := newAdminTestServer(t)
	var gotKeys []string
	calls := 0
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batch int, classifier string) {
		calls++
		gotKeys = keys
	})
	first := `{"api_keys":["kira_real_secret_key_1234"],"api_url":"https://example.com/v1/chat/completions","model":"m1"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", first)); rr.Code != 200 {
		t.Fatalf("首存应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 首存（明文）走的也是热重建：这里就必须是真钥
	if len(gotKeys) != 1 || gotKeys[0] != "kira_real_secret_key_1234" {
		t.Fatalf("首存热重建应用真钥, got %+v", gotKeys)
	}
	// 模拟前端：把 GET 回读的脱敏值原样回提交（用户只改了模型）
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/llm", ""))
	var echo struct {
		APIKeys []string `json:"api_keys"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &echo); err != nil {
		t.Fatalf("回读解析: %v", err)
	}
	if len(echo.APIKeys) != 1 || !isMaskedSecret(echo.APIKeys[0]) {
		t.Fatalf("前置假设失败：回读应为脱敏哨兵, got %+v", echo.APIKeys)
	}
	second := fmt.Sprintf(`{"api_keys":[%q],"api_url":"https://example.com/v1/chat/completions","model":"m2"}`,
		echo.APIKeys[0])
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", second)); rr.Code != 200 {
		t.Fatalf("二存应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if calls != 2 {
		t.Fatalf("热重建应触发 2 次, got %d", calls)
	}
	if len(gotKeys) != 1 || gotKeys[0] != "kira_real_secret_key_1234" {
		t.Fatalf("哨兵回提交时热重建应解析为库中真钥, got %+v（收到哨兵=线上客户端会用字面量掩码发请求，全部 401）", gotKeys)
	}
}

// TestSetLLMConfigAllSentinelNoOldKeyKeepsClient 提交全为脱敏哨兵、而库中并无原值可映射时：
// 不得拿哨兵重建客户端（宁可不重建），也不得把掩码落库。
//
// §P0 2026-09-18 语义收紧：此前返回 200，于是 UI 会弹"已保存并热生效"，而实际上什么也没发生
// （库里的密钥根本没动）——这正是用户"改了没生效"的体感来源。现在明确 400 并说明原因：
// 这次提交里不含任何可用密钥材料，需要用户重新填写真钥。
// English: an all-sentinel submit with no stored original now fails loudly (400) instead of
// returning 200 while silently doing nothing.
func TestSetLLMConfigAllSentinelNoOldKeyKeepsClient(t *testing.T) {
	s, admin := newAdminTestServer(t)
	calls := 0
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batch int, classifier string) {
		calls++
	})
	body := `{"api_keys":["kira…1234"],"api_url":"https://example.com/v1/chat/completions","model":"m1"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", body)); rr.Code != 400 {
		t.Fatalf("无可用真钥应 400（不能假装成功）, got %d body=%s", rr.Code, rr.Body.String())
	}
	if calls != 0 {
		t.Fatalf("无可用真钥时不应热重建（哨兵会打坏线上客户端）, got %d 次", calls)
	}
	if v, _ := s.auth.GetConfig(admin.ID, "llm_api_keys"); v != "" {
		t.Fatalf("哨兵不得被当成真钥落库: %q", v)
	}
}

// TestSetLLMConfigBlankURLModelKeepsStored 空 api_url / model = **保持原值**，不是清空。
//
// 事故机理：咨询页的配置表单只发送用户填了的字段（api_keys/api_url/model 各自 `|| undefined`），
// 只改 Key 时提交体里就没有 api_url/model —— Go 侧一律解成空串，旧实现直接写进账号配置，
// 于是**已配好的供应商地址与模型被静默清掉**：客户端回落内置默认供应商 + llm.DefaultModel，
// 配着别家的 Key/模型名就是一片 401/404，表现为"改完 LLM 就挂了"。
//
// English: a partial submit (the consult page) must not blank the stored api_url/model.
func TestSetLLMConfigBlankURLModelKeepsStored(t *testing.T) {
	s, admin := newAdminTestServer(t)
	var gotURL, gotModel string
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batch int, classifier string) {
		gotURL, gotModel = url, model
	})
	first := `{"api_keys":["kira_first_secret_key"],"api_url":"https://example.com/v1/chat/completions","model":"qwen-x"}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", first)); rr.Code != 200 {
		t.Fatalf("首存应 200, got %d", rr.Code)
	}
	// 咨询页形态：只提交新的 Key（body 里没有 api_url / model 字段）
	second := `{"api_keys":["kira_second_secret_key"]}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/llm", second)); rr.Code != 200 {
		t.Fatalf("二存应 200, got %d", rr.Code)
	}
	if gotURL != "https://example.com/v1/chat/completions" || gotModel != "qwen-x" {
		t.Fatalf("留空不得清掉已存地址/模型: 热重建收到 url=%q model=%q", gotURL, gotModel)
	}
	cfg := s.cfg.GetLLMConfigFor(admin.ID)
	if cfg.APIURL != "https://example.com/v1/chat/completions" || cfg.Model != "qwen-x" {
		t.Fatalf("账号配置里的地址/模型被清空: %+v", cfg)
	}
	// 而 Key 必须换成新的那把
	if v, _ := s.auth.GetConfig(admin.ID, "llm_api_keys"); v != "kira_second_secret_key" {
		t.Fatalf("新 Key 未生效: %q", v)
	}
}
