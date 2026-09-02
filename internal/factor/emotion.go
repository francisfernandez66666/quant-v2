// 情绪因子（§Phase2 盘口/情绪因子入池）。
// 素材为市场每日情绪统计（涨停家数/炸板率/最高连板），由调用方经 Store.EmotionStatsRange
// 预装载进 StockSeries.Emo* 字段。情绪属于"市场状态因子"——当日全体标的取值相同，
// 不适合单独做横截面 IC（IC 无区分度），主要用于：情绪相位分组回测、与风格因子交叉、
// 或作为研究面板的降维展示输入。
// English: market-sentiment factors (Phase 2). Built from per-day market sentiment stats
// (limit-up count / blast rate / max board height) preloaded by the caller into StockSeries.Emo*.
// These are market-state factors — every symbol shares the same value on a given day, so they are
// not suited to standalone cross-sectional IC; they serve emotion-phase-grouped backtests,
// crossovers with style factors, or panel reduction views.
package factor

import "math"

// sentimentFactor 生成情绪因子计算函数。
// kind 决定取哪列；缺失（NaN）序列全 NaN，由 B3 正常过滤。
// English: sentimentFactor builds an emotion-factor compute func; missing input yields all-NaN.
func sentimentFactor(sel func(*StockSeries) []float64) func(*StockSeries) []float64 {
	return func(s *StockSeries) []float64 {
		return sel(s)
	}
}

// emoArray 安全取情绪列，长度不足返回 nil（用例层过滤）。
func emoArray(s *StockSeries, f func(*StockSeries) []float64) []float64 {
	if s == nil || len(s.Dates) == 0 {
		return nil
	}
	return f(s)
}

func init() {
	Register(Def{
		ID:   "emo_limit_up",
		Name: "涨停家数",
		Cat:  CatSentiment,
		Desc: "当日全市场涨停家数（情绪温度：高=赚钱效应强）",
		Compute: func(s *StockSeries) []float64 {
			return emoArray(s, func(x *StockSeries) []float64 { return x.EmoLimitUp })
		},
	})
	Register(Def{
		ID:   "emo_blast_rate",
		Name: "炸板率",
		Cat:  CatSentiment,
		Desc: "当日炸板率 %（炸板/涨停+炸板）：高=情绪退潮风险",
		Compute: func(s *StockSeries) []float64 {
			return emoArray(s, func(x *StockSeries) []float64 { return x.EmoBlastRate })
		},
	})
	Register(Def{
		ID:   "emo_max_board",
		Name: "最高连板",
		Cat:  CatSentiment,
		Desc: "当日全市场最高连板高度（空间高度：高=低位补涨预期）",
		Compute: func(s *StockSeries) []float64 {
			return emoArray(s, func(x *StockSeries) []float64 { return x.EmoMaxBoard })
		},
	})
	Register(Def{
		ID:   "emo_blast_cnt",
		Name: "炸板家数",
		Cat:  CatSentiment,
		Desc: "当日炸板家数（绝对量，配合涨停家数判断情绪拐点）",
		Compute: func(s *StockSeries) []float64 {
			return emoArray(s, func(x *StockSeries) []float64 { return x.EmoBreakCnt })
		},
	})
	Register(Def{
		ID:   "emo_chg5",
		Name: "情绪5日变化",
		Cat:  CatSentiment,
		Desc: "涨停家数 5 日变化（当日 − 5 日前；正=情绪升温，负=退潮）",
		Compute: func(s *StockSeries) []float64 {
			v := emoArray(s, func(x *StockSeries) []float64 { return x.EmoLimitUp })
			if len(v) < 5 {
				return v
			}
			out := make([]float64, len(v))
			for i := range out {
				if i < 5 || math.IsNaN(v[i]) || math.IsNaN(v[i-5]) {
					out[i] = math.NaN()
				} else {
					out[i] = v[i] - v[i-5]
				}
			}
			return out
		},
	})
}
