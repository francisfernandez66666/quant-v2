// 文件概述（internal/combat_agent/prod_t1_exit_test.go）
// §PROD-T1（2026-09-18 生产实录）：EntryAt 零值守卫回归测试。
// 实盘建议视图（trading.execLogsFromReal 映射 real_positions）早期不携带开仓日，
// report.ExecLog.EntryAt 零值被 buildExitContext 格式化为 "0001-01-01"，
// 使"持仓超期离场"判定（today ≥ entry+maxHoldDays 交易日）对全部实盘持仓恒成立——
// 生产实录：603468 当日自动买入，14:13 即被推"止盈 … 持仓超期离场"（+0.66%）。
// 现约定：EntryAt 零值 → ctx.EntryAt=""（未知），超期分支跳过；真实开仓日仍正常判定。
// English: §PROD-T1 regression — a zero entry time must yield an empty EntryAt (unknown) so the
// hold-timeout check is skipped instead of firing on "0001-01-01"; real entry dates still trigger.
package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/report"
)

// TestBuildExitContextZeroEntryAtIsEmpty buildExitContextZeroEntryAtIsEmpty。
// 验证入场日最高价=0 时出场上下文退化为空值，不误用持仓期最高价。
func TestBuildExitContextZeroEntryAtIsEmpty(t *testing.T) {
	pos := report.ExecLog{Code: "603468", Name: "津富士达", EntryPrice: 22.61, HighestPrice: 22.61, Status: "持仓中"}
	// EntryAt 零值（未开仓日场景）：必须得到空串而非 "0001-01-01"
	ctx := buildExitContext(pos, 22.76, nil, time.Now())
	if ctx.EntryAt != "" {
		t.Fatalf("EntryAt 零值应映射为空串（未知），got %q", ctx.EntryAt)
	}
	// 超期判定对未知开仓日必须跳过：不得产出"持仓超期离场"
	if r := genericTrailingExit(ctx, time.Now()); r != nil && r.Reason == "持仓超期离场" {
		t.Fatalf("开仓日未知不得误判超期，got %s", r.Reason)
	}
	// 有真实开仓日则照常传递
	pos.EntryAt = time.Date(2026, 9, 1, 9, 35, 0, 0, time.Local)
	ctx2 := buildExitContext(pos, 22.76, nil, time.Now())
	if ctx2.EntryAt != "2026-09-01" {
		t.Fatalf("真实开仓日应透传 YYYY-MM-DD，got %q", ctx2.EntryAt)
	}
}

// TestGenericTrailingExitStillTimeoutsForOldEntry 反向保护：守卫不得把真超期也吞掉——
// 开仓日远早于 maxHoldDays 个交易日前，"持仓超期离场"必须照常触发。
// English: guard must not swallow genuine timeouts — an entry older than maxHoldDays trading
// days still triggers the overdue exit.
func TestGenericTrailingExitStillTimeoutsForOldEntry(t *testing.T) {
	pos := report.ExecLog{
		Code: "600000", Name: "浦发", EntryPrice: 10, HighestPrice: 10.1,
		EntryAt: time.Now().AddDate(0, -2, 0), Status: "持仓中",
	}
	ctx := buildExitContext(pos, 10.05, nil, time.Now())
	r := genericTrailingExit(ctx, time.Now())
	if r == nil || r.Reason != "持仓超期离场" {
		t.Fatalf("两月前开仓应触发超期离场，got %+v", r)
	}
}
