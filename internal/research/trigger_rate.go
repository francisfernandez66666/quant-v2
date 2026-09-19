// trigger_rate.go §RFIX-3：候选产出期的「预期触发率」估算——用与实盘因子 runner
// （strategies/factor.scoreRule）同构的分位打分公式，在样本内逐日统计复合分 ≥阈值
// 的股票只数，把「阈值-触发率失校准」在审批前就暴露出来（生产 fac_1 教训：应用时
// 阈值被扫参覆盖到 95，实盘数月零信号，lifecycle 因"无观测=不判定"整晚静默空转）。
//
// 口径说明（近似，仅展示/告警，不参与任何护栏判定）：
//   - 因子分位 = 该股因子值在自身历史（截至当日，不含未来）中的分位——复刻 scoreRule
//     的时间序列分位；财务因子的 finaScore 绝对区间映射与实盘略有差异，接受近似。
//   - 复合分 = (Σ w·dir分位 / Σw + 1) / 2 × 100，与 scoreRule 完全同式
//     （dir<0 的贡献取 1-pct）。
//   - 未注册/缺失因子日不计入（与 runner used 语义一致）。
//
// English: §RFIX-3 expected-trigger-rate estimate mirroring the live scoreRule percentile
// formula, counted per in-sample day; display/alert only, never part of any guard decision.
package research

import "sort"

// TriggerEstimate 预期触发估算结果。
type TriggerEstimate struct {
	PerDay map[float64]float64 // threshold → 日均触发只数
	Days   int                 // 参与统计的交易日数（0=无有效日）
}

// TriggerRateFromPanels 在面板集合上按 [start,end] 日期段估算各阈值的日均触发只数。
// dirs/weights 用发现产出的最终口径（§RFIX-2 拟合后），逐股逐日只用截至当日的历史分位。
// 当日可打分股票不足 minStocks 则该日不计入日均（避免残缺窗噪声）。
// English: estimates per-threshold average daily trigger counts over the panels' [start,end]
// range, using only up-to-date history for each percentile (no lookahead).
func TriggerRateFromPanels(panels []*Panel, factors []string, dirs map[string]int, weights map[string]float64, thresholds []float64, start, end string, minStocks int) TriggerEstimate {
	out := TriggerEstimate{PerDay: map[float64]float64{}}
	if len(panels) == 0 || len(factors) == 0 || len(thresholds) == 0 {
		return out
	}
	dates := unionDates(panels)
	counts := make(map[float64]float64, len(thresholds))
	days := 0
	for _, d := range dates {
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		hit := make(map[float64]int, len(thresholds))
		scored := 0
		for _, p := range panels {
			sc, ok := panelRunnerScore(p, factors, dirs, weights, d)
			if !ok {
				continue
			}
			scored++
			for _, th := range thresholds {
				if sc >= th {
					hit[th]++
				}
			}
		}
		if scored < minStocks || scored == 0 {
			continue // 当日可打分股票太少，不计入日均
		}
		days++
		for _, th := range thresholds {
			counts[th] += float64(hit[th])
		}
	}
	if days == 0 {
		return out
	}
	for _, th := range thresholds {
		out.PerDay[th] = counts[th] / float64(days)
	}
	out.Days = days
	return out
}

// panelRunnerScore 单只股票某日的 runner 口径复合分（0-100）。
// 分位 = 当日值在该股截至当日的历史因子值中的占比（含当日，≥ 计 1，> 计 0）。
// English: one stock's runner-scale composite on a date; each factor percentile is taken
// over that stock's own history up to the day (inclusive — no future data).
func panelRunnerScore(p *Panel, factors []string, dirs map[string]int, weights map[string]float64, d string) (float64, bool) {
	i, ok := p.DateIdx[d]
	if !ok {
		return 0, false
	}
	var total, used float64
	for _, fid := range factors {
		vals, ok := p.Factors[fid]
		if !ok || i >= len(vals) || isNaN(vals[i]) {
			continue
		}
		pct := histPercentile(vals[:i+1], vals[i])
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
		c := pct
		if dir < 0 {
			c = 1 - pct
		}
		total += w * c
		used += w
	}
	if used <= 0 {
		return 0, false
	}
	score := (total/used + 1) / 2 * 100
	return score, true
}

// histPercentile 历史分位：xs 中 ≤v 的占比（v 为 xs 末位，升序平判分位与 runner percentile 同式）。
func histPercentile(xs []float64, v float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	// 上二分：≤v 的个数
	lo, hi := 0, len(sorted)
	for lo < hi {
		mid := (lo + hi) / 2
		if sorted[mid] <= v {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return float64(lo) / float64(len(sorted))
}
