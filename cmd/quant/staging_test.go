// staging_test.go — §WS-G staging 环境隔离单测：
// getDataDir 强制 ~/.quant-staging + isStaging 判定（QUANT_ENV=staging）。
// §W7-FATAL（2026-10-06 修复批 波 7）把 fail-fast 从 log.Fatalf 改成 verifyDeployment 返回 error
// （Fatalf 在 main 的 defer 链之外终结进程：fetcher/qmtFeed/nAgent 的 Stop 与状态文件句柄 Close
// 全部被跳过）。语义没变——staging + qmt.enabled=true 依然拒绝启动、依然非零退出码——
// 但**能单测了**：原来这条判据只有 staging_up.sh 的侧护栏兜着，仓内断言为零（头注释自认「属进程终止
// 语义，集成验证」），正是「验证不到所以不验」的形态。现在配对反证（staging 必红 / 非 staging 必不红）。
// English: the fail-fast now returns an error instead of log.Fatalf, so the guard is unit-testable.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

// setenv 在用例内临时改写环境变量：先记录原值与是否存在，再用 t.Cleanup 精确还原，
// 因为 staging 判定读的是进程级 QUANT_ENV/QUANT_DATA_DIR，不还原会污染后续用例。
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

// TestStagingFailFastRefusesRealGateway §WS-G ＋ §W7-FATAL 正腿：staging 且 qmt.enabled=true 必须被拒。
// 判据读的是「verifyDeployment 交出 error」而不是「日志里出现过一句话」——后者会让改文案的人以为
// 自己在改注释，实际把资损级护栏改成了纸糊的。
func TestStagingFailFastRefusesRealGateway(t *testing.T) {
	setenv(t, "QUANT_ENV", "staging")
	rules := &config.Rules{} // 独立副本：DefaultRules 是包级共享指针，测试不许往上写
	rules.QMT.Enabled = true
	cfgMgr := config.NewManagerWithRules(rules)
	err := verifyDeployment(cfgMgr, nil)
	if err == nil {
		t.Fatal("staging + qmt.enabled=true 必须拒绝启动（返回 error），实得 nil")
	}
	if !strings.Contains(err.Error(), "qmt.enabled") {
		t.Errorf("拒绝原因必须点名 qmt.enabled，实得 %q", err.Error())
	}
}

// TestStagingAllowedWhenDisabled 反证配对的另一半：staging 但 qmt.enabled=false 不得拒绝
// （只留正腿的话，「永远返回 error」也能骗过上面那条——同 §DEADGAUGE 的成对反证口径）。
func TestStagingAllowedWhenDisabled(t *testing.T) {
	setenv(t, "QUANT_ENV", "staging")
	rules := &config.Rules{} // 独立副本：DefaultRules 是包级共享指针，测试不许往上写
	rules.QMT.Enabled = false
	cfgMgr := config.NewManagerWithRules(rules)
	if err := verifyDeployment(cfgMgr, nil); err != nil {
		t.Fatalf("staging 且 qmt.enabled=false 不应拒绝启动, got %v", err)
	}
}

// TestNonStagingNeverRefused 第二组对照：非 staging 环境即便 qmt.enabled=true 也不归这条守卫管
// （实盘环境本来就该连着网关；把它一起拦了就是拿卫生项打掉了主链路）。
func TestNonStagingNeverRefused(t *testing.T) {
	setenv(t, "QUANT_ENV", "")
	rules := &config.Rules{} // 独立副本：DefaultRules 是包级共享指针，测试不许往上写
	rules.QMT.Enabled = true
	cfgMgr := config.NewManagerWithRules(rules)
	if err := verifyDeployment(cfgMgr, nil); err != nil {
		t.Fatalf("非 staging 不应被 §WS-G 守卫拒绝, got %v", err)
	}
}
