// validate_backtest.go BacktestConfig 保存入口校验（A0.2）：
// 调用点在 server PUT /api/research/backtest-config——BacktestConfig 不挂 Rules，
// 故不进 Validate() 主函数（与第二轮方案不同，第三轮管线定稿）。
// English: validation for the backtest enhancement config, invoked at the server PUT
// /api/research/backtest-config save entry (not part of the Rules pipeline).
package config

import (
	"fmt"
	"sort"
)

// ValidateBacktest 校验回测增强配置的取值域与分档表单调性；非法返回聚合错误。
func ValidateBacktest(cfg *BacktestConfig) error {
	if cfg == nil {
		return fmt.Errorf("backtest 配置为空")
	}
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if cfg.OrderValueYuan < 0 || cfg.OrderValueYuan > 1e8 {
		add("order_value_yuan=%v 越界（0~1e8 元）", cfg.OrderValueYuan)
	}
	if cfg.PaperModelSlippageBps < 0 || cfg.PaperModelSlippageBps > 50 {
		add("paper_model_slippage_bps=%v 越界（0~50bp）", cfg.PaperModelSlippageBps)
	}

	s := &cfg.Slippage
	if s.BaseBps < 0 || s.BaseBps > 50 {
		add("slippage.base_bps=%v 越界（0~50bp）", s.BaseBps)
	}
	if s.BuyExtraBps < 0 || s.BuyExtraBps > 50 {
		add("slippage.buy_extra_bps=%v 越界（0~50bp）", s.BuyExtraBps)
	}
	if s.CalibMinSample < 0 || s.CalibMinSample > 10000 {
		add("slippage.calib_min_sample=%d 越界（0~10000）", s.CalibMinSample)
	}
	if s.CalibWindowDays < 0 || s.CalibWindowDays > 730 {
		add("slippage.calib_window_days=%d 越界（0~730）", s.CalibWindowDays)
	}
	// 分档表：非负 + 单调（VolumeTiers 按 max_volume_wan 升序且罚点递减——越流动罚越少；
	// SizeTiers 按 min_ratio 降序且罚点递减——占比越大冲击越重罚越多），
	// 引擎按序线性扫描，乱序配置会静默错档，保存端直接拒绝。
	for i, t := range s.VolumeTiers {
		if t.MaxVolumeWan <= 0 || t.ExtraBps < 0 || t.ExtraBps > 50 {
			add("volume_tiers[%d] 非法（max_volume_wan>0 且 extra_bps∈[0,50]）", i)
		}
		if i > 0 && t.MaxVolumeWan <= s.VolumeTiers[i-1].MaxVolumeWan {
			add("volume_tiers[%d].max_volume_wan 必须严格升序", i)
		}
		if i > 0 && t.ExtraBps >= s.VolumeTiers[i-1].ExtraBps {
			add("volume_tiers[%d].extra_bps 必须随成交额档位上升而递减（越不流动罚得越多）", i)
		}
	}
	for i, t := range s.SizeTiers {
		if t.MinRatio <= 0 || t.MinRatio > 1 || t.ExtraBps < 0 || t.ExtraBps > 50 {
			add("size_tiers[%d] 非法（min_ratio∈(0,1] 且 extra_bps∈[0,50]）", i)
		}
		if i > 0 && t.MinRatio >= s.SizeTiers[i-1].MinRatio {
			add("size_tiers[%d].min_ratio 必须严格降序", i)
		}
		if i > 0 && t.ExtraBps > s.SizeTiers[i-1].ExtraBps {
			add("size_tiers[%d].extra_bps 必须随占比档位下降而递减（冲击越大罚得越多）", i)
		}
	}

	l := &cfg.Liquidity
	if l.LimitUpOpenableExtraBps < 0 || l.LimitUpOpenableExtraBps > 50 {
		add("liquidity.limit_up_openable_extra_bps=%v 越界（0~50bp）", l.LimitUpOpenableExtraBps)
	}
	if l.LimitDownSealedExtraBps < 0 || l.LimitDownSealedExtraBps > 50 {
		add("liquidity.limit_down_sealed_extra_bps=%v 越界（0~50bp）", l.LimitDownSealedExtraBps)
	}
	if l.FillRateMin < 0 || l.FillRateMin > 1 {
		add("liquidity.fill_rate_min=%v 越界（0~1）", l.FillRateMin)
	}

	p := &cfg.Pareto
	if p.MinWinRate < 0 || p.MinWinRate > 100 {
		add("pareto.min_win_rate=%v 越界（0~100）", p.MinWinRate)
	}
	if p.MinProfitFactor < 0 || p.MinProfitFactor > 100 {
		add("pareto.min_profit_factor=%v 越界（0~100）", p.MinProfitFactor)
	}
	if p.MinSharpe < 0 || p.MinSharpe > 20 {
		add("pareto.min_sharpe=%v 越界（0~20）", p.MinSharpe)
	}
	if p.MinCalmar < 0 || p.MinCalmar > 100 {
		add("pareto.min_calmar=%v 越界（0~100）", p.MinCalmar)
	}
	if p.MaxFrontPoints < 0 || p.MaxFrontPoints > 1000 {
		add("pareto.max_front_points=%d 越界（0~1000）", p.MaxFrontPoints)
	}

	w := &cfg.WalkForward
	if w.TrainDays < 0 || w.TrainDays > 2000 {
		add("walk_forward.train_days=%d 越界（0~2000）", w.TrainDays)
	}
	if w.ValidateDays < 0 || w.ValidateDays > 1000 {
		add("walk_forward.validate_days=%d 越界（0~1000）", w.ValidateDays)
	}
	if w.StepDays < 0 || w.StepDays > 1000 {
		add("walk_forward.step_days=%d 越界（0~1000）", w.StepDays)
	}
	if w.CandidateTopN < 0 || w.CandidateTopN > 1000 {
		add("walk_forward.candidate_top_n=%d 越界（0~1000）", w.CandidateTopN)
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("backtest 配置非法: %v", errs)
	}
	return nil
}
