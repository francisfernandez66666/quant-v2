// replay_test.go — 回放引擎单测：summarize 统计口径、chgPct/avgVolK/toDataKLine 工具、
// 以及库规则适配器缺省出场（§GAP2.2 负号语义）回归。
// English: replay engine tests — summarize stats, helper functors, and the rule-adapter default-exit
// regression (§GAP2.2 negative-sign semantics).
package btreplay

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy"
)

// TestSummarize 验证胜率/平均盈亏/盈亏比/平均持仓的统计口径。
func TestSummarize(t *testing.T) {
	trades := []trade{
		{Strategy: "DoubleBump", PnlPct: 10, HoldDays: 2},
		{Strategy: "DoubleBump", PnlPct: -5, HoldDays: 1},
		{Strategy: "DoubleBump", PnlPct: 20, HoldDays: 3},
		{Strategy: "DoubleBump", PnlPct: -5, HoldDays: 1},
	}
	s := summarize(trades)
	if s.Count != 4 {
		t.Fatalf("Count=%d, want 4", s.Count)
	}
	if s.Win != 2 || s.Loss != 2 {
		t.Fatalf("Win/Loss=%d/%d, want 2/2", s.Win, s.Loss)
	}
	if s.WinRate != 50 {
		t.Fatalf("WinRate=%f, want 50", s.WinRate)
	}
	if s.AvgWinPct != 15 {
		t.Fatalf("AvgWinPct=%f, want 15", s.AvgWinPct)
	}
	if s.AvgLossPct != -5 {
		t.Fatalf("AvgLossPct=%f, want -5", s.AvgLossPct)
	}
	if s.ProfitFactor != 3 {
		t.Fatalf("ProfitFactor=%f, want 3", s.ProfitFactor)
	}
	if s.AvgHold != 1.75 {
		t.Fatalf("AvgHold=%f, want 1.75", s.AvgHold)
	}
}

// TestSummarizeEmpty 无交易时统计应为零值且不除零。
func TestSummarizeEmpty(t *testing.T) {
	s := summarize(nil)
	if s.Count != 0 || s.WinRate != 0 || s.ProfitFactor != 0 || s.AvgHold != 0 {
		t.Fatalf("empty summary = %+v, want zero", s)
	}
}

// TestChgPct 涨跌幅百分比口径。
func TestChgPct(t *testing.T) {
	if got := chgPct(11, 10); got != 10 {
		t.Fatalf("chgPct(11,10)=%f, want 10", got)
	}
	if got := chgPct(9.5, 10); got != -5 {
		t.Fatalf("chgPct(9.5,10)=%f, want -5", got)
	}
	if got := chgPct(0, 0); got != 0 {
		t.Fatalf("chgPct(0,0)=%f, want 0", got)
	}
}

// TestAvgVolK 取截止第 n 根的最近 lookback 根量均值。
func TestAvgVolK(t *testing.T) {
	klines := make([]data.KLine, 22)
	for i := range klines {
		klines[i].Volume = float64(i + 1)
	}
	// 截止第 21 根（klines[20]），取最近 20 根即 klines[1..20] 均值
	got := avgVolK(klines, 21, 20)
	var sum float64
	for i := 1; i <= 20; i++ {
		sum += float64(i + 1)
	}
	if got != sum/20 {
		t.Fatalf("avgVolK=%f, want %f", got, sum/20)
	}
	// 总根数不足窗口时返回 0
	if got := avgVolK(klines, 10, 20); got != 0 {
		t.Fatalf("avgVolK(10,20)=%f, want 0", got)
	}
}

// TestToDataKLine store.Bar 到 data.KLine 的字段映射（Date 解析 YYYYMMDD，Vol 映射到 Volume）。
func TestToDataKLine(t *testing.T) {
	bars := []store.Bar{{Date: "20230101", Open: 1, High: 2, Low: 0.5, Close: 1.5, Vol: 100, Amount: 150}}
	out := toDataKLine(bars)
	if len(out) != 1 {
		t.Fatalf("len=%d, want 1", len(out))
	}
	if out[0].Date.Format("20060102") != "20230101" {
		t.Fatalf("Date=%s, want 20230101", out[0].Date.Format("20060102"))
	}
	if out[0].Close != 1.5 || out[0].Volume != 100 {
		t.Fatalf("Close/Volume=%f/%f, want 1.5/100", out[0].Close, out[0].Volume)
	}
}

// TestRuleEvalAdapterExitTrailingSign §GAP2.2 回归：库规则缺省出场必须是负号语义
// （从阶段高点回撤达 8% 才触发），与实盘 genericTrailingExitWith 同口径。
// 旧实现缺省 +8.0：任何曾盈利的持仓当日即被"移动止盈"平仓（持仓≈1天）。
// English: regression for §GAP2.2 — the rule adapter's default exit must require an actual
// drawdown (≤ -8% from stage high), matching the live genericTrailingExitWith semantics.
func TestRuleEvalAdapterExitTrailingSign(t *testing.T) {
	a := &ruleEvalAdapter{name: "测试规则", ruleID: "fac_1"}
	now := time.Date(2026, 8, 25, 15, 0, 0, 0, time.Local)

	t.Run("曾盈利未回撤不触发", func(t *testing.T) {
		ctx := &strategy.ExitContext{
			CostPrice: 100, CurPrice: 105, // 浮盈且创新高：旧实现此处立即平仓
			EntryMeta: map[string]float64{}, Now: now,
		}
		if res, exited := a.Exit(ctx, nil); exited {
			t.Fatalf("浮盈未回撤不应触发移动止盈, got %+v", res)
		}
	})

	t.Run("缺省回撤达8%触发", func(t *testing.T) {
		ctx := &strategy.ExitContext{
			CostPrice: 100, CurPrice: 108,
			EntryMeta: map[string]float64{"highest_price": 120}, // 从高点回撤 -10%
			Now:       now,
		}
		res, exited := a.Exit(ctx, nil)
		if !exited || res == nil || res.Reason != "回撤止损(移动止盈)" {
			t.Fatalf("回撤-10%% 应触发移动止盈, got %v %v", res, exited)
		}
	})

	t.Run("覆盖参数按审批值生效", func(t *testing.T) {
		trail := 15.0
		aa := &ruleEvalAdapter{name: "测试规则", ruleID: "fac_1", trailOverride: &trail}
		ctx := &strategy.ExitContext{
			CostPrice: 100, CurPrice: 95,
			EntryMeta: map[string]float64{"highest_price": 110}, // 回撤 -13.6%，未达 -15%
			Now:       now,
		}
		if _, exited := aa.Exit(ctx, nil); exited {
			t.Fatalf("回撤-13.6%% 未达 -15%% 不应触发")
		}
		ctx.CurPrice = 92 // 回撤 -16.4% ≤ -15%
		if _, exited := aa.Exit(ctx, nil); !exited {
			t.Fatalf("回撤-16.4%% 应触发")
		}
	})
}
