// pareto.go 回测自动增强模块 C：多目标 Pareto 前沿与自动推荐解。
//
// 目标集（均为"越大越好"）：胜率% / 盈亏比 / 夏普 / 卡玛。
// 算法：先按胜率降序排序，逐点做前沿插入式 skyline（新点被任一前沿点支配则丢弃，
// 否则插入并剔除被其支配的旧点），总代价 O(n·log n + n·F)，F=前沿实际规模。
// 朴素双重枚举 O(n²) 在 10 万组合下需 1e10 次比较不可行（性能结论见设计文档 §〇）。
//
// 入口过滤：触发数 < sweepMinTrades(20) 的组合无统计意义，直接剔除。
// 前沿中途膨胀护栏：F 超过 2×MaxFrontPoints 时即时按触发数降序裁剪，
// 保证复杂度有硬上界（末尾再按同一口径截到 MaxFrontPoints）。
// English: 4-objective skyline via sort + insert-frontier (O(n log n + n·F)); low-sample
// combos are dropped and the frontier is capped both mid-scan and at the end by trigger count.
package btreplay

import (
	"sort"

	"quant-trading-v2/internal/config"
)

// dominates 四维支配判定：a 在所有目标上 ≥ b 且至少一个严格 >。
// English: four-objective Pareto dominance.
func dominates(a, b *sweepResult) bool {
	ge := a.WinRate >= b.WinRate && a.ProfitFactor >= b.ProfitFactor &&
		a.Sharpe >= b.Sharpe && a.Calmar >= b.Calmar
	gt := a.WinRate > b.WinRate || a.ProfitFactor > b.ProfitFactor ||
		a.Sharpe > b.Sharpe || a.Calmar > b.Calmar
	return ge && gt
}

// paretoFront 计算非支配解集合（前沿）。maxPoints<=0 时不截断。
// 返回值按胜率降序（与内部排序一致，前端散点图直接可用）。
func paretoFront(results []sweepResult, maxPoints int) []sweepResult {
	// 最小样本过滤：20 笔以下的"100% 胜率"没有统计意义
	cand := make([]sweepResult, 0, len(results))
	for _, r := range results {
		if r.Count >= sweepMinTrades {
			cand = append(cand, r)
		}
	}
	// 胜率降序（并列取盈亏比高者）——决定前沿插入顺序与末尾展示顺序
	sort.Slice(cand, func(i, j int) bool {
		if cand[i].WinRate != cand[j].WinRate {
			return cand[i].WinRate > cand[j].WinRate
		}
		return cand[i].ProfitFactor > cand[j].ProfitFactor
	})
	var front []sweepResult
	for i := range cand {
		x := &cand[i]
		// 被任一前沿点支配 → 丢弃
		bad := false
		for j := range front {
			if dominates(&front[j], x) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		// 插入并剔除被新点支配的旧点
		kept := front[:0]
		for j := range front {
			if !dominates(x, &front[j]) {
				kept = append(kept, front[j])
			}
		}
		front = append(kept, *x)
		// 中途膨胀护栏：最坏情形前沿可到 n，这里给硬上界（保留统计最可信的解）
		if maxPoints > 0 && len(front) > 2*maxPoints {
			front = capFront(front, maxPoints)
		}
	}
	return capFront(front, maxPoints)
}

// capFront 前沿超限时按触发数降序截断（保留统计最可信的解），保持胜率降序展示。
// English: cap the frontier by keeping the most-triggered points, then restore win-rate order.
func capFront(front []sweepResult, maxPoints int) []sweepResult {
	if maxPoints <= 0 || len(front) <= maxPoints {
		return front
	}
	byCount := append([]sweepResult(nil), front...)
	sort.Slice(byCount, func(i, j int) bool {
		if byCount[i].Count != byCount[j].Count {
			return byCount[i].Count > byCount[j].Count
		}
		return byCount[i].WinRate > byCount[j].WinRate
	})
	out := byCount[:maxPoints]
	sort.Slice(out, func(i, j int) bool { return out[i].WinRate > out[j].WinRate })
	return out
}

// recommendedSolution 硬门槛全达标的前沿解中取 Sharpe 最高者（并列取触发数多者）。
// A1 轮落地后评分键切换为样本外 IR（验证窗日频 Sharpe）——届时代码只换键名。
// 返回 nil = 无达标解（前端降级为纯前沿展示）。
// English: among frontier points passing all hard gates, pick the best Sharpe (tie: more triggers);
// nil when no solution qualifies.
func recommendedSolution(front []sweepResult, cfg config.ParetoConfig) *sweepResult {
	var best *sweepResult
	for i := range front {
		s := &front[i]
		if s.WinRate < cfg.MinWinRate || s.ProfitFactor < cfg.MinProfitFactor ||
			s.Sharpe < cfg.MinSharpe || s.Calmar < cfg.MinCalmar {
			continue // 硬门槛
		}
		if best == nil || s.Sharpe > best.Sharpe ||
			(s.Sharpe == best.Sharpe && s.Count > best.Count) {
			best = s
		}
	}
	return best
}

// paretoPointJSON 单解 → 前沿点 JSON（无解返回 nil → JSON null）。
// English: serialize one sweep result as a frontier point (nil-safe).
func paretoPointJSON(r *sweepResult) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"params": map[string]any{"take_profit_pct": r.Trail, "stop_loss_pct": r.StopLossPct,
			"hold_days": r.Hold, "min_score": r.MinScore, "atr_stop_mult": r.AtrStopMult},
		"win_rate":      r.WinRate,
		"profit_factor": r.ProfitFactor,
		"sharpe":        r.Sharpe,
		"calmar":        r.Calmar,
		"trigger_count": r.Count,
		"expectancy":    r.Expectancy,
	}
}

// paretoPointsJSON 前沿点集序列化。
func paretoPointsJSON(front []sweepResult) []map[string]any {
	out := make([]map[string]any, 0, len(front))
	for i := range front {
		out = append(out, paretoPointJSON(&front[i]))
	}
	return out
}
