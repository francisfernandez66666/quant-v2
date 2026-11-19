// flow_quadrant_alert_test.go — §FLOW-QUADRANT 减仓卖点信号构造锁。
// 三条：① Reduce 构造 SellLevel=减仓 的提醒信号（不自动减仓）；② 形态字段齐全（Direction=提醒/Action=卖出）；
// ③ 代码/名称沿用基底（信号归属到具体持仓票）。
package engine

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

func TestQuadrantReduceAlert(t *testing.T) {
	base := combat_agent.Signal{Code: "600000", Name: "浦发", Strategy: "龙头"}
	alert := quadrantReduceAlert(base, "bull_surge_high_reduce")
	if alert.Direction != "提醒" || alert.Action != "卖出" {
		t.Fatalf("形态应为提醒级卖出信号, got dir=%q action=%q", alert.Direction, alert.Action)
	}
	if alert.SellLevel != "减仓" || alert.AlertType != "减仓" {
		t.Fatalf("应为减仓级, got level=%q type=%q", alert.SellLevel, alert.AlertType)
	}
	if alert.Code != base.Code || alert.Name != base.Name {
		t.Fatalf("应沿用基底持仓票, got %+v", alert)
	}
	if !strings.Contains(alert.Reason, "bull_surge_high_reduce") {
		t.Fatalf("理由应带四象限 reason, got %q", alert.Reason)
	}
	if !strings.HasPrefix(alert.ID, "quadrant:") {
		t.Fatalf("幂等键应以 quadrant: 开头, got %q", alert.ID)
	}
}
