// IC/IR 计算：每个重平衡日期的横截面秩相关。
package research

import (
	"math"
	"sort"
)

// nan 返回 NaN。
func nan() float64 { return math.NaN() }

func isNaN(v float64) bool { return math.IsNaN(v) }

// ICRow 单日横截面 IC 结果。
type ICRow struct {
	Date string  // 重平衡日期
	N    int     // 有效股票数
	IC   float64 // Spearman 秩相关
}

// IR IC 均值 / IC 标准差（比率）。无有效 IC 返回 NaN。
// （IR is the information ratio: mean(IC)/std(IC).）
func IR(rows []ICRow) float64 {
	if len(rows) < 2 {
		return nan()
	}
	mean := 0.0
	for _, r := range rows {
		mean += r.IC
	}
	mean /= float64(len(rows))
	var v float64
	for _, r := range rows {
		d := r.IC - mean
		v += d * d
	}
	std := math.Sqrt(v / float64(len(rows)))
	if std == 0 {
		return nan()
	}
	return mean / std
}

// pair 因子值与前瞻收益的配对。
type pair struct {
	f float64 // 因子值
	r float64 // 前瞻收益
}

// SpearmanIC 计算两组序列的 Spearman 秩相关。
// 任一对含 NaN 跳过；有效对数 < 3 或某序列无变差返回 NaN。
// （SpearmanIC computes rank correlation between two aligned series, dropping NaN pairs.）
func SpearmanIC(f, r []float64) float64 {
	var pairs []pair
	n := len(f)
	if len(r) < n {
		n = len(r)
	}
	for i := 0; i < n; i++ {
		if isNaN(f[i]) || isNaN(r[i]) {
			continue
		}
		pairs = append(pairs, pair{f[i], r[i]})
	}
	if len(pairs) < 3 {
		return nan()
	}
	rf := rank(pairsF(pairs))
	rr := rank(pairsR(pairs))
	return pearson(rf, rr)
}

func pairsF(p []pair) []float64 {
	out := make([]float64, len(p))
	for i := range p {
		out[i] = p[i].f
	}
	return out
}

func pairsR(p []pair) []float64 {
	out := make([]float64, len(p))
	for i := range p {
		out[i] = p[i].r
	}
	return out
}

// rank 计算平均秩（并列取平均）。
func rank(xs []float64) []float64 {
	type idx struct {
		v   float64
		pos int
	}
	arr := make([]idx, len(xs))
	for i, v := range xs {
		arr[i] = idx{v, i}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].v < arr[j].v })
	out := make([]float64, len(xs))
	for i := 0; i < len(arr); {
		j := i
		for j < len(arr) && arr[j].v == arr[i].v {
			j++
		}
		avg := float64(i+j+1) / 2 // 平均秩（1-based）
		for k := i; k < j; k++ {
			out[arr[k].pos] = avg
		}
		i = j
	}
	return out
}

// pearson 皮尔逊相关（任一侧无变差返回 NaN）。
func pearson(x, y []float64) float64 {
	if len(x) != len(y) || len(x) == 0 {
		return nan()
	}
	mx, my := 0.0, 0.0
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(len(x))
	my /= float64(len(y))
	var sxx, syy, sxy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	if sxx == 0 || syy == 0 {
		return nan()
	}
	return sxy / math.Sqrt(sxx*syy)
}

// ICByDate 对每个有足够样本的日期计算因子（factorID）相对未来 h 日收益的横截面 IC。
// 日期取自各股票面板的交集并集（按全局日期序），同一天多股票交叉截取。
// （ICByDate computes per-date cross-sectional IC of a factor vs h-day forward returns.）
func ICByDate(panels []*Panel, factorID string, h, minStocks int) []ICRow {
	dates := unionDates(panels)
	var rows []ICRow
	for _, d := range dates {
		var fv, rv []float64
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
			fv = append(fv, f)
			rv = append(rv, r)
		}
		if len(fv) < minStocks {
			continue
		}
		ic := SpearmanIC(fv, rv)
		if isNaN(ic) {
			continue
		}
		rows = append(rows, ICRow{Date: d, N: len(fv), IC: ic})
	}
	return rows
}

// CompositeIC 计算因子加权复合的逐日横截面 IC。
// 每个交易日先对各因子做横截面 z 标准化，复合分 = Σ w·z（缺失因子贡献 0），
// 再与前瞻收益做 Spearman 相关。返回逐日 IC 行（供优化器/护栏用）。
// （CompositeIC computes per-date cross-sectional IC of a weighted factor composite
// against forward returns; missing factor values contribute 0.）
func CompositeIC(panels []*Panel, factors []string, weights map[string]float64, h, minStocks int) []ICRow {
	return CompositeICRange(panels, factors, weights, h, minStocks, "", "")
}

// CompositeICRange 与 CompositeIC 相同，但仅统计 [start,end] 日期范围内的 IC 行。
// start/end 为空串表示不设界限（全区间）。供 E3 时间分段（样本内/样本外）验证使用。
// English: like CompositeIC but only includes IC rows whose date falls within [start,end].
// Empty start/end means no bound (full range). Used by E3 train/test time-split validation.
func CompositeICRange(panels []*Panel, factors []string, weights map[string]float64, h, minStocks int, start, end string) []ICRow {
	if len(factors) == 0 {
		return nil
	}
	var rows []ICRow
	for _, d := range unionDates(panels) {
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		// 每因子截面值
		vals := make(map[string]map[string]float64, len(factors)) // fid → code → value
		for _, p := range panels {
			idx, ok := p.DateIdx[d]
			if !ok {
				continue
			}
			for _, fid := range factors {
				fv, ok := p.Factors[fid]
				if !ok || idx >= len(fv) || isNaN(fv[idx]) {
					continue
				}
				if vals[fid] == nil {
					vals[fid] = make(map[string]float64)
				}
				vals[fid][p.Code] = fv[idx]
			}
		}
		// 截面 z 标准化（按 factor 缓存 mean/std）
		type zs struct{ mean, std float64 }
		zstats := make(map[string]zs, len(factors))
		for fid, m := range vals {
			if len(m) < 2 {
				continue
			}
			var sum, sum2 float64
			for _, v := range m {
				sum += v
				sum2 += v * v
			}
			mean := sum / float64(len(m))
			std := math.Sqrt(sum2/float64(len(m)) - mean*mean)
			if std > 0 {
				zstats[fid] = zs{mean, std}
			}
		}
		if len(zstats) == 0 {
			continue
		}
		// 复合分 + 前瞻收益配对
		type pair struct{ c, r float64 }
		var pairs []pair
		for _, p := range panels {
			idx, ok := p.DateIdx[d]
			if !ok {
				continue
			}
			comp := 0.0
			used := 0
			for _, fid := range factors {
				zs, has := zstats[fid]
				if !has {
					continue
				}
				v, ok := vals[fid][p.Code]
				if !ok {
					continue
				}
				w := 1.0
				if weights != nil {
					if wv, ok := weights[fid]; ok {
						w = wv
					}
				}
				comp += w * (v - zs.mean) / zs.std
				used++
			}
			if used == 0 {
				continue
			}
			r := forwardReturn(p.Series, idx, h)
			if isNaN(r) {
				continue
			}
			pairs = append(pairs, pair{comp, r})
		}
		if len(pairs) < minStocks {
			continue
		}
		cf := make([]float64, len(pairs))
		cr := make([]float64, len(pairs))
		for i, pr := range pairs {
			cf[i], cr[i] = pr.c, pr.r
		}
		ic := SpearmanIC(cf, cr)
		if isNaN(ic) {
			continue
		}
		rows = append(rows, ICRow{Date: d, N: len(pairs), IC: ic})
	}
	return rows
}

// unionDates 收集各面板日期并集并排序。
func unionDates(panels []*Panel) []string {
	seen := make(map[string]bool)
	for _, p := range panels {
		for _, d := range p.Series.Dates {
			seen[d] = true
		}
	}
	dates := make([]string, 0, len(seen))
	for d := range seen {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return dates
}