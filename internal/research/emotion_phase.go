// 情绪相位分参回测（§Phase3）：把历史每日情绪统计（涨停家数/最高连板）按与实时
// DetectEmotionPhaseV2 相同的阈值口径标定为六个情绪阶段，供"按情绪阶段分组回测"使用：
// 同一战法在高潮期与冰点期胜率/期望天然不同，分段统计辅助分参（如冰点期加严门槛、高潮期放宽）。
// English: sentiment-phase parameterization backtest (Phase 3). Labels each historical day's sentiment
// stats (limit-up count / max board height) into one of six phases using the same thresholds as the
// realtime DetectEmotionPhaseV2, enabling phase-grouped backtests — the same strategy performs
// differently in climax vs freeze phases, so per-phase stats guide parameterization (tighter gates in
// freeze, wider in climax).
package research

import (
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// emotionPhases 六个情绪阶段中文名（与 data 包 DetectEmotionPhaseV2 同一套阶段）。
// English: the six sentiment-phase Chinese names (same phases as DetectEmotionPhaseV2).
var emotionPhases = []string{"冰点", "启动", "发酵", "高潮", "退潮", "背离"}

// emotionPhaseDefaultCfg 内置默认阈值（无运行时配置时使用，与 data 包测试桩同量级）；
// 配置存在时以 cfg 为准（rules.json 的 EmotionConfig 动态可调）。
// English: built-in default thresholds used when no runtime config is given (same scale as the data
// package's test stub); when a config exists it takes precedence.
func emotionPhaseDefaultCfg() *config.EmotionConfig {
	return &config.EmotionConfig{
		EmoIceLimitupMax:      14,
		EmoIceBoardMax:        2,
		EmoStartLimitupMin:    15,
		EmoStartLimitupMax:    39,
		EmoStartBoardMax:      3,
		EmoFermentLimitupMin:  40,
		EmoFermentLimitupMax:  79,
		EmoFermentBoardMax:    6,
		EmoClimaxLimitupMin:   80,
		EmoClimaxBoardMin:     7,
		EmoRetreatLimitupMax:  14,
		EmoRetreatBoardMax:    3,
		EmoDivergeLimitupDrop: 15,
		EmoDivergeBoardDrop:   2,
	}
}

// PhaseFromEmotionStat 按涨停家数与最高连板判定历史某日情绪阶段（与 DetectEmotionPhaseV2 同口径，
// 省略炸板率维度——历史炸板池可用性低于涨停池，先用温度轴标定）。
// cfg 为 nil 时用内置默认阈值。
// English: labels a historical day's sentiment phase from limit-up count and max board height
// (same rules as DetectEmotionPhaseV2, dropping the blast-rate dimension which is less reliably
// available historically). nil cfg falls back to built-in defaults.
func PhaseFromEmotionStat(stat store.EmotionStat, cfg *config.EmotionConfig) string {
	if cfg == nil {
		cfg = emotionPhaseDefaultCfg()
	}
	lu, mb := stat.LimitUp, stat.MaxBoard
	switch {
	case lu <= 0:
		// 无涨停（历史数据缺失或空池）保守归"启动"，与实时 DetectEmotionPhaseV2 空池兜底一致，
		// 避免历史/实时标定漂移。
		// English: a zero limit-up day is conservatively "启动", matching the realtime empty-pool
		// fallback so historical and live labeling stay consistent.
		return "启动"
	case lu >= cfg.EmoClimaxLimitupMin && mb >= cfg.EmoClimaxBoardMin:
		return "高潮"
	case lu >= cfg.EmoFermentLimitupMin && lu <= cfg.EmoFermentLimitupMax && mb <= cfg.EmoFermentBoardMax:
		return "发酵"
	case lu >= cfg.EmoStartLimitupMin && lu <= cfg.EmoStartLimitupMax && mb <= cfg.EmoStartBoardMax:
		return "启动"
	case lu <= cfg.EmoIceLimitupMax && mb <= cfg.EmoIceBoardMax:
		return "冰点"
	case lu <= cfg.EmoRetreatLimitupMax && mb <= cfg.EmoRetreatBoardMax:
		return "退潮"
	case lu == 0:
		return "启动"
	default:
		return "启动"
	}
}

// PhaseHist 区间情绪阶段分布统计。
type PhaseHist struct {
	From      string         `json:"from"`       // 起始日期
	To        string         `json:"to"`         // 结束日期
	Days      int            `json:"days"`       // 有效交易日数
	PhaseDays map[string]int `json:"phase_days"` // 相位 → 该相位天数
	Last      string         `json:"last_phase"` // 区间末尾阶段（最近情绪）
}

// EmotionPhaseHist 统计某区间的每日情绪阶段分布（供分参依据与报告）。
// English: EmotionPhaseHist tallies the daily sentiment-phase distribution over a date range.
func EmotionPhaseHist(stats []store.EmotionStat, cfg *config.EmotionConfig) PhaseHist {
	h := PhaseHist{PhaseDays: map[string]int{}}
	if len(stats) == 0 {
		return h
	}
	h.From, h.To = stats[0].Date, stats[len(stats)-1].Date
	h.Days = len(stats)
	for _, s := range stats {
		p := PhaseFromEmotionStat(s, cfg)
		h.PhaseDays[p]++
		h.Last = p
	}
	return h
}
