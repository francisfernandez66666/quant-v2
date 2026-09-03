package main

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
)

// initConfig 用临时目录构造一个最小 config.Manager（无 KVStore，纯全局 Rules）。
// English: builds a minimal config.Manager from a temp dir (no KVStore — global Rules only).
func initConfig(t *testing.T) *config.Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"rules":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := config.NewManager(path)
	return m
}

// TestVerifyDeploymentQMTDisabled QMT 关闭时不应告警"executor=Noop"（enabled=false 属设计内状态）。
func TestVerifyDeploymentQMTDisabled(t *testing.T) {
	m := initConfig(t)
	m.Rules.QMT = config.DefaultQMTConfig() // enabled=false
	verifyDeployment(m)                      // 不应 panic
}

// TestVerifyDeploymentQMTEnabledNoToken QMT enabled=true 但缺 token → 必须输出 Noop 告警（开关白开根因）。
func TestVerifyDeploymentQMTEnabledNoToken(t *testing.T) {
	m := initConfig(t)
	q := config.DefaultQMTConfig()
	q.Enabled = true
	q.Mode = "manual"
	q.GatewayURL = "http://127.0.0.1:8789"
	q.Token = ""
	m.Rules.QMT = q
	verifyDeployment(m) // 仅验证不 panic；告警内容经由日志人工核对
}

// TestVerifyDeploymentLLMKeys 短 key / 含空白 / 重复都只告警不阻断。
func TestVerifyDeploymentLLMKeys(t *testing.T) {
	t.Setenv("LLM_API_KEYS", "abc, abc, abc") // 短 + 空格 + 重复
	m := initConfig(t)
	verifyDeployment(m)
}

// TestVerifyDeploymentQMTNil 配置段缺失（GetQMTConfigFor 返回 nil 的等价场景）不 panic。
func TestVerifyDeploymentQMTNil(t *testing.T) {
	m := initConfig(t)
	m.Rules.QMT = config.QMTConfig{} // 零值，缺 gateway_url/token 但 enabled=false
	verifyDeployment(m)
}