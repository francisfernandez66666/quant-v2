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
	"sort"
	"sync"

	"quant-trading-v2/internal/data"
	factorlib "quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// Cond 形态模板中的单个算子条件：某因子值落在 [Min, Max) 才视为满足。
// （Cond is one operator condition: the factor value must lie in [Min, Max).）
type Cond struct {
	Factor string  `json:"factor"` // 因子名
	Min    float64 `json:"min"`    // 下界（含），值需 >= Min
	Max    float64 `json:"max"`    // 上界（不含），值需 < Max
}

// PatternRule 实盘形态模板规则（由 applied 的 pattern 候选构造）。
// （PatternRule is the live pattern-template rule, built from an applied pattern candidate.）
type PatternRule struct {
	Name         string // 模板名
	Conds        []Cond // 条件集（AND）
	BuyThreshold int    // 可选：额外总分门槛（暂未用，保留扩展）
}

// ActivePattern 战法库中的一条已应用形态战法（带独立 ID/名称/来源候选/运行统计）。
// English: one applied pattern strategy in the library (with its own ID/name/source-candidate/run stats).
type ActivePattern struct {
	ID     string // 规则唯一 ID（"pat_<candID>"）
	Name   string // 显示名
	CandID int64  // 来源候选 ID
	Conds  []Cond // 条件集
	// 效果监测
	SignalCount int     // 触发信号数
	Win         int     // 触发后 5 日收益率为正的次数
	Loss        int     // 触发后 5 日收益率为负的次数
	CumReturn   float64 // 累计前向收益（监测实战效果用）
}

// PatternStrategy 形态战法策略：按一组 ActivePattern 对实盘个股解释执行。
// 支持多形态同时实盘：任一满足全部条件的形态触发，以其 Name 作为信号 strategy 名。
// English: interprets a set of ActivePatterns on live stocks. Multiple patterns run concurrently;
// any whose conditions are all met fires a signal named by that pattern.
type PatternStrategy struct {
	mu    sync.RWMutex
	rules []*ActivePattern
	// 本回合缓存：code → 触发的形态（GenerateSignal 取用）
	pending map[string]*ActivePattern
}

// New 创建形态战法策略实例（默认未启用，需 SetRules 注入有效规则后生效）。
// English: creates a PatternStrategy; disabled until SetRules injects valid rules.
func New() *PatternStrategy {
	return &PatternStrategy{rules: nil, pending: make(map[string]*ActivePattern)}
}

// SetRule 注入单条模板规则（兼容旧版：清空后加一条）。空 Conds 视为禁用。
// English: injects a single template rule (back-compat: clears then adds one). Empty Conds disables.
func (s *PatternStrategy) SetRule(r PatternRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(r.Conds) == 0 {
		s.rules = nil
		return
	}
	name := r.Name
	if name == "" {
		name = "形态战法"
	}
	s.rules = []*ActivePattern{{ID: "pat_0", Name: name, CandID: 0, Conds: r.Conds}}
}

// SetRules 注入多规则（形态战法库）。空列表禁用。
// English: injects multiple rules (pattern library). Empty disables.
func (s *PatternStrategy) SetRules(rules []*ActivePattern) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = nil
	for _, r := range rules {
		if r == nil || len(r.Conds) == 0 {
			continue
		}
		if r.ID == "" {
			r.ID = "pat_auto"
		}
		if r.Name == "" {
			r.Name = "形态战法"
		}
		s.rules = append(s.rules, r)
	}
}

// Enabled 返回是否启用（已注入至少一条有效规则）。
// English: reports whether the strategy is enabled (at least one valid rule injected).
func (s *PatternStrategy) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rules) > 0
}

// Name 返回策略中文名。
func (s *PatternStrategy) Name() string { return "形态战法" }

// Type 返回信号类型 SignalPattern。
func (s *PatternStrategy) Type() strategy.SignalType { return strategy.SignalPattern }

// Stats 返回各规则运行统计快照（效果监测用）。
// English: returns a snapshot of each rule's run stats (for effectiveness monitoring).
func (s *PatternStrategy) Stats() []ActivePattern {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ActivePattern, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, *r)
	}
	return out
}

// RecordForwardReturn 记录某条形态规则一条触发股的 5 日前向收益（效果监测）。
// English: records a pattern rule's 5-day forward return for one triggered stock (monitoring).
func (s *PatternStrategy) RecordForwardReturn(ruleID string, ret float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
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

// Evaluate 对单只股票解释执行全部模板（实现 Strategy 接口）。data 为 *strategy_engine.StockMarketData。
// 任一模板满足全部算子条件即 Pass（full_chain）；取满足条件数最多的模板作为结果。
// English: interprets all templates on one stock (Strategy interface); data is
// *strategy_engine.StockMarketData. Pass when any template's conditions are all met (full_chain); the
// template with the most met conditions wins.
func (s *PatternStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	s.mu.RLock()
	rules := make([]*ActivePattern, len(s.rules))
	copy(rules, s.rules)
	s.mu.RUnlock()
	if len(rules) == 0 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata", Details: map[string]float64{}}, nil
	}
	md, ok := data.(*strategy_engine.StockMarketData)
	if !ok || md == nil || len(md.KLines) < 30 {
		return &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "nodata",
			Details: map[string]float64{}, Reasons: map[string]string{"pattern": "K线不足30根"}}, nil
	}
	series := seriesFromKLines(md.KLines)

	// 取满足条件数最多的模板
	var bestRule *ActivePattern
	best := &strategy.Evaluation{TotalScore: 0, Pass: false, Level: "watch", Details: map[string]float64{}}
	for _, r := range rules {
		eval := evalRule(r.Conds, series)
		if eval.Pass || eval.TotalScore > best.TotalScore {
			if eval.Pass {
				best = eval
				bestRule = r
				break // 有模板全部命中即采用（AND 语义，取第一个全命中）
			}
			best = eval
		}
	}
	s.mu.Lock()
	if bestRule != nil {
		s.pending[code] = bestRule
	} else {
		delete(s.pending, code)
	}
	s.mu.Unlock()
	return best, nil
}

// evalRule 对单条模板解释执行。
// English: interprets a single template.
func evalRule(conds []Cond, series *factorlib.StockSeries) *strategy.Evaluation {
	met := 0
	details := make(map[string]float64)
	for _, c := range conds {
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
			Details: details, Reasons: map[string]string{"pattern": "形态条件未满足"}}
	}
	all := len(conds)
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
	}
}

// GenerateSignal 把评分转为交易信号（实现 Strategy 接口）。信号以触发形态名为 strategy 名。
// English: converts the evaluation into a trade signal (Strategy interface), named by the triggering pattern.
func (s *PatternStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
	}
	s.mu.Lock()
	ap := s.pending[code]
	delete(s.pending, code)
	s.mu.Unlock()
	name := "形态战法"
	if ap != nil {
		name = ap.Name
		ap.SignalCount++
	}
	prio := strategy.P3
	if eval.Confidence >= 0.9 {
		prio = strategy.P2
	}
	candID := int64(0)
	if ap != nil {
		candID = ap.CandID
	}
	meta := map[string]float64{"strategy_id": float64(candID)}
	// §D1-D4 修复：形态战法此前 Meta 只带 strategy_id，前端 D1~D4 全 0。
	// 现把命中/得分最高的 4 个算子条件值写入 d1..d4（前端维度列展示形态条件强度）。
	// English: pattern signals previously carried only strategy_id in Meta, so the frontend D1~D4 all
	// rendered 0. Now the top-4 condition values are written to d1..d4 for the dimension columns.
	for i, fid := range topConditionIDs(eval.Details, 4) {
		if i < 4 {
			meta[[]string{"d1", "d2", "d3", "d4"}[i]] = eval.Details[fid]
		}
	}
	return &strategy.Signal{
		Code: code, Type: strategy.SignalPattern, Action: strategy.ActionBuy,
		Priority: prio, Confidence: eval.Confidence,
		StrategyName: name,
		Meta:         meta,
		Reason:       name + "触发: " + eval.Reasons["pattern"],
	}, nil
}

// topConditionIDs 返回 Details 中值最大的前 n 个条件因子 ID（值降序，跳过零/空）。
// English: returns the top-n condition factor IDs in Details by value (descending, skipping empty/zero).
func topConditionIDs(details map[string]float64, n int) []string {
	// kv 条件因子键值对（用于排序取 Top-N）。
	type kv struct {
		k string
		v float64
	}
	all := make([]kv, 0, len(details))
	for k, v := range details {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, e.k)
	}
	return out
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
