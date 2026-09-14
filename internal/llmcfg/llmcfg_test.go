package llmcfg

// §UI-AUTHORITATIVE 统一接入运营配置源的回归：三个入口（引擎/backtest/latency）共用
// Resolve 的优先级链——UI 保存 > 环境变量 > 全局 auth > 全局 config.json。
// 本测试锁死两件事：① 运营在设置页保存过的配置必须压过环境变量（生产 NSSM 旧 env 不再顶掉）；
// ② 无保存时行为回退旧链（env → 全局），不影响存量部署。
// English: pins the resolver precedence — UI-saved operator config beats process env;
// with nothing saved the legacy env/global chain still applies.

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// newHarness 建一套临时 认证+配置 管理器并创建管理员（运营归属账号）。
func newHarness(t *testing.T) (*config.Manager, *auth.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	am := auth.NewManager(dir)
	if err := am.Init(); err != nil { // 建库：未 Init 的 Manager 读写会 panic
		t.Fatalf("auth init: %v", err)
	}
	u, err := am.CreateUser("boss", "Passw0rd!x", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	cm := config.NewManager(filepath.Join(dir, "config.json"))
	cm.SetStore(am)
	cm.SetOperatorID(u.ID)
	return cm, am, u.ID
}

// TestResolveSavedWinsOverEnv 设置页保存的 kiraai 配置 + 按账号多 key 必须全面压过 env。
func TestResolveSavedWinsOverEnv(t *testing.T) {
	cm, am, uid := newHarness(t)
	cm.SetLLMConfigFor(uid, &config.LLMConfig{APIURL: "https://kiraai.vn/v1/chat/completions", Model: "qwen3.8-flash-free"})
	am.SetConfig(uid, "llm_api_keys", "kira_a111, kira_b222")
	// 模拟 NSSM 遗留旧 bootstrap env——按修复口径必须被忽略
	t.Setenv("LLM_API_URL", "https://api.siliconflow.cn/v1/chat/completions")
	t.Setenv("LLM_MODEL", "THUDM/GLM-Z1-9B-0414")
	t.Setenv("LLM_API_KEYS", "sk-decoy1,sk-decoy2")

	got := Resolve(cm, am)
	if got.APIURL != "https://kiraai.vn/v1/chat/completions" || got.Model != "qwen3.8-flash-free" {
		t.Fatalf("UI 保存未压过 env: %+v", got)
	}
	if len(got.APIKeys) != 2 || got.APIKeys[0] != "kira_a111" || got.APIKeys[1] != "kira_b222" {
		t.Fatalf("按账号密钥解析错: %+v", got.APIKeys)
	}
	if got.APIKey != "kira_a111" {
		t.Fatalf("主 key 应取首把: %s", got.APIKey)
	}
}

// TestResolveEnvFallbackWhenNothingSaved 无任何保存时回退环境变量（存量部署行为不变）。
func TestResolveEnvFallbackWhenNothingSaved(t *testing.T) {
	cm, am, _ := newHarness(t)
	t.Setenv("LLM_API_KEY", "sk-env-only")
	t.Setenv("LLM_API_URL", "https://env.example/v1/chat/completions")
	t.Setenv("LLM_MODEL", "env-model")
	got := Resolve(cm, am)
	if got.APIURL != "https://env.example/v1/chat/completions" || got.Model != "env-model" || got.APIKey != "sk-env-only" {
		t.Fatalf("env 兜底失效: %+v", got)
	}
}

// TestResolveGlobalAuthLegacy 无保存无 env 时落到历史全局 auth（"" 键）——兼容旧单机形态。
func TestResolveGlobalAuthLegacy(t *testing.T) {
	cm, am, _ := newHarness(t)
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_API_KEYS", "")
	t.Setenv("LLM_API_URL", "")
	t.Setenv("LLM_MODEL", "")
	am.SetConfig("", "llm_api_key", "sk-global-legacy")
	am.SetConfig("", "llm_api_url", "https://legacy.example/v1")
	got := Resolve(cm, am)
	if got.APIKey != "sk-global-legacy" || got.APIURL != "https://legacy.example/v1" {
		t.Fatalf("全局 auth 兜底失效: %+v", got)
	}
}
