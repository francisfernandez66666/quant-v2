// qmt_shared_engine_test.go — §WMQ-1（20260917）：共享引擎 QMT 配置热同步空洞回归。
// 缺口（docs/BUGFIX_WATCHLIST_20260917.md #4）：syncAccountConfig 对共享引擎（userID==""）
// 整体 return——QueueConfigUpdate 永不入队，配置保存后待装配/executor 切换零生效，
// 靠 FIX#11 告警兜底。修复后共享引擎按 qmtCfgUserID（首建/管理员成员，与构建期
// registry 装配同源）热同步 QMT 配置。
package engine

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// 回归钉：共享引擎（userID 为空）在管理员改过 QMT 配置后必须把新配置排进待生效队列，
// 而不是像旧实现那样整体跳过、等到重启才生效。
func TestSharedEngineQueuesQMTConfig(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wmq1.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cm := config.NewManager("")
	cm.Get().QMT.Enabled = true
	cm.Get().QMT.GatewayURL = "http://first-host:8789"

	// 共享引擎：.userID 为空；QMT 控制器在构建期由 registry 装配（归属首建成员 u_ops）。
	e := &Engine{}
	e.SetQMT(trading.NewController(trading.NoopExecutor{}, db, "", config.DefaultQMTConfig(), nil), db)
	e.cfgMgr = cm
	e.SetQMTCfgSource("u_ops")
	// 模拟配置保存（管理员改网关地址后 5s 热同步轮询到达）。
	cm.Get().QMT.GatewayURL = "http://new-host:8789"

	e.syncAccountConfig()

	// 待生效队列应有该配置（旧实现此处队列为空——重启前永不生效的空洞）。
	ctrl := e.QMTController()
	if ctrl == nil {
		t.Fatalf("qmt controller nil")
	}
	if !ctrl.ApplyPendingConfig() {
		t.Fatalf("共享引擎 syncAccountConfig 后待生效队列应为非空（旧实现在 userID==\"\" 时整体跳过）")
	}
	if got := ctrl.Config(); got.GatewayURL != "http://new-host:8789" || !got.Enabled {
		t.Fatalf("应用后配置未生效: gateway=%q enabled=%v", got.GatewayURL, got.Enabled)
	}
}

// 共享引擎未配置 QMT 数据源时跳过同步。
func TestSharedEngineWithoutQMTSourceSkipsSync(t *testing.T) {
	// 无 QMT 控制器 & 无热同步源的纯共享引擎：syncAccountConfig 不应 panic（维持旧跳过语义）。
	e := &Engine{}
	e.syncAccountConfig()
}
