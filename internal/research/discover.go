// 因子战法自动发现（E2/E3）：从全因子池贪心前向选择子集 → 权重优化 →
// 样本内/样本外分段验证 + 同环境反推泛化验证，产出"因子型新战法"候选。
// English: automatic factor-strategy discovery (E2/E3) — greedy forward subset selection from the
// full factor pool, weight optimization, train/test time-split validation plus same-environment
// reverse-extension (反推) generalization, producing "factor-strategy" candidates.
package research

import (
	"math"
	"sort"
	"strconv"

	"quant-trading-v2/internal/factor"
)

// DiscoverOpts 因子发现选项。
// （DiscoverOpts configures factor-strategy discovery.）
type DiscoverOpts struct {
	Factors    []string // 候选因子池（缺省取 factor.All() 全部 ID）
	Horizon    int      // 前瞻天数（默认 5）
	MinStocks  int      // 每日最小样本（默认 10）
	Metric     string   // 优化目标：ir|ic（默认 ir）
	MaxFactors int      // 组合最大因子数（默认 8）
	Step       float64  // 权重坐标上升步长（默认 0.1）
	MinIR      float64  // 全区间护栏 |IR| 下限（默认 0.3）
	MinDays    int      // 全区间护栏有效日下限（默认 20）
	SplitPct   float64  // 样本内占比（0~1，默认 0.7），用于时间分段样本外验证
	// MinGenT 反推泛化护栏（Welch t 检验）：高分组 vs 非高分组 的 5 日前瞻收益差
	// 的 t 统计量低于该负阈值才拦截（默认 -2，约 5% 单侧显著）。
	// 用统计显著性而非固定百分比——样本量海量时标准误极小，哪怕 0.3% 的负超额也可能
	// t=-11 显著为负，固定百分数阈值会错误放行；小样本下小幅负超额因 t 不显著则正常放行。
	// English: reverse-extension guard (Welch t-test) — the t-statistic of the top-quintile vs
	// non-top 5-day return difference below this negative threshold fails the guard (default -2, ~5%
	// one-sided). Uses statistical significance rather than a fixed percentage: with huge sample sizes
	// the standard error is tiny so even a 0.3% negative excess can be t=-11 and significantly negative,
	// which a fixed-percent threshold would wrongly pass; with small samples a mildly negative excess
	// passes because its t is not significant.
	MinGenT float64 // 默认 -2
	// ExcludeCombos §F4 已驳回候选去重：禁止再次生成的因子组合集合（每个元素=一组因子 ID）。
	// 贪心前向选择每步评估候选集时，若 selected+当前因子 与任一已驳回组合同集合（排序后逐元素相等）
	// 则跳过该因子，强制探索其它组合；horizon/方向为每轮参数与类别默认方向，天然一致。
	// English: §F4 rejected-combo de-duplication — factor sets that must not be regenerated. During
	// greedy selection a candidate (selected + candidate factor) whose sorted set matches any excluded
	// combo is skipped, forcing discovery to explore different combinations.
	ExcludeCombos [][]string
}

// DiscoverResult 因子发现结果。
// （DiscoverResult is the output of one factor-strategy discovery.）
type DiscoverResult struct {
	Factors     []string           // 选中的因子集合（按加入顺序）
	Directions  map[string]int     // factorID → 方向（+1 看多 / -1 看空）
	Weights     map[string]float64 // 归一化权重（L1 和 = 1）
	ICMean      float64            // 样本内平均 IC（信息系数）
	IR          float64            // 信息比率 = ICMean / ICStdev
	NDays       int                // 有效评估天数
	PassGuard   bool               // 是否通过护栏（样本量/信号强度校验）
	Reason      string             // 未通过护栏时的原因说明
	InsampleIR  float64            // E3 样本内 IR（前半段）
	OutsampleIR float64            // E3 样本外 IR（后半段）
	// 反推泛化（E3）：在样本外区间，把每日按复合分排序，高分组（top 20%）平均前瞻收益
	// vs 非高分组平均 —— 衡量"相同因子环境在同类股票上是否普适上涨"。
	GenTopMean float64 // 高分组平均前瞻收益
	GenAllMean float64 // 非高分组平均前瞻收益
	GenExcess  float64 // 高分组超额 = GenTopMean - GenAllMean（>0 表示因子环境普适）
	GenStdErr  float64 // 超额的标准误（Welch）
	GenT       float64 // 超额 t 统计量 = GenExcess / GenStdErr（< -2 显著为负 → 反推失败）
}

// DiscoverFactors 执行因子子集选择 + 分段/反推验证。
// 流程：
//  1. 单因子预筛：对候选池逐一算 IR，剔除 IR 过低或 NaN 的因子。
//  2. 贪心前向选择：从空集开始，每步加入"使复合 IR 提升最大"的因子，直到
//     不再提升或达到 MaxFactors。每步用等权评估（方向按因子类别默认）。
//  3. 对最终集合做坐标上升权重优化（复用 OptimizeWeights 的评估逻辑）。
//  4. E3 验证：样本内（前半段）与样本外（后半段）分别算 IR；
//     反推泛化用样本外区间的高分组超额。
//
// English: runs factor subset selection plus train/test and reverse-extension validation:
// (1) single-factor pre-screen dropping low/NaN IR factors; (2) greedy forward selection adding the
// factor that most improves composite IR until no gain or MaxFactors reached, using equal weights
// with category-default directions; (3) coordinate-ascent weight optimization on the final set;
// (4) E3 validation — train (first SplitPct) and test (remainder) IR, plus test-window top-quintile
// reverse-extension excess.
func DiscoverFactors(panels []*Panel, opts DiscoverOpts) DiscoverResult {
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinStocks <= 0 {
		opts.MinStocks = 10
	}
	if opts.MaxFactors <= 0 {
		opts.MaxFactors = 8
	}
	if opts.Step <= 0 {
		opts.Step = 0.1
	}
	if opts.MinIR <= 0 {
		opts.MinIR = 0.3
	}
	if opts.MinDays <= 0 {
		opts.MinDays = 20
	}
	if opts.SplitPct <= 0 || opts.SplitPct >= 1 {
		opts.SplitPct = 0.7
	}
	if opts.MinGenT >= 0 {
		// 未显式设置（>=0 视为未配置）→ 默认 -2（约 5% 单侧显著）。
		// 只有显式传负值才收紧/放宽该护栏。
		// English: when unset (>=0 means not configured), default to -2 (~5% one-sided). Only an
		// explicit negative value tightens/loosens this guard.
		opts.MinGenT = -2
	}
	if len(opts.Factors) == 0 {
		// 缺省用全部已注册因子
		for _, d := range factor.All() {
			opts.Factors = append(opts.Factors, d.ID)
		}
	}
	res := DiscoverResult{Directions: map[string]int{}, Weights: map[string]float64{}}
	if len(panels) == 0 {
		res.Reason = "无有效面板"
		return res
	}

	// 日期分段边界：取全局日期并集的中位切点
	dates := unionDates(panels)
	if len(dates) < 10 {
		res.Reason = "日期过少"
		return res
	}
	splitIdx := int(float64(len(dates)) * opts.SplitPct)
	splitDate := dates[splitIdx]

	// 1) 单因子预筛：§GAP 二.3#4 只看样本内（≤splitDate）|IR|，保留有效因子
	var pre []string
	for _, fid := range opts.Factors {
		rows := ICByDate(panels, fid, opts.Horizon, opts.MinStocks)
		if len(rows) < opts.MinDays {
			continue
		}
		ir := absf(IR(rowsUntil(rows, splitDate)))
		if isNaN(ir) || ir < 0.05 {
			continue
		}
		pre = append(pre, fid)
	}
	if len(pre) == 0 {
		res.Reason = "预筛后无有效因子"
		return res
	}

	// 2) 贪心前向选择：等权 + 类别默认方向，逐轮加使复合 IR 提升最大的因子
	selected := make([]string, 0, opts.MaxFactors)
	selectedSet := map[string]bool{}
	bestIR := -1e9
	dirs := map[string]int{}
	for len(selected) < opts.MaxFactors {
		bestFid := ""
		bestCandIR := bestIR
		for _, fid := range pre {
			if selectedSet[fid] {
				continue
			}
			// §F4 已驳回组合跳过（与非 windowed 版本一致）。English: skip already-rejected combos.
			if comboExcluded(selected, fid, opts.ExcludeCombos) {
				continue
			}
			// 候选集 = selected + fid，等权评估样本内 IR（§GAP 二.3#4 真 hold-out）
			candFactors := append(append([]string{}, selected...), fid)
			w := map[string]float64{}
			for _, f := range candFactors {
				w[f] = 1.0
			}
			rows := CompositeICRange(panels, candFactors, w, opts.Horizon, opts.MinStocks, "", splitDate)
			ir := absf(IR(rows))
			if isNaN(ir) {
				continue
			}
			if ir > bestCandIR {
				bestCandIR = ir
				bestFid = fid
			}
		}
		if bestFid == "" || bestCandIR <= bestIR {
			break // 无提升或无法再选
		}
		selected = append(selected, bestFid)
		selectedSet[bestFid] = true
		bestIR = bestCandIR
	}
	if len(selected) == 0 {
		res.Reason = "前向选择未选出因子"
		return res
	}

	// 3) 方向：按因子类别默认方向（价值/成长/质量/动量/流动性看多，规模/波动率看空）
	for _, fid := range selected {
		if d, ok := factor.Get(fid); ok {
			dirs[fid] = dirOfCat(d.Cat)
		} else {
			dirs[fid] = 1
		}
	}
	// 权重优化：坐标上升最大化复合 |IR|（§GAP 二.3#4 只喂样本内，样本外留作验证）
	opt := OptimizeWeights(panels, OptimizeOpts{
		Factors: selected, Horizon: opts.Horizon, MinStocks: opts.MinStocks,
		Metric: opts.Metric, Step: opts.Step, MaxIter: 6,
		GuardMinIR: opts.MinIR, GuardMinDays: opts.MinDays,
		End: splitDate,
	})
	res.Factors = selected
	res.Directions = dirs
	res.Weights = opt.Weights
	res.ICMean = opt.ICMean
	res.IR = opt.IR
	res.NDays = opt.NDays
	res.PassGuard = opt.PassGuard
	res.Reason = opt.Reason

	// 4) E3 分段 + 反推验证
	res.InsampleIR = compositeIRInRange(panels, selected, opt.Weights, opts, "", splitDate)
	res.OutsampleIR = compositeIRInRange(panels, selected, opt.Weights, opts, splitDate, "")
	res.GenTopMean, res.GenAllMean, res.GenExcess, res.GenStdErr, res.GenT = reverseExtension(panels, selected, dirs, opt.Weights, opts, splitDate, "")

	// 样本外护栏：样本外 IR 也需达标才视为稳健
	if res.OutsampleIR < opts.MinIR {
		if res.PassGuard {
			res.Reason = "样本内过护栏但样本外IR不足(" + trimFloat(res.OutsampleIR) + ")"
			res.PassGuard = false
		}
	}
	// 反推泛化护栏（Welch t 检验）：高分组 vs 非高分组 收益差的 t 统计量显著为负
	// （t < MinGenT，默认 -2）才拦截。样本量海量时标准误极小，固定百分比阈值会误放行
	// 显著负超额，因此用统计显著性判断；小样本下小幅负超额 t 不显著则正常放行。
	// GenT 为 NaN（样本不足/无波动）时不否决（数据不足以否定组合稳健性）。
	// English: the reverse-extension guard uses a Welch t-test — only a t-statistic below MinGenT
	// (default -2) fails the guard. With huge sample sizes a fixed-percent threshold would wrongly pass
	// a significantly negative excess, so statistical significance is used; small-sample mildly
	// negative excesses pass because their t is not significant. A NaN GenT (insufficient data) is not
	// a veto.
	if res.PassGuard && !isNaN(res.GenT) && res.GenT < opts.MinGenT {
		res.Reason = "反推泛化不足（高分组超额" + trimFloat(res.GenExcess) + "，t=" + trimFloat(res.GenT) + "显著为负）"
		res.PassGuard = false
	}
	return res
}

// comboExcluded §F4 判断 已选中因子集 selected + 候选因子 fid 的组合 是否命中已驳回组合。
// 组合按排序后的集合比较（顺序无关）；单元素命中即视为该组合已驳回。
// English: reports whether the factor set (selected ∪ {fid}) equals any rejected combo,
// compared set-wise after sorting.
func comboExcluded(selected []string, fid string, excludes [][]string) bool {
	if len(excludes) == 0 {
		return false
	}
	cur := make([]string, 0, len(selected)+1)
	cur = append(cur, selected...)
	cur = append(cur, fid)
	sort.Strings(cur)
	for _, ex := range excludes {
		if len(ex) != len(cur) {
			continue
		}
		sorted := make([]string, len(ex))
		copy(sorted, ex)
		sort.Strings(sorted)
		same := true
		for i := range cur {
			if cur[i] != sorted[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// IsComboRejected §F4 判断一个最终因子组合是否命中已驳回组合集合（集合比较，顺序无关）。
// 供调用方在落库前兜底过滤。English: reports whether a final factor set matches any rejected combo
// (set-wise, order-independent); used as a final guard before persisting a candidate.
func IsComboRejected(factors []string, excludes [][]string) bool {
	if len(excludes) == 0 || len(factors) == 0 {
		return false
	}
	cur := make([]string, len(factors))
	copy(cur, factors)
	sort.Strings(cur)
	for _, ex := range excludes {
		if len(ex) != len(cur) {
			continue
		}
		sorted := make([]string, len(ex))
		copy(sorted, ex)
		sort.Strings(sorted)
		same := true
		for i := range cur {
			if cur[i] != sorted[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// rowsUntil 返回 date ≤ end 的 IC 行子集（§GAP 二.3#4 样本内切分辅助；end 为空=原样返回）。
func rowsUntil(rows []ICRow, end string) []ICRow {
	if end == "" {
		return rows
	}
	out := make([]ICRow, 0, len(rows))
	for _, r := range rows {
		if r.Date <= end {
			out = append(out, r)
		}
	}
	return out
}

// compositeIRInRange 计算复合权重在某日期范围内的 |IR|。
// English: computes the |IR| of a weighted factor composite within a date range.
func compositeIRInRange(panels []*Panel, factors []string, weights map[string]float64, opts DiscoverOpts, start, end string) float64 {
	rows := CompositeICRange(panels, factors, weights, opts.Horizon, opts.MinStocks, start, end)
	ir := IR(rows)
	if isNaN(ir) {
		return 0
	}
	return math.Abs(ir)
}

// reverseExtension 反推泛化：在 [start,end] 区间，每日按复合分排序，
// 高分组（top 20%）平均前瞻收益 vs 全样本平均，返回 (topMean, allMean, excess)。
// 衡量"相同因子环境在同类股票上是否普适上涨"（对应需求：反推其他股票看同环境是否涨）。
// 与 CompositeIC 同口径：每日对复合分做截面 z 标准化后再排序，保证 top 分组与 IR 一致，
// 避免原始因子量纲差异导致高分组选错股票。
// English: reverse-extension generalization — in [start,end], each day ranks stocks by the
// cross-sectional z-scored composite (consistent with CompositeIC), compares the top-quintile mean
// forward return to the non-top mean, and returns (topMean, restMean, excess, stdErr, t) where t is
// the Welch t-statistic of the excess (top vs non-top). Measures whether the factor environment
// generalizes to similar stocks, using statistical significance rather than a fixed percentage.
func reverseExtension(panels []*Panel, factors []string, dirs map[string]int, weights map[string]float64, opts DiscoverOpts, start, end string) (float64, float64, float64, float64, float64) {
	var topRets, restRets []float64
	for _, d := range unionDates(panels) {
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		// 当日截面：原始复合分 + 前瞻收益
		type kv struct {
			code string
			sc   float64
			r    float64
		}
		var day []kv
		for _, p := range panels {
			idx, ok := p.DateIdx[d]
			if !ok {
				continue
			}
			r := forwardReturn(p.Series, idx, opts.Horizon)
			if isNaN(r) {
				continue
			}
			sc := compositeScore(p, factors, dirs, weights, d)
			if isNaN(sc) {
				continue
			}
			day = append(day, kv{p.Code, sc, r})
		}
		if len(day) < opts.MinStocks {
			continue
		}
		// 截面 z 标准化（与 CompositeIC 同口径）：z = (sc - mean) / std
		var sum, sum2 float64
		for _, v := range day {
			sum += v.sc
			sum2 += v.sc * v.sc
		}
		mean := sum / float64(len(day))
		std := math.Sqrt(sum2/float64(len(day)) - mean*mean)
		if std <= 0 {
			continue
		}
		zs := make([]kv, len(day))
		for i, v := range day {
			zs[i] = kv{v.code, (v.sc - mean) / std, v.r}
		}
		sort.Slice(zs, func(i, j int) bool { return zs[i].sc > zs[j].sc })
		nTop := len(zs) / 5
		if nTop < 1 {
			nTop = 1
		}
		for i, v := range zs {
			if i < nTop {
				topRets = append(topRets, v.r)
			} else {
				restRets = append(restRets, v.r)
			}
		}
	}
	if len(topRets) == 0 || len(restRets) == 0 {
		return 0, 0, 0, 0, nan()
	}
	topMean := meanOf(topRets)
	restMean := meanOf(restRets)
	excess := topMean - restMean
	// Welch t 检验：t = (topMean - restMean) / sqrt(var_top/n_top + var_rest/n_rest)
	varTop := varianceOf(topRets, topMean)
	varRest := varianceOf(restRets, restMean)
	stdErr := math.Sqrt(varTop/float64(len(topRets)) + varRest/float64(len(restRets)))
	var t float64
	if stdErr > 0 {
		t = excess / stdErr
	} else {
		t = nan()
	}
	return topMean, restMean, excess, stdErr, t
}

// meanOf 返回切片均值（空切片返回 0）。
func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, v := range xs {
		s += v
	}
	return s / float64(len(xs))
}

// varianceOf 返回切片总体方差（相对给定均值）。
func varianceOf(xs []float64, mean float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var s float64
	for _, v := range xs {
		d := v - mean
		s += d * d
	}
	return s / float64(len(xs))
}

// compositeScore 计算单只股票在某日的加权复合分（含方向）。
// English: computes one stock's weighted composite score on a date (direction applied).
func compositeScore(p *Panel, factors []string, dirs map[string]int, weights map[string]float64, d string) float64 {
	idx, ok := p.DateIdx[d]
	if !ok {
		return nan()
	}
	total, used := 0.0, 0
	for _, fid := range factors {
		fv, ok := p.Factors[fid]
		if !ok || idx >= len(fv) || isNaN(fv[idx]) {
			continue
		}
		dir := 1
		if v, ok := dirs[fid]; ok {
			dir = v
		}
		w := 1.0
		if weights != nil {
			if v, ok := weights[fid]; ok {
				w = v
			}
		}
		total += float64(dir) * w * fv[idx]
		used++
	}
	if used == 0 {
		return nan()
	}
	return total
}

// dirOfCat 因子类别 → 默认方向：价值/成长/质量/动量/流动性看多（+1），规模/波动率看空（-1）。
// 与 backtest.dirOf 语义一致，保证发现的组合在 B4 回测中方向自洽。
// English: factor-category default direction — value/growth/quality/momentum/liquidity long (+1),
// size/volatility short (-1). Consistent with backtest.dirOf so discovered combos backtest coherently.
func dirOfCat(c factor.Category) int {
	switch c {
	case factor.CatValue, factor.CatGrowth, factor.CatQuality,
		factor.CatMomentum, factor.CatLiquidity:
		return 1
	default: // 规模/波动率
		return -1
	}
}

// absf 返回绝对值。
func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// trimFloat 浮点转短字符串（护栏理由用，避免长尾）。
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}
