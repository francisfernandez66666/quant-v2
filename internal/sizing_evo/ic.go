// ic.go — §SIZING-EVO 独立实现的 IC/IR（横截面 Spearman）。
//
// 为什么独立实现而不借 internal/research：回路独立铁律（§2.7.0）——方法论同构、
// 代码独立。本文件的口径与 research/ic.go 逐字对齐（平均秩 Spearman、逐日横截面、
// IR=mean/std），但零 import；任何 future 的 research 改动都不得悄悄改掉本回路语义。
//
// L1 目标函数（§2.7.3）：对每个候选配置，把历史信号集在新配置下重算出 scale，
// 按日期分组算 (scale, 前向收益) 的横截面 IC，聚合为 IR——"这个加权能不能把好信号
// 排前面"。弃权纪律：任一侧 NaN/缺 → 该观测不进截面；组内样本 < minN 或某序列无
// 变差 → 该日跳过（不当 0、不伪造）。
//
// English: independently implemented cross-sectional Spearman IC/IR for the sizing loop.
package sizing_evo

import (
	"math"
	"sort"
)

// nan 返回 NaN（与缺失/无效值统一标记）。
func nan() float64 { return math.NaN() }

// isNaN 判断 NaN。
func isNaN(v float64) bool { return math.IsNaN(v) }

// ICSample 一个截面观测：某交易日某信号的 scale（当时生效值或候选配置重算值）与
// 前向收益。State 供 R10 市场状态分层（bull/range/bear；空=未分层）。
// English: one cross-sectional observation — scale vs forward return, tagged by market state.
type ICSample struct {
	Date  string  // 北京交易日 YYYYMMDD
	Code  string  // 纯数字代码（同日内去重/调试用）
	Scale float64 // 缩放系数（候选配置下重算；未定权=缺，不进截面）
	Ret   float64 // 前向收益（小数，0.03=+3%）
	State string  // 市场状态分层键（R10；空=不分层）
}

// ICRow 单日横截面 IC 结果。
// English: one day's cross-sectional IC.
type ICRow struct {
	Date string  `json:"date"`
	N    int     `json:"n"`
	IC   float64 `json:"ic"`
}

// IR = mean(IC)/std(IC)（样本标准差，n-1）。有效行 <2 或 std==0 → NaN。
// English: IR is mean(IC)/std(IC); NaN when fewer than two rows or zero variance.
func IR(rows []ICRow) float64 {
	if len(rows) < 2 {
		return nan()
	}
	mean := 0.0
	for _, r := range rows {
		mean += r.IC
	}
	mean /= float64(len(rows))
	var ss float64
	for _, r := range rows {
		d := r.IC - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(len(rows)-1))
	if std == 0 {
		return nan()
	}
	return mean / std
}

// MeanIC IC 均值（无有效行 → NaN）。
func MeanIC(rows []ICRow) float64 {
	if len(rows) == 0 {
		return nan()
	}
	s := 0.0
	for _, r := range rows {
		s += r.IC
	}
	return s / float64(len(rows))
}

// ICByDate 按日期分组计算横截面 Spearman IC。
// 组内观测 < minN、scale/ret 任一侧 NaN、或某序列无变差 → 该日跳过。
// 返回的行按日期升序（确定性输出，测试与报告友好）。
// English: per-date cross-sectional Spearman IC; groups below minN or without variance are skipped.
func ICByDate(samples []ICSample, minN int) []ICRow {
	if minN <= 0 {
		minN = 3 // 与包内 SpearmanIC 的最小对数同口径
	}
	byDate := make(map[string][]ICSample)
	for _, s := range samples {
		if isNaN(s.Scale) || isNaN(s.Ret) {
			continue // 弃权：缺侧不进截面
		}
		byDate[s.Date] = append(byDate[s.Date], s)
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	var rows []ICRow
	for _, d := range dates {
		grp := byDate[d]
		if len(grp) < minN {
			continue
		}
		f := make([]float64, len(grp))
		r := make([]float64, len(grp))
		for i, s := range grp {
			f[i], r[i] = s.Scale, s.Ret
		}
		ic := SpearmanIC(f, r)
		if isNaN(ic) {
			continue
		}
		rows = append(rows, ICRow{Date: d, N: len(grp), IC: ic})
	}
	return rows
}

// ICByState R10 市场状态分层：按 State 键分组各自算 IC/IR（空键="未分层"也单独成组）。
// 返回 map[state][]ICRow（确定性：每组内按日期升序）。
// English: stratified IC by market state (R10); the empty state forms its own group.
func ICByState(samples []ICSample, minN int) map[string][]ICRow {
	byState := make(map[string][]ICSample)
	for _, s := range samples {
		byState[s.State] = append(byState[s.State], s)
	}
	out := make(map[string][]ICRow, len(byState))
	for st, grp := range byState {
		out[st] = ICByDate(grp, minN)
	}
	return out
}

// SpearmanIC 两组等长序列的 Spearman 秩相关（平均秩处理并列）。
// 任一对含 NaN 跳过；有效对数 < 3 或某序列无变差 → NaN。
// English: Spearman rank correlation with average ranks for ties; NaN pairs are dropped.
func SpearmanIC(f, r []float64) float64 {
	n := len(f)
	if len(r) < n {
		n = len(r)
	}
	var fs, rs []float64
	for i := 0; i < n; i++ {
		if isNaN(f[i]) || isNaN(r[i]) {
			continue
		}
		fs = append(fs, f[i])
		rs = append(rs, r[i])
	}
	if len(fs) < 3 {
		return nan()
	}
	rf := rank(fs)
	rr := rank(rs)
	return pearson(rf, rr)
}

// rank 平均秩（1-based，并列取平均）。
// English: average ranks (1-based, ties share the mean rank).
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
		avg := float64(i+j+1) / 2
		for k := i; k < j; k++ {
			out[arr[k].pos] = avg
		}
		i = j
	}
	return out
}

// pearson 皮尔森相关（任一侧无变差 → NaN）。
// English: Pearson correlation; NaN when either side has zero variance.
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
