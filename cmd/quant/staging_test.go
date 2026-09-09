// staging_test.go — §WS-G staging 环境隔离单测：
// getDataDir 强制 ~/.quant-staging + isStaging 判定（QUANT_ENV=staging）。
// fail-fast（qmt.enabled=true 拒绝启动）走 log.Fatalf 属进程终止语义，由 staging_up.sh 侧护栏 + 集成验证。
// English: §WS-G staging isolation unit tests — forced ~/.quant-staging data dir + env detection.
// The qmt.enabled=true fail-fast is process-fatal by design and covered by staging_up.sh's own guard.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setenv(t *testing.T, k, v string) {
	t.Helper()
	old, had := os.LookupEnv(k)
	if err := os.Setenv(k, v); err != nil {
		t.Fatalf("setenv %s: %v", k, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(k, old)
		} else {
			_ = os.Unsetenv(k)
		}
	})
}

// TestGetDataDirStagingForced QUANT_ENV=staging 时忽略 QUANT_DATA_DIR，强制 ~/.quant-staging。
func TestGetDataDirStagingForced(t *testing.T) {
	home, _ := os.UserHomeDir()
	setenv(t, "QUANT_ENV", "staging")
	setenv(t, "QUANT_DATA_DIR", "/tmp/should-be-ignored")
	if got := getDataDir(); got != filepath.Join(home, ".quant-staging") {
		t.Fatalf("staging 应强制 staging 目录, got %q", got)
	}
	if !isStaging() {
		t.Fatalf("isStaging 应 true")
	}
}

// TestGetDataDirNormalEnv QUANT_DATA_DIR 优先，缺省 ~/.quant-trading-v2。
func TestGetDataDirNormalEnv(t *testing.T) {
	home, _ := os.UserHomeDir()
	setenv(t, "QUANT_ENV", "")
	setenv(t, "QUANT_DATA_DIR", "/tmp/custom")
	if got := getDataDir(); got != "/tmp/custom" {
		t.Fatalf("QUANT_DATA_DIR 应优先, got %q", got)
	}
	setenv(t, "QUANT_DATA_DIR", "")
	if got := getDataDir(); got != filepath.Join(home, ".quant-trading-v2") {
		t.Fatalf("缺省应 ~/.quant-trading-v2, got %q", got)
	}
	if isStaging() {
		t.Fatalf("非 staging 环境 isStaging 应 false")
	}
}
