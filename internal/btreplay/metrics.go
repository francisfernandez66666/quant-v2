// metrics.go — 组合级绩效指标（§GAP4.5）：夏普 / 最大回撤 / 年化收益 / 卡玛。
// 此前全系统零风险调整指标，胜率/盈亏比之外无任何波动与回撤刻画；
// 现对逐笔净额收益率序列统一计算，供回放汇总（summary）与扫参排名（sweepResult）消费。
//
// 口径说明（§WS-D D3 修正）：
//   - 输入为按时间排序的逐笔收益率（%，净额口径含成本）与对应入场日（YYYYMMDD）；
//   - Sharpe 改**日频净值口径**：把逐笔收益按入场日聚合（同日多笔复利合成当日收益）得到日收益序列，
//     Sharpe = (mean(R_daily) − rf/252) / std(R_daily) × sqrt(252)，rf 为年化无风险利率（默认 0，
//     A 股参考口径可配国债利率如 0.02）；旧口径为"逐笔收益均值/标准差 × sqrt(年化笔数)"——
//     非标准（笔数频率≠日历频率），已废弃。日样本 <2 笔或标准差为 0 返回 0；
//   - MaxDrawdown：逐笔复利净值曲线的最大峰谷回撤（输出正数 %）；
//   - AnnualReturn：期末复利总收益年化（%；净值非正时返回 -100）；
//   - Calmar = |年化收益 / 最大回撤|（MDD=0 时返回 0）。
package btreplay

import (
	"math"
	"sort"
	"time"
)

// perfMetrics 计算一组交易的夏普/最大回撤/年化/卡玛（rf=0 的便捷封装，语义同 perfMetricsRF）。
// English: perfMetrics wraps perfMetricsRF with a zero risk-free rate.
func perfMetrics(pnls []float64, dates []string) (sharpe, maxDD, annual, calmar float64) {
	return perfMetricsRF(pnls, dates, 0)
}

// perfMetricsRF 计算一组交易的夏普/最大回撤/年化/卡玛（§WS-D D3 日频口径）。
//  1. 按入场日聚合逐笔收益（同日多笔复利合成当日收益）→ 日收益序列 R_daily；
//  2. Sharpe = (mean(R_daily) − rf/252) / std(R_daily) × sqrt(252)；rf 为年化无风险利率；
//  3. MaxDD/AnnualReturn/Calmar 沿用逐笔复利净值曲线口径（不受 Sharpe 口径变更影响）。
//
// English: §WS-D D3 — daily-frequency Sharpe: per-trade returns are compounded into a daily series by
// entry date, then Sharpe = (mean(R_daily) − rf/252) / std(R_daily) × sqrt(252) with rf as the annual
// risk-free rate. MaxDD/annual/Calmar keep the compounded per-trade equity-curve semantics.
func perfMetricsRF(pnls []float64, dates []string, rf float64) (sharpe, maxDD, annual, calmar float64) {
	n := len(pnls)
	if n == 0 || len(dates) != n {
		return 0, 0, 0, 0
	}

	// 按日聚合：同日多笔复利合成当日收益；dayOf 存小数（0.2=20%），逐笔复利 (1+dayOf)*(1+p/100)−1。
	dayOf := map[string]float64{}
	var dayIdx []string
	for i, p := range pnls {
		d := dates[i]
		if _, ok := dayOf[d]; !ok {
			dayIdx = append(dayIdx, d)
		}
		dayOf[d] = (1+dayOf[d])*(1+p/100) - 1
	}
	sort.Strings(dayIdx)
	daily := make([]float64, len(dayIdx))
	for i, d := range dayIdx {
		daily[i] = dayOf[d]
	}

	// 日频 Sharpe：(mean(R_daily) − rf/252) / std(R_daily) × sqrt(252)
	if len(daily) >= 2 {
		mean := 0.0
		for _, r := range daily {
			mean += r
		}
		mean /= float64(len(daily))
		var ss float64
		for _, r := range daily {
			ss += (r - mean) * (r - mean)
		}
		std := math.Sqrt(ss / float64(len(daily)))
		if std > 1e-12 {
			sharpe = (mean - rf/252) / std * math.Sqrt(252)
		}
	}

	// 复利净值曲线 → 最大回撤 + 期末净值（沿用逐笔口径）
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
	if n >= 2 {
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
