// space.go — §SIZING-EVO 进化搜索空间（§2.7.1 三层）与硬围栏。
//
// 搜索空间 = 整个 SizingConfig 的三层：
//
//	A 因子权重层：全部因子 scale 映射（emotion/state/tier/macro_window/macro_lead/holiday/
//	              index/llm + flow_factor 全块 buy_5d/buy_today/sell 的 scale 与 band）；
//	B 结构参数层：合成器结构与强度（kelly/vol/power_p/group_floors/floor/cap）；
//	C 组合层    ：哪些因子参与合成（0=该因子所有档位写 0 → 各因子弃权；1=出厂默认）。
//
// 硬围栏（§2.7.1）：每个参数 [Lo,Hi] 内置限幅；evo 账号配置的 search_bounds **只能收紧、
// 不能放宽**（Bounds 应用时取交集）；演化产出必须仍在 sizing.go 既有 clamp/Floor/Cap 之内
// （双保险——Apply 末尾再钳一次全局 Floor/Cap）。
//
// 明确**不进**搜索空间的（纪律，不是遗漏）：
//   - SignalQualityTable 表内权重（R6：已有自己的回路自留地）；
//   - 硬拦因子（情绪冰点硬拦 / RedBlockNewBuy 应急阀 / 连续止损熔断 / 长假前禁开）——
//     组合层不允许把硬拦进化掉（§2.7.1）；
//   - breadth_missing_scale 的参与面（平面字段无三态，缺键与显式 0 不可分；强度仍可搜）；
//   - BudgetMode/Mode/Enabled/Shadow 等开关轴（账号级语义，不是进化对象）。
//
// English: the three-layer search space with hard rails. Config-level search_bounds may only
// tighten the built-in rails, never widen them. Quality-table weights and hard-block factors
// are deliberately excluded.
package sizing_evo

import (
	"sort"
	"strconv"
	"strings"

	"quant-trading-v2/internal/config"
)

// Param 一个可进化参数。Key 是稳定路径（与 EvoConfig.SearchBounds 覆盖键同键空间），
// Get/Set 直接读写 config.SizingConfig 的导出字段/映射。
// English: one evolvable parameter with a stable key path and direct config accessors.
type Param struct {
	Key    string
	Layer  string // A / B / C
	Lo, Hi float64
	Step   float64 // 网格步长（C 层固定 1）
	Get    func(cfg *config.SizingConfig) float64
	Set    func(cfg *config.SizingConfig, v float64)
}

// Grid 参数的候选网格（Lo..Hi 含端点按 Step 递增；C 层 = {0,1}）。
// English: the candidate grid for one parameter (inclusive endpoints).
func (p Param) Grid() []float64 {
	if p.Layer == "C" {
		return []float64{0, 1}
	}
	if p.Step <= 0 || p.Hi <= p.Lo {
		return []float64{p.Lo}
	}
	n := int((p.Hi-p.Lo)/p.Step + 1e-9)
	out := make([]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		out = append(out, p.Lo+float64(i)*p.Step)
	}
	if out[len(out)-1] < p.Hi {
		out = append(out, p.Hi)
	}
	return out
}

// ParamSpace 全部可进化参数（构造时确定顺序，迭代按 Key 排序——确定性铁律）。
// English: the full evolvable parameter space (iteration is key-sorted for determinism).
type ParamSpace struct {
	params []Param
	byKey  map[string]*Param
}

// DefaultParamSpace 出厂搜索空间（§2.7.1 全套 + §2.7.1 围栏示例值）。
// English: the factory search space with the §2.7.1 rails.
func DefaultParamSpace() *ParamSpace {
	ps := &ParamSpace{byKey: map[string]*Param{}}
	add := func(p Param) {
		if p.Get == nil || p.Set == nil || p.Key == "" {
			return
		}
		ps.params = append(ps.params, p)
	}
	scale := func(key, layer string, lo, hi, step float64, get func(*config.SizingConfig) float64, set func(*config.SizingConfig, float64)) Param {
		return Param{Key: key, Layer: layer, Lo: lo, Hi: hi, Step: step, Get: get, Set: set}
	}
	mkMap := func(key, layer string, lo, hi, step float64, at func(cfg *config.SizingConfig) *map[string]float64, mkey string) Param {
		return scale(key, layer, lo, hi, step,
			func(c *config.SizingConfig) float64 { return (*at(c))[mkey] },
			func(c *config.SizingConfig, v float64) { (*at(c))[mkey] = v })
	}

	// ── A 因子权重层：离散因子 scale 映射 ──
	emotionAt := func(c *config.SizingConfig) *map[string]float64 { return &c.EmotionScales }
	for _, k := range []string{"退潮", "背离", "高潮", "发酵", "启动"} {
		add(mkMap("emotion."+k, "A", 0.30, 1.15, 0.05, emotionAt, k))
	}
	stateAt := func(c *config.SizingConfig) *map[string]float64 { return &c.StateScales }
	for _, k := range []string{"bull", "range", "bear"} {
		add(mkMap("state."+k, "A", 0.30, 1.15, 0.05, stateAt, k))
	}
	tierAt := func(c *config.SizingConfig) *map[string]float64 { return &c.TierScales }
	add(mkMap("tier.Yellow", "A", 0.30, 1.00, 0.05, tierAt, "Yellow"))
	add(mkMap("tier.Red", "A", 0.20, 1.00, 0.05, tierAt, "Red"))
	macroWinAt := func(c *config.SizingConfig) *map[string]float64 { return &c.MacroWindowScales }
	for _, k := range []string{"high", "medium", "low"} {
		add(mkMap("macro_window."+k, "A", 0.30, 1.00, 0.05, macroWinAt, k))
	}
	for i := 0; i < 5; i++ { // macro_lead_curve[T-5..T-1]
		idx := i
		add(scale("macro_lead."+string(rune('1'+i)), "A", 0.30, 1.00, 0.05,
			func(c *config.SizingConfig) float64 {
				if idx < len(c.MacroLeadCurve) {
					return c.MacroLeadCurve[idx]
				}
				return 0
			},
			func(c *config.SizingConfig, v float64) {
				for len(c.MacroLeadCurve) <= idx {
					c.MacroLeadCurve = append(c.MacroLeadCurve, 1.0)
				}
				c.MacroLeadCurve[idx] = v
			}))
	}
	holidayAt := func(c *config.SizingConfig) *map[string]float64 { return &c.HolidayScales }
	for _, k := range []string{"weekend", "small1", "small2", "long1", "long2", "long3"} {
		add(mkMap("holiday."+k, "A", 0.30, 1.00, 0.05, holidayAt, k))
	}
	idxAt := func(c *config.SizingConfig) *map[string]float64 { return &c.IndexScaleThresholds }
	for _, k := range []string{"-1", "-2", "deep"} {
		add(mkMap("index."+k, "A", 0.30, 1.15, 0.05, idxAt, k))
	}
	add(scale("llm_bearish", "A", 0.30, 1.00, 0.05,
		func(c *config.SizingConfig) float64 { return c.LLMBearishScale },
		func(c *config.SizingConfig, v float64) { c.LLMBearishScale = v }))

	// ── A 因子权重层：flow_factor 全块（scale + band） ──
	flowScaleAt := func(sel string) func(*config.SizingConfig) *map[string]float64 {
		return func(c *config.SizingConfig) *map[string]float64 {
			switch sel {
			case "buy5d":
				return &c.FlowFactor.Buy5DScales
			case "buytoday":
				return &c.FlowFactor.BuyTodayScales
			default:
				return &c.FlowFactor.SellScales
			}
		}
	}
	flowBandAt := func(sel string) func(*config.SizingConfig) *map[string]float64 {
		return func(c *config.SizingConfig) *map[string]float64 {
			switch sel {
			case "buy5d":
				return &c.FlowFactor.Buy5DBands
			case "buytoday":
				return &c.FlowFactor.BuyTodayBands
			default:
				return &c.FlowFactor.SellBands
			}
		}
	}
	// 买入 scale：流入档只许 ≥1（放大），流出档只许 ≤1（惩罚）——防进化把符号改反。
	for _, sel := range []string{"buy5d", "buytoday"} {
		for _, b := range []string{config.FlowBandStrongIn, config.FlowBandMildIn} {
			add(mkMap("flow."+sel+"."+b, "A", 1.00, 1.15, 0.025, flowScaleAt(sel), b))
		}
		for _, b := range []string{config.FlowBandMildOut, config.FlowBandStrongOut} {
			add(mkMap("flow."+sel+"."+b, "A", 0.70, 1.00, 0.025, flowScaleAt(sel), b))
		}
		for _, b := range []string{"strong_in_pct", "mild_in_pct", "mild_out_pct", "strong_out_pct"} {
			add(mkMap("flow."+sel+".band."+b, "A", 0.5, 10, 0.5, flowBandAt(sel), b))
		}
	}
	// 卖出 scale：恒 <1（缩短窗），下限 0.80 防"等于取消观察窗"。
	for _, b := range []string{config.FlowBandStrongOut, config.FlowBandMildOut, "today_strong_out", "today_mild_out"} {
		add(mkMap("flow.sell."+b, "A", 0.80, 0.995, 0.01, flowScaleAt("sell"), b))
	}
	add(mkMap("flow.sell.band.strong_out_pct", "A", 0.5, 10, 0.5, flowBandAt("sell"), "strong_out_pct"))
	add(mkMap("flow.sell.band.mild_out_pct", "A", 0.5, 10, 0.5, flowBandAt("sell"), "mild_out_pct"))

	// ── B 结构参数层 ──
	add(scale("kelly_lambda", "B", 0.2, 0.8, 0.05,
		func(c *config.SizingConfig) float64 { return c.KellyLambda },
		func(c *config.SizingConfig, v float64) { c.KellyLambda = v }))
	add(scale("kelly_min_prob", "B", 0.50, 0.65, 0.01,
		func(c *config.SizingConfig) float64 { return c.KellyMinProb },
		func(c *config.SizingConfig, v float64) { c.KellyMinProb = v }))
	add(scale("vol_target_pct", "B", 1.0, 4.0, 0.25,
		func(c *config.SizingConfig) float64 { return c.VolTargetPct },
		func(c *config.SizingConfig, v float64) { c.VolTargetPct = v }))
	add(scale("vol_clamp_lo", "B", 0.30, 0.80, 0.05,
		func(c *config.SizingConfig) float64 { return c.VolClampLo },
		func(c *config.SizingConfig, v float64) { c.VolClampLo = v }))
	add(scale("vol_clamp_hi", "B", 1.00, 2.00, 0.05,
		func(c *config.SizingConfig) float64 { return c.VolClampHi },
		func(c *config.SizingConfig, v float64) { c.VolClampHi = v }))
	add(scale("power_p", "B", 0.0, 1.0, 0.1,
		func(c *config.SizingConfig) float64 { return c.PowerPOf() },
		func(c *config.SizingConfig, v float64) {
			p := v
			c.PowerP = &p
		}))
	for _, g := range []string{"market", "stock"} {
		g := g
		add(scale("group_floor."+g, "B", 0.20, 0.70, 0.05,
			func(c *config.SizingConfig) float64 { return c.GroupFloor(g) },
			func(c *config.SizingConfig, v float64) { c.GroupFloors[g] = v }))
	}
	add(scale("floor", "B", 0.05, 0.30, 0.025,
		func(c *config.SizingConfig) float64 { return c.Floor },
		func(c *config.SizingConfig, v float64) { c.Floor = v }))
	add(scale("cap", "B", 1.00, 1.30, 0.025,
		func(c *config.SizingConfig) float64 { return c.Cap },
		func(c *config.SizingConfig, v float64) { c.Cap = v }))

	// ── C 组合层：因子参与面（0=该因子所有档位写 0 弃权；1=出厂默认） ──
	cFactor := func(key string, off, on func(c *config.SizingConfig)) {
		add(Param{Key: key, Layer: "C", Lo: 0, Hi: 1, Step: 1,
			Get: func(c *config.SizingConfig) float64 { return 1 }, // 参与面不入 Get（基线恒 1）
			Set: func(c *config.SizingConfig, v float64) {
				if v < 0.5 {
					off(c)
				} else {
					on(c)
				}
			}})
	}
	zeroAll := func(at func(*config.SizingConfig) *map[string]float64) func(*config.SizingConfig) {
		return func(c *config.SizingConfig) {
			m := *at(c)
			for k := range m {
				m[k] = 0
			}
		}
	}
	restore := func(at func(*config.SizingConfig) *map[string]float64, def map[string]float64) func(*config.SizingConfig) {
		return func(c *config.SizingConfig) {
			m := *at(c)
			for k, v := range def {
				m[k] = v
			}
		}
	}
	def := config.DefaultSizingConfig()
	cFactor("factor.emotion", zeroAll(emotionAt), restore(emotionAt, def.EmotionScales))
	cFactor("factor.state", zeroAll(stateAt), restore(stateAt, def.StateScales))
	cFactor("factor.tier", zeroAll(tierAt), restore(tierAt, def.TierScales))
	cFactor("factor.macro_window", zeroAll(macroWinAt), restore(macroWinAt, def.MacroWindowScales))
	cFactor("factor.holiday", zeroAll(holidayAt), restore(holidayAt, def.HolidayScales))
	cFactor("factor.index", zeroAll(idxAt), restore(idxAt, def.IndexScaleThresholds))
	cFactor("factor.llm", func(c *config.SizingConfig) { c.LLMBearishScale = 0 },
		func(c *config.SizingConfig) { c.LLMBearishScale = def.LLMBearishScale })
	cFactor("factor.flow5d", zeroAll(flowScaleAt("buy5d")), restore(flowScaleAt("buy5d"), def.FlowFactor.Buy5DScales))
	cFactor("factor.flowtoday", zeroAll(flowScaleAt("buytoday")), restore(flowScaleAt("buytoday"), def.FlowFactor.BuyTodayScales))
	cFactor("factor.flowsell", zeroAll(flowScaleAt("sell")), restore(flowScaleAt("sell"), def.FlowFactor.SellScales))
	cFactor("factor.macro_lead", func(c *config.SizingConfig) {
		for i := range c.MacroLeadCurve {
			c.MacroLeadCurve[i] = 0
		}
	}, func(c *config.SizingConfig) {
		c.MacroLeadCurve = append([]float64(nil), def.MacroLeadCurve...)
	})

	sort.Slice(ps.params, func(i, j int) bool { return ps.params[i].Key < ps.params[j].Key })
	for i := range ps.params {
		ps.byKey[ps.params[i].Key] = &ps.params[i]
	}
	return ps
}

// Params 全部参数（按 Key 升序的副本——调用方不得改内部顺序）。
// English: all parameters in key order.
func (ps *ParamSpace) Params() []Param {
	out := make([]Param, len(ps.params))
	copy(out, ps.params)
	return out
}

// Lookup 按键取参数定义。
func (ps *ParamSpace) Lookup(key string) (Param, bool) {
	p, ok := ps.byKey[key]
	if !ok {
		return Param{}, false
	}
	return *p, true
}

// Clamp 把 v 钳到参数的围栏（未知键 → 原值；配置覆盖只许收紧，故这里以内置围栏为准，
// 收紧由 WithBounds 完成）。
// English: clamps v to the parameter's built-in rails.
func (ps *ParamSpace) Clamp(key string, v float64) float64 {
	p, ok := ps.byKey[key]
	if !ok {
		return v
	}
	if v < p.Lo {
		return p.Lo
	}
	if v > p.Hi {
		return p.Hi
	}
	return v
}

// WithBounds 应用账号级 search_bounds 覆盖：**只许收紧**（与内置围栏取交集）。
// 畸形条目（长度≠2 / lo>hi / 完全不相交）→ 忽略该覆盖并保持内置围栏（防配置写反扩权）。
// English: applies config-level bound overrides as intersections with the built-in rails only.
func (ps *ParamSpace) WithBounds(over map[string][]float64) *ParamSpace {
	if len(over) == 0 {
		return ps
	}
	out := &ParamSpace{params: append([]Param(nil), ps.params...), byKey: map[string]*Param{}}
	for i := range out.params {
		p := &out.params[i]
		if b, ok := over[p.Key]; ok && len(b) == 2 && b[0] <= b[1] {
			lo, hi := maxf(p.Lo, b[0]), minf(p.Hi, b[1]) // 交集=收紧
			if lo <= hi {
				p.Lo, p.Hi = lo, hi
			}
		}
		out.byKey[p.Key] = p
	}
	return out
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ComboKey 候选的参数组合指纹（排序后 key=value 串）——已驳回组合去重（ExcludeCombos）与
// 候选 ID 派生共用。C 层只记 0（显式关闭）；1=基线不记（减小指纹面）。
// English: the candidate's combo fingerprint (sorted key=value), shared by dedup and IDs.
func ComboKey(params map[string]float64) []string {
	out := make([]string, 0, len(params))
	for k, v := range params {
		if v == 1 && strings.HasPrefix(k, "factor.") {
			continue // C 层"开"=基线，不记入指纹
		}
		out = append(out, k+"="+trimFloat(v))
	}
	sort.Strings(out)
	return out
}

// comboEqual 两个指纹是否同集合（排序后逐元素相等，与 research ExcludeCombos 同口径）。
func comboEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// trimFloat 指纹用的紧凑数值串（去尾零，防 1.1000000001 类浮点噪声造成去重失效）。
func trimFloat(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
