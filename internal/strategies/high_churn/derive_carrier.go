package high_churn

// derive_carrier.go —— 高位滞涨阈值的**只读导出**（成因见 break_down/derive_carrier.go，
// 同一条链：载体的规则代码是从这些战法移植的，值必须从同一处取，不许人手抄第二份）。
//
// 边界
//   本文件一个数字都没有：每一项都转调 high_churn.go 里**生产判定正在用的**常量。
//   置信度加成（+0.1）与 0.95 封顶没有导出——载体不发 watch、也没有置信度字段，
//   给一个它用不上的数只会多一本账。
//   English: read-only export; every value delegates to the constant the scorer itself uses.

// DeriveCarrierThresholds 返回本战法的全部评分阈值（键名＝载体 params 键名）。
func DeriveCarrierThresholds() map[string]interface{} {
	return map[string]interface{}{
		"hc_min_bars":        hcMinBars,
		"hc_score_threshold": hcDefaultMinScore,
		// 因子一：位置高度（现价口径与 20 日涨幅口径取高者，共用同一枚帽）
		"hc_pos_start":          hcPosStart,
		"hc_pos_span":           hcPosSpan,
		"hc_pos_base_score":     hcPosBaseScore,
		"hc_pos_step_score":     hcPosStepScore,
		"hc_gain_start":         hcGainStart,
		"hc_gain_span":          hcGainSpan,
		"hc_gain_base_score":    hcGainBaseScore,
		"hc_gain_step_score":    hcGainStepScore,
		"hc_position_max_score": hcPositionMaxScore,
		// 因子二：量价背离
		"hc_div_heavy_ratio": hcDivHeavyRatio,
		"hc_div_stall_pct":   hcDivStallPct,
		"hc_div_full_score":  hcDivFullScore,
		"hc_div_half_score":  hcDivHalfScore,
		// 因子三：K 线走弱（逐项累计后封顶）
		"hc_shadow_ratio":       hcShadowRatio,
		"hc_shadow_score":       hcShadowScore,
		"hc_below_ma5_score":    hcBelowMA5Score,
		"hc_consec_down_days":   hcConsecDownDays,
		"hc_consec_down_score":  hcConsecDownScore,
		"hc_weakness_max_score": hcWeaknessMaxScore,
	}
}
