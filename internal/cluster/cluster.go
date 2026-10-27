// cluster.go — §T-COST B3（2026-10-27）相关性簇：把"同一件事的两个投影"并成一簇。
//
// 为什么需要它：单票 35% / 行业 35% 两道帽管不住"三只不同行业、走势却几乎一样"的持仓
// （典型：同一轮主题炒作里的龙头/跟风/补涨）。相关性帽回答的是"这几笔买入是不是同一个赌注"。
//
// 双轨合并（方案 §5.2，取严不取宽）：
//   - 收益相关：近 60 个交易日日收益两两 Pearson，ρ ≥ 0.7 ⇒ 同簇；
//   - 行业相同：industry(a) == industry(b)（非空）⇒ 同簇，不看相关系数。
//
// 并查集合并后簇 id 取**簇内最小代码**：稳定、可对账（日志里两个代码的簇 id 一致即同簇，
// 不需要把整张矩阵打出来）。
//
// 弃权与缺失：有效重叠样本 < minObs（默认 40）时相关系数不可信 ⇒ 按"同簇"处理（fail-closed）。
// 方向与跨账号互斥、持仓读不到即不开仓一致：宁可少买一只，也不要因为读不到而把同一注下两遍。
// 这条会把"新上市不足 40 天的票"直接并进所有簇——这是刻意的，报告里必须点名，不许静默。
//
// English: correlation clusters for B3. Two tracks union together (ρ ≥ 0.7 on 60-day returns, or
// the same industry). Cluster id = the smallest code in the set. Pairs with fewer than minObs
// overlapping samples fail closed (treated as the same cluster) — the same direction as the
// cross-account exclusion: never spend an unreadable reading as if it were diversification.
package cluster

import "math"

const (
	// DefaultRho 默认相关阈值（owner 2026-10-27 拍板 0.7）。
	DefaultRho = 0.70
	// DefaultMinObs 有效重叠样本下限（少于该值视为读数不可得）。
	DefaultMinObs = 40
	// DefaultWindowDays 标定窗口的交易日数（近 60 个交易日）。
	DefaultWindowDays = 60
)

// Series 一只票的日收益序列（日期 → 收益率，小数不是百分数）。
// 用 map 而不是切片是刻意的：停牌日**缺席**而不是补 0（补 0 会把停牌算成"当天不涨不跌"，
// 直接稀释相关系数，让两只本来同步的票看起来不相关——方向正好是"更敢买"）。
// English: per-code daily returns keyed by date; suspended days are absent rather than zero-filled.
type Series map[string]float64

// Pearson 两条收益序列在**重叠日期**上的相关系数。
// 返回 NaN 表示读数不可得（重叠 < 2 或任一侧零方差），调用方必须按"不可得"处理，
// 不许把它当成 0（0 的含义是"确实不相关"，与"算不出来"是两件事）。
// English: Pearson correlation over the overlapping dates; NaN means the reading is unavailable
// (fewer than 2 overlapping samples, or a zero-variance side) and must not be read as 0.
func Pearson(a, b Series) (rho float64, n int) {
	// 先取交集：只比较两边都有收益的交易日。
	xs := make([]float64, 0, len(a))
	ys := make([]float64, 0, len(b))
	for d, x := range a {
		y, ok := b[d]
		if !ok {
			continue
		}
		xs = append(xs, x)
		ys = append(ys, y)
	}
	n = len(xs)
	if n < 2 {
		return math.NaN(), n
	}
	var sx, sy float64
	for i := 0; i < n; i++ {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/float64(n), sy/float64(n)
	var sxy, sxx, syy float64
	for i := 0; i < n; i++ {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	// 任一侧零方差（整段停牌/一字板）⇒ 相关系数没有定义。
	if sxx <= 0 || syy <= 0 {
		return math.NaN(), n
	}
	rho = sxy / math.Sqrt(sxx*syy)
	// 浮点尾差可能给出 1.0000000000000002：夹回 [-1,1]，否则下游"ρ≥阈值"的比较会读出怪数。
	if rho > 1 {
		rho = 1
	}
	if rho < -1 {
		rho = -1
	}
	return rho, n
}

// Index 簇索引（构建后只读）。
type Index struct {
	// id 代码 → 簇 id（簇内最小代码）。
	id map[string]string
	// unknown 因"读数不可得"被强制并簇的代码对数（留痕用；>0 时报告必须点名）。
	unknown int
	// pairs 参与比较的代码对数（分母，便于回答"这簇是怎么并出来的"）。
	pairs int
}

// UnknownPairs 因读数不可得（样本不足/零方差）被强制并簇的对数。
func (i *Index) UnknownPairs() int {
	if i == nil {
		return 0
	}
	return i.unknown
}

// Pairs 参与比较的代码对数。
func (i *Index) Pairs() int {
	if i == nil {
		return 0
	}
	return i.pairs
}

// ClusterOf 该代码的簇 id（未知代码返回自身，即"自成簇"）。
// 未知代码自成簇是**保守但可解释**的：它没有参与任何比较，就不该被算进别人的簇。
// 真正的 fail-closed 发生在 Build 里（读数不可得的**已比较**对按同簇处理）。
func (i *Index) ClusterOf(code string) string {
	if i == nil || i.id == nil {
		return code
	}
	if v, ok := i.id[code]; ok {
		return v
	}
	return code
}

// Same 两只票是否同簇（同代码恒真）。
func (i *Index) Same(a, b string) bool {
	if a == b {
		return true
	}
	return i.ClusterOf(a) == i.ClusterOf(b)
}

// Build 按双轨规则构建簇索引。
//
//   - returns 每只票的日收益（缺失/空 ⇒ 该票与所有票的相关系数都不可得 ⇒ 全部按同簇处理）；
//   - industries 代码 → 行业（空串表示行业未知，此时只有相关轨生效）；
//   - rho ≤ 0 用 DefaultRho；minObs ≤ 0 用 DefaultMinObs。
//
// 复杂度 O(n²)：调用点的候选集是"持仓 ∪ 在途 ∪ 这一只"，n 通常 < 12，不需要优化。
// English: builds the index with the two union tracks; O(n²) over a set of a dozen codes.
func Build(codes []string, returns map[string]Series, industries map[string]string,
	rho float64, minObs int) *Index {
	if rho <= 0 {
		rho = DefaultRho
	}
	if minObs <= 0 {
		minObs = DefaultMinObs
	}
	idx := &Index{id: make(map[string]string, len(codes))}
	// 并查集：parent[x] 指向上一级，路径压缩在 find 里做。
	parent := make(map[string]string, len(codes))
	var find func(string) string
	find = func(x string) string {
		p, ok := parent[x]
		if !ok || p == x {
			parent[x] = x
			return x
		}
		root := find(p)
		parent[x] = root
		return root
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		// 让字典序较小者当根：簇 id 直接就是根，不必再扫一遍求最小值。
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	for _, c := range codes {
		parent[c] = c
	}
	for i := 0; i < len(codes); i++ {
		for j := i + 1; j < len(codes); j++ {
			a, b := codes[i], codes[j]
			idx.pairs++
			// 行业轨优先：同行业直接并簇，不看相关系数（行业是硬事实，相关系数是估计）。
			if ia := industries[a]; ia != "" && ia == industries[b] {
				union(a, b)
				continue
			}
			r, n := Pearson(returns[a], returns[b])
			switch {
			case n < minObs || math.IsNaN(r):
				// 读数不可得 ⇒ fail-closed 并簇，并计数（报告要点名，不许静默）。
				idx.unknown++
				union(a, b)
			case r >= rho:
				union(a, b)
			}
		}
	}
	for _, c := range codes {
		idx.id[c] = find(c)
	}
	return idx
}

// ValueOf 同一簇内已持仓/在途的市值合计（**不含**目标代码本身的那一份）。
// 单独抽成函数是为了让调用点只有一行：任何"顺手把目标自己也加进去"的写法都会让
// 簇额度凭空少掉一笔，而那种偏差在读数上完全看不出来。
// English: sums the market value of the cluster, excluding the target code itself.
func ValueOf(idx *Index, code string, values map[string]float64) float64 {
	if idx == nil {
		return 0
	}
	cid := idx.ClusterOf(code)
	sum := 0.0
	for c, v := range values {
		if c == code || v <= 0 {
			continue
		}
		if idx.ClusterOf(c) == cid {
			sum += v
		}
	}
	return sum
}
