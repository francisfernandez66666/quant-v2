// Package factor 实现"因子战法"（E6）：把自动研究（discover-factors）发现的因子组合
// 在实盘打分池消费，产出交易信号。与四大形态战法并列，同属 8a（利多）扫描链路。
//
// 数据口径：实盘数据（StockMarketData）只有日K（open/high/low/close/vol/amount），
// 没有研究库的财务/估值字段。因此本战法只对"纯价量因子"（动量/波动率/流动性/技术指标）
// 打分，估值/成长/质量/规模类因子因实盘缺数据记 0 分。发现候选若含财务因子，实盘侧自动跳过。
//
// 打分：对每只股票，各价量因子取其自身历史序列的当前分位（0~1，时序相对强弱），
// 复合分 = Σ w·dir·分位（w 权重、dir 方向 +1/-1），再归一化到 0~100。
// 这是对横截面 IC 的单股时序近似，保证无需全池截面即可独立评分。
//
// （English: implements the "factor strategy" (E6) — consumes factor combos discovered by
// discover-factors on the live scoring pool, producing trade signals alongside the four shape
// strategies under the 8a (bullish) scan. Live data only has daily bars, so only price-volume
// factors (momentum/volatility/liquidity/technical) are scored; valuation/growth/quality/size
// factors are skipped as 0 due to missing data. Each factor is scored by its current time-series
// percentile (0~1), and the composite = Σ w·dir·percentile, normalized to 0~100 — a per-stock
// time-series proxy for cross-sectional IC, scorable independently without a full cross-section.）
package factor

import (
	"math"
	"sort"
	"strconv"
	"sync"

	"quant-trading-v2/internal/data"
	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// Rule 因子战法的评分规则（由 applied 的 discover-factors 候选构造）。
// （Rule is the scoring rule for the factor strategy, built from an applied discover-factors candidate.）
type Rule struct {
	Factors    []string           // 因子 ID 列表
	Weights    map[string]float64 // factorID → 权重
	Directions map[string]int     // factorID → 方向（+1 看多 / -1 看空）
	// BuyThreshold 触发买入的复合分阈值（0~100，默认 70）。
	// English: composite threshold to fire a buy (0~100, default 70).
	BuyThreshold float64
}

// ActiveRule 一条已应用的因子战法规则（战法库中的一条），带独立 ID/名称与运行统计（效果监测）。
// English: one applied factor-strategy rule (an entry in the strategy library), with its own ID, name
// and running stats for live effectiveness monitoring.
type ActiveRule struct {
	ID     string // 规则唯一 ID（如 "fac_<候选ID>"）
	Name   string // 规则显示名（如 "因子战法#1"）
	CandID int64  // 来源候选 ID（0=手动/兼容旧文件）
	Rule
	// 效果监测：本规则在实盘触发的信号数、命中/亏损计数与累计前向收益。
	// English: live-effect monitoring — signals fired, win/loss counts, cumulative forward return.
	SignalCount int
	// Win 表示"触发后 5 日（Horizon）收益为正"的次数；Loss 为负。
	Win  int
	Loss int
	// CumReturn 累计前向收益（触发股 5 日收益累加，监测战法实际效果用）。
	CumReturn float64
}

// FactorStrategy 因子战法策略：按一组 ActiveRule 对实盘个股打分并出信号。
// 支持多个规则同时实盘：每只股票对各规则独立评分，得分最高且过阈值的规则产出一条信号，
// 并以该规则的 Name 作为信号 strategy 名（去重键互不冲突）。各规则触发情况计入运行统计供效果监测。
// English: scores live stocks per a set of ActiveRules and emits signals. Multiple rules run
// simultaneously: each rule scores a stock independently; the highest-scoring passing rule emits a
// signal named by that rule (distinct dedup keys). Per-rule triggers are counted for effectiveness monitoring.
type FactorStrategy struct {
	mu    sync.RWMutex
	rules []*ActiveRule
	// 本回合缓存：code → (winning rule id, winning eval)。GenerateSignal 取用。
	pending map[string]*pendingEval
}

// pendingEval 一次 Evaluate 的最高分规则结果，供 GenerateSignal 使用。
// English: the highest-scoring rule result of one Evaluate, consumed by GenerateSignal.
type pendingEval struct {
	rule *ActiveRule
	eval *strategy.Evaluation
}

// New 创建因子战法策略实例（默认未启用，需 SetRules 注入有效规则后生效）。
// English: creates a FactorStrategy; disabled until SetRules injects valid rules.
func New() *FactorStrategy {
	return &FactorStrategy{rules: nil, pending: make(map[string]*pendingEval)}
}

// SetRule 注入单条评分规则（兼容旧版：清空后加一条）。空 Factors 视为禁用。
// English: injects a single scoring rule (back-compat: clears then adds one). Empty Factors disables.
func (f *FactorStrategy) SetRule(r Rule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(r.Factors) == 0 {
		f.rules = nil
		return
	}
	f.rules = []*ActiveRule{{ID: "fac_0", Name: "因子战法", CandID: 0, Rule: r}}
}

// SetRules 注入多规则（战法库）。仅保留 enabled 的规则；空列表禁用。
// English: injects multiple rules (strategy library). Only enabled rules are kept; empty disables.
func (f *FactorStrategy) SetRules(rules []*ActiveRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = nil
	for _, r := range rules {
		if r == nil || len(r.Factors) == 0 {
			continue
		}
		if r.BuyThreshold <= 0 {
			r.BuyThreshold = 70
		}
		if r.ID == "" {
			r.ID = "fac_auto"
		}
		if r.Name == "" {
			r.Name = "因子战法"
		}
		f.rules = append(f.rules, r)
	}
}

// Enabled 返回是否启用（已注入至少一条有效规则）。
// English: reports whether the strategy is enabled (at least one valid rule was injected).
func (f *FactorStrategy) Enabled() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.rules) > 0
}

// RuleCount 返回已注入的规则数量。
// English: returns the number of injected rules.
func (f *FactorStrategy) RuleCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.rules)
}

// Name 返回策略中文名。
func (f *FactorStrategy) Name() string { return "因子战法" }

// Type 返回信号类型 SignalFactor。
func (f *FactorStrategy) Type() strategy.SignalType { return strategy.SignalFactor }

// Stats 返回各规则的运行统计快照（效果监测用）。
// English: returns a snapshot of each rule's running stats (for effectiveness monitoring).
func (f *FactorStrategy) Stats() []ActiveRule {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]ActiveRule, 0, len(f.rules))
	for _, r := range f.rules {
		out = append(out, *r)
	}
	return out
}

// Evaluate 对单只股票评分（实现 Strategy 接口）。data 为 *strategy_engine.StockMarketData。
// 对每条启用规则独立评分，取最高分（且过阈值的）规则作为本次结果；未过阈值取最高原始分。
// 仅用日K价量因子，输出 Evaluation{TotalScore, Pass, Level, Confidence}。
// English: scores one stock (Strategy interface); data is *strategy_engine.StockMarketData.
// Evaluates each enabled rule independently and returns the highest-scoring passing rule (or highest
// raw score if none pass). Only price-volume factors are used from daily bars.
func (f *FactorStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	f.mu.RLock()
	rules := make([]*ActiveRule, len(f.rules))
	copy(rules, f.rules)
	f.mu.RUnlock()
	if len(rules) == 0 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata", Details: map[string]float64{}}, nil
	}
	md, ok := data.(*strategy_engine.StockMarketData)
	if !ok || md == nil || len(md.KLines) < 30 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: map[string]float64{}, Reasons: map[string]string{"factor": "K线不足30根"}}, nil
	}
	series := seriesFromKLines(md.KLines, md.Fina)

	best := &pendingEval{}
	var bestScore = -1.0
	for _, r := range rules {
		eval := f.scoreRule(r, series)
		if eval.TotalScore > bestScore {
			bestScore = eval.TotalScore
			best = &pendingEval{rule: r, eval: eval}
		}
	}
	f.mu.Lock()
	f.pending[code] = best
	f.mu.Unlock()
	return best.eval, nil
}

// scoreRule 对单条规则打分。
// English: scores a stock against a single rule.
func (f *FactorStrategy) scoreRule(r *ActiveRule, series *factorlib.StockSeries) *strategy.Evaluation {
	var total, used float64
	details := make(map[string]float64)
	for _, fid := range r.Factors {
		df, ok := factorlib.Get(fid)
		if !ok {
			continue
		}
		vals := df.Compute(series)
		if len(vals) == 0 {
			continue
		}
		// 财务类因子（质量/成长）用最新报告期数值直接归一化打分（横截面不可得，用绝对区间近似）；
		// 价量因子用时间序列分位。English: financial factors (quality/growth) are scored by the latest
		// value normalized to an absolute range (no live cross-section available); price-volume factors use
		// their time-series percentile.
		var pct float64
		if isFinancialFactor(df) {
			last := vals[len(vals)-1]
			if math.IsNaN(last) {
				continue
			}
			pct = finaScore(fid, last)
		} else {
			pct = percentile(vals)
		}
		if math.IsNaN(pct) {
			continue
		}
		dir := 1.0
		if d, ok := r.Directions[fid]; ok {
			dir = float64(d)
		}
		w := 1.0
		if wv, ok := r.Weights[fid]; ok {
			w = wv
		}
		contrib := w * dir * pct
		if dir < 0 {
			contrib = w * (1 - pct)
		}
		total += contrib
		used += w
		details[fid] = pct
	}
	if used <= 0 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: details, Reasons: map[string]string{"factor": "无可用因子"}}
	}
	score := (total/used + 1) / 2 * 100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	threshold := r.BuyThreshold
	if threshold <= 0 {
		threshold = 70
	}
	pass := score >= threshold
	level := "watch"
	if pass {
		level = "full_chain"
	}
	return &strategy.Evaluation{
		TotalScore: score, Pass: pass, Level: level,
		Confidence: score / 100,
		Details:    details,
		Reasons:    map[string]string{"factor": "因子复合分" + strconv.FormatFloat(score, 'f', 2, 64)},
	}
}

// GenerateSignal 把最高分规则评分转为交易信号（实现 Strategy 接口）。
// 信号以该规则的 Name 作为 StrategyName，使消息中心去重键按规则独立；Reason 附规则名。
// English: converts the highest-scoring rule's evaluation into a trade signal. The signal is named by
// that rule (StrategyName) so message-center dedup keys stay distinct per rule; Reason includes the rule name.
func (f *FactorStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
	}
	f.mu.Lock()
	pe := f.pending[code]
	delete(f.pending, code)
	f.mu.Unlock()
	ruleName := "因子战法"
	candID := int64(0)
	if pe != nil && pe.rule != nil {
		ruleName = pe.rule.Name
		candID = pe.rule.CandID
		// 效果监测：该规则触发信号数 +1
		pe.rule.SignalCount++
	}
	prio := strategy.P3
	if eval.Confidence >= 0.8 {
		prio = strategy.P1
	} else if eval.Confidence >= 0.6 {
		prio = strategy.P2
	}
	return &strategy.Signal{
		Code: code, Type: strategy.SignalFactor, Action: strategy.ActionBuy,
		Priority: prio, Confidence: eval.Confidence,
		StrategyName: ruleName,
		Meta:         map[string]float64{"strategy_id": float64(candID)},
		Reason:       ruleName + "触发: " + strconv.FormatFloat(eval.TotalScore, 'f', 2, 64),
	}, nil
}

// RecordForwardReturn 记录某规则一条触发股的 5 日（Horizon）前向收益，用于效果监测。
// 在监控层（引擎/战法库）调用；正收益计入 Win，负计入 Loss，并累加 CumReturn。
// English: records a rule's 5-day (Horizon) forward return for one triggered stock, for effect
// monitoring; positive returns count toward Win, negative toward Loss, and accumulate into CumReturn.
func (f *FactorStrategy) RecordForwardReturn(ruleID string, ret float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rules {
		if r.ID == ruleID {
			if ret > 0 {
				r.Win++
			} else if ret < 0 {
				r.Loss++
			}
			r.CumReturn += ret
			return
		}
	}
}

// ResetStats 重置全部规则的运行统计。
// English: resets all rules' running stats.
func (f *FactorStrategy) ResetStats() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rules {
		r.SignalCount, r.Win, r.Loss, r.CumReturn = 0, 0, 0, 0
	}
}

// seriesFromKLines 从日K + 财务指标构造 factorlib.StockSeries。
// 价量字段来自日K；财务字段（ROE/净利同比/毛利/净利/负债率/EPS）来自 md.Fina（最新报告期，缺失 0）。
// 使财务类因子（ROE 质量 / YoyNetProfit 成长 / SUE 等）在实盘打分时也有值。
// English: builds a factorlib.StockSeries from daily bars + financials. Price/volume fields come from
// daily bars; financial fields (ROE/YoyNetProfit/margins/debt/EPS) come from md.Fina (latest report, 0
// when missing), so financial factors (ROE quality / YoyNetProfit growth / SUE etc.) score live too.
func seriesFromKLines(kl []data.KLine, fina *strategy_engine.FinancialData) *factorlib.StockSeries {
	n := len(kl)
	s := &factorlib.StockSeries{Dates: make([]string, n)}
	s.Open, s.High, s.Low, s.CloseHfq = make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	s.CloseRaw = make([]float64, n)
	s.Vol, s.Amount = make([]float64, n), make([]float64, n)
	// 财务字段为最新报告期常量（时间序列上逐日同值；缺失记 NaN 让因子返回 NaN 不参与复合分）
	var roe, yoyNP, gp, np, debt, eps []float64
	if fina != nil {
		roe = constant(n, fina.Roe)
		yoyNP = constant(n, fina.YoyNetProfit)
		gp = constant(n, fina.GrossMargin)
		np = constant(n, fina.NetMargin)
		debt = constant(n, fina.DebtToAssets)
		eps = constant(n, fina.Eps)
	}
	s.Roe = roe
	s.YoyNetProfit = yoyNP
	s.SingleQuarterNIYoy = yoyNP // 实盘无单季同比，用净利同比近似（SUE 降级）
	s.GrossMargin = gp
	s.NetMargin = np
	s.DebtToAssets = debt
	s.Eps = eps
	for i, k := range kl {
		s.Dates[i] = k.Date.Format("20060102")
		s.Open[i], s.High[i], s.Low[i], s.CloseHfq[i] = k.Open, k.High, k.Low, k.Close
		s.CloseRaw[i] = k.Close
		s.Vol[i], s.Amount[i] = k.Volume, k.Amount
	}
	return s
}

// constant 返回长度为 n、值均为 v 的切片（财务字段时间序列常量）。
// English: returns a length-n slice filled with v (a constant financial series).
func constant(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// percentile 返回序列末值在整体序列中的分位（0~1）；序列过短/无有效值返回 NaN。
// 用于把因子当前值映射为"相对自身历史"的相对强弱。
// English: returns the percentile rank (0~1) of the last value within the series; NaN when too short
// or no valid values. Maps a factor's current value to its relative strength vs its own history.
func percentile(vals []float64) float64 {
	if len(vals) < 5 {
		return math.NaN()
	}
	last := vals[len(vals)-1]
	if math.IsNaN(last) {
		return math.NaN()
	}
	var valid []float64
	for _, v := range vals[:len(vals)-1] {
		if !math.IsNaN(v) {
			valid = append(valid, v)
		}
	}
	if len(valid) == 0 {
		return math.NaN()
	}
	sort.Float64s(valid)
	// 末值在历史中的秩（比它小的比例）
	below := 0
	for _, v := range valid {
		if v < last {
			below++
		}
	}
	return float64(below) / float64(len(valid))
}

// isFinancialFactor 判断某因子是否为财务类（质量/成长/估值），这类因子在实盘用最新值直接打分。
// English: reports whether a factor is financial (quality/growth/value), scored live by its latest value.
func isFinancialFactor(df factorlib.Def) bool {
	switch df.Cat {
	case factorlib.CatQuality, factorlib.CatGrowth, factorlib.CatValue:
		return true
	}
	return false
}

// finaScore 把财务类因子最新值归一化到 0~1（绝对区间近似，缺横截面排名）。
// 越高分代表该指标越"强"（ROE/净利同比/毛利/净利越高越好；负债率越低越好）。
// English: normalizes a financial factor's latest value to 0~1 (absolute-range approximation, no live
// cross-section). Higher = stronger (high ROE/YoyNetProfit/margins good; low debt good).
func finaScore(fid string, v float64) float64 {
	if math.IsNaN(v) {
		return math.NaN()
	}
	switch fid {
	case "ROE":
		return clamp01(v / 20) // ROE≥20% 满分
	case "YoyNetProfit", "SUE":
		return clamp01(v / 30) // 净利同比≥30% 满分
	case "GrossMargin":
		return clamp01(v / 40)
	case "NetMargin":
		return clamp01(v / 20)
	case "DebtToAssets":
		return clamp01((100 - v) / 100) // 负债率越低越好
	case "EP_ttm", "BP", "SP_ttm", "CFP_ttm":
		return clamp01(v) // 估值倒数，越大越便宜（0 处截断）
	case "DP":
		return clamp01(v / 5) // 股息率
	default:
		return clamp01(v)
	}
}

// clamp01 把值截断到 [0,1]。
// English: clamps a value into [0,1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
