// Package pattern 实现"形态战法"（F3）：把自动研究（discover-patterns）发现的形态模板
// 在实盘打分池解释执行，产出交易信号。与四大手写形态战法并列，同属 8a（利多）扫描链路，
// 无需编译 Go 代码即可上线新形态。
//
// 数据口径：实盘数据（StockMarketData）只有日K，因此只支持由 F1 纯价量形态算子
// （Drawdown20/VolShrink/BullAlign/VolSurge5/Brk20 等）构成的模板条件。
//
// English: implements the "pattern strategy" (F3) — interprets shape templates discovered by
// discover-patterns on the live scoring pool, producing trade signals alongside the four hand-written
// shape strategies under the 8a (bullish) scan, without recompiling Go code. Live data only has daily
// bars, so only templates built from the F1 price-volume morphology operators are supported.
package pattern

import (
	"math"

	"quant-trading-v2/internal/data"
	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// Cond 形态模板中的单个算子条件：某因子值落在 [Min, Max) 才视为满足。
// （Cond is one operator condition: the factor value must lie in [Min, Max).）
type Cond struct {
	Factor string  `json:"factor"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// PatternRule 实盘形态模板规则（由 applied 的 pattern 候选构造）。
// （PatternRule is the live pattern-template rule, built from an applied pattern candidate.）
type PatternRule struct {
	Name         string // 模板名
	Conds        []Cond // 条件集（AND）
	BuyThreshold int    // 可选：额外总分门槛（暂未用，保留扩展）
}

// PatternStrategy 形态战法策略：按 PatternRule 对实盘个股解释执行。
// （PatternStrategy interprets a PatternRule on live stocks.）
type PatternStrategy struct {
	rule    PatternRule
	enabled bool
}

// New 创建形态战法策略实例（默认未启用，需 SetRule 注入有效规则后生效）。
// English: creates a PatternStrategy; disabled until SetRule injects a valid rule.
func New() *PatternStrategy {
	return &PatternStrategy{rule: PatternRule{}, enabled: false}
}

// SetRule 注入模板规则（来自审批通过的 pattern 候选）。空 Conds 视为禁用。
// English: injects the template rule (from an approved pattern candidate). Empty Conds disables.
func (s *PatternStrategy) SetRule(r PatternRule) {
	s.rule = r
	s.enabled = len(r.Conds) > 0
}

// Enabled 返回是否启用。
func (s *PatternStrategy) Enabled() bool { return s.enabled }

// Name 返回策略中文名。
func (s *PatternStrategy) Name() string {
	if s.rule.Name != "" {
		return s.rule.Name
	}
	return "形态战法"
}

// Type 返回信号类型 SignalPattern。
func (s *PatternStrategy) Type() strategy.SignalType { return strategy.SignalPattern }

// Evaluate 对单只股票解释执行模板（实现 Strategy 接口）。data 为 *strategy_engine.StockMarketData。
// 满足全部算子条件 → Pass（full_chain），否则 watch。
// English: interprets the template on one stock (Strategy interface); data is
// *strategy_engine.StockMarketData. Pass when all operator conditions are met (full_chain), else watch.
func (s *PatternStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	if !s.enabled {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata", Details: map[string]float64{}}, nil
	}
	md, ok := data.(*strategy_engine.StockMarketData)
	if !ok || md == nil || len(md.KLines) < 30 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: map[string]float64{}, Reasons: map[string]string{"pattern": "K线不足30根"}}, nil
	}
	series := seriesFromKLines(md.KLines)

	// 计算各形态算子当前值并判定是否满足全部条件
	met := 0
	details := make(map[string]float64)
	for _, c := range s.rule.Conds {
		df, ok := factorlib.Get(c.Factor)
		if !ok {
			continue
		}
		vals := df.Compute(series)
		if len(vals) == 0 {
			continue
		}
		v := vals[len(vals)-1]
		details[c.Factor] = v
		if math.IsNaN(v) {
			continue
		}
		if v >= c.Min && v < c.Max {
			met++
		}
	}
	if met == 0 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "watch",
			Details: details, Reasons: map[string]string{"pattern": "形态条件未满足"}}, nil
	}
	// 全部条件满足 → 触发
	all := len(s.rule.Conds)
	score := float64(met) / float64(all) * 100
	pass := met == all
	level := "watch"
	if pass {
		level = "full_chain"
	}
	return &strategy.Evaluation{
		TotalScore: score, Pass: pass, Level: level,
		Confidence: score / 100,
		Details:    details,
		Reasons:    map[string]string{"pattern": "形态触发 " + itoa(met) + "/" + itoa(all)},
	}, nil
}

// GenerateSignal 把评分转为交易信号（实现 Strategy 接口）。
// English: converts the evaluation into a trade signal (Strategy interface).
func (s *PatternStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
	}
	prio := strategy.P3
	if eval.Confidence >= 0.9 {
		prio = strategy.P2
	}
	return &strategy.Signal{
		Code: code, Type: strategy.SignalPattern, Action: strategy.ActionBuy,
		Priority: prio, Confidence: eval.Confidence,
		Reason: "形态战法触发: " + eval.Reasons["pattern"],
	}, nil
}

// seriesFromKLines 从日K构造 factorlib.StockSeries（仅价量字段）。
// English: builds a factorlib.StockSeries from daily bars (price/volume only).
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

// itoa 整数转字符串（避免引入 strconv 依赖）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
