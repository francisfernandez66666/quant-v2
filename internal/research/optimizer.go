// 自动研究优化器（B5）：对因子权重做坐标上升，优化复合 IC/IR，带护栏输出候选。
package research

import (
	"math"
	"sort"
)

// OptimizeOpts 优化器选项。
// （OptimizeOpts configures the weight optimizer.）
type OptimizeOpts struct {
	Factors   []string // 因子池
	Horizon   int      // 前瞻天数（默认 5）
	MinStocks int      // 每日最小样本（默认 10）
	Metric    string   // 优化目标：ir | ic（默认 ir）
	MaxIter   int      // 坐标上升轮数（默认 6）
	Step      float64  // 每轮步长（默认 0.1）
	GuardMinIR    float64 // 护栏：|IR| 下限（默认 0.3）
	GuardMinDays  int      // 护栏：有效日下限（默认 20）
}

// OptResult 优化结果。
// （OptResult is the optimizer output for one weight set.）
type OptResult struct {
	Weights  map[string]float64 // 归一化权重（L1 和 = 1）
	ICMean   float64
	ICStd    float64
	IR       float64
	NDays    int
	PassGuard bool
	Reason   string
}

// OptimizeWeights 用坐标上升搜索因子权重，最大化复合 |IR|（或 |IC|）。
// 权重保持非负并归一化（L1=1）。返回带护栏判定的结果。
// （OptimizeWeights runs coordinate ascent over factor weights to maximize composite
// |IR| (or |IC|); weights stay non-negative and L1-normalized.）
func OptimizeWeights(panels []*Panel, opts OptimizeOpts) OptResult {
	if len(opts.Factors) == 0 {
		return OptResult{Reason: "因子池为空"}
	}
	if opts.Horizon <= 0 {
		opts.Horizon = 5
	}
	if opts.MinStocks <= 0 {
		opts.MinStocks = 10
	}
	if opts.MaxIter <= 0 {
		opts.MaxIter = 6
	}
	if opts.Step <= 0 {
		opts.Step = 0.1
	}

	// 初始等权（L1 归一化，与后续候选同尺度比较）
	w := make(map[string]float64, len(opts.Factors))
	for _, f := range opts.Factors {
		w[f] = 1.0
	}
	w = cloneWeights(w)
	best := evaluate(panels, opts, w)

	// 坐标上升：对每个因子试"增/减 step 后归一化"，保留最优
	for it := 0; it < opts.MaxIter; it++ {
		improved := false
		for _, f := range opts.Factors {
			for _, delta := range []float64{opts.Step, -opts.Step} {
				cand := cloneWeights(w)
				cand[f] += delta
				if cand[f] < 0 {
					cand[f] = 0
				}
				r := evaluate(panels, opts, cand)
				if better(r, best, opts.Metric) {
					best = r
					w = cand
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	best.Weights = cloneWeights(w) // 末尾再归一化一次（候选 bump 后未归一）
	// 护栏判定
	ir := math.Abs(best.IR)
	switch {
	case len(best.Weights) == 0:
		best.PassGuard, best.Reason = false, "无有效因子"
	case best.NDays < opts.GuardMinDays:
		best.PassGuard, best.Reason = false, "有效日不足"
	case ir < opts.GuardMinIR:
		best.PassGuard, best.Reason = false, "|IR| 低于护栏"
	default:
		best.PassGuard, best.Reason = true, "通过护栏"
	}
	return best
}

// evaluate 计算给定权重下的 IC 统计。
func evaluate(panels []*Panel, opts OptimizeOpts, w map[string]float64) OptResult {
	rows := CompositeIC(panels, opts.Factors, w, opts.Horizon, opts.MinStocks)
	return OptResult{
		ICMean: meanIC(rows), ICStd: stdIC(rows), IR: IR(rows), NDays: len(rows),
	}
}

// better 比较两个结果（目标越大越好）。
func better(a, b OptResult, metric string) bool {
	va := math.Abs(a.ICMean)
	vb := math.Abs(b.ICMean)
	if metric == "ir" {
		va = math.Abs(a.IR)
		vb = math.Abs(b.IR)
	}
	return va > vb
}

func cloneWeights(w map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(w))
	for k, v := range w {
		out[k] = v
	}
	// L1 归一化
	var sum float64
	for _, v := range out {
		sum += v
	}
	if sum == 0 {
		return out
	}
	for k, v := range out {
		out[k] = v / sum
	}
	return out
}

// TopFactors 按 |IR| 排序返回因子（供 D1 因子纳管：筛出有效因子）。
// （TopFactors sorts the pool's per-factor reports by |IR| descending.）
func TopFactors(reports []*FactorReport) []*FactorReport {
	out := make([]*FactorReport, len(reports))
	copy(out, reports)
	sort.SliceStable(out, func(i, j int) bool {
		return math.Abs(out[i].IR) > math.Abs(out[j].IR)
	})
	return out
}