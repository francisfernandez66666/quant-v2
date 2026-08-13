package combat_agent

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestReloadConfigAppliesPositionThreshold 热加载应同时应用持仓当日跌幅提醒阈值，
// 保证前端/手动改 config.json 后无需重启即可生效。
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