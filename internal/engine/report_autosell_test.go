// 文件：report_autosell_test.go
// 包名：engine
// 模块用途：FIX#15 report 账本自动执行卖出（手动录入持仓/未进 paper 账本的持仓）——
// close 信号全平、trim 信号半仓每日一次去重，且 paper 账本已持有的 code 不再重复处理。
// English: report-book auto sell (FIX#15) — close-signal full exit, trim-signal half once per day,
// and codes already held by the paper engine are left to paper (no double-selling).

package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
)

// reportAutoSellEngine 构造 FIX#15 测试引擎：report 账本 + 可选的 paper 引擎。
// English: builds a FIX#15 test engine — a report book plus an optional paper engine.
func reportAutoSellEngine(t *testing.T, withPaper bool) (*Engine, *report.Report, *paper.Engine) {
	t.Helper()
	rpt := report.New(t.TempDir() + "/report.json")
	e := &Engine{rpt: rpt}
	if withPaper {
		pe := paper.New(paper.Config{Enabled: true, FixedAmount: 10000, MaxPositions: 10, InitialCapital: 100000}, t.TempDir()+"/paper.json")
		e.SetPaper(pe)
	}
	return e, rpt, e.paper
}

// TestAutoExitReportSellsClose report 账本 close 信号 → 全平退出。
// English: a close signal on the report book fully exits the position.
func TestAutoExitReportSellsClose(t *testing.T) {
	e, rpt, _ := reportAutoSellEngine(t, false)
	rpt.LogSignalWithMetaQty("r1", "600000", "浦发银行", "做多", "手动", 10.0, 8, 5, 400, nil)

	e.autoExitReportSells([]combat_agent.Signal{
		{Code: "600000", Name: "浦发银行", Direction: "提醒", Action: "卖出", AlertType: "清仓"},
	}, map[string]*data.StockInfo{"600000": {Code: "600000", Name: "浦发银行", Price: 9.2}})

	for _, p := range rpt.HeldPositions() {
		if p.Code == "600000" {
			t.Fatalf("close 信号后 report 持仓应已全平, got %+v", p)
		}
	}
}

// TestAutoExitReportSellsTrimHalfOnceDaily report 账本 trim 信号 → 半仓且每码每日一次。
// English: a trim signal on the report book halves the position once per code per trading day.
func TestAutoExitReportSellsTrimHalfOnceDaily(t *testing.T) {
	e, rpt, _ := reportAutoSellEngine(t, false)
	rpt.LogSignalWithMetaQty("r2", "000300", "平安", "做多", "手动", 10.0, 8, 5, 400, nil)

	sig := combat_agent.Signal{Code: "000300", Name: "平安", Direction: "提醒", Action: "卖出", AlertType: "减仓"}
	quotes := map[string]*data.StockInfo{"000300": {Code: "000300", Name: "平安", Price: 9.5}}

	e.autoExitReportSells([]combat_agent.Signal{sig}, quotes)
	pos := mustReportHold(t, rpt, "000300")
	if pos.Quantity != 200 {
		t.Fatalf("首次 trim 应剩 200 股, got %.0f", pos.Quantity)
	}

	// 同日第二次 trim 不去重（已被 reportTrimDone 挡住）
	e.autoExitReportSells([]combat_agent.Signal{sig}, quotes)
	if p := mustReportHold(t, rpt, "000300"); p.Quantity != 200 {
		t.Fatalf("同日二次 trim 应被去重仍剩 200 股, got %.0f", p.Quantity)
	}
}

// TestAutoExitReportSellsSkipsPaperHeld paper 账本已持有的 code → 留给 paper，report 不重复处理。
// English: a code already held by the paper engine is left to paper; the report book does not double-sell it.
func TestAutoExitReportSellsSkipsPaperHeld(t *testing.T) {
	e, rpt, pe := reportAutoSellEngine(t, true)
	// paper 账本预置同一 code 的持仓
	pe.SetStrategyPools([]string{"n_shape"})
	if err := pe.BuyExInPool("600111", "北方稀土", "手动", "n_shape", 10.0, 10.0, 100, nil); err != nil {
		t.Fatalf("paper 预置持仓失败: %v", err)
	}
	// report 账本也有同名持仓
	rpt.LogSignal("r3", "600111", "北方稀土", "做多", "手动", 10.0, 8, 5)

	sig := combat_agent.Signal{Code: "600111", Name: "北方稀土", Direction: "提醒", Action: "卖出", AlertType: "清仓"}
	e.autoExitReportSells([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600111": {Code: "600111", Name: "北方稀土", Price: 9.0}})

	// report 账本不处理（交给 paper），持仓仍保留
	if len(rpt.HeldPositions()) != 1 {
		t.Fatalf("paper 持有的 code 不应在 report 重复卖出, got %d 条持仓", len(rpt.HeldPositions()))
	}
}

// mustReportHold 断言 report 账本存在某持仓并返回；不存在则 Fatal。
// English: asserts the report book holds the code and returns it; Fatals otherwise.
func mustReportHold(t *testing.T, rpt *report.Report, code string) *report.ExecLog {
	t.Helper()
	for i := range rpt.HeldPositions() {
		p := rpt.HeldPositions()[i]
		if p.Code == code {
			return &p
		}
	}
	t.Fatalf("report 账本应持有 %s", code)
	return nil
}
