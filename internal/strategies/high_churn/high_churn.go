// Package high_churn 实现「高位滞涨」做空战法（8b 反向信号之一）。
//
// 核心思想：股价处于阶段高位、量能持续放大但价格滞涨——放量不涨即派发，
// 是持多个股最典型的出货时点。三因子评分（加权总分 0~100）：
//
//   - 位置高度（满分 30）：现价 ≥60日最高×0.92 或 近20日累计涨幅 ≥30%，越高越危险；
//   - 量价背离（满分 40）：近5日均量/前20日均量 ≥1.5 且 近3日价格涨幅 <2%（放量不涨=派发）；
//   - K线走弱（满分 30）：上影线占比 ≥40% 或 收盘跌破5日线 或 连续2日阴线。
//
// 信号级别：总分 ≥60（可配）→ full_chain（持仓 sell / 非持仓 watch）；50~60 → watch；其余 none。
// 硬闸：日K ≥60 根（不足按数据缺口 0 分安全降级）。
//
// （Package high_churn implements the "high-level churn" bear tactic: heavy volume with
// stalled price near stage highs signals distribution. Three factors — position height (30),
// volume-price divergence (40), candle weakness (30) — map to sell/watch signals.）
package high_churn

import (
	"fmt"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategies/shortbase"
	"quant-trading-v2/internal/strategy"
)

// Strategy 高位滞涨战法实例（持有配置管理器以读取账号级阈值热参）。
// （Strategy is the high-churn tactic with a config handle for per-user thresholds.）
type Strategy struct {
	cfg *config.Manager // 配置管理器（可为 nil，走默认参数）（Config manager; nil = defaults）
}

// New 创建高位滞涨战法实例。
// 参数：
//   - cfg: 配置管理器（账号级参数来源，可为 nil）
//
// （New creates the tactic.）
func New(cfg *config.Manager) *Strategy { return &Strategy{cfg: cfg} }

// Name 返回策略标识名称"high_churn"。
// （Name returns the strategy identifier.）
func (s *Strategy) Name() string { return "high_churn" }

// Type 返回信号类型标识。
// （Type returns the signal type.）
func (s *Strategy) Type() strategy.SignalType { return strategy.SignalHighChurn }

// scoreThreshold 返回买入门槛（默认 60，账号级 rules.strategy.short.high_churn_min 可覆盖）。
// （scoreThreshold resolves the pass gate with a 60 default.）
func (s *Strategy) scoreThreshold() float64 {
	if s.cfg != nil {
		sc := s.cfg.GetStrategyConfig()
		if sc != nil && sc.Short.HighChurnMin > 0 {
			return sc.Short.HighChurnMin
		}
	}
	return 60
}

// Evaluate 执行高位滞涨三因子评分。
// 参数：
//   - code: 股票代码
//   - data: 必须为 *shortbase.Data（适配层派生的做空共享输入），否则返回空结果
//
// 返回值：
//   - *strategy.Evaluation: 总分 + 三因子明细 + 级别（full_chain/watch/none）
//   - error: 当前实现不返回错误
//
// （Evaluate runs the three-factor scoring.）
func (s *Strategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	sd, ok := data.(*shortbase.Data)
	if !ok || sd == nil || len(sd.KLines) < 60 {
		// 数据不足：0 分安全降级（nodata 由上层 markDataGap 识别）
		// English: insufficient bars → safe 0-score degrade.
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "nodata",
			Details: map[string]float64{}}, nil
	}

	pos := s.positionScore(sd)
	div := s.divergenceScore(sd)
	weak := s.weaknessScore(sd)
	total := pos + div + weak

	thr := s.scoreThreshold()
	level := "none"
	pass := total >= thr
	switch {
	case pass:
		level = "full_chain"
	case total >= 50:
		level = "watch"
	}

	// 置信度 = 总分/100；位置与背离同时满分时 +0.1（派发特征齐备），封顶 0.95。
	conf := total / 100.0
	if pos >= 30 && div >= 40 {
		conf += 0.1
	}
	if conf > 0.95 {
		conf = 0.95
	}

	// 组装评分结果：三因子分 + 位置/涨幅/量比/近3日原始指标 + 各维度中文理由（供前端展示与复盘）。
	// English: assemble the evaluation with factor scores, raw indicators and per-dimension reasons.
	return &strategy.Evaluation{
		TotalScore: total,
		Pass:       pass,
		Level:      level,
		Confidence: conf,
		Details: map[string]float64{
			"position":   pos,
			"divergence": div,
			"weakness":   weak,
			"pos_high":   sd.PosHigh,
			"gain20":     sd.Gain20 * 100,
			"vol_ratio":  sd.VolRatio5_20,
			"last3_pct":  sd.Last3RangePct,
		},
		Reasons: map[string]string{
			"position":   fmt.Sprintf("位置%.0f%%/20日涨%.1f%%", sd.PosHigh*100, sd.Gain20*100),
			"divergence": fmt.Sprintf("量比%.2f/近3日%.1f%%", sd.VolRatio5_20, sd.Last3RangePct),
			"weakness":   fmt.Sprintf("上影%.0f%%/破5日=%v/连阴%d", sd.UpperShadowPct*100, sd.BelowMA5, sd.ConsecDownDays),
		},
	}, nil
}

// positionScore 位置高度（满分 30）：现价相对60日高 + 近20日涨幅双口径取高者。
// （positionScore scores stage height, max 30.）
func (s *Strategy) positionScore(sd *shortbase.Data) float64 {
	var sc float64
	// 现价 ≥60日最高×0.92 起评，线性到 1.0 满分
	if sd.PosHigh >= 0.92 {
		sc = 15 + (sd.PosHigh-0.92)/0.08*15
	}
	// 近20日涨幅 ≥30% 起评，50% 满分
	if sd.Gain20 >= 0.30 {
		g := 15 + (sd.Gain20-0.30)/0.20*15
		if g > 30 {
			g = 30
		}
		if g > sc {
			sc = g
		}
	}
	if sc > 30 {
		sc = 30
	}
	return sc
}

// divergenceScore 量价背离（满分 40）：放量（5日/20日均量比≥1.5）且滞涨（近3日<2%）。
// 只满足单边减半。
// （divergenceScore scores heavy-volume-stalled-price, max 40.）
func (s *Strategy) divergenceScore(sd *shortbase.Data) float64 {
	heavy := sd.VolRatio5_20 >= 1.5
	stall := sd.Last3RangePct < 2.0
	switch {
	case heavy && stall:
		return 40
	case heavy || stall:
		return 20
	}
	return 0
}

// weaknessScore K线走弱（满分 30）：长上影/破5日线/连续阴线，逐项累计封顶。
// （weaknessScore scores candle weakness, max 30.）
func (s *Strategy) weaknessScore(sd *shortbase.Data) float64 {
	var sc float64
	if sd.UpperShadowPct >= 0.4 {
		sc += 12
	}
	if sd.BelowMA5 {
		sc += 10
	}
	if sd.ConsecDownDays >= 2 {
		sc += 8
	}
	if sc > 30 {
		sc = 30
	}
	return sc
}

// GenerateSignal 依据评分生成做空信号（Action=sell；持仓与否由 ScanShort 统一降级 watch）。
// 参数：
//   - code: 股票代码
//   - eval: Evaluate 输出
//
// 返回值：
//   - *strategy.Signal: 未通过返回 nil；通过返回 sell 信号（理由含三因子摘要）
//   - error: 当前实现不返回错误
//
// （GenerateSignal emits a sell signal for a passed evaluation.）
func (s *Strategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if eval == nil || !eval.Pass {
		return nil, nil
	}
	reason := "高位滞涨：放量不涨疑似派发"
	if r := eval.Reasons["position"]; r != "" {
		reason = fmt.Sprintf("高位滞涨：%s|%s|%s", r, eval.Reasons["divergence"], eval.Reasons["weakness"])
	}
	return &strategy.Signal{
		Type:       strategy.SignalHighChurn,
		Action:     strategy.ActionSell,
		Priority:   strategy.P2,
		Confidence: eval.Confidence,
		Reason:     reason,
		Meta:       eval.Details,
	}, nil
}
