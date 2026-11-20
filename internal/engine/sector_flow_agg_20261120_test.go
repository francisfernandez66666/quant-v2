// sector_flow_agg_20261120_test.go — §FLOW-SECTOR-AGG 的纯函数锁（owner 2026-11-20「看能不能拿到」）。
//
// 背景：`flow_factor_daily.sector_flow_pct` / `flow_sector_delta` 两列一直恒 NULL ——
// 全仓没有写点。它们不是装饰：`flow_sector_delta` 会被 `withRegimeFlowDeltas` 桥进 Regime，
// 成为逐票板块腿（`MetaFlowSectorDelta`）的**兜底**；逐票没命中时靠它，否则整条板块腿弃权。
//
// 本文件钉四件事：
//  1. 两列的**口径**：pct 是加权合计（与 market_flow_pct 同形态），delta 是等权均值（夹到 ±1）；
//  2. 样本不足（<20 个板块）⇒ 两条都弃权，绝不拿一两个板块冒充全市场；
//  3. 成交额合计 ≤0 或 NaN ⇒ 弃权（不返回 0 冒充中性）；
//  4. 方向要对：全为负流入时 delta 必须为负（符号错了比没值更贵）。
package engine

import (
	"math"
	"testing"
)

// TestAggregateSectorFlowWeightedPctAndMeanDelta 两列的两个不同口径都要对。
func TestAggregateSectorFlowWeightedPctAndMeanDelta(t *testing.T) {
	// 25 个板块（过样本线）：净额合计 250 亿、成交额合计 5000 亿 ⇒ 5%。
	// delta 等权均值：20 个 -1.0 + 5 个 +0.5 ⇒ (-20+2.5)/25 = -0.7
	pct, delta := aggregateSectorFlow(250e8, 5000e8, -20.0+2.5, 25)
	if pct == nil || delta == nil {
		t.Fatal("样本充足时必须出两列")
	}
	if math.Abs(*pct-5.0) > 1e-9 {
		t.Fatalf("sector_flow_pct got %v want 5.0（净额合计/成交额合计×100）", *pct)
	}
	if math.Abs(*delta-(-0.7)) > 1e-9 {
		t.Fatalf("flow_sector_delta got %v want -0.7（各板块 delta 等权均值）", *delta)
	}
}

// TestAggregateSectorFlowSameFormulaAsMarket 大盘腿的公式与板块腿的 pct 必须同形。
// 同输入下若把 pct 换成别的口径（比如中位数），这条会红。
func TestAggregateSectorFlowSameFormulaAsMarket(t *testing.T) {
	net, amt := -1234.5e8, 9876.5e8
	pct, _ := aggregateSectorFlow(net, amt, 0, 40)
	if pct == nil {
		t.Fatal("应出值")
	}
	want := net / amt * 100
	if math.Abs(*pct-want) > 1e-9 {
		t.Fatalf("pct got %v want %v", *pct, want)
	}
}

// TestAggregateSectorFlowAbstainsOnThinSample 残缺回包必须弃权（这是本函数存在的主要理由）。
func TestAggregateSectorFlowAbstainsOnThinSample(t *testing.T) {
	for _, n := range []int{0, 1, 5, sectorAggMinBoards - 1} {
		pct, delta := aggregateSectorFlow(-10e8, 100e8, -1.0, n)
		if pct != nil || delta != nil {
			t.Fatalf("n=%d 样本不足却出了值: pct=%v delta=%v", n, pct, delta)
		}
	}
	// 样本刚好达标 ⇒ 出值（边界不许划错）。
	pct, delta := aggregateSectorFlow(-10e8, 100e8, -1.0, sectorAggMinBoards)
	if pct == nil || delta == nil {
		t.Fatalf("n=%d 刚好达标却弃权", sectorAggMinBoards)
	}
}

// TestAggregateSectorFlowAbstainsWithoutTurnover 没有成交额就没有分母；不许除零，也不许返回 0。
func TestAggregateSectorFlowAbstainsWithoutTurnover(t *testing.T) {
	for _, amt := range []float64{0, -1, math.NaN()} {
		pct, delta := aggregateSectorFlow(-1e8, amt, -0.5, 50)
		if pct != nil || delta != nil {
			t.Fatalf("amt=%v 时应弃权", amt)
		}
	}
	// 净额为 0 但有成交额 ⇒ 是**真中性 0**，必须出值（"净流入恰好为零"不等于"没读数"）。
	pct, delta := aggregateSectorFlow(0, 100e8, 0, 50)
	if pct == nil || delta == nil || *pct != 0 || *delta != 0 {
		t.Fatalf("真 0 应出值: pct=%v delta=%v", pct, delta)
	}
}

// TestAggregateSectorFlowClampsDelta 等权均值夹到 [-1,1]（与 FlowPctToDelta 的值域一致）。
func TestAggregateSectorFlowClampsDelta(t *testing.T) {
	_, delta := aggregateSectorFlow(-1e8, 100e8, 30.0, 25) // 均值 1.2
	if delta == nil || *delta != 1 {
		t.Fatalf("上界未夹住: %v", delta)
	}
	_, delta = aggregateSectorFlow(-1e8, 100e8, -30.0, 25)
	if delta == nil || *delta != -1 {
		t.Fatalf("下界未夹住: %v", delta)
	}
}

// TestAggregateSectorFlowKeepsDirection 方向必须为负（符号错比没值更贵）。
func TestAggregateSectorFlowKeepsDirection(t *testing.T) {
	if _, d := aggregateSectorFlow(-500e8, 4000e8, -18.0, 25); d == nil || *d >= 0 {
		t.Fatalf("全为负流入时 delta 必须为负，got %v", d)
	}
	if _, d := aggregateSectorFlow(500e8, 4000e8, 18.0, 25); d == nil || *d <= 0 {
		t.Fatalf("全为正流入时 delta 必须为正，got %v", d)
	}
}
