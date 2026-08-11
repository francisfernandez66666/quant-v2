// 预期差检测：新闻预期 vs 股价实际反应的偏差。
// Expectation-gap detection: the deviation between news expectations and the actual price reaction.
// 借鉴 astock-market-engine market_reasoning_engine 的 6 类预期差：
// 利好不涨 / 利空不跌 / 放量涨次日弱(需次日数据,暂跳过) / 高位放量滞涨 / 缩量上涨 / 业绩利好反而跌。
// Borrowed from the astock-market-engine market_reasoning_engine, 6 gap types:
// bullish news no-rise / bearish news no-drop / lagging surge on high volume / shrinking-volume rise /
// earnings beat but fall.
// 每类 0-4 分规则累计，归一化后 ≥0.4 触发提醒信号。
// Each type accumulates a raw score of 0-4, normalized to 0-1; a value >= 0.4 triggers an alert.
// 检测入口为 CheckExpectationGap，在 ScanLimitUp 中被涨停池与 8a/8b 个股复用。
// Entry point is CheckExpectationGap, reused by the limit-up pool and 8a/8b stocks in ScanLimitUp.
package combat_agent

import (
	"strings"
)

// gapType 预期差类型常量。
// gapType constants for the expectation-gap types.
// GapBullishNoRise: clear bullish news but no price rise.
// GapBearishNoDrop: clear bearish news but the price does not drop.
// GapHighVolSluggish: high volume but insufficient gain (sluggish).
// GapShrinkRise: rising on shrinking volume, weak follow-through.
// GapEarningsFall: earnings beat but the price falls.
const (
	GapBullishNoRise   = "利好不涨"    // 有明确利好新闻，股价却未涨
	GapBearishNoDrop   = "利空不跌"    // 有明确利空新闻，股价却不跌反涨
	GapHighVolSluggish = "高位放量滞涨"  // 放量但涨幅不足（滞涨）
	GapShrinkRise      = "缩量上涨"    // 缩量创新高/上涨，承接弱
	GapEarningsFall    = "业绩利好反而跌" // 业绩增长但股价下跌
)

// ExpectationGapResult 预期差检测结果。
// ExpectationGapResult is the detection result for one expectation gap.
//   - GapType: gap type (e.g. GapBullishNoRise)
//   - Score: normalized 0-1 (raw 0-4 score divided by 4)
//   - Trigger: whether it triggers an alert (Score >= 0.4)
//   - Reason: human-readable description
type ExpectationGapResult struct {
	GapType string  // 预期差类型（GapBullishNoRise 等常量）
	Score   float64 // 归一化 0-1（由 0-4 原始分 /4 得到）
	Trigger bool    // 是否触发提醒（>=0.4）
	Reason  string  // 可读描述
}

// CheckExpectationGap 检测单只股票的预期差。
// CheckExpectationGap detects an expectation gap for a single stock.
// newsText 为该股关联新闻文本，positive 表示新闻方向（true=利好/利空取反）。
// newsText is the related news text; positive indicates the news direction (true = bullish, false = bearish).
// changePct 为当前涨跌幅(%)，turnover 为换手率(%)，volRatio 为量比（当日量/近5日均量，未知传0）。
// changePct is the current change %, turnover is the turnover %, and volRatio is the volume ratio
// (today's volume over the 5-day average; pass 0 when unknown).
// 无新闻方向或新闻文本为空时返回不触发。
// Returns a non-triggered result when the news text is empty.
func CheckExpectationGap(newsText string, positive bool, changePct, turnover, volRatio float64) ExpectationGapResult {
	res := ExpectationGapResult{Score: 0}
	newsText = strings.TrimSpace(newsText)
	// 无新闻文本 → 无从判断预期差，直接返回未触发
	// No news text -> nothing to evaluate, return as not triggered.
	if newsText == "" {
		return res
	}

	if positive {
		// 利好方向：新闻利好但股价反应不足 → 预期差
		// Bullish: bullish news but an insufficient price reaction -> a gap.
		switch {
		case changePct <= 0:
			// 利好不涨：跌幅越大分越高（0-4）
			// Bullish-no-rise: the deeper the drop, the higher the raw score (0-4).
			// 微跌（>-3%）按比例加权，深跌直接给满分 4
			// Slight drops (>-3%) are weighted proportionally; deep drops get a full 4.
			raw := 4.0
			if changePct > -3 {
				raw = 2 + (-changePct)*0.6
			}
			res.GapType = GapBullishNoRise
			res.Reason = "有利好新闻但股价未涨"
			res.setNorm(raw)
		case changePct < 3:
			// 利好反应不足：涨幅低于 3% 视为没充分兑现
			// Insufficient bullish reaction: a gain below 3% means the news was not fully priced in.
			raw := 1.5 + (3-changePct)*0.5
			res.GapType = GapBullishNoRise
			res.Reason = "有利好新闻但涨幅不足"
			res.setNorm(raw)
		case changePct >= 3 && changePct < 9.5:
			// 高位放量滞涨：涨幅 3-9.5% 且换手异常放大
			// High-placement sluggish rise: 3-9.5% gain with abnormally inflated turnover.
			if turnover > 20 && volRatio > 2 {
				// 高换手 + 高量比但涨幅一般 → 滞涨
				// High turnover + high volume ratio but a modest gain -> sluggish.
				res.GapType = GapHighVolSluggish
				res.Reason = "放量滞涨，承接盘待观察"
				res.setNorm(3.5)
			} else if volRatio < 0.7 {
				// 量比明显萎缩的上涨 → 承接弱，注意缩量上行
				// A rise with a clearly shrinking volume ratio -> weak follow-through.
				res.GapType = GapShrinkRise
				res.Reason = "缩量上涨，量能承接不足"
				res.setNorm(2.5)
			} else {
				// 量价配合正常，利好已合理反映 → 不触发
				// Normal volume-price alignment; the news is fully priced in -> not triggered.
				res.Score = 0
				return res
			}
		default:
			// 涨停或大幅上涨（≥9.5%）：利好兑现，不触发
			// Limit-up or a large gain (>=9.5%): the bullish news is realized -> not triggered.
			res.Score = 0
			return res
		}
		res.Trigger = res.Score >= 0.4
		return res
	}

	// 利空方向：新闻利空但股价反应不足 → 预期差
	// Bearish: bearish news but an insufficient price reaction -> a gap.
	switch {
	case changePct >= 0:
		// 利空不跌反涨：涨幅越大分越高
		// Bearish-no-drop: the higher the gain, the higher the score.
		res.GapType = GapBearishNoDrop
		res.Reason = "有利空新闻但股价不跌反涨"
		res.setNorm(2.0 + changePct*0.4)
	case changePct > -3:
		// 利空但跌幅不足（微跌）
		// Bearish but the drop is insufficient (a slight dip).
		res.GapType = GapBearishNoDrop
		res.Reason = "有利空新闻但跌幅不足"
		res.setNorm(1.5 + (3+changePct)*0.4)
	default:
		// 深跌（≤-3%）：利空兑现，不触发
		// Deep drop (<=-3%): the bearish news is realized -> not triggered.
		res.Score = 0
		return res
	}
	res.Trigger = res.Score >= 0.4
	return res
}

// setNorm 将 0-4 分归一化为 0-1 并写入 Score。
// setNorm clamps the raw score to [0,4] and stores the normalized 0-1 value in Score.
// 先把原始分裁剪到 0~4 区间，再除以 4 得到 0~1 的归一化分数。
// The raw score is clipped to the 0-4 range, then divided by 4 to yield a 0-1 fraction.
func (r *ExpectationGapResult) setNorm(raw float64) {
	if raw > 4 {
		raw = 4
	}
	if raw < 0 {
		raw = 0
	}
	r.Score = raw / 4
}
