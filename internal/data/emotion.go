// Package data — 市场情绪六阶段检测。
// 基于涨停家数、炸板率两个核心指标将市场分为六个情绪阶段。
package data

import "quant-trading-v2/internal/config"

// DetectEmotionPhase 根据行情快照计算市场情绪阶段。
// 返回与 N 形策略评分器兼容的中文阶段名称：
// "冰点" / "启动" / "发酵" / "高潮" / "背离" / "退潮"
// 判定逻辑来自 rules.json 中的 EmotionConfig 阈值。
func DetectEmotionPhase(snap *MarketSnapshot, cfg *config.EmotionConfig) string {
	if snap == nil || len(snap.Stocks) == 0 {
		return "启动"
	}

	limitUpCnt := 0
	blastCnt := 0
	for _, si := range snap.Stocks {
		if si == nil {
			continue
		}
		if si.ChangePct >= 9.5 {
			limitUpCnt++
		}
		// 触及涨停但未封住：最高>=9.5% 但当前涨幅<5%
		if si.High >= 9.5 && si.ChangePct < 5.0 {
			blastCnt++
		}
	}

	totalAttempt := limitUpCnt + blastCnt
	blastRate := 0.0
	if totalAttempt > 0 {
		blastRate = float64(blastCnt) / float64(totalAttempt) * 100
	}

	if limitUpCnt >= cfg.EmoClimaxLimitupMin && blastRate <= cfg.EmoClimaxBlastMax {
		return "高潮"
	}
	if limitUpCnt >= cfg.EmoFermentLimitupMin &&
		limitUpCnt <= cfg.EmoFermentLimitupMax &&
		blastRate <= cfg.EmoFermentBlastMax {
		return "发酵"
	}
	if limitUpCnt >= cfg.EmoStartLimitupMin &&
		limitUpCnt <= cfg.EmoStartLimitupMax &&
		blastRate >= cfg.EmoStartBlastMin &&
		blastRate <= cfg.EmoStartBlastMax {
		return "启动"
	}
	if limitUpCnt <= cfg.EmoIceLimitupMax && blastRate >= cfg.EmoIceBlastMin {
		return "冰点"
	}
	if limitUpCnt <= cfg.EmoRetreatLimitupMax && blastRate >= cfg.EmoRetreatBlastMin {
		return "退潮"
	}
	if limitUpCnt < cfg.EmoDivergeLimitupDrop && blastRate > cfg.EmoDivergeBlastRise {
		return "背离"
	}
	return "启动"
}
