// 文件：loader_test.go
// 包名：combat_agent
// 所属模块：「对抗式/量化交易决策 agent（买卖信号、风控）」
// 模块职责：本文件属于 对抗式/量化交易决策 agent（买卖信号、风控），负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

package combat_agent

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestReloadConfigAppliesPositionThreshold 热加载应同时应用持仓当日跌幅提醒阈值，
// 保证前端/手动改 config.json 后无需重启即可生效。
// English: TestReloadConfigAppliesPositionThreshold hot reload must also apply the position daily-drop alert threshold, so frontend/manual edits to config.json take effect without a restart.
func TestReloadConfigAppliesPositionThreshold(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"rules":{"strategy":{"momentum":{}},"position":{"daily_drop_alert_pct":7}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	a := New(&config.StrategyConfig{})
	if a.PositionDailyDropPct() != 0 {
		t.Fatalf("初始阈值应为 0, got %v", a.PositionDailyDropPct())
	}
	a.reloadConfig(p)
	if got := a.PositionDailyDropPct(); got != 7 {
		t.Fatalf("热加载后阈值应为 7, got %v", got)
	}
}
