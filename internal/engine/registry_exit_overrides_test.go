// registry_exit_overrides_test.go §P2-d 实盘接线集成测试：
// 引擎账号 runner 装配必须同步种子规则级出场覆盖注册表——扫参审批写进
// applied_*.json 的出场参数，对懒加载构建的实盘引擎即时生效。
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func TestNewAccountRunnersSeedsExitOverrides(t *testing.T) {
	dir := t.TempDir()
	entry := map[string]any{
		"id": "fac_7", "name": "因子战法#7", "enabled": true,
		"candidate_id": 7, "applied_at": "2026-08-23 00:00:00",
		"factors":       []string{"mom_5"},
		"weights":       map[string]float64{"mom_5": 1},
		"directions":    map[string]int{"mom_5": 1},
		"buy_threshold": 60.0, "horizon": 5,
		"exit_trail_pct": 6.0, "exit_max_hold_days": 9,
	}
	b, _ := json.Marshal([]map[string]any{entry})
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	runners := newAccountRunners(config.NewManager(""), nil, "tester", dir)
	if len(runners) == 0 {
		t.Fatal("runners 未构建")
	}
	trail, hold, ok := combat_agent.RuleExitOverrideFor("因子战法#7")
	if !ok || trail != 6 || hold != 9 {
		t.Fatalf("装配后覆盖未生效: ok=%v trail=%v hold=%v", ok, trail, hold)
	}
	if _, _, ok := combat_agent.RuleExitOverrideFor("fac_7"); !ok {
		t.Fatal("ID 键缺失")
	}
}
