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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// patternWarmupDays 窗口左侧预热边距（交易日）：覆盖形态算子最大的回看窗（20 日）再加余量，
// 消除窗口头的算子截断误差。English: left warm-up margin in trade days for morphology lookbacks.
const patternWarmupDays = 40

// patWinAgg 单窗口聚合产物（断点 payload）：基准和/计数 + 每个参数组合的触发收益与样本外拆分。
// 全部为线性可合并量——跨窗口累加即得全局统计，天然支持断点续算。
// English: per-window aggregate (checkpoint payload): benchmark sums/counts plus per-combo trigger
// returns and out-of-sample split — all linearly mergeable across windows.
type patWinAgg struct {
	BaseSum float64     `json:"bs"`   // 基准窗口收益和（得分为 0 的空窗口基准）
	BaseN   int         `json:"bn"`   // 基准窗口计数
	Rets    [][]float64 `json:"rets"` // 各参数组合触发收益
	OutSums []float64   `json:"os"`   // 组合外（样本外）累计收益
	OutNs   []int       `json:"on"`   // 组合外样本数
}

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
	Name  string     // 模板名称
	Conds []CondGrid // 各算子的参数网格（搜索时做笛卡尔积）
}

// CondGrid 单个算子的参数搜索网格。
// （CondGrid is one operator's parameter search grid.）
type CondGrid struct {
	Factor  string    // 算子名称（如因子ID）
	MinVals []float64 // 下界候选值列表
	MaxVals []float64 // 上界候选值列表
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
	// rec 单次触发记录：日期 + 前瞻 h 日收益（用于全样本/样本外汇总）。
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

// DiscoverPatternsWindowed 形态模板搜索的窗口分块版（内存治理收口）：
// 旧路径一次性全量装配全市场×全区间面板（实测 RSS ~700MB，1.6G 小机内存挤压元凶），
// 改为按交易日窗口（60 日，与因子发现同口径）逐窗装配-评估-释放；聚合量全部线性可合并，
// 跨窗口累加结果与全量版同口径。支持窗口级断点（stage="pattern"，被抢占续跑跳过已完成窗口）
// 与"发现进度 xx%"进度输出。English: window-chunked pattern search — assembles per 60-day window
// (same cadence as factor discovery) instead of one full-range panel set (~700MB RSS); all aggregates
// are linearly mergeable so cross-window totals match the full version. Checkpoint-aware
// (stage "pattern") with progress lines for the queue worker.
func DiscoverPatternsWindowed(db *store.DB, codes []string, start, end string,
	templates []PatternTemplate, opts DiscoverOptsPattern) []Pattern {

	combos, accs, baseMean := discoverPatternsWindowedRaw(db, codes, start, end, templates, opts)

	baseMeanV := baseMean
	out := make([]Pattern, 0, len(combos))
	for ci := range combos {
		n := len(accs[ci].rets)
		if n < opts.MinTrigger {
			continue
		}
		p := combos[ci]
		sum := 0.0
		wins := 0.0
		for _, r := range accs[ci].rets {
			sum += r
			if r > baseMeanV {
				wins++
			}
		}
		p.Triggers = n
		p.MeanRet = sum / float64(n)
		p.Excess = p.MeanRet - baseMeanV
		p.HitRate = wins / float64(n)
		if accs[ci].outN >= int(float64(opts.MinTrigger)*0.3) {
			p.SampleOut = accs[ci].outSum/float64(accs[ci].outN) - baseMeanV
		}
		// 护栏与全量版一致：触发数达标 + 平均超额达标 + 样本外超额为正
		if p.Triggers >= opts.MinTrigger && p.Excess >= opts.MinExcess && p.SampleOut > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Excess > out[j].Excess })
	return out
}

// discoverPatternsWindowedRaw 窗口聚合内核（无护栏，供公共出口与测试复用）：
// 返回展开后的参数组合、每组合的触发收益数组/样本外拆分、全局基准均值。
// English: window-aggregation core without guard rails (shared by the public entry and tests).
func discoverPatternsWindowedRaw(db *store.DB, codes []string, start, end string,
	templates []PatternTemplate, opts DiscoverOptsPattern,
) ([]Pattern, []struct {
	rets   []float64
	outSum float64
	outN   int
}, float64) {

	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinTrigger <= 0 {
		opts.MinTrigger = 20
	}
	if opts.MinExcess <= 0 {
		opts.MinExcess = 0.01
	}
	if len(templates) == 0 || len(codes) == 0 {
		return nil, nil, 0
	}

	dates, err := db.TradeDates(start, end)
	if err != nil || len(dates) == 0 {
		return nil, nil, 0
	}
	chunks := WindowChunks(dates, 0)
	// 样本外拆分日：按全局交易日序列取分位点（与全量版 unionDates 口径一致）
	splitDate := ""
	if opts.SplitPct > 0 && opts.SplitPct < 1 {
		idx := int(float64(len(dates)) * opts.SplitPct)
		if idx < len(dates) {
			splitDate = dates[idx]
		}
	}

	// 只装配模板用到的形态算子（而非全部注册因子），进一步压内存
	needFid := map[string]bool{}
	for _, tmpl := range templates {
		for _, cg := range tmpl.Conds {
			needFid[cg.Factor] = true
		}
	}
	var defs []factor.Def
	for _, d := range factor.All() {
		if needFid[d.ID] {
			defs = append(defs, d)
		}
	}

	// 参数网格展开一次（每组合一个累积器）
	var combos []Pattern
	for _, tmpl := range templates {
		combos = append(combos, expandTemplate(tmpl, opts.Horizon)...)
	}
	accs := make([]struct {
		rets   []float64
		outSum float64
		outN   int
	}, len(combos))
	baseSum, baseN := 0.0, 0

	rk := fmt.Sprintf("dp|%s|%s|h%d|mt%d|%.2f|%.0f|%s",
		start, end, opts.Horizon, opts.MinTrigger, opts.MinExcess, opts.SplitPct*100,
		func() string {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%v", codes)))
			return hex.EncodeToString(sum[:])[:10]
		}())
	log.Printf("[discover-patterns] 断点key=%s 窗口数=%d", rk, len(chunks))
	prog := newStageProgress(5, 95, len(chunks))

	for _, w := range chunks {
		var wa patWinAgg
		wck := winCkpt{db: db, resumeKey: rk, stage: "pattern"}
		if !wck.load(w, &wa) {
			asmbEnd := w[1]
			for i := 0; i < opts.Horizon; i++ {
				asmbEnd = nextDayStr(asmbEnd)
			}
			// 左侧预热边距：形态算子含 20 日级回看，窗口头若直接从 w0 装配，
			// 前 ~20 日算子值为 NaN 导致触发丢失。回退 40 个交易日取预热历史，
			// 评估仍然只计 [w0,w1] 内日期——与全量版口径对齐。
			// English: left warm-up margin — operators need ~20d lookback; assemble from 40 trade-dates
			// before w0 (evaluation still restricted to [w0,w1]) so edge values match the full run.
			asmbStart := w[0]
			if gi := sort.SearchStrings(dates, w[0]); gi > 0 {
				lo := gi - patternWarmupDays
				if lo < 0 {
					lo = 0
				}
				asmbStart = dates[lo]
			}
			panels, err := BuildPanels(db, codes, asmbStart, asmbEnd, defs)
			if err != nil {
				prog.tick()
				continue
			}
			wa = patWinAgg{Rets: make([][]float64, len(combos)), OutSums: make([]float64, len(combos)), OutNs: make([]int, len(combos))}
			// 基准：窗口内全部股票日的 h 日前瞻收益（与全量版 baseRet 同集合）
			for _, pnl := range panels {
				for i := 0; i < pnl.Series.Len()-opts.Horizon; i++ {
					d := pnl.Series.Dates[i]
					if d < w[0] || d > w[1] {
						continue
					}
					if r := forwardReturn(pnl.Series, i, opts.Horizon); !isNaN(r) {
						wa.BaseSum += r
						wa.BaseN++
					}
				}
			}
			// 触发：单遍扫描同时评估所有参数组合（比逐组合重扫快「组合数」倍）
			for ci := range combos {
				p := combos[ci]
				for _, pnl := range panels {
					for i := 0; i < pnl.Series.Len()-opts.Horizon; i++ {
						d := pnl.Series.Dates[i]
						if d < w[0] || d > w[1] {
							continue
						}
						if !patternTriggers(pnl, p, i) {
							continue
						}
						r := forwardReturn(pnl.Series, i, opts.Horizon)
						if isNaN(r) {
							continue
						}
						wa.Rets[ci] = append(wa.Rets[ci], r)
						if splitDate != "" && d >= splitDate {
							wa.OutSums[ci] += r
							wa.OutNs[ci]++
						}
					}
				}
			}
			wck.save(w, wa)
		}
		baseSum += wa.BaseSum
		baseN += wa.BaseN
		for ci := range combos {
			accs[ci].rets = append(accs[ci].rets, wa.Rets[ci]...)
			accs[ci].outSum += wa.OutSums[ci]
			accs[ci].outN += wa.OutNs[ci]
		}
		prog.tick()
	}
	baseMean := 0.0
	if baseN > 0 {
		baseMean = baseSum / float64(baseN)
	}
	return combos, accs[:], baseMean
}
