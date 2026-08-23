package strategy

import (
	"testing"
	"time"
)

func TestApplyTrailingHoldExitDisabled(t *testing.T) {
	// 两个旋钮都未配置(0)→返回 nil，既有规则不受影响（默认零行为变更保证）
	ctx := &ExitContext{CostPrice: 100, CurPrice: 80,
		EntryMeta: map[string]float64{"highest_price": 110}, Now: time.Now()}
	if res := ApplyTrailingHoldExit(ctx, 0, 0); res != nil {
		t.Fatalf("未配置应不干预: %+v", res)
	}
	// 非法价
	ctx2 := &ExitContext{CostPrice: 0, CurPrice: 0, Now: time.Now()}
	if res := ApplyTrailingHoldExit(ctx2, 5, 5); res != nil {
		t.Fatalf("非法价应放行: %+v", res)
	}
}

func TestApplyTrailingHoldExitTrailing(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	// 成本100，阶段高110，现价104.4 → 回撤 -5.09% ≤ -5% 且曾盈利 → 移动止盈
	ctx := &ExitContext{CostPrice: 100, CurPrice: 104.4,
		EntryAt: "2026-08-22", EntryMeta: map[string]float64{"highest_price": 110}, Now: now}
	res := ApplyTrailingHoldExit(ctx, 5, 30)
	if res == nil || res.Reason != "扫参止盈(移动回撤)" {
		t.Fatalf("移动止盈未触发: %+v", res)
	}
	// 未盈利过（高点=成本）不触发
	ctx.EntryMeta["highest_price"] = 100
	if res := ApplyTrailingHoldExit(ctx, 5, 30); res != nil {
		t.Fatalf("未盈利不应触发: %+v", res)
	}
}

func TestApplyTrailingHoldExitTimeout(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	// EntryAt=08-19 → 恰好3天；maxHold=3 → 超期离场
	ctx := &ExitContext{CostPrice: 100, CurPrice: 101,
		EntryAt: "2026-08-20", EntryMeta: map[string]float64{}, Now: now}
	if res := ApplyTrailingHoldExit(ctx, 8, 3); res == nil || res.Reason != "扫参超期离场" {
		t.Fatalf("超期未触发: %+v", res)
	}
	// 未到期不触发
	ctx.EntryAt = "2026-08-22"
	if res := ApplyTrailingHoldExit(ctx, 8, 3); res != nil {
		t.Fatalf("未超期不应触发: %+v", res)
	}
}
