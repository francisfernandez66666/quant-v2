// candidate.go — §SIZING-EVO 候选配置：应用 / 去重 / 生成（坐标上升+网格+确定性变异）。
//
// 候选 = 相对**基线配置**的一组参数覆盖（含 C 组合层 0/1）。Apply 产出完整
// SizingConfig 副本（深拷贝被触碰的映射，绝不改基线）；末尾再钳一次全局 Floor/Cap
// （§2.7.1 双保险：围栏内产出 + sizing.go 既有 clamp）。
//
// 生成方法论（§2.7.3，同构 research discover/optimizer）：
//   - 坐标上升：每轮对每个参数试网格全部取值，保留目标最优；无改进即停；
//   - 网格：参数自带 Lo..Hi×Step；
//   - 变异：对当前最优做定点扰动（每参数 ±1 格），确定性 PRNG（固定种子）抽样——
//     "同输入同候选"铁律：随机数种子是常量，不掺时钟/地址。
//
// English: candidate configs — apply (deep-copy, never mutate the base), dedup via combo
// fingerprints, and deterministic generation (coordinate ascent + grid + seeded mutation).
package sizing_evo

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"

	"quant-trading-v2/internal/config"
)

// evoMutSeed 变异抽样的固定种子（确定性铁律：同输入同候选）。
// English: fixed seed for mutation sampling (determinism: same input → same candidates).
const evoMutSeed = 20261025

// Candidate 一个候选配置（相对基线的参数覆盖集）。
// English: one candidate config (a set of overrides over the base).
type Candidate struct {
	ID     string             `json:"id"`
	Origin string             `json:"origin"` // baseline | coordinate | grid | mutate
	Params map[string]float64 `json:"params"`
}

// Apply 把候选应用到基线配置的深拷贝上（基线不被修改）。
// 未知键忽略（防配置漂移带进不存在的参数）；每个值先过围栏 Clamp。
// English: applies the candidate onto a deep copy of the base (base untouched).
func (c Candidate) Apply(base config.SizingConfig, ps *ParamSpace) config.SizingConfig {
	out := cloneSizing(base)
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 确定性：应用顺序固定
	for _, k := range keys {
		p, ok := ps.Lookup(k)
		if !ok {
			continue
		}
		p.Set(&out, ps.Clamp(k, c.Params[k]))
	}
	// 双保险：全局 Floor/Cap 夹持（§2.7.1——演化产出不得突破合成器既有 clamp）。
	if out.Floor < 0.05 {
		out.Floor = 0.05
	}
	if out.Cap > 1.30 {
		out.Cap = 1.30
	}
	if out.Floor > out.Cap {
		out.Floor = out.Cap
	}
	return out
}

// cloneSizing SizingConfig 深拷贝（只拷会被 Set 触碰的映射/切片 + 指针字段）。
// English: deep-copies the config (the maps/slices/pointers the setters touch).
func cloneSizing(c config.SizingConfig) config.SizingConfig {
	out := c
	out.EmotionScales = cloneMap(c.EmotionScales)
	out.StateScales = cloneMap(c.StateScales)
	out.TierScales = cloneMap(c.TierScales)
	out.MacroWindowScales = cloneMap(c.MacroWindowScales)
	out.HolidayScales = cloneMap(c.HolidayScales)
	out.IndexScaleThresholds = cloneMap(c.IndexScaleThresholds)
	out.GroupFloors = cloneMap(c.GroupFloors)
	out.MacroLeadCurve = append([]float64(nil), c.MacroLeadCurve...)
	// FlowFactor 子块（三张 scale + 三张 band）。
	out.FlowFactor.Buy5DScales = cloneMap(c.FlowFactor.Buy5DScales)
	out.FlowFactor.BuyTodayScales = cloneMap(c.FlowFactor.BuyTodayScales)
	out.FlowFactor.SellScales = cloneMap(c.FlowFactor.SellScales)
	out.FlowFactor.Buy5DBands = cloneMap(c.FlowFactor.Buy5DBands)
	out.FlowFactor.BuyTodayBands = cloneMap(c.FlowFactor.BuyTodayBands)
	out.FlowFactor.SellBands = cloneMap(c.FlowFactor.SellBands)
	return out
}

func cloneMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// StableID 由参数组合指纹导出的稳定候选 ID（同组合永远同 ID）。
// 周度运行里同一个最优组合不会因为日期不同而换 ID——灰度/生效/驳回集合都按它归组。
// English: a stable candidate ID derived from the combo fingerprint (same combo → same ID).
func (c Candidate) StableID() string {
	h := fnv.New32a()
	for _, k := range ComboKey(c.Params) {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("evo%08x", h.Sum32())
}

// Dedup 候选去重：按 ComboKey 集合判重（同集合只留首个），并剔除命中 ExcludeCombos 的。
// English: dedups candidates by combo fingerprint and drops excluded combos.
func Dedup(cands []Candidate, exclude [][]string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, c := range cands {
		key := fmt.Sprint(ComboKey(c.Params))
		if seen[key] {
			continue
		}
		if excluded(ComboKey(c.Params), exclude) {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// excluded 指纹是否命中任一已驳回组合（集合相等）。
func excluded(fp []string, exclude [][]string) bool {
	for _, e := range exclude {
		if comboEqual(fp, e) {
			return true
		}
	}
	return false
}

// GenerateCandidates 生成候选集（确定性）：
//  1. 基线候选（空参数）；
//  2. 坐标上升：从基线出发逐参数试网格，保留目标最优，直到无改进或 MaxIter 轮；
//  3. 变异：对上升终点做 ±1 格定点扰动，固定种子抽样 MaxMutate 个。
//
// eval 是调用方注入的目标函数（L1 评估器）；返回 (目标值, 可评估)。
// 生成器本身不读任何样本——纯搜索逻辑，可单测。
//
// English: deterministic candidate generation — baseline, coordinate ascent over each
// parameter's grid, then seeded ±1-step mutations of the ascent winner.
func GenerateCandidates(base config.SizingConfig, ps *ParamSpace, eval func(Candidate) (float64, bool), opts GenerateOpts) []Candidate {
	if eval == nil {
		return nil
	}
	opts.fill()
	params := ps.Params()
	baseCand := Candidate{ID: "baseline", Origin: "baseline", Params: map[string]float64{}}
	best := baseCand
	bestScore, ok := eval(best)
	if !ok {
		bestScore = 0 // 基线不可评估（冷启动）时以 0 起步，仍产出候选供上层判冷启动
	}

	// 坐标上升。
	cur := cloneParams(best.Params)
	for it := 0; it < opts.MaxIter; it++ {
		improved := false
		for _, p := range params {
			for _, v := range p.Grid() {
				if v == cur[p.Key] {
					continue
				}
				trial := cloneParams(cur)
				trial[p.Key] = v
				score, ok := eval(Candidate{ID: "trial", Origin: "coordinate", Params: trial})
				if !ok {
					continue
				}
				if score > bestScore {
					bestScore, improved = score, true
					cur = trial
				}
			}
		}
		if !improved {
			break
		}
	}
	if len(cur) > 0 {
		best = Candidate{ID: "coord-ascent", Origin: "coordinate", Params: cur}
	}

	// 变异（固定种子，确定性）。
	out := []Candidate{baseCand, best}
	if opts.MaxMutate > 0 && len(cur) > 0 {
		rng := rand.New(rand.NewSource(evoMutSeed))
		keys := make([]string, 0, len(cur))
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i := 0; i < opts.MaxMutate; i++ {
			trial := cloneParams(cur)
			// 扰动 1~2 个参数（±1 格）。
			nMut := 1 + rng.Intn(2)
			for j := 0; j < nMut && len(keys) > 0; j++ {
				k := keys[rng.Intn(len(keys))]
				p, ok := ps.Lookup(k)
				if !ok || p.Layer == "C" {
					continue
				}
				grid := p.Grid()
				idx := gridIndex(grid, trial[k])
				step := 1
				if rng.Intn(2) == 0 {
					step = -1
				}
				idx += step
				if idx < 0 || idx >= len(grid) {
					continue
				}
				trial[k] = grid[idx]
			}
			out = append(out, Candidate{ID: fmt.Sprintf("mutate-%d", i), Origin: "mutate", Params: trial})
		}
	}
	return out
}

// GenerateOpts 生成器选项（零值回落内置默认）。
// English: generator options (zero values fall back to defaults).
type GenerateOpts struct {
	MaxIter   int // 坐标上升轮数（默认 6）
	MaxMutate int // 变异候选数（默认 8）
}

func (o *GenerateOpts) fill() {
	if o.MaxIter <= 0 {
		o.MaxIter = 6
	}
	if o.MaxMutate <= 0 {
		o.MaxMutate = 8
	}
}

func cloneParams(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// gridIndex 值在网格中的下标（找不到返回 -1；调用方按 -1 跳过）。
func gridIndex(grid []float64, v float64) int {
	for i, g := range grid {
		if g == v {
			return i
		}
	}
	return -1
}
