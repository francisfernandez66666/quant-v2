// 形态模板搜索（F2）：用已注册形态算子（F1）定义可参数化模板，在参数空间
// 网格搜索"触发次日买入的前瞻超额"，产出 kind="pattern" 候选。
//
// 模板模型：一个形态 = 若干算子的区间条件（AND 组合），当日全部满足即触发，
// 次日开盘价买入、h 日后按收盘卖出，评估相对基准（或相对全样本）超额与命中率。
//
// English: pattern-template search (F2) — defines parameterizable templates from the registered
// morphology operators (F1), grid-searches the parameter space for forward-excess after triggering,
// producing kind="pattern" candidates. A pattern is an AND-combination of per-operator range conditions;
// when all are met on a day it triggers a buy at next open, sold h days later, scored by excess (vs the
// full-sample mean) and hit rate.
package research

import (
	"math"
	"sort"
)

// PatternCond 模板中的一个算子条件：某因子值落在 [Min, Max) 区间才视为满足。
// （PatternCond is one operator condition: the factor value must lie in [Min, Max).）
type PatternCond struct {
	Factor string  `json:"factor"` // 算子因子 ID（须为已注册形态算子）
	Min    float64 `json:"min"`    // 区间下界（含）
	Max    float64 `json:"max"`    // 区间上界（不含）
}

// Pattern 一个形态模板：一组 AND 条件的参数化定义 + 回测证据。
// （Pattern is one shape template — a parameterized AND-condition set plus backtest evidence.）
type Pattern struct {
	Name    string        // 模板名
	Conds   []PatternCond // 条件集（AND）
	Horizon int           // 前瞻天数
	// 证据（回测产出）
	Triggers  int     // 触发次数
	Excess    float64 // 平均超额（相对全样本同 h 日收益）
	HitRate   float64 // 命中率（超额>0 占比）
	MeanRet   float64 // 平均绝对收益
	SampleOut float64 // 样本外超额（后半段）
}

// DiscoverOptsPattern 形态搜索选项。
// （DiscoverOptsPattern configures pattern search.）
type DiscoverOptsPattern struct {
	Horizon    int     // 前瞻天数（默认 5）
	MinStocks  int     // 单日触发样本下限（默认 1）
	MinTrigger int     // 护栏：最小触发次数（默认 20）
	MinExcess  float64 // 护栏：最小平均超额（默认 0.01）
	SplitPct   float64 // 样本外占比（默认 0.7）
}

// PatternTemplate 可搜索的模板骨架：固定算子的参数网格。
// Min/Max 各自提供搜索值列表，搜索时做笛卡尔积。
// （PatternTemplate is a searchable template skeleton with per-operator parameter grids; the search
// takes the Cartesian product of Min/Max candidates.）
type PatternTemplate struct {
	Name  string
	Conds []CondGrid
}

// CondGrid 单个算子的参数搜索网格。
// （CondGrid is one operator's parameter search grid.）
type CondGrid struct {
	Factor  string
	MinVals []float64
	MaxVals []float64
}

// evalPattern 对某模板在 [start,end] 区间逐日评估触发与前瞻超额。
// 返回证据（含样本外拆分）。panels 已含全部形态算子因子值。
// English: evaluates a pattern template over [start,end] day by day for triggers and forward excess,
// returning evidence including a train/test split.
func evalPattern(panels []*Panel, p Pattern, opts DiscoverOptsPattern, start, end string) Pattern {
	dates := unionDates(panels)
	splitDate := ""
	if opts.SplitPct > 0 && opts.SplitPct < 1 {
		splitIdx := int(float64(len(dates)) * opts.SplitPct)
		if splitIdx < len(dates) {
			splitDate = dates[splitIdx]
		}
	}
	type rec struct {
		date string
		ret  float64
	}
	var allRecs []rec
	var allRetSum, outRetSum float64
	var allN, outN int
	// 全样本同 h 日收益（基准：所有可交易日的平均前瞻收益）
	var baseRet []float64
	for _, pnl := range panels {
		for i := range pnl.Series.Dates {
			if i+opts.Horizon < pnl.Series.Len() {
				r := forwardReturn(pnl.Series, i, opts.Horizon)
				if !isNaN(r) {
					baseRet = append(baseRet, r)
				}
			}
		}
	}
	baseMean := 0.0
	if len(baseRet) > 0 {
		for _, r := range baseRet {
			baseMean += r
		}
		baseMean /= float64(len(baseRet))
	}

	for _, pnl := range panels {
		for i := 0; i < pnl.Series.Len()-opts.Horizon; i++ {
			d := pnl.Series.Dates[i]
			if start != "" && d < start {
				continue
			}
			if end != "" && d > end {
				continue
			}
			if !patternTriggers(pnl, p, i) {
				continue
			}
			r := forwardReturn(pnl.Series, i, opts.Horizon)
			if isNaN(r) {
				continue
			}
			allRecs = append(allRecs, rec{d, r})
			allN++
			allRetSum += r
			if splitDate != "" && d >= splitDate {
				outN++
				outRetSum += r
			}
		}
	}
	if allN < opts.MinTrigger {
		return p
	}
	p.Triggers = allN
	p.MeanRet = allRetSum / float64(allN)
	p.Excess = p.MeanRet - baseMean
	var wins float64
	for _, r := range allRecs {
		if r.ret > baseMean {
			wins++
		}
	}
	p.HitRate = wins / float64(allN)
	if outN >= int(float64(opts.MinTrigger)*0.3) {
		p.SampleOut = outRetSum/float64(outN) - baseMean
	}
	return p
}

// patternTriggers 判断第 i 日是否满足模板全部算子条件（AND）。
// 算子值为 NaN 视为不满足。
// English: reports whether day i satisfies all operator conditions (AND); NaN operator values fail.
func patternTriggers(pnl *Panel, p Pattern, i int) bool {
	for _, c := range p.Conds {
		fv, ok := pnl.Factors[c.Factor]
		if !ok || i >= len(fv) || isNaN(fv[i]) {
			return false
		}
		v := fv[i]
		if v < c.Min || v >= c.Max {
			return false
		}
	}
	return true
}

// DiscoverPatterns 对给定模板骨架做参数网格搜索，返回全部通过护栏的形态（按超额降序）。
// English: grid-searches the given template skeletons and returns all patterns passing the guard,
// sorted by excess descending.
func DiscoverPatterns(panels []*Panel, templates []PatternTemplate, opts DiscoverOptsPattern) []Pattern {
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinTrigger <= 0 {
		opts.MinTrigger = 20
	}
	if opts.MinExcess <= 0 {
		opts.MinExcess = 0.01
	}
	var out []Pattern
	for _, tmpl := range templates {
		for _, p := range expandTemplate(tmpl, opts.Horizon) {
			res := evalPattern(panels, p, opts, "", "")
			// 护栏：触发数达标 + 样本外超额为正（稳健性）才入选
			if res.Triggers >= opts.MinTrigger && res.Excess >= opts.MinExcess && res.SampleOut > 0 {
				out = append(out, res)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Excess > out[j].Excess })
	return out
}

// expandTemplate 把模板骨架的参数网格笛卡尔积展开为具体 Pattern 列表。
// English: expands a template skeleton's parameter grids (Cartesian product) into concrete Patterns.
func expandTemplate(tmpl PatternTemplate, horizon int) []Pattern {
	var result []Pattern
	// 对每个条件展开为 (Min,Max) 候选组合，然后对所有条件做笛卡尔积
	var condCombos [][]PatternCond
	for _, cg := range tmpl.Conds {
		var combos []PatternCond
		for _, mn := range cg.MinVals {
			for _, mx := range cg.MaxVals {
				if mn < mx {
					combos = append(combos, PatternCond{Factor: cg.Factor, Min: mn, Max: mx})
				}
			}
		}
		if len(combos) == 0 {
			combos = []PatternCond{{Factor: cg.Factor, Min: math.Inf(-1), Max: math.Inf(1)}}
		}
		condCombos = append(condCombos, combos)
	}
	// 笛卡尔积
	indices := make([]int, len(condCombos))
	for {
		conds := make([]PatternCond, len(condCombos))
		for i := range condCombos {
			conds[i] = condCombos[i][indices[i]]
		}
		result = append(result, Pattern{Name: tmpl.Name, Conds: conds, Horizon: horizon})
		// 进位
		k := len(condCombos) - 1
		for k >= 0 {
			indices[k]++
			if indices[k] < len(condCombos[k]) {
				break
			}
			indices[k] = 0
			k--
		}
		if k < 0 {
			break
		}
	}
	return result
}
