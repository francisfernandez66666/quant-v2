// metrics.go — 组合级绩效指标（§GAP4.5）：夏普 / 最大回撤 / 年化收益 / 卡玛。
// 此前全系统零风险调整指标，胜率/盈亏比之外无任何波动与回撤刻画；
// 现对逐笔净额收益率序列统一计算，供回放汇总（summary）与扫参排名（sweepResult）消费。
//
// 口径说明：
//   - 输入为按时间排序的逐笔收益率（%，净额口径含成本）与对应入场日（YYYYMMDD）；
//   - Sharpe = 逐笔收益均值/总体标准差 × sqrt(年化笔数)；年化笔数 = N / 跨度年数，
//     样本 <2 笔或标准差为 0 返回 0；
//   - MaxDrawdown：逐笔复利净值曲线的最大峰谷回撤（输出正数 %）；
//   - AnnualReturn：期末复利总收益年化（%；净值非正时返回 -100）；
//   - Calmar = |年化收益 / 最大回撤|（MDD=0 时返回 0）。
package btreplay

import (
	"math"
	"time"
)

// perfMetrics 计算一组交易的夏普/最大回撤/年化/卡玛。
func perfMetrics(pnls []float64, dates []string) (sharpe, maxDD, annual, calmar float64) {
	n := len(pnls)
	if n == 0 {
		return 0, 0, 0, 0
	}

	// 均值 / 总体标准差
	mean := 0.0
	for _, p := range pnls {
		mean += p
	}
	mean /= float64(n)
	if n >= 2 {
		var ss float64
		for _, p := range pnls {
			ss += (p - mean) * (p - mean)
		}
		std := math.Sqrt(ss / float64(n))
		if std > 1e-12 && len(dates) == n {
			if years := spanYears(dates[0], dates[n-1]); years > 0 {
				sharpe = mean / std * math.Sqrt(float64(n)/years)
			}
		}
	}

	// 复利净值曲线 → 最大回撤 + 期末净值
	eq, peak := 1.0, 1.0
	for _, p := range pnls {
		eq *= 1 + p/100
		if eq > peak {
			peak = eq
		}
		if peak > 0 {
			if dd := (peak - eq) / peak * 100; dd > maxDD {
				maxDD = dd
			}
		}
	}

	// 年化收益 + 卡玛
	if n >= 2 && len(dates) == n {
		if years := spanYears(dates[0], dates[n-1]); years > 0 {
			if eq <= 0 {
				annual = -100
			} else {
				annual = (math.Pow(eq, 1/years) - 1) * 100
			}
			if maxDD > 1e-9 {
				calmar = math.Abs(annual / maxDD)
			}
		}
	}
	return sharpe, maxDD, annual, calmar
}

// spanYears 首末日期（YYYYMMDD）跨度折年（下限 1 天防除零）。
func spanYears(first, last string) float64 {
	t0, err0 := time.Parse("20060102", first)
	t1, err1 := time.Parse("20060102", last)
	if err0 != nil || err1 != nil || t1.Before(t0) {
		return 0
	}
	days := t1.Sub(t0).Hours() / 24
	if days < 1 {
		days = 1
	}
	return days / 365.25
}
