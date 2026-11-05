// Package leader_decay 实现「龙头断板」做空战法（8b 反向信号之一）。
//
// 核心思想：连板龙头的行情由情绪接力维持，断板日若表现弱势（大面/炸板回落/天量）
// 且板块或市场情绪同步退潮，往往标志整段行情终结——持多个股应离场，跟风股应规避。
// 三因子评分（加权总分 0~100）：
//
//   - 连板高度（满分 30）：今日之前连板 ≥2 起评，板数越高分越高（5板+ 满分）；
//   - 断板弱势（满分 40）：收盘 ≤ 昨收×0.97（大面）20 分 + 盘中触板后炸板回落 15 分
//   - 天量（当日量 ≥5日均量×2）10 分，叠加封顶 40；
//   - 情绪退潮（满分 30）：所属板块涨停家数较昨日降 ≥50% 给 20 分；市场情绪相位
//     ∈ {退潮, 高潮转退潮} 给 10 分。
//
// 反包保护：分钟K显示 14:30 后重新封板 → 当日不发信号（level=reseal，留待次日确认）。
// 硬闸：日K ≥10 根 + 此前连板 ≥2；门槛默认 65（四战法最严，断板有反包风险）。
//
// （Package leader_decay implements the "leader board-break" bear tactic: when a consecutive
// limit-up leader fails to re-seal with weak price action while sector/market sentiment cools,
// the run is likely over. An afternoon re-seal suppresses the signal until next day.）
package leader_decay

import (
	"fmt"
	"strings"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategies/shortbase"
	"quant-trading-v2/internal/strategy"
)

// Strategy 龙头断板战法实例。
// （Strategy is the leader-decay tactic.）
type Strategy struct {
	cfg *config.Manager // 配置管理器（可为 nil）（Config manager; nil = defaults）
}

// New 创建龙头断板战法实例。
// 参数：
//   - cfg: 配置管理器（账号级参数来源，可为 nil）
//
// （New creates the tactic.）
func New(cfg *config.Manager) *Strategy { return &Strategy{cfg: cfg} }

// Name 返回策略标识名称"leader_decay"。
// （Name returns the strategy identifier.）
func (s *Strategy) Name() string { return "leader_decay" }

// Type 返回信号类型标识。
// （Type returns the signal type.）
func (s *Strategy) Type() strategy.SignalType { return strategy.SignalLeaderDecay }

// 评分阈值单点（§DERIVE-CARRIER-1105）：龙头断板三因子的档位、加分项与帽。
// 成因与 break_down.go 同源（载体只认名字不认人手的第二本账），这里不重复说明。
// English: the tactic's own thresholds, named once, exported read-only by derive_carrier.go.
const (
	ldMinBars          = 10   // 日K 硬闸：不足 10 根按数据缺口安全降级
	ldMinBoards        = 2    // 连板高度是存在前提：不足 2 板不适用本战法
	ldBoardMaxScore    = 30.0 // 连板高度满分
	ldBoardFullBoards  = 5    // 5 板及以上记满分
	ldBoardBaseScore   = 12.0 // 2 板的起评分
	ldBoardBaseBoards  = 2.0  // 起评的板数（线性段从这里往上数）
	ldBoardSpanBoards  = 3.0  // 2 板到 5 板之间跨 3 个板
	ldBoardSpanScore   = 18.0 // 这 3 个板换来 18 分
	ldBigLossRatio     = 0.97 // 收盘 ≤ 昨收×0.97 记"大面"
	ldBigLossScore     = 20.0 // 大面加分
	ldTouchedScore     = 15.0 // 盘中触板后炸板回落加分
	ldHugeVolumeRatio  = 2.0  // 当日量 ≥5日均量×2 记天量
	ldHugeVolumeScore  = 10.0 // 天量加分
	ldWeaknessMaxScore = 40.0 // 断板弱势的帽
	ldSectorDropRatio  = 0.5  // 板块涨停家数较昨日降幅 ≥50%
	ldSectorDropScore  = 20.0 // 板块退潮加分
	ldPhaseRetreatMark = "退潮" // 市场相位名里的退潮标记（真源见 research 的相位表）
	ldPhaseScore       = 10.0 // 市场相位仍在退潮的加分
	ldRetreatMaxScore  = 30.0 // 情绪退潮的帽
	ldDefaultMinScore  = 65.0 // 通过门槛出厂值：四战法最严（断板有反包风险）
)

// scoreThreshold 返回通过门槛（默认 65，rules.strategy.short.leader_decay_min 可覆盖）。
// （scoreThreshold resolves the pass gate, default 65.）
func (s *Strategy) scoreThreshold() float64 {
	if s.cfg != nil {
		sc := s.cfg.GetStrategyConfig()
		if sc != nil && sc.Short.LeaderDecayMin > 0 {
			return sc.Short.LeaderDecayMin
		}
	}
	return ldDefaultMinScore
}

// Evaluate 执行龙头断板三因子评分。
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
	if !ok || sd == nil || len(sd.KLines) < ldMinBars {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "nodata",
			Details: map[string]float64{}}, nil
	}
	// 非连板股不适用本战法（连板高度是存在前提）
	if sd.ConsecBoards < ldMinBoards {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "no_board",
			Details: map[string]float64{"consec_boards": float64(sd.ConsecBoards)}}, nil
	}
	// 今日仍封板 → 行情未断，不发信号
	if sd.SealedToday {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "still_sealed",
			Details: map[string]float64{"consec_boards": float64(sd.ConsecBoards)}}, nil
	}
	// 反包保护：14:30 后回封 → 当日不发（次日再判）
	if sd.AfternoonReseal {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Level: "reseal",
			Details: map[string]float64{"consec_boards": float64(sd.ConsecBoards)}}, nil
	}

	board := s.boardScore(sd)
	weak := s.weaknessScore(sd)
	retreat := s.retreatScore(sd)
	total := board + weak + retreat

	// 门槛从配置取（rules.strategy.short.leader_decay_min，缺省 65，四战法最严——断板存在反包风险）：
	// 达标给 full_chain，50~门槛只记 watch。
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

	// 组装评分结果：三因子分 + 连板数/涨幅/量比/板块降幅原始指标 + 中文理由。
	// English: assemble evaluation with factor scores, raw indicators and Chinese reasons.
	return &strategy.Evaluation{
		TotalScore: total,
		Pass:       pass,
		Level:      level,
		Confidence: conf,
		Details: map[string]float64{
			"boards":        board,
			"weakness":      weak,
			"retreat":       retreat,
			"consec_boards": float64(sd.ConsecBoards),
			"change_pct":    sd.ChangePct,
			"today_vol_vs5": sd.TodayVolVs5d,
			"sector_drop":   sd.SectorLimitUpDropPct * 100,
		},
		Reasons: map[string]string{
			"boards":   fmt.Sprintf("此前%d连板", sd.ConsecBoards),
			"weakness": fmt.Sprintf("断板收%.2f(%.2f%%)触板=%v量比%.1f", sd.Price, sd.ChangePct, sd.TouchedBoardToday, sd.TodayVolVs5d),
			"retreat":  fmt.Sprintf("板块涨停降%.0f%%/相位[%s]", sd.SectorLimitUpDropPct*100, sd.EmotionPhase),
		},
	}, nil
}

// boardScore 连板高度（满分 30）：2板=12 起评，5板+ 满分线性。
// （boardScore scores consecutive limit-up height, max 30.）
func (s *Strategy) boardScore(sd *shortbase.Data) float64 {
	b := float64(sd.ConsecBoards)
	if b >= ldBoardFullBoards {
		return ldBoardMaxScore
	}
	return ldBoardBaseScore + (b-ldBoardBaseBoards)/ldBoardSpanBoards*ldBoardSpanScore
}

// weaknessScore 断板弱势（满分 40）：大面 20 + 炸板回落 15 + 天量 10，叠加封顶。
// （weaknessScore scores the break-day weakness, max 40.）
func (s *Strategy) weaknessScore(sd *shortbase.Data) float64 {
	var sc float64
	if sd.PrevClose > 0 && sd.Price <= sd.PrevClose*ldBigLossRatio {
		sc += ldBigLossScore // 断板大面（≥-3%）
	}
	if sd.TouchedBoardToday {
		sc += ldTouchedScore // 盘中触板后炸板回落
	}
	if sd.TodayVolVs5d >= ldHugeVolumeRatio {
		sc += ldHugeVolumeScore // 天量分歧
	}
	if sc > ldWeaknessMaxScore {
		sc = ldWeaknessMaxScore
	}
	return sc
}

// retreatScore 情绪退潮（满分 30）：板块涨停家数骤降 20 + 市场相位退潮 10。
// （retreatScore scores sentiment retreat, max 30.）
func (s *Strategy) retreatScore(sd *shortbase.Data) float64 {
	var sc float64
	if sd.SectorLimitUpDropPct >= ldSectorDropRatio {
		sc += ldSectorDropScore
	}
	// 相位名按"包含"判（Go 的相位串可能是"高潮转退潮"这种复合写法）；
	// 这个词是从 params 走载体的——中文写死在移植后的 py 里会被 GBK 终端静默换成 ?。
	// English: substring match on the retreat marker; the carrier gets it as a param list.
	phase := sd.EmotionPhase
	if strings.Contains(phase, ldPhaseRetreatMark) {
		sc += ldPhaseScore
	}
	if sc > ldRetreatMaxScore {
		sc = ldRetreatMaxScore
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
	reason := fmt.Sprintf("龙头断板：%s|%s|%s", eval.Reasons["boards"], eval.Reasons["weakness"], eval.Reasons["retreat"])
	return &strategy.Signal{
		Type:       strategy.SignalLeaderDecay,
		Action:     strategy.ActionSell,
		Priority:   strategy.P1, // 断板退潮时效性最强，优先级最高
		Confidence: eval.Confidence,
		Reason:     reason,
		Meta:       eval.Details,
	}, nil
}
