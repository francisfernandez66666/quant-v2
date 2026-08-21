// C3 纸面开仓止盈/止损映射测试（阶段1.2 两本账合一后保留：paperOpenBuy 已由
// registry.paperMirror 镜像取代，映射函数仍供镜像与百分比止盈/止损提醒使用）。
// English: C3 TP/SL mapping tests (kept after the unified-book refactor — paperOpenBuy was superseded
// by the registry.paperMirror; the mapping still serves the mirror and percentage TP/SL alerts).
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
)

// TestPaperOpenTpSl 战法→止盈/止损百分比映射（比例源 ×100，默认值兜底）。
// English: TestPaperOpenTpSl strategy→take-profit/stop-loss percentage mapping (ratio source ×100, defaults as fallback).
func TestPaperOpenTpSl(t *testing.T) {
	sc := config.NewManager("").GetStrategyConfig()
	if tp, sl := paperOpenTpSl("dragon", sc); tp != 10 || sl != 8 {
		t.Errorf("dragon 应 10/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("double_bump", sc); tp != 15 || sl != 8 {
		t.Errorf("double_bump 应 15/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("n_shape", sc); tp != 10 || sl != 8 {
		t.Errorf("n_shape 应 10/8, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("dragon_return", sc); tp != 25 || sl != 5 {
		t.Errorf("dragon_return 应 25/5, got %.0f/%.0f", tp, sl)
	}
	if tp, sl := paperOpenTpSl("手动", nil); tp != 10 || sl != 8 {
		t.Errorf("默认 应 10/8, got %.0f/%.0f", tp, sl)
	}
}
