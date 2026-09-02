// KDJ 随机指标（9,3,3，通达信口径）。
// English: KDJ stochastic indicator (9,3,3, Tongdaxin convention).
package indicator

// KDJPoint 单根K线的 KDJ 三值。
// English: KDJPoint holds the three KDJ values for a single bar.
type KDJPoint struct {
	RSV float64 // 未成熟随机值
	// English: RSV is the immature stochastic value.
	K float64 // K 值（慢速随机，由 RSV 平滑递推）
	D float64 // D 值（慢速随机，由 K 平滑递推）
	J float64 // J 值 = 3K − 2D（超买超卖领先指标）
}

// KDJ 计算 KDJ 序列（默认 9,3,3）。
// RSV 以最近 n 根高低价区间衡量收盘位置；区间为 0（最高==最低）时 RSV=50（中性）。
// K/D 初始值 50，按 1/3 平滑递推。序列从头即有值。
// English: KDJ computes the KDJ series (default 9,3,3). RSV measures the close position within the recent n-bar high/low range; RSV=50 (neutral) when the range is zero (high==low). K/D start at 50 and are smoothed recursively by a factor of 1/3. The series has values from the first bar.
// （KDJ computes the KDJ series (default 9,3,3). RSV=50 when the n-bar range is zero.
// K/D start at 50 and smooth with a 1/3 factor.）
func KDJ(closes, highs, lows []float64, n, m1, m2 int) []KDJPoint {
	out := make([]KDJPoint, len(closes))
	if n <= 0 || m1 <= 0 || m2 <= 0 || len(closes) == 0 {
		return out // 非法参数直接返回空结果
	}
	k, d := 50.0, 50.0 // K/D 初值取 50（中位起点）
	for i := range closes {
		lo, hi := lows[i], highs[i]
		from := 0
		if i-n+1 > from {
			from = i - n + 1 // 滑动窗口起点（不足 n 根时从 0 开始）
		}
		// 求窗口内最低/最高价
		for j := from; j <= i; j++ {
			if lows[j] < lo {
				lo = lows[j]
			}
			if highs[j] > hi {
				hi = highs[j]
			}
		}
		rsv := 50.0
		if hi != lo {
			rsv = (closes[i] - lo) / (hi - lo) * 100 // 未成熟随机值 RSV
		}
		k = k*(1-1/float64(m1)) + rsv/float64(m1) // K 平滑（1/m1 权重）
		d = d*(1-1/float64(m2)) + k/float64(m2)   // D 平滑（1/m2 权重）
		out[i] = KDJPoint{RSV: rsv, K: k, D: d, J: 3*k - 2*d}
	}
	return out
}

// KDJDefault 以默认参数（9,3,3）计算 KDJ。
// English: KDJDefault computes KDJ with the default 9,3,3 parameters.
// （KDJDefault computes KDJ with the default 9,3,3 parameters.）
func KDJDefault(closes, highs, lows []float64) []KDJPoint {
	return KDJ(closes, highs, lows, 9, 3, 3)
}
