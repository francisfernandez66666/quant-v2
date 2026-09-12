// Package good_news_fade 实现「利好兑现砸盘」做空战法（8b 反向信号之一，决策①新增）。
//
// 核心思想："买预期、卖现实"——利好事件驱动拉升后，事件热度仍在但价格滞涨转弱，
// 是持多个股最典型的派发时点。属事件×量价双驱动战法（与 NewsAgent 打通）。
// 三因子评分（加权总分 0~100）：
//
//   - 事件驱动在窗（满分 25）：近 5 交易日该股有已打标利好事件（Stage2 利好），事件越新分越高；
//   - 涨幅已兑现（满分 35）：事件日收盘至现价累计涨幅 ≥10% 起评，10~25% 线性，>25% 满分；
//   - 高位转弱（满分 40）：近3日滞涨(<1%) 15 + 放量滞涨(量比≥1.5) 10 + 跌破5日线 8 + 长上影 7。
//
// 防误杀：事件为产业链上游传导类（持续性更强）总分打 7 折；近3日内仍有一天涨幅 ≥5%
// （主升未止）不发信号。硬闸：日K ≥20 根 + 事件在窗（无事件直接 0 分）。门槛默认 60。
//
// （Package good_news_fade implements the "good-news-fully-priced" bear tactic: after a bullish
// event drives a ≥10% rally, stalling price with heavy volume near the highs marks distribution.
// Propagated supply-chain events are discounted; a fresh ≥5% up-day suppresses the signal.）
package good_news_fade

import (
	"fmt"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategies/shortbase"
	"quant-trading-v2/internal/strategy"
)

// Strategy 利好兑现砸盘战法实例。
// （Strategy is the good-news-fade tactic.）
type Strategy struct {
	cfg *config.Manager // 配置管理器（可为 nil）（Config manager; nil = defaults）
}

// New 创建利好兑现砸盘战法实例。
// 参数：
//   - cfg: 配置管理器（账号级参数来源，可为 nil）
//
// （New creates the tactic.）
func New(cfg *config.Manager) *Strategy { return &Strategy{cfg: cfg} }

// Name 返回策略标识名称"good_news_fade"。
// （Name returns the strategy identifier.）
func (s *Strategy) Name() string { return "good_news_fade" }

// Type 返回信号类型标识。
// （Type returns the signal type.）
func (s *Strategy) Type() strategy.SignalType { return strategy.SignalGoodNewsFade }

// scoreThreshold 返回通过门槛（默认 60，rules.strategy.short.good_news_fade_min 可覆盖）。
// （scoreThreshold resolves the pass gate.）
func (s *Strategy) scoreThreshold() float64 {
	if s.cfg != nil {
		sc := s.cfg.GetStrategyConfig()
		if sc != nil && sc.Short.GoodNewsFadeMin > 0 {
			return sc.Short.GoodNewsFadeMin
		}
	}
	return 60
}

// Evaluate 执行利好兑现三因子评分。
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
	if !ok || sd == nil || len(sd.KLines) < 20 {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "nodata",
			Details: map[string]float64{}}, nil
	}
	// 事件不在窗：本战法不成立（事件驱动前提）
	if !sd.EventInWindow || sd.EventDayClose <= 0 {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "no_event",
			Details: map[string]float64{}}, nil
	}
	// 防误杀：近3日内仍有一天涨幅 ≥5%（主升未止）不发
	if s.freshSurge(sd) {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "still_rising",
			Details: map[string]float64{}}, nil
	}

	ev := s.eventScore(sd)
	faded := s.gainSinceEvent(sd)
	weak := s.weaknessScore(sd)
	total := ev + faded + weak

	// 传导类利好持续性更强：总分 7 折
	propagationDiscount := false
	if sd.EventPropagation {
		total *= 0.7
		propagationDiscount = true
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
	if sd.EventScore >= 0.75 {
		conf += 0.1 // 重大利好兑现，砸盘概率更高
	}
	if conf > 0.95 {
		conf = 0.95
	}

	return &strategy.Evaluation{
		TotalScore: total,
		Pass:       pass,
		Level:      level,
		Confidence: conf,
		Details: map[string]float64{
			"event":       ev,
			"gain_faded":  faded,
			"weakness":    weak,
			"event_age":   float64(sd.EventAgeDays),
			"event_score": sd.EventScore,
			"gain_since":  faded / 35 * 25, // 还原兑现幅度近似值供前端展示
			"propagation": boolFloat(sd.EventPropagation),
			"prop_disc":   boolFloat(propagationDiscount),
		},
		Reasons: map[string]string{
			"event":    fmt.Sprintf("利好在窗(%d日前,强度%.2f)", sd.EventAgeDays, sd.EventScore),
			"gain":     fmt.Sprintf("事件日至今%.1f%%", (sd.Price-sd.EventDayClose)/sd.EventDayClose*100),
			"weakness": fmt.Sprintf("近3日%.1f%%/量比%.2f/破5日=%v", sd.Last3RangePct, sd.VolRatio5_20, sd.BelowMA5),
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

// eventScore 事件驱动在窗（满分 25）：1日=25 线性衰减到 5日=5。
// （eventScore scores event freshness, max 25.）
func (s *Strategy) eventScore(sd *shortbase.Data) float64 {
	age := float64(sd.EventAgeDays)
	if age <= 1 {
		return 25
	}
	if age >= 5 {
		return 5
	}
	return 25 - (age-1)/4*20
}

// gainSinceEvent 涨幅已兑现（满分 35）：事件日收盘至现价 ≥10% 起评，10~25% 线性，>25% 满分。
// （gainSinceEvent scores how much of the event premium has been realized, max 35.）
func (s *Strategy) gainSinceEvent(sd *shortbase.Data) float64 {
	if sd.EventDayClose <= 0 {
		return 0
	}
	g := (sd.Price - sd.EventDayClose) / sd.EventDayClose
	if g < 0.10 {
		return 0
	}
	if g >= 0.25 {
		return 35
	}
	return (g - 0.10) / 0.15 * 35
}

// weaknessScore 高位转弱（满分 40）：滞涨 15 + 放量滞涨 10 + 破5日线 8 + 长上影 7。
// （weaknessScore scores stalling/softening near highs, max 40.）
func (s *Strategy) weaknessScore(sd *shortbase.Data) float64 {
	var sc float64
	if sd.Last3RangePct < 1.0 {
		sc += 15 // 滞涨
		if sd.VolRatio5_20 >= 1.5 {
			sc += 10 // 放量滞涨=派发
		}
	}
	if sd.BelowMA5 {
		sc += 8
	}
	if sd.UpperShadowPct >= 0.4 {
		sc += 7
	}
	if sc > 40 {
		sc = 40
	}
	return sc
}

// freshSurge 判定近3日内是否仍有一天涨幅 ≥5%（主升未止，防误杀）。
// （freshSurge reports whether any of the last 3 bars still gained ≥5%.）
func (s *Strategy) freshSurge(sd *shortbase.Data) bool {
	kl := sd.KLines
	n := len(kl)
	if n < 4 {
		return false
	}
	for i := n - 3; i < n; i++ {
		prev := kl[i-1].Close
		if prev > 0 && kl[i].Close/prev-1 >= 0.05 {
			return true
		}
	}
	return false
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
	reason := fmt.Sprintf("利好兑现砸盘：%s|兑现%s|%s", eval.Reasons["event"], eval.Reasons["gain"], eval.Reasons["weakness"])
	return &strategy.Signal{
		Type:       strategy.SignalGoodNewsFade,
		Action:     strategy.ActionSell,
		Priority:   strategy.P2,
		Confidence: eval.Confidence,
		Reason:     reason,
		Meta:       eval.Details,
	}, nil
}
