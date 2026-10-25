// space_test.go — §SIZING-EVO 搜索空间与硬围栏单测。
// 钉死：参数完备性（Get/Set/围栏/网格）、Apply 深拷贝不污染基线、C 层开关语义、
// 符号围栏（流入档≥1/流出档≤1）、search_bounds 只许收紧、组合指纹与去重。
package sizing_evo

import (
	"reflect"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestParamSpaceCompleteness 每个参数都有 Get/Set、Lo<=Hi、网格非空；C 层网格={0,1}。
func TestParamSpaceCompleteness(t *testing.T) {
	ps := DefaultParamSpace()
	if len(ps.params) < 50 {
		t.Fatalf("搜索空间应覆盖 §2.7.1 三层（期望 ≥50 参数），得 %d", len(ps.params))
	}
	for _, p := range ps.Params() {
		if p.Get == nil || p.Set == nil {
			t.Errorf("%s: Get/Set 不得为 nil", p.Key)
		}
		if p.Lo > p.Hi {
			t.Errorf("%s: Lo=%v > Hi=%v", p.Key, p.Lo, p.Hi)
		}
		g := p.Grid()
		if len(g) == 0 {
			t.Errorf("%s: 网格为空", p.Key)
		}
		if p.Layer == "C" {
			if len(g) != 2 || g[0] != 0 || g[1] != 1 {
				t.Errorf("%s: C 层网格应为 {0,1}，得 %v", p.Key, g)
			}
			continue
		}
		for _, v := range g {
			if v < p.Lo-1e-9 || v > p.Hi+1e-9 {
				t.Errorf("%s: 网格值 %v 越界 [%v,%v]", p.Key, v, p.Lo, p.Hi)
			}
		}
	}
	// 三层都在场。
	layers := map[string]int{}
	for _, p := range ps.Params() {
		layers[p.Layer]++
	}
	for _, l := range []string{"A", "B", "C"} {
		if layers[l] == 0 {
			t.Errorf("层 %s 没有参数", l)
		}
	}
}

// TestApplyRoundtripAndNoBaseMutation Apply 后 Get 读回一致，且基线映射不被污染。
func TestApplyRoundtripAndNoBaseMutation(t *testing.T) {
	ps := DefaultParamSpace()
	base := config.DefaultSizingConfig().WithDefaults()
	before := base.EmotionScales["高潮"]
	c := Candidate{ID: "t", Params: map[string]float64{"emotion.高潮": 0.95, "kelly_lambda": 0.7, "cap": 1.25}}
	out := c.Apply(base, ps)
	if out.EmotionScales["高潮"] != 0.95 || out.KellyLambda != 0.7 || out.Cap != 1.25 {
		t.Fatalf("Apply 未生效: %+v", out)
	}
	if base.EmotionScales["高潮"] != before || base.KellyLambda != 0.5 || base.Cap != 1.15 {
		t.Fatal("Apply 污染了基线配置（深拷贝失败）")
	}
	// 未知键忽略（不 panic）。
	c2 := Candidate{Params: map[string]float64{"no.such.param": 1}}
	if got := c2.Apply(base, ps); got.KellyLambda != base.KellyLambda {
		t.Fatal("未知键应被忽略")
	}
	// 双保险：floor/cap 越界被夹回。
	c3 := Candidate{Params: map[string]float64{"floor": 0.30, "cap": 1.30}}
	got := c3.Apply(base, ps)
	if got.Floor > got.Cap {
		t.Fatalf("floor(%v) > cap(%v) 未被夹回", got.Floor, got.Cap)
	}
}

// TestCLayerFactorSwitch C 组合层：0=该因子所有档位写 0（弃权）；1=恢复出厂默认。
func TestCLayerFactorSwitch(t *testing.T) {
	ps := DefaultParamSpace()
	base := config.DefaultSizingConfig().WithDefaults()
	off := Candidate{Params: map[string]float64{"factor.emotion": 0, "factor.flow5d": 0, "factor.macro_lead": 0}}
	out := off.Apply(base, ps)
	for k, v := range out.EmotionScales {
		if v != 0 {
			t.Errorf("factor.emotion=0 后 %s=%v，应全 0", k, v)
		}
	}
	for k, v := range out.FlowFactor.Buy5DScales {
		if v != 0 {
			t.Errorf("factor.flow5d=0 后 %s=%v，应全 0", k, v)
		}
	}
	for i, v := range out.MacroLeadCurve {
		if v != 0 {
			t.Errorf("factor.macro_lead=0 后 curve[%d]=%v，应全 0", i, v)
		}
	}
	// 1=恢复出厂（在已被关掉的副本上再开）。
	on := Candidate{Params: map[string]float64{"factor.emotion": 1, "factor.flow5d": 1, "factor.macro_lead": 1}}
	back := on.Apply(out, ps)
	def := config.DefaultSizingConfig()
	if !reflect.DeepEqual(back.EmotionScales, def.EmotionScales) {
		t.Errorf("factor.emotion=1 应恢复出厂，得 %v", back.EmotionScales)
	}
	if !reflect.DeepEqual(back.FlowFactor.Buy5DScales, def.FlowFactor.Buy5DScales) {
		t.Errorf("factor.flow5d=1 应恢复出厂，得 %v", back.FlowFactor.Buy5DScales)
	}
	if !reflect.DeepEqual(back.MacroLeadCurve, def.MacroLeadCurve) {
		t.Errorf("factor.macro_lead=1 应恢复出厂，得 %v", back.MacroLeadCurve)
	}
}

// TestFlowBandSignRails flow band 符号围栏：买入流入档 Lo≥1、流出档 Hi≤1（防进化把符号改反）。
func TestFlowBandSignRails(t *testing.T) {
	ps := DefaultParamSpace()
	for _, key := range []string{"flow.buy5d.strong_in", "flow.buy5d.mild_in", "flow.buytoday.strong_in", "flow.buytoday.mild_in"} {
		p, ok := ps.Lookup(key)
		if !ok {
			t.Fatalf("%s 应在搜索空间", key)
		}
		if p.Lo < 1.0 {
			t.Errorf("%s Lo=%v <1：流入档不得被进化成惩罚", key, p.Lo)
		}
	}
	for _, key := range []string{"flow.buy5d.mild_out", "flow.buy5d.strong_out", "flow.buytoday.mild_out", "flow.buytoday.strong_out"} {
		p, ok := ps.Lookup(key)
		if !ok {
			t.Fatalf("%s 应在搜索空间", key)
		}
		if p.Hi > 1.0 {
			t.Errorf("%s Hi=%v >1：流出档不得被进化成奖励", key, p.Hi)
		}
	}
	// 卖出权重恒 <1 且 ≥0.80（不得压到 0 = 取消观察窗）。
	for _, key := range []string{"flow.sell.strong_out", "flow.sell.mild_out", "flow.sell.today_strong_out", "flow.sell.today_mild_out"} {
		p, _ := ps.Lookup(key)
		if p.Lo < 0.80 || p.Hi >= 1.0 {
			t.Errorf("%s 围栏 [%v,%v] 应为 [0.80,<1)", key, p.Lo, p.Hi)
		}
	}
}

// TestWithBoundsTightenOnly search_bounds 只许收紧：交集生效；放宽/畸形被忽略。
func TestWithBoundsTightenOnly(t *testing.T) {
	ps := DefaultParamSpace()
	tight := ps.WithBounds(map[string][]float64{"kelly_lambda": {0.4, 0.6}})
	if v := tight.Clamp("kelly_lambda", 0.1); v != 0.4 {
		t.Errorf("收紧后 Clamp(0.1) = %v, want 0.4", v)
	}
	if v := tight.Clamp("kelly_lambda", 0.9); v != 0.6 {
		t.Errorf("收紧后 Clamp(0.9) = %v, want 0.6", v)
	}
	// 放宽尝试：与内置 [0.2,0.8] 取交集 → 仍是 [0.2,0.8]。
	widen := ps.WithBounds(map[string][]float64{"kelly_lambda": {0.1, 0.95}})
	if v := widen.Clamp("kelly_lambda", 0.1); v != 0.2 {
		t.Errorf("放宽不得生效：Clamp(0.1) = %v, want 内置 0.2", v)
	}
	if v := widen.Clamp("kelly_lambda", 0.9); v != 0.8 {
		t.Errorf("放宽不得生效：Clamp(0.9) = %v, want 内置 0.8", v)
	}
	// 畸形条目忽略（长度≠2 / lo>hi / 未知键）。
	bad := ps.WithBounds(map[string][]float64{"kelly_lambda": {0.5}, "cap": {1.2, 1.1}, "no.such": {0, 1}})
	if v := bad.Clamp("kelly_lambda", 0.1); v != 0.2 {
		t.Errorf("畸形覆盖应被忽略，得 %v", v)
	}
	// 原空间不被 WithBounds 修改。
	if v := ps.Clamp("kelly_lambda", 0.1); v != 0.2 {
		t.Errorf("WithBounds 不得改原空间，得 %v", v)
	}
}

// TestComboKeyAndDedup 组合指纹：排序确定、C 层"开"不记、去重与 ExcludeCombos 集合判等。
func TestComboKeyAndDedup(t *testing.T) {
	a := ComboKey(map[string]float64{"kelly_lambda": 0.7, "factor.emotion": 1})
	b := ComboKey(map[string]float64{"factor.emotion": 1, "kelly_lambda": 0.7})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("指纹应与键序无关：%v vs %v", a, b)
	}
	if len(a) != 1 || a[0] != "kelly_lambda=0.7" {
		t.Fatalf("C 层 1（开=基线）不应记入指纹，得 %v", a)
	}
	// C 层 0（显式关）记入。
	if fp := ComboKey(map[string]float64{"factor.emotion": 0}); len(fp) != 1 || fp[0] != "factor.emotion=0" {
		t.Fatalf("C 层 0 应记入指纹，得 %v", fp)
	}
	// 去重 + 排除。
	cands := []Candidate{
		{ID: "1", Params: map[string]float64{"kelly_lambda": 0.7}},
		{ID: "2", Params: map[string]float64{"kelly_lambda": 0.7}}, // 重复
		{ID: "3", Params: map[string]float64{"kelly_lambda": 0.6}},
	}
	got := Dedup(cands, nil)
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" {
		t.Fatalf("去重应保留 1/3，得 %+v", got)
	}
	got2 := Dedup(cands, [][]string{{"kelly_lambda=0.7"}})
	if len(got2) != 1 || got2[0].ID != "3" {
		t.Fatalf("ExcludeCombos 应剔除 0.7 组合，得 %+v", got2)
	}
}
