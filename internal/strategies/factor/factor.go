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

// FactorStrategy 因子战法策略：按 Rule 对实盘个股打分并出信号。
// （FactorStrategy scores live stocks per Rule and emits signals.）
type FactorStrategy struct {
	rule    Rule
	enabled bool
}

// New 创建因子战法策略实例（默认未启用，需 SetRule 注入有效规则后生效）。
// English: creates a FactorStrategy; disabled until SetRule injects a valid rule.
func New() *FactorStrategy {
	return &FactorStrategy{rule: Rule{BuyThreshold: 70}, enabled: false}
}

// SetRule 注入评分规则（来自审批通过的因子候选）。空 Factors 视为禁用。
// English: injects the scoring rule (from an approved factor candidate). Empty Factors disables.
func (f *FactorStrategy) SetRule(r Rule) {
	f.rule = r
	f.enabled = len(r.Factors) > 0
}

// Enabled 返回是否启用（已注入有效规则）。
// English: reports whether the strategy is enabled (a valid rule was injected).
func (f *FactorStrategy) Enabled() bool { return f.enabled }

// Name 返回策略中文名。
func (f *FactorStrategy) Name() string { return "因子战法" }

// Type 返回信号类型 SignalFactor。
func (f *FactorStrategy) Type() strategy.SignalType { return strategy.SignalFactor }

// Evaluate 对单只股票评分（实现 Strategy 接口）。data 为 *strategy_engine.StockMarketData。
// 仅用日K价量因子，输出 Evaluation{TotalScore, Pass, Level, Confidence}。
// English: scores one stock (Strategy interface); data is *strategy_engine.StockMarketData.
// Only price-volume factors are used from daily bars.
func (f *FactorStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	if !f.enabled {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata", Details: map[string]float64{}}, nil
	}
	md, ok := data.(*strategy_engine.StockMarketData)
	if !ok || md == nil || len(md.KLines) < 30 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: map[string]float64{}, Reasons: map[string]string{"factor": "K线不足30根"}}, nil
	}
	series := seriesFromKLines(md.KLines)

	// 计算各因子的当前时序分位（0~1）并复合打分
	var total, used float64
	details := make(map[string]float64)
	for _, fid := range f.rule.Factors {
		df, ok := factorlib.Get(fid)
		if !ok {
			continue
		}
		vals := df.Compute(series)
		if len(vals) == 0 {
			continue
		}
		pct := percentile(vals) // 当前值在历史序列中的分位 0~1
		if math.IsNaN(pct) {
			continue
		}
		dir := 1.0
		if d, ok := f.rule.Directions[fid]; ok {
			dir = float64(d)
		}
		w := 1.0
		if wv, ok := f.rule.Weights[fid]; ok {
			w = wv
		}
		// 看多因子：分位越高贡献越大；看空因子：分位越低（1-pct）贡献越大
		// English: long factor contributes more at high percentile; short factor more at low (1-pct).
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
			Details: details, Reasons: map[string]string{"factor": "无可用价量因子"}}, nil
	}
	// 复合分归一化到 0~100
	score := (total/used + 1) / 2 * 100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	threshold := f.rule.BuyThreshold
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
	}, nil
}

// GenerateSignal 把评分转为交易信号（实现 Strategy 接口）。
// English: converts the evaluation into a trade signal (Strategy interface).
func (f *FactorStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
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
		Reason: "因子战法触发: " + strconv.FormatFloat(eval.TotalScore, 'f', 2, 64),
	}, nil
}

// seriesFromKLines 从日K构造 factorlib.StockSeries（仅价量字段，其余 NaN）。
// English: builds a factorlib.StockSeries from daily bars (price/volume only, rest NaN).
func seriesFromKLines(kl []data.KLine) *factorlib.StockSeries {
	n := len(kl)
	s := &factorlib.StockSeries{Dates: make([]string, n)}
	s.Open, s.High, s.Low, s.CloseHfq = make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	s.CloseRaw = make([]float64, n)
	s.Vol, s.Amount = make([]float64, n), make([]float64, n)
	for i, k := range kl {
		s.Dates[i] = k.Date.Format("20060102")
		s.Open[i], s.High[i], s.Low[i], s.CloseHfq[i] = k.Open, k.High, k.Low, k.Close
		s.CloseRaw[i] = k.Close
		s.Vol[i], s.Amount[i] = k.Volume, k.Amount
	}
	return s
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
