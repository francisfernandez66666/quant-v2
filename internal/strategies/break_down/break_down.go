// Package break_down 实现「放量破位」做空战法（8b 反向信号之一）。
//
// 核心思想：趋势终结的标准动作是"放量跌破关键支撑"——20日线或近20日最低被有效击穿
// 且量能确认（缩量破位多为洗盘，减半计分），日内跌幅/高开低走提供当日确认。
// 三因子评分（加权总分 0~100）：
//
//   - 破位深度（满分 30）：收盘跌破20日线（深度分档）或跌破近20日最低，双破位满分；
//   - 量能确认（满分 40）：当日量 ≥5日均量×1.5 满分；缩量破位减半（洗盘嫌疑）；
//   - 日内弱势（满分 30）：现价 ≤ 昨收×0.98；高开低走（open>昨收 且 close<open×0.99）加分。
//
// 防洗盘：5日线仍上穿10日线且破位深度 <1% 时总分打 7 折（趋势未死缓判）。
// 信号级别：总分 ≥60（可配）→ full_chain；50~60 → watch。硬闸：日K ≥25 根。
//
// （Package break_down implements the "volume breakdown" bear tactic: a decisive close below
// the 20-day MA / 20-day low confirmed by heavy volume marks trend termination; a shallow break
// while MA5 still crosses above MA10 is discounted as a possible shakeout.）
package break_down

import (
	"fmt"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategies/shortbase"
	"quant-trading-v2/internal/strategy"
)

// Strategy 放量破位战法实例。
// （Strategy is the break-down tactic.）
type Strategy struct {
	cfg *config.Manager // 配置管理器（可为 nil）（Config manager; nil = defaults）
}

// New 创建放量破位战法实例。
// 参数：
//   - cfg: 配置管理器（账号级参数来源，可为 nil）
//
// （New creates the tactic.）
func New(cfg *config.Manager) *Strategy { return &Strategy{cfg: cfg} }

// Name 返回策略标识名称"break_down"。
// （Name returns the strategy identifier.）
func (s *Strategy) Name() string { return "break_down" }

// Type 返回信号类型标识。
// （Type returns the signal type.）
func (s *Strategy) Type() strategy.SignalType { return strategy.SignalBreakDown }

// scoreThreshold 返回通过门槛（默认 60，rules.strategy.short.break_down_min 可覆盖）。
// （scoreThreshold resolves the pass gate.）
func (s *Strategy) scoreThreshold() float64 {
	if s.cfg != nil {
		sc := s.cfg.GetStrategyConfig()
		if sc != nil && sc.Short.BreakDownMin > 0 {
			return sc.Short.BreakDownMin
		}
	}
	return 60
}

// Evaluate 执行放量破位三因子评分。
// 参数：
//   - code: 股票代码
//   - data: 必须为 *shortbase.Data
//
// 返回值：
//   - *strategy.Evaluation: 总分 + 明细 + 级别
//   - error: 当前实现不返回错误
//
// （Evaluate runs the scoring.）
func (s *Strategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	sd, ok := data.(*shortbase.Data)
	if !ok || sd == nil || len(sd.KLines) < 25 {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "nodata",
			Details: map[string]float64{}}, nil
	}
	// 无任何破位 → 直接 0 分（本战法以破位为存在前提）
	if !sd.BreakMA20 && !sd.BreakLow20 {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "no_break",
			Details: map[string]float64{"break_depth": 0}}, nil
	}

	depth := s.depthScore(sd)
	vol := s.volumeScore(sd)
	intra := s.intradayScore(sd)
	total := depth + vol + intra

	// 防洗盘缓判：均线多头未死（MA5>MA10）且破位浅（<1%）→ 总分 7 折
	shakeoutDiscount := false
	if sd.MA5 > sd.MA10 && sd.BreakDepthPct < 1.0 {
		total *= 0.7
		shakeoutDiscount = true
	}

	thr := s.scoreThreshold()
	level := "none"
	pass := total >= thr
	switch {
	case pass:
		level = "full_chain"
	case total >= 50:
		level = "watch"
	}

	conf := total / 100.0
	if conf > 0.95 {
		conf = 0.95
	}

	// 组装评分结果：三因子分 + 破位深度/量比/涨跌幅/洗盘标记原始指标 + 中文理由。
	// English: assemble evaluation with factor scores, raw indicators and Chinese reasons.
	return &strategy.Evaluation{
		TotalScore: total,
		Pass:       pass,
		Level:      level,
		Confidence: conf,
		Details: map[string]float64{
			"depth":         depth,
			"volume":        vol,
			"intraday":      intra,
			"break_depth":   sd.BreakDepthPct,
			"today_vol_vs5": sd.TodayVolVs5d,
			"change_pct":    sd.ChangePct,
			"shakeout_disc": boolFloat(shakeoutDiscount),
		},
		Reasons: map[string]string{
			"depth":    fmt.Sprintf("破20日线=%v/破20日低=%v/深度%.1f%%", sd.BreakMA20, sd.BreakLow20, sd.BreakDepthPct),
			"volume":   fmt.Sprintf("当日量/5日均=%.2f", sd.TodayVolVs5d),
			"intraday": fmt.Sprintf("涨跌%.2f%%", sd.ChangePct),
		},
	}, nil
}

// boolFloat 布尔转 0/1 评分明细值。
// （boolFloat converts a bool to 0/1 for the details map.）
func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// depthScore 破位深度（满分 30）：双破位满分；单破位按深度 0.5%~3% 线性。
// （depthScore scores breakdown depth, max 30.）
func (s *Strategy) depthScore(sd *shortbase.Data) float64 {
	if sd.BreakMA20 && sd.BreakLow20 {
		return 30
	}
	d := sd.BreakDepthPct
	if d <= 0 {
		return 0
	}
	sc := d / 3.0 * 30
	if sc > 25 {
		sc = 25 // 单破位上限 25，双破位才满分
	}
	return sc
}

// volumeScore 量能确认（满分 40）：当日量 ≥5日均量×1.5 满分，线性到 0.8 倍起评；缩量破位减半。
// （volumeScore scores volume confirmation, max 40.）
func (s *Strategy) volumeScore(sd *shortbase.Data) float64 {
	r := sd.TodayVolVs5d
	if r >= 1.5 {
		return 40
	}
	if r >= 1.0 {
		return r / 1.5 * 40
	}
	// 缩量破位：洗盘嫌疑，减半计分
	return r / 1.5 * 40 * 0.5
}

// intradayScore 日内弱势（满分 30）：跌幅分档 + 高开低走（由上影近似）加分。
// （intradayScore scores intraday weakness, max 30.）
func (s *Strategy) intradayScore(sd *shortbase.Data) float64 {
	var sc float64
	switch {
	case sd.ChangePct <= -5:
		sc = 30
	case sd.ChangePct <= -2:
		sc = 20 + (-sd.ChangePct-2)/3*10
	case sd.ChangePct <= 0:
		sc = -sd.ChangePct / 2 * 10
	}
	if sd.UpperShadowPct >= 0.5 {
		sc += 5 // 高开低走/冲高回落近似
	}
	if sc > 30 {
		sc = 30
	}
	return sc
}

// GenerateSignal 依据评分生成做空信号（Action=sell，持仓降级由 ScanShort 统一处理）。
// 参数：
//   - code: 股票代码
//   - eval: Evaluate 输出
//
// 返回值：
//   - *strategy.Signal: 未通过返回 nil
//   - error: 当前实现不返回错误
//
// （GenerateSignal emits a sell signal for a passed evaluation.）
func (s *Strategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
	}
	reason := fmt.Sprintf("放量破位：%s|%s|%s", eval.Reasons["depth"], eval.Reasons["volume"], eval.Reasons["intraday"])
	return &strategy.Signal{
		Type:       strategy.SignalBreakDown,
		Action:     strategy.ActionSell,
		Priority:   strategy.P2,
		Confidence: eval.Confidence,
		Reason:     reason,
		Meta:       eval.Details,
	}, nil
}
