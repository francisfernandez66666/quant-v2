// evaluate_series.go — §REPLAY-INPUTS 2026-11-28（档 1）：按 as-of 索引给已装配序列打分。
//
// 为什么需要第二条入口而不是把 `Evaluate` 改成收序列：
//
//	`Evaluate` 的入参是 `*strategy_engine.StockMarketData`（K 线 + 财务 + 当日 D1），
//	每次调用都跑一遍 `seriesFromKLines`。回放的形状是"同一只票、连续 900 天、每天问一次"，
//	于是同一份序列被重算 900 次，而且**情绪/涨停微结构这些列根本进不去**——
//	StockMarketData 里没有它们。这就是 fac_172 那批规则在回放里只会静默 decline 的根因。
//
// 本函数只做一件事：拿整段序列 + 判定日索引，把"只看过去"这件事在入口完成一次
// （`series.UpTo(asofIdx)` 是零拷贝切片视图），然后走**与实盘完全相同的** `scoreRule`。
// 共用同一个打分函数是刻意的：两条路径各写一份评分算术，回测结论与实盘行为迟早分家
// （本仓 §MANUAL-BOOK/§FLOW 那几批都是"第二份公式"漂出来的）。
//
// 原 `Evaluate` 一字未改，实盘路径不受影响。
//
// English: a second entry point that scores a pre-assembled series at an as-of index via the
// exact same scoreRule the live path uses; the live Evaluate() is untouched.
package factor

import (
	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/strategy"
)

// EvaluateSeries 用整段序列在第 asofIdx 个交易日上打分（含当日，视图为 [0, asofIdx]）。
//
// 参数取向（每一句都有后果）：
//   - `series == nil` / 空序列 / asofIdx 越界 ⇒ **钳制到可用范围**而不是 panic：
//     回放在一只票上崩掉会带走整轮（11-19 那次只读腿 NameError 带走整轮的教训）；
//   - 可用长度不足 `strategy.ScoreMinBars` ⇒ 返回 nodata，与 `Evaluate` 的"K线不足30根"同语义，
//     两条入口的门槛必须是同一把尺，否则回放会比实盘更容易出信号；
//   - `code` 只用于写 pending（`GenerateSignal` 靠它还原"这条信号出自哪条规则"），
//     不参与任何计算。
func (f *FactorStrategy) EvaluateSeries(code string, series *factorlib.StockSeries, asofIdx int) (*strategy.Evaluation, error) {
	f.mu.RLock()
	rules := make([]*ActiveRule, len(f.rules))
	copy(rules, f.rules)
	f.mu.RUnlock()

	nodata := func(reason string) (*strategy.Evaluation, error) {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: map[string]float64{}, Reasons: map[string]string{"factor": reason}}, nil
	}
	if len(rules) == 0 {
		return nodata("无启用规则")
	}
	if series == nil || series.Len() == 0 {
		return nodata("序列未装配")
	}
	// 钳制而不是报错：asofIdx 由调用方从日期二分查得，落在边界外（如当日停牌没这根 bar）
	// 是合法形态，此时按"最后一个可用日"算，绝不让整轮回放因一只票的边界而中断。
	idx := asofIdx
	if idx < 0 {
		idx = 0
	}
	if idx > series.Len()-1 {
		idx = series.Len() - 1
	}
	if idx+1 < strategy.ScoreMinBars {
		return nodata("序列不足30根")
	}

	// 截断视图在**入口**做一次，下游 scoreRule/percentile 一行都不用改（方案 §1.2 第 1 条）。
	// 视图零拷贝且与缓存共享底层数组，所以往下传的是"只读视图"，任何一腿都不许写它。
	view := series.UpTo(idx)
	best := &pendingEval{}
	bestScore := -1.0
	for _, r := range rules {
		eval := f.scoreRule(r, view)
		if eval.TotalScore > bestScore {
			bestScore = eval.TotalScore
			best = &pendingEval{rule: r, eval: eval}
		}
	}
	f.mu.Lock()
	if f.pending == nil {
		f.pending = make(map[string]*pendingEval)
	}
	f.pending[code] = best
	f.mu.Unlock()
	return best.eval, nil
}
