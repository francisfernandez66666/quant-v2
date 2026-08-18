// 分层收益：按因子值分位数考察前瞻收益单调性。
package research

import (
	"math"
	"sort"
)

// LayerSummary 某分层的汇总（跨重平衡日期池化）。
type LayerSummary struct {
	Layer     int     // 0 = 因子值最低层
	N         int     // 参与统计的 股票-日期 数
	MeanReturn float64 // 平均前瞻收益
}

// LayerReturns 分层检验：每个重平衡日期把当日股票按因子值分 k 层，
// 计算各层平均前瞻收益，再跨日期池化求每层均值。
// 层 0 为因子值最低层，层 k−1 为最高层。
// （LayerReturns splits stocks into k quantiles per date by factor value and pools each
// layer's mean h-day forward return across dates.）
func LayerReturns(panels []*Panel, factorID string, h, quantiles, minStocks int) []LayerSummary {
	if quantiles < 2 {
		quantiles = 2
	}
	sums := make([]float64, quantiles)
	counts := make([]int, quantiles)

	for _, d := range unionDates(panels) {
		type fr struct {
			f, r float64
		}
		var list []fr
		for _, p := range panels {
			i, ok := p.DateIdx[d]
			if !ok {
				continue
			}
			f := p.Factors[factorID][i]
			if isNaN(f) {
				continue
			}
			r := forwardReturn(p.Series, i, h)
			if isNaN(r) {
				continue
			}
			list = append(list, fr{f, r})
		}
		if len(list) < minStocks {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].f < list[j].f })
		// 均分到各层（余数分给靠后层）
		base := len(list) / quantiles
		extra := len(list) % quantiles
		idx := 0
		for layer := 0; layer < quantiles; layer++ {
			size := base
			if layer < extra {
				size++
			}
			for k := 0; k < size; k++ {
				sums[layer] += list[idx].r
				counts[layer]++
				idx++
			}
		}
	}

	out := make([]LayerSummary, quantiles)
	for i := range out {
		out[i] = LayerSummary{Layer: i, N: counts[i], MeanReturn: math.NaN()}
		if counts[i] > 0 {
			out[i].MeanReturn = sums[i] / float64(counts[i])
		}
	}
	return out
}

// Monotonic 判断分层收益是否单调（相邻层差值同号，忽略相等层）。
// 返回 (单调, 方向)：(true, +1) 单调递增 / (true, −1) 单调递减。
// （Monotonic reports whether layer mean returns are monotonic, with direction.）
func Monotonic(layers []LayerSummary) (bool, int) {
	var signs []int
	prev := math.NaN()
	for _, l := range layers {
		if isNaN(l.MeanReturn) {
			continue
		}
		if !isNaN(prev) {
			d := l.MeanReturn - prev
			if math.Abs(d) > 1e-12 {
				if d > 0 {
					signs = append(signs, 1)
				} else {
					signs = append(signs, -1)
				}
			}
		}
		prev = l.MeanReturn
	}
	if len(signs) == 0 {
		return false, 0
	}
	first := signs[0]
	for _, s := range signs[1:] {
		if s != first {
			return false, 0
		}
	}
	return true, first
}