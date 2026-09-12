// Package data — 市场情绪六阶段检测。
// 基于涨停家数、炸板率两个核心指标将市场分为六个情绪阶段。
// English: Package data — market sentiment six-phase detection.
// English: Classifies the market into six sentiment phases using two core metrics: limit-up count and blast-out rate.
package data

import "quant-trading-v2/internal/config"

// DetectEmotionPhase 根据行情快照计算市场情绪阶段。
// 返回与 N 形策略评分器兼容的中文阶段名称：
// "冰点" / "启动" / "发酵" / "高潮" / "背离" / "退潮"
// 判定逻辑来自 rules.json 中的 EmotionConfig 阈值。
// English: DetectEmotionPhase computes the market sentiment phase from a quote snapshot.
// English: Returns Chinese phase names compatible with the N-shaped strategy scorer:
// English: "freeze" / "start" / "ferment" / "climax" / "divergence" / "retreat".
// English: The rules come from the EmotionConfig thresholds in rules.json.
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
		// 触及涨停但未封住：盘中最高涨幅>=9.5%（按现价+涨跌幅反推昨收折算）但当前涨幅<5%。
		// §GAP2.1 修复：原实现把当日最高价（元）直接与 9.5 比较——高价股恒触发、低价股永不触发。
		// StockInfo 无昨收字段，用 price/(1+changePct/100) 反推；行情缺失（价格无效）时跳过该股炸板判定。
		// English: Touched limit-up but failed to seal: intraday high gain >=9.5% (derived from
		// price+changePct back-computed prior close) while current change <5%.
		// English: §GAP2.1 fix — the old code compared the day-high PRICE (yuan) against 9.5, which always
		// fired for high-priced stocks and never for low-priced ones.
		if hiGain := intradayHighGainPct(si); hiGain >= 9.5 && si.ChangePct < 5.0 {
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

// intradayHighGainPct 由现价+涨跌幅反推昨收，计算盘中最高价相对昨收的涨幅（%）。
// 行情无效（现价≤0 / 最高价≤0 / 反推结果非正）返回 0（视为未触及涨停，不参与炸板计数）。
// English: intradayHighGainPct back-computes the prior close from price+changePct and returns the
// day-high gain over it (%). Returns 0 for invalid quotes (treated as not touching limit-up).
func intradayHighGainPct(si *StockInfo) float64 {
	if si == nil || si.Price <= 0 || si.High <= 0 || si.ChangePct <= -100 {
		return 0
	}
	prevClose := si.Price / (1 + si.ChangePct/100)
	if prevClose <= 0 {
		return 0
	}
	return (si.High - prevClose) / prevClose * 100
}

// DetectEmotionPhaseV2 基于真实涨停池判定市场情绪阶段（升级版）。
// 输入为当日东财涨停池（涨停家数 + 最高连板高度），配合涨跌家数辅助修正。
// 六个阶段：冰点 / 启动 / 发酵 / 高潮 / 背离 / 退潮。
// 涨停池为空且涨跌家数未知（upCount=0）时返回"启动"（数据缺失兜底）。
// English: DetectEmotionPhaseV2 determines the sentiment phase from the real limit-up pool (upgraded version).
// English: Input is the day's EastMoney limit-up pool (limit-up count + max consecutive-board height), corrected by up/down counts.
// English: Six phases: freeze / start / ferment / climax / divergence / retreat.
// English: Returns "start" when the pool is empty and up/down counts are unknown (upCount=0) as a data-missing fallback.
func DetectEmotionPhaseV2(pool []LimitUpStock, upCount, downCount int, cfg *config.EmotionConfig) string {
	if len(pool) == 0 {
		return "启动" // 数据缺失（盘前/接口异常）不判冰点，保守归"启动"
		// English: Data missing (pre-market/API error): don't judge freeze, conservatively return "start".
	}
	limitUpCnt := len(pool)
	maxBoard := 0 // 最高连板数（涨停池中 LianBan 的最大值）
	// English: Max consecutive-board count (max of LianBan in the limit-up pool).
	for _, s := range pool {
		if s.LianBan > maxBoard {
			maxBoard = s.LianBan
		}
	}

	rawPhase := "启动"
	switch {
	case limitUpCnt >= cfg.EmoClimaxLimitupMin && maxBoard >= cfg.EmoClimaxBoardMin:
		rawPhase = "高潮"
	case limitUpCnt >= cfg.EmoFermentLimitupMin &&
		limitUpCnt <= cfg.EmoFermentLimitupMax &&
		maxBoard <= cfg.EmoFermentBoardMax:
		rawPhase = "发酵"
	case limitUpCnt >= cfg.EmoStartLimitupMin &&
		limitUpCnt <= cfg.EmoStartLimitupMax &&
		maxBoard <= cfg.EmoStartBoardMax:
		rawPhase = "启动"
	case limitUpCnt <= cfg.EmoIceLimitupMax && maxBoard <= cfg.EmoIceBoardMax:
		rawPhase = "冰点"
	case limitUpCnt <= cfg.EmoRetreatLimitupMax && maxBoard <= cfg.EmoRetreatBoardMax:
		rawPhase = "退潮"
	case limitUpCnt < cfg.EmoDivergeLimitupDrop && maxBoard < cfg.EmoDivergeBoardDrop:
		rawPhase = "背离"
	}
	// §MARKET_RISK_GATE P1：涨停池口径看不到全市场普跌，用真实涨跌家数纠偏（阈值未配或家数缺失=原样返回）。
	return applyBreadthCorrection(rawPhase, upCount, downCount, cfg)
}

// emotionSeverity 情绪阶段按"强弱"排序（越大越冷/越危险），供纠偏时只降不升。
// 冰点(5) < 退潮(4) < 背离(3) 为冷侧；高潮(1)/发酵(2)/启动(0) 为暖侧。
// English: orders phases by "coldness" (higher = riskier) so breadth correction can only escalate.
func emotionSeverity(phase string) int {
	switch phase {
	case "高潮":
		return 1
	case "发酵":
		return 2
	case "启动":
		return 0
	case "背离":
		return 3
	case "退潮":
		return 4
	case "冰点":
		return 5
	default:
		return 0
	}
}

// applyBreadthCorrection 用真实涨跌家数对涨停池口径的情绪阶段做**单向纠偏**（只往更冷方向修正，
// 永不把危险阶段修成安全）。下跌占比 down/(up+down) 达 IceDownRatio 强制冰点、达 RetreatDownRatio
// 至少降为退潮。以下任一条件成立则弃权原样返回：家数缺失(total<=0，接口失败≠涨跌各半)、阈值未配(<=0)、
// 阈值配置非法(ice<retreat)。ice 阈值应 ≥ retreat 才有分层意义，若配反则取二者较大值兜底防误伤。
// English: applies a ONE-WAY breadth correction to the limit-up-pool phase using real up/down counts
// (never warms a dangerous phase). down-ratio ≥ Ice → force ice; ≥ Retreat → demote to at least retreat.
// It abstains when counts are missing (total≤0: a failed fetch is NOT treated as 50/50), thresholds are
// unset (≤0), or misconfigured; a reversed config falls back to the larger value to avoid over-triggering.
func applyBreadthCorrection(phase string, upCount, downCount int, cfg *config.EmotionConfig) string {
	total := upCount + downCount
	if total <= 0 || cfg == nil {
		return phase
	}
	ice, retreat := cfg.EmoBreadthIceDownRatio, cfg.EmoBreadthRetreatDownRatio
	if ice <= 0 && retreat <= 0 {
		return phase // 未配置阈值 → 不纠偏
	}
	if ice > 0 && retreat > 0 && ice < retreat {
		// 配置反了（冰点阈值本应更极端）：取较大者作冰点、较小者作退潮，避免弱广度误判冰点。
		ice, retreat = retreat, ice
	}
	downRatio := float64(downCount) / float64(total)
	if ice > 0 && downRatio >= ice {
		return "冰点"
	}
	if retreat > 0 && downRatio >= retreat {
		if emotionSeverity(phase) < emotionSeverity("退潮") {
			return "退潮"
		}
	}
	return phase
}
