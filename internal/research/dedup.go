// dedup.go — 因子相关度去重（§SIGNAL_EDGE_ENHANCEMENT_PLAN P1.3）。
// 高相关因子在复合分里重复计权会造假性 IR 提升。做法：
//   1. 对候选因子两两求"逐日 IC 序列"的 Pearson 相关（复用 ic.go 的 ICByDate/pearson）；
//   2. 相关性 ≥ 阈值（默认 0.7）贪心成簇，簇内保留均值 |IC| 最高者；
//   3. 权重按"去相关后的均值 IC"归一分配（L1=1），替代单纯按 IR/等权。
// 不改发现流程本身，只在结果产出前做一次去重（开关 DiscoverOpts.DedupCorr=0 时零操作）。
// English: factor correlation de-duplication (P1.3). Highly correlated factors double-count in a
// composite and fake-inflate IR. Steps: (1) pairwise Pearson of per-date IC series (reusing
// ICByDate/pearson from ic.go); (2) greedy clusters for correlation ≥ threshold (0.7 default),
// keeping the highest mean |IC| per cluster; (3) re-normalize weights by de-correlated IC (L1=1).
// Only a post-filter before results are emitted; no-op when DiscoverOpts.DedupCorr == 0.

package research

import (
	"math"
	"sort"
)

// FactorICCorr 两因子逐日 IC 序列的 Pearson 相关（按共有日期对齐，丢弃单侧缺失）。
// 返回 [0] 相关度；不可比时返回 NaN。
// English: Pearson correlation between two factors' per-date IC series, aligned on common dates
// with one-sided drops. Returns NaN when not comparable.
func FactorICCorr(aIC, bIC []ICRow) float64 {
	byDate := make(map[string]float64, len(aIC))
	for _, r := range aIC {
		byDate[r.Date] = r.IC
	}
	var x, y []float64
	for _, r := range bIC {
		if v, ok := byDate[r.Date]; ok && !isNaN(v) && !isNaN(r.IC) {
			x = append(x, v)
			y = append(y, r.IC)
		}
	}
	if len(x) < 2 {
		return math.NaN()
	}
	return pearson(x, y)
}

// factorMeanIC 某因子全区间均值 IC（跨共有日期）。空序列返回 NaN。
func factorMeanIC(rows []ICRow) float64 {
	var sum, n float64
	for _, r := range rows {
		if isNaN(r.IC) {
			continue
		}
		sum += r.IC
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / n
}

// DedupClusters 对 selected 因子集按 IC 序列相关贪心聚类去重。
// 返回簇划分（clusterID → factorID 列表）与保留因子集（每簇均值 |IC| 最高者），
// 保留集按原 selected 顺序输出。
// English: greedily clusters the selected factors by IC-series correlation, returning the cluster
// partition and the kept factor set (highest mean |IC| per cluster), in the original order.
func DedupClusters(selected []string, icByFactor map[string][]ICRow, thresh float64) ([][]string, []string) {
	if thresh <= 0 || len(selected) < 2 {
		return [][]string{append([]string{}, selected...)}, append([]string{}, selected...)
	}
	// 各因子均值 |IC|，预排序用作贪心启发
	type sc struct {
		id string
		ic float64
	}
	var scored []sc
	for _, id := range selected {
		rows, ok := icByFactor[id]
		if !ok || len(rows) < 2 {
			continue
		}
		m := factorMeanIC(rows)
		if isNaN(m) {
			continue
		}
		scored = append(scored, sc{id: id, ic: math.Abs(m)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].ic > scored[j].ic })

	seen := make(map[string]bool, len(selected))
	var kept []string
	for _, c := range scored {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		kept = append(kept, c.id)
		// 同簇：把与 c.id 相关度 ≥ 阈值的未入簇因子一并归入并剔除
		for _, o := range scored {
			if o.id == c.id || seen[o.id] {
				continue
			}
			corr := FactorICCorr(icByFactor[c.id], icByFactor[o.id])
			if !isNaN(corr) && math.Abs(corr) >= thresh {
				seen[o.id] = true
			}
		}
	}
	return nil, kept
}

// DedupWeights 按去相关后的均值 |IC| 给保留因子归一分配权重（L1=1）。
// IC 缺失的因子权重 0（调用方应在此前用 DedupClusters 过滤）。返回 factorID→weight。
// English: normalizes weights of the kept factors by de-correlated mean |IC| (L1=1). Factors
// without an IC series get weight 0 (filter them with DedupClusters first).
func DedupWeights(kept []string, icByFactor map[string][]ICRow) map[string]float64 {
	out := make(map[string]float64, len(kept))
	var total float64
	absIC := make(map[string]float64, len(kept))
	for _, id := range kept {
		rows, ok := icByFactor[id]
		if !ok {
			continue
		}
		m := factorMeanIC(rows)
		if isNaN(m) {
			continue
		}
		v := math.Abs(m)
		absIC[id] = v
		total += v
	}
	if total <= 0 {
		return nil
	}
	for id, v := range absIC {
		out[id] = v / total
	}
	return out
}

// ApplyDedup 汇总入口：给定 selected/directions/weights 与逐因子 IC 序列，返回去重后的
// directions 与去相关权重。thresh<=0 时原样返回（开关关闭零操作）。
// English: combined entry point — with selected/directions/weights and per-factor IC series,
// returns post-dedup directions and de-correlated weights; unchanged when thresh<=0.
func ApplyDedup(selected []string, directions map[string]int, icByFactor map[string][]ICRow, thresh float64) ([]string, map[string]int, map[string]float64) {
	if thresh <= 0 {
		return selected, directions, nil // nil 表示调用方保留原权重
	}
	_, kept := DedupClusters(selected, icByFactor, thresh)
	if len(kept) == len(selected) {
		return selected, directions, nil
	}
	w := DedupWeights(kept, icByFactor)
	if len(w) == 0 {
		return selected, directions, nil
	}
	newDirs := make(map[string]int, len(kept))
	for _, id := range kept {
		if d, ok := directions[id]; ok {
			newDirs[id] = d
		} else {
			newDirs[id] = 1
		}
	}
	return kept, newDirs, w
}
