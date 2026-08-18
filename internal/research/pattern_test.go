// F2 形态模板搜索测试：触发判定、模板展开、护栏过滤。
package research

import (
	"testing"

	"quant-trading-v2/internal/factor"
)

// mkPatternPanels 构造带形态算子因子的合成面板：
// 每只股票给 Drawdown20（回撤）与 VolShrink（缩量）与 BullAlign 序列。
// 构造使"回调 0.1~0.2 + 缩量<0.6"条件在部分日期触发且前瞻收益为正。
func mkPatternPanels(t *testing.T) []*Panel {
	t.Helper()
	dates := makeDates(40)
	// 2 只股票，人为设计触发日（第 25 天回撤 0.15、缩量 0.5、多头 1）
	var panels []*Panel
	for k := 0; k < 2; k++ {
		idx := make(map[string]int, len(dates))
		for i, d := range dates {
			idx[d] = i
		}
		dd := make([]float64, len(dates))
		vs := make([]float64, len(dates))
		ba := make([]float64, len(dates))
		// 默认无触发
		for i := range dd {
			dd[i] = 0.9
			vs[i] = 1.5
			ba[i] = 0
		}
		// 第 25 天触发条件
		dd[25] = 0.15
		vs[25] = 0.5
		ba[25] = 1
		// 为满足 MinTrigger 让多日触发：第 24~27 天连续满足
		for i := 24; i <= 27; i++ {
			dd[i] = 0.15
			vs[i] = 0.5
			ba[i] = 1
		}
		// 收盘序列：非触发日 +0.5%，触发日（24-28）后 +2%（使触发前瞻收益显著更高）
		closes := make([]float64, len(dates))
		closes[0] = 100
		for i := 1; i < len(dates); i++ {
			gain := 1.005
			if i >= 24 && i <= 28 {
				gain = 1.02
			}
			closes[i] = closes[i-1] * gain
		}
		series := &factor.StockSeries{Dates: dates, CloseHfq: closes}
		panels = append(panels, &Panel{
			Code:    itoaTest(k),
			DateIdx: idx,
			Factors: map[string][]float64{
				"Drawdown20": dd, "VolShrink": vs, "BullAlign": ba,
			},
			Series: series,
		})
	}
	return panels
}

func itoaTest(v int) string {
	return intToString2(v)
}

func intToString2(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// TestPatternTriggers 条件满足/不满足判定。
func TestPatternTriggers(t *testing.T) {
	panels := mkPatternPanels(t)
	p := Pattern{Conds: []PatternCond{
		{Factor: "Drawdown20", Min: 0.1, Max: 0.2},
		{Factor: "VolShrink", Min: 0, Max: 0.6},
		{Factor: "BullAlign", Min: 0.5, Max: 1.5},
	}}
	// 第 25 天满足全部条件
	if !patternTriggers(panels[0], p, 25) {
		t.Fatalf("第25天应触发")
	}
	// 第 10 天不满足（dd=0.9 超区间）
	if patternTriggers(panels[0], p, 10) {
		t.Fatalf("第10天不应触发")
	}
}

// TestDiscoverPatterns 搜索应产出通过护栏的形态（触发次数达标 + 正超额 + 样本外正）。
func TestDiscoverPatterns(t *testing.T) {
	panels := mkPatternPanels(t)
	templates := []PatternTemplate{{
		Name: "回调缩量多头",
		Conds: []CondGrid{
			{Factor: "Drawdown20", MinVals: []float64{0.1}, MaxVals: []float64{0.2}},
			{Factor: "VolShrink", MinVals: []float64{0}, MaxVals: []float64{0.6}},
			{Factor: "BullAlign", MinVals: []float64{0.5}, MaxVals: []float64{1.5}},
		},
	}}
	opts := DiscoverOptsPattern{Horizon: 1, MinTrigger: 4, MinExcess: 0.001, SplitPct: 0.6}
	results := DiscoverPatterns(panels, templates, opts)
	if len(results) == 0 {
		t.Fatalf("应产出至少一个形态")
	}
	best := results[0]
	if best.Triggers < 4 {
		t.Fatalf("触发次数=%d 应≥4", best.Triggers)
	}
	if best.Excess <= 0 {
		t.Fatalf("超额=%f 应为正", best.Excess)
	}
	if best.SampleOut <= 0 {
		t.Fatalf("样本外超额=%f 应为正", best.SampleOut)
	}
}

// TestExpandTemplate 笛卡尔积展开。
func TestExpandTemplate(t *testing.T) {
	tmpl := PatternTemplate{Conds: []CondGrid{
		{Factor: "A", MinVals: []float64{0, 1}, MaxVals: []float64{2}},
		{Factor: "B", MinVals: []float64{0}, MaxVals: []float64{1}},
	}}
	// A: (0,2),(1,2) 2 种；B: (0,1) 1 种 → 共 2
	ps := expandTemplate(tmpl, 5)
	if len(ps) != 2 {
		t.Fatalf("展开数=%d 期望 2", len(ps))
	}
	for _, p := range ps {
		if p.Horizon != 5 {
			t.Fatalf("horizon 应=5")
		}
	}
}
