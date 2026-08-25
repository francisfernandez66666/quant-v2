package btreplay

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	data "quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// mkKLine 由收盘价序列构造日线（开=收、高=收+1%、低=收-1%），日期自 2024-01-01 起逐日。
// English: builds daily bars from closes (open=close, high=+1%, low=-1%), dates from 2024-01-01.
func mkKLine(closes []float64) []data.KLine {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]data.KLine, len(closes))
	for i, c := range closes {
		out[i] = data.KLine{
			Date:  base.AddDate(0, 0, i),
			Open:  c,
			Close: c,
			High:  c * 1.01,
			Low:   c * 0.99,
		}
	}
	return out
}

// ── uniformExitV2 统一出厂引擎 v2：止盈线/止损线/移动止盈/超期兜底 四条件逐日独立检查 ──

func TestUniformExitV2Trailing(t *testing.T) {
	// 只开移动止盈（trail=8%，TP/SL 关闭）：涨到 120 后回撤到 108（-10% ≤ -8% 且曾盈利）→ 离场
	closes := []float64{100, 105, 110, 115, 120, 118, 112, 108}
	kls := mkKLine(closes)
	exitJ, pnl := uniformExitV2(kls, 0, 100, 100, 0, 0, 8, 30)
	// j=6 cur=112 阶段高 120 回撤 -6.7% 未触发；j=7 cur=108 回撤 -10% 触发。
	// §GAP4.1 净额口径：pnl 含双边滑点+佣金+印花税（≈8% - 0.21% ≈ 7.79）
	if exitJ != 7 {
		t.Fatalf("exitJ=%d want 7", exitJ)
	}
	if want := costRoundTripPnl(100, 108); abs(pnl-want) > 0.01 {
		t.Fatalf("pnl=%.2f want %.2f(净额)", pnl, want)
	}
}

func TestUniformExitV2TakeProfit(t *testing.T) {
	// 止盈线优先：涨破 8% 即卖，不等回撤也不等超期
	closes := []float64{100, 104, 109, 112, 115}
	kls := mkKLine(closes)
	exitJ, pnl := uniformExitV2(kls, 0, 100, 100, 8, 0, 0, 30)
	// j=2 cur=109 → 触发止盈（净额口径见 §GAP4.1）
	if exitJ != 2 {
		t.Fatalf("exitJ=%d want 2", exitJ)
	}
	if want := costRoundTripPnl(100, 109); abs(pnl-want) > 0.01 {
		t.Fatalf("pnl=%.2f want %.2f(净额)", pnl, want)
	}
}

func TestUniformExitV2StopLoss(t *testing.T) {
	// 止损线优先于一切：跌破 -5% 立即卖出控制损失（不等盈利确认、不等超期）
	closes := []float64{100, 98, 95, 94, 96}
	kls := mkKLine(closes)
	exitJ, pnl := uniformExitV2(kls, 0, 100, 100, 20, 5, 8, 30)
	// 循环自 j=entryDay+1=2 起：j=2 cur=95 → 立即触发（净额口径见 §GAP4.1）
	if exitJ != 2 {
		t.Fatalf("exitJ=%d want 2", exitJ)
	}
	if want := costRoundTripPnl(100, 95); abs(pnl-want) > 0.01 {
		t.Fatalf("pnl=%.2f want %.2f(净额)", pnl, want)
	}
}

func TestUniformExitV2Timeout(t *testing.T) {
	// 温和上涨不触发任何止盈条件 → 持仓满 5 天超期兜底离场
	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108}
	kls := mkKLine(closes)
	entryDay := 0 + 1
	exitJ, _ := uniformExitV2(kls, 0, 100, 100, 0, 0, 8, 5)
	if exitJ-entryDay != 5 {
		t.Fatalf("exitJ=%d 持仓=%d want 5", exitJ, exitJ-entryDay)
	}
}

func TestUniformExitV2NeverProfitable(t *testing.T) {
	// 单边下跌：stageHigh 永不超过 entry → 移动止盈不触发；止损线关闭时靠超期结算亏损
	closes := []float64{100, 95, 92, 90, 89, 88}
	kls := mkKLine(closes)
	_, pnl := uniformExitV2(kls, 0, 100, 100, 0, 0, 8, 3)
	if pnl >= 0 {
		t.Fatalf("下跌场景应为亏损, got %.2f", pnl)
	}
}

func TestUniformExitV2ForcedClose(t *testing.T) {
	// 数据末尾仍未满足任何出场条件 → 末日收盘强制结算
	closes := []float64{100, 102, 103, 101}
	kls := mkKLine(closes)
	exitJ, _ := uniformExitV2(kls, 0, 100, 100, 0, 0, 50, 100)
	if exitJ != len(kls)-1 {
		t.Fatalf("exitJ=%d want %d", exitJ, len(kls)-1)
	}
}

// ── applyComboParams 组合参数应用/恢复：分战法回测的核心保证——组合之间互不污染 ──

func TestApplyComboParamsRestoreRuleAdapter(t *testing.T) {
	// 库规则适配器：应用止盈/兜底覆盖后字段生效，restore 后回到原值（nil 指针）
	a := &ruleEvalAdapter{name: "因子战法#1"}
	restore := applyComboParams(a, 15, 10, 25, 0)
	if a.trailOverride == nil || *a.trailOverride != 15 {
		t.Fatalf("止盈覆盖未生效: %v", a.trailOverride)
	}
	if a.holdOverride == nil || *a.holdOverride != 25 {
		t.Fatalf("兜底覆盖未生效: %v", a.holdOverride)
	}
	restore()
	if a.trailOverride != nil || a.holdOverride != nil {
		t.Fatalf("恢复失败: %v %v", a.trailOverride, a.holdOverride)
	}
}

func TestApplyComboParamsRestoreBuiltinConfig(t *testing.T) {
	// 内置战法适配器：改写出厂配置的出场旋钮，restore 后逐字段还原
	cfg := &config.DoubleBumpConfig{TrailingDrawbackPct: 8, DoubleBumpTakeProfitPct: 10, MaxHoldDays: 20}
	a := &doubleBumpAdapter{cfg: cfg}
	restore := applyComboParams(a, 12, 6, 30, 0)
	if cfg.TrailingDrawbackPct != 12 || cfg.MaxHoldDays != 30 {
		t.Fatalf("内置参数未生效: %+v", cfg)
	}
	restore()
	if cfg.TrailingDrawbackPct != 8 || cfg.DoubleBumpTakeProfitPct != 10 || cfg.MaxHoldDays != 20 {
		t.Fatalf("内置配置恢复失败: %+v", cfg)
	}
}

func TestApplyComboParamsUnknownAdapterSafe(t *testing.T) {
	// 未识别的适配器类型：返回空恢复函数，绝不 panic
	restore := applyComboParams(fakeAdapter{}, 10, 5, 20, 0)
	restore() // 应为 no-op
}

// fakeAdapter 仅实现 adapter 接口的最小桩，用于未知类型的健壮性验证。
type fakeAdapter struct{}

func (fakeAdapter) Name() string        { return "fake" }
func (fakeAdapter) Description() string { return "" }
func (fakeAdapter) Trigger(_ []data.KLine, _, _ float64) (map[string]float64, bool) {
	return nil, false
}
func (fakeAdapter) Exit(_ *strategy.ExitContext, _ []strategy.KLine) (*strategy.ExitResult, bool) {
	return nil, false
}

// ── 目标函数与自适应门槛 ──

func TestObjectiveValueRanking(t *testing.T) {
	a := sweepResult{ProfitFactor: 1.5, WinRate: 40, AvgWinPct: 9}
	b := sweepResult{ProfitFactor: 1.2, WinRate: 55, AvgWinPct: 5}
	if objectiveValue("profitfactor", &a) <= objectiveValue("profitfactor", &b) {
		t.Fatal("盈亏比目标排序错误")
	}
	if objectiveValue("winrate", &a) >= objectiveValue("winrate", &b) {
		t.Fatal("胜率目标排序错误")
	}
	cap := sweepResult{ProfitFactor: 500}
	if objectiveValue("profitfactor", &cap) != 99 {
		t.Fatal("盈亏比封顶失效")
	}
	// 期望收益目标：E = WR×AvgWin + (1-WR)×AvgLoss，直接取值比较
	e1 := sweepResult{Expectancy: 5.6}
	e2 := sweepResult{Expectancy: 1.2}
	if objectiveValue("expectancy", &e1) <= objectiveValue("expectancy", &e2) {
		t.Fatal("期望收益目标排序错误")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestScoreQuantilesAdaptive(t *testing.T) {
	// 分布 60~95：p40/p60/p80/p95 应真实切分且各不相同
	scores := make([]float64, 100)
	for i := range scores {
		scores[i] = float64(60 + i%36) // 60..95 循环
	}
	q := scoreQuantiles(scores)
	if q == nil || len(q) < 2 {
		t.Fatalf("应有自适应阈值: %v", q)
	}
	for i := 1; i < len(q); i++ {
		if q[i] <= q[i-1] {
			t.Fatalf("阈值应严格递增去重: %v", q)
		}
	}
	// 样本不足 → nil（跳过门槛维）
	if q := scoreQuantiles([]float64{70, 70, 70}); q != nil {
		t.Fatalf("小样本应 nil: %v", q)
	}
	// 全同分（无区分度）→ nil：这正是"四档数据完全一样"的免疫机制
	same := make([]float64, 50)
	for i := range same {
		same[i] = 85
	}
	if q := scoreQuantiles(same); q != nil {
		t.Fatalf("无区分度分布应 nil: %v", q)
	}
}

func TestStepRange(t *testing.T) {
	// 步进序列：含起终点、步长正确、两位小数无浮点尾差
	got := stepRange(5, 15, 2.5)
	want := []float64{5, 7.5, 10, 12.5, 15}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if abs(got[i]-want[i]) > 0.001 {
			t.Fatalf("got[%d]=%v want %v", i, got[i], want[i])
		}
	}
	// 未知战法回退默认池
	if p := poolFor("不存在"); len(p.tpRange) == 0 {
		t.Fatal("未知战法应回退 defaultPool")
	}
	// 已登记战法命中专属池
	if p := poolFor("N形"); p.maxHold != 15 {
		t.Fatalf("N形应命中独立寻优池 maxHold=15, got %d", p.maxHold)
	}
}
