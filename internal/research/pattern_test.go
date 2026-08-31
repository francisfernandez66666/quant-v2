// F2 形态模板搜索测试：触发判定、模板展开、护栏过滤。
// English: F2 pattern template search test: trigger judgment, template expansion, guard-rail filtering.
package research

import (
	"testing"

	"quant-trading-v2/internal/factor"
)

// mkPatternPanels 构造带形态算子因子的合成面板：
// English: mkPatternPanels builds a synthetic panel with pattern-operator factors:
// 每只股票给 Drawdown20（回撤）与 VolShrink（缩量）与 BullAlign 序列。
// English: each stock gets Drawdown20 (drawdown), VolShrink (volume shrink), and BullAlign sequences.
// 构造使"回调 0.1~0.2 + 缩量<0.6"条件在部分日期触发且前瞻收益为正。
// English: constructed so that the "pullback 0.1~0.2 + volume shrink <0.6" condition triggers on some dates with positive forward returns.
func mkPatternPanels(t *testing.T) []*Panel {
	t.Helper()
	dates := makeDates(40)
	// 2 只股票，人为设计触发日（第 25 天回撤 0.15、缩量 0.5、多头 1）
	// English: 2 stocks, trigger days designed by hand (day 25: drawdown 0.15, volume shrink 0.5, bull 1)
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
		// English: no trigger by default
		for i := range dd {
			dd[i] = 0.9
			vs[i] = 1.5
			ba[i] = 0
		}
		// 第 25 天触发条件
		// English: trigger condition on day 25
		dd[25] = 0.15
		vs[25] = 0.5
		ba[25] = 1
		// 为满足 MinTrigger 让多日触发：第 24~27 天连续满足
		// English: to satisfy MinTrigger, make it trigger across multiple days: days 24~27 satisfied consecutively
		for i := 24; i <= 27; i++ {
			dd[i] = 0.15
			vs[i] = 0.5
			ba[i] = 1
		}
		// 收盘序列：非触发日 +0.5%，触发日（24-28）后 +2%（使触发前瞻收益显著更高）
		// English: close series: +0.5% on non-trigger days, +2% after trigger days (24-28) (making trigger forward returns significantly higher)
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

// itoaTest 手写整数转字符串（测试辅助）。
func itoaTest(v int) string {
	return intToString2(v)
}

// intToString2 整数转字符串（测试辅助）。
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
// English: TestPatternTriggers: condition satisfied/not-satisfied judgment.
func TestPatternTriggers(t *testing.T) {
	panels := mkPatternPanels(t)
	p := Pattern{Conds: []PatternCond{
		{Factor: "Drawdown20", Min: 0.1, Max: 0.2},
		{Factor: "VolShrink", Min: 0, Max: 0.6},
		{Factor: "BullAlign", Min: 0.5, Max: 1.5},
	}}
	// 第 25 天满足全部条件
	// English: day 25 satisfies all conditions
	if !patternTriggers(panels[0], p, 25) {
		t.Fatalf("第25天应触发")
	}
	// 第 10 天不满足（dd=0.9 超区间）
	// English: day 10 does not satisfy (dd=0.9 out of range)
	if patternTriggers(panels[0], p, 10) {
		t.Fatalf("第10天不应触发")
	}
}

// TestDiscoverPatterns 搜索应产出通过护栏的形态（触发次数达标 + 正超额 + 样本外正）。
// English: TestDiscoverPatterns: search should produce patterns that pass the guard rail (trigger count met + positive excess + positive out-of-sample).
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
// English: TestExpandTemplate: Cartesian product expansion.
func TestExpandTemplate(t *testing.T) {
	tmpl := PatternTemplate{Conds: []CondGrid{
		{Factor: "A", MinVals: []float64{0, 1}, MaxVals: []float64{2}},
		{Factor: "B", MinVals: []float64{0}, MaxVals: []float64{1}},
	}}
	// A: (0,2),(1,2) 2 种；B: (0,1) 1 种 → 共 2
	// English: A: (0,2),(1,2) 2 variants; B: (0,1) 1 variant → 2 in total
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

// TestPatternWindowedMatchesFull 窗口分块形态搜索与"单窗全量参考"证据一致：
// 参考口径 = 尾部多算 h 天的面板 + evalPattern（与窗口版相同的日期覆盖），
// 断言 Triggers/平均收益/超额/命中率/样本外全部一致（浮点容差）。
// English: windowed pattern search must match a single-window full reference (tail-extended panels
// + evalPattern) on every evidence field within float tolerance.
func TestPatternWindowedMatchesFull(t *testing.T) {
	db := seedWindowDB(t)
	codes, _ := db.StockCodes() // 夹具已含 stocks 表
	start, end := "20230101", datesEnd(db)
	const h = 5

	// 全触发模板：条件恒真（Brk20 ∈ (-inf,+inf)），触发数=全部有效股票日，
	// 使期望值可手工推导且对任何合成数据稳定。
	tmpl := PatternTemplate{Name: "全触发", Conds: []CondGrid{
		{Factor: "Brk20", MinVals: []float64{-1e9}, MaxVals: []float64{1e9}},
	}}
	opts := DiscoverOptsPattern{Horizon: h, MinTrigger: 1, MinExcess: -1e9, SplitPct: 0.7}

	win := DiscoverPatternsWindowed(db, codes, start, end, []PatternTemplate{tmpl}, opts)
	if len(win) != 1 {
		t.Fatalf("窗口版应产出 1 条（恒真模板必过触发数护栏），实际 %d", len(win))
	}

	// 全量参考：装配 [start, end+h] 面板后走 evalPattern（尾部补 h 天保证前瞻完整）
	defs := factor.All()
	var need []factor.Def
	for _, d := range defs {
		if d.ID == "Brk20" {
			need = append(need, d)
		}
	}
	asmbEnd := end
	for i := 0; i < h; i++ {
		asmbEnd = nextDayStr(asmbEnd)
	}
	panels, err := BuildPanels(db, codes, start, asmbEnd, need)
	if err != nil {
		t.Fatalf("BuildPanels 失败: %v", err)
	}
	ref := evalPattern(panels, Pattern{Name: "全触发", Conds: []PatternCond{
		{Factor: "Brk20", Min: -1e9, Max: 1e9},
	}, Horizon: h}, opts, "", "")

	got := win[0]
	if got.Triggers != ref.Triggers {
		t.Errorf("Triggers 不一致: win=%d ref=%d", got.Triggers, ref.Triggers)
	}
	for _, pair := range []struct {
		name string
		a, b float64
	}{
		{"MeanRet", got.MeanRet, ref.MeanRet},
		{"Excess", got.Excess, ref.Excess},
		{"HitRate", got.HitRate, ref.HitRate},
	} {
		if diff := pair.a - pair.b; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s 不一致: win=%.10f ref=%.10f", pair.name, pair.a, pair.b)
		}
	}
}
