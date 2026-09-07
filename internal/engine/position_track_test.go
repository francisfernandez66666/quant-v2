// C3 纸面开仓止盈/止损映射测试（阶段1.2 两本账合一后保留：paperOpenBuy 已由
// registry.paperMirror 镜像取代，映射函数仍供镜像与百分比止盈/止损提醒使用）。
// English: C3 TP/SL mapping tests (kept after the unified-book refactor — paperOpenBuy was superseded
// by the registry.paperMirror; the mapping still serves the mirror and percentage TP/SL alerts).
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
)

// TestPaperOpenTpSl 统一纪律止盈/止损映射：镜像开仓统一走 rules.paper.discipline 总纪律
// （默认止盈+15/止损−6），不再按战法各自默认；未配置（全 0）时兜底出厂默认。
// English: TestPaperOpenTpSl unified-discipline TP/SL mapping — the paper mirror uses the unified
// rules.paper.discipline (default +15/−6) for every strategy; zero config falls back to factory defaults.
func TestPaperOpenTpSl(t *testing.T) {
	disc := config.DefaultDisciplineConfig()
	if tp, sl := paperOpenTpSl(disc); tp != 15 || sl != 6 {
		t.Errorf("统一纪律应 15/6, got %.0f/%.0f", tp, sl)
	}
	// 自定义纪律（后台可配）：取值生效
	custom := config.DefaultDisciplineConfig()
	custom.TakeProfitPct, custom.StopLossPct = 20, 8
	if tp, sl := paperOpenTpSl(custom); tp != 20 || sl != 8 {
		t.Errorf("自定义纪律应 20/8, got %.0f/%.0f", tp, sl)
	}
	// 空配置兜底出厂默认
	if tp, sl := paperOpenTpSl(config.DisciplineConfig{}); tp != 15 || sl != 6 {
		t.Errorf("空纪律应兜底 15/6, got %.0f/%.0f", tp, sl)
	}
}
