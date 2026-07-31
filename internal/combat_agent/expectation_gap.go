// 预期差检测：新闻预期 vs 股价实际反应的偏差。
// 借鉴 astock-market-engine market_reasoning_engine 的 6 类预期差：
// 利好不涨 / 利空不跌 / 放量涨次日弱(需次日数据,暂跳过) / 高位放量滞涨 / 缩量上涨 / 业绩利好反而跌。
// 每类 0-4 分规则累计，归一化后 ≥0.4 触发提醒信号。
package combat_agent

import (
	"strings"
)

// gapType 预期差类型常量。
const (
	GapBullishNoRise   = "利好不涨"   // 有明确利好新闻，股价却未涨
	GapBearishNoDrop   = "利空不跌"   // 有明确利空新闻，股价却不跌反涨
	GapHighVolSluggish = "高位放量滞涨" // 放量但涨幅不足（滞涨）
	GapShrinkRise      = "缩量上涨"    // 缩量创新高/上涨，承接弱
	GapEarningsFall    = "业绩利好反而跌" // 业绩增长但股价下跌
)

// ExpectationGapResult 预期差检测结果。
type ExpectationGapResult struct {
	GapType string  // 预期差类型
	Score   float64 // 归一化 0-1
	Trigger bool    // 是否触发提醒（>=0.4）
	Reason  string  // 可读描述
}

// CheckExpectationGap 检测单只股票的预期差。
// newsText 为该股关联新闻文本，positive 表示新闻方向（true=利好/利空取反）。
// changePct 为当前涨跌幅(%)，turnover 为换手率(%)，volRatio 为量比（当日量/近5日均量，未知传0）。
// 无新闻方向或新闻文本为空时返回不触发。
func CheckExpectationGap(newsText string, positive bool, changePct, turnover, volRatio float64) ExpectationGapResult {
	res := ExpectationGapResult{Score: 0}
	newsText = strings.TrimSpace(newsText)
	if newsText == "" {
		return res
	}

	if positive {
		// 利好方向
		switch {
		case changePct <= 0:
			// 利好不涨：跌幅越大分越高（0-4）
			raw := 4.0
			if changePct > -3 {
				raw = 2 + (-changePct)*0.6
			}
			res.GapType = GapBullishNoRise
			res.Reason = "有利好新闻但股价未涨"
			res.setNorm(raw)
		case changePct < 3:
			// 利好反应不足
			raw := 1.5 + (3-changePct)*0.5
			res.GapType = GapBullishNoRise
			res.Reason = "有利好新闻但涨幅不足"
			res.setNorm(raw)
		case changePct >= 3 && changePct < 9.5:
			// 高位放量滞涨：涨幅 3-9.5% 且换手异常放大
			if turnover > 20 && volRatio > 2 {
				res.GapType = GapHighVolSluggish
				res.Reason = "放量滞涨，承接盘待观察"
				res.setNorm(3.5)
			} else if volRatio < 0.7 {
				res.GapType = GapShrinkRise
				res.Reason = "缩量上涨，量能承接不足"
				res.setNorm(2.5)
			} else {
				// 正常反应，不触发
				res.Score = 0
				return res
			}
		default:
			// 涨停或大幅上涨：利好兑现，不触发
			res.Score = 0
			return res
		}
		res.Trigger = res.Score >= 0.4
		return res
	}

	// 利空方向
	switch {
	case changePct >= 0:
		res.GapType = GapBearishNoDrop
		res.Reason = "有利空新闻但股价不跌反涨"
		res.setNorm(2.0 + changePct*0.4)
	case changePct > -3:
		res.GapType = GapBearishNoDrop
		res.Reason = "有利空新闻但跌幅不足"
		res.setNorm(1.5 + (3+changePct)*0.4)
	default:
		// 利空兑现，不触发
		res.Score = 0
		return res
	}
	res.Trigger = res.Score >= 0.4
	return res
}

// setNorm 将 0-4 分归一化为 0-1 并写入 Score。
func (r *ExpectationGapResult) setNorm(raw float64) {
	if raw > 4 {
		raw = 4
	}
	if raw < 0 {
		raw = 0
	}
	r.Score = raw / 4
}
