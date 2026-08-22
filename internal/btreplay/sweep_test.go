package btreplay

import (
	"testing"
	"time"

	data "quant-trading-v2/internal/data"
)

// mkKLine 由收盘价序列构造日线（开=收、高=收+1%、低=收-1%），日期自 2024-01-01 起逐日。
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

func TestUniformExitTrailing(t *testing.T) {
	// 涨到 120 后回撤到 108（-10% ≤ -8% 且曾盈利）→ 移动止盈离场
	closes := []float64{100, 105, 110, 115, 120, 118, 112, 108}
	kls := mkKLine(closes)
	exitJ, pnl := uniformExit(kls, 0, 100, 100, 8, 30)
	// 入场日=1；j 从 2 起；j=6 时 cur=112? 阶段高 120，112 回撤 -6.7 未触发；
	// j=7 cur=108 → dd=(108-120)/120=-10% 触发
	if exitJ != 7 {
		t.Fatalf("exitJ=%d want 7", exitJ)
	}
	if want := float64(8); abs(pnl-want) > 0.01 {
		t.Fatalf("pnl=%.2f want %.2f", pnl, want)
	}
}

func TestUniformExitTimeout(t *testing.T) {
	// 温和上涨不触发止盈 → 持仓满 5 天超期离场
	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108}
	kls := mkKLine(closes)
	entryDay := 0 + 1
	exitJ, _ := uniformExit(kls, 0, 100, 100, 8, 5)
	if exitJ-entryDay != 5 {
		t.Fatalf("exitJ=%d 持仓=%d want 5", exitJ, exitJ-entryDay)
	}
}

func TestUniformExitNeverProfitable(t *testing.T) {
	// 单边下跌：stageHigh 永不超过 entry → 不触发移动止盈，超期/末日结算亏损
	closes := []float64{100, 95, 92, 90, 89, 88}
	kls := mkKLine(closes)
	_, pnl := uniformExit(kls, 0, 100, 100, 8, 3)
	if pnl >= 0 {
		t.Fatalf("下跌场景应为亏损, got %.2f", pnl)
	}
}

func TestUniformExitForcedClose(t *testing.T) {
	// 数据末尾仍未满足条件 → 末日收盘强制结算
	closes := []float64{100, 102, 103, 101}
	kls := mkKLine(closes)
	exitJ, _ := uniformExit(kls, 0, 100, 100, 50, 100)
	if exitJ != len(kls)-1 {
		t.Fatalf("exitJ=%d want %d", exitJ, len(kls)-1)
	}
}

func TestSimulateComboFilterAndOverlap(t *testing.T) {
	kls := map[string][]data.KLine{
		"600000": mkKLine([]float64{100, 110, 120, 130, 125, 118, 110, 105, 108, 112}),
	}
	trigs := []sweepTrigger{
		{code: "600000", sigIdx: 0, entry: 110, score: 80, highest: 100},
		{code: "600000", sigIdx: 1, entry: 120, score: 55, highest: 110}, // 与上一笔重叠 → 应跳过
		{code: "600000", sigIdx: 7, entry: 105, score: 40, highest: 105}, // 门槛 50 → 过滤
		{code: "600000", sigIdx: 7, entry: 105, score: 60, highest: 105},
	}
	res := simulateCombo("测试", "", trigs[:2], kls, 8, 30, 0)
	if res.Count != 1 {
		t.Fatalf("重叠过滤失败 count=%d", res.Count)
	}
	res2 := simulateCombo("测试", "", trigs, kls, 8, 30, 50)
	if res2.Count != 2 { // #3 被 50 门槛过滤，#4 通过且与首笔已平仓不重叠
		t.Fatalf("门槛过滤失败 count=%d", res2.Count)
	}
	if res2.WinRate <= 0 || res2.AvgWinPct <= 0 || res2.Loss != 0 {
		t.Fatalf("指标聚合异常: %+v", res2)
	}
	// 无分维度（score=-1）不被门槛过滤
	res3 := simulateCombo("形态", "pat_9", []sweepTrigger{{code: "600000", sigIdx: 0, entry: 110, score: -1, highest: 100}},
		kls, 8, 30, 80)
	if res3.Count != 1 {
		t.Fatalf("无分战法被误过滤")
	}
}

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
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
