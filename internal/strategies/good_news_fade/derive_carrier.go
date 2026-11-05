package good_news_fade

// derive_carrier.go —— 利好兑现砸盘阈值的**只读导出**（成因见 break_down/derive_carrier.go：
// 载体的规则代码是从这些战法移植的，值必须从同一处取，不许人手抄第二份）。
//
// 边界
//   本文件一个数字都没有，全部转调 good_news_fade.go 里生产判定正在用的常量。
//   gf_event_window_days（事件在窗几天）**不在本包**：那个 5 写在适配层
//   buildShortData 的事件族里（adapter.go），所以由 combat_agent 那边导出，
//   两处各出各的键、绝不互相代填——代填就是第三本账。
//   置信度加成（EventScore ≥0.75 时 +0.1）不导出：载体没有置信度字段。
//   English: read-only export of this tactic's constants; the event-window length lives in
//   the adapter and is exported from there, never re-declared here.

// DeriveCarrierThresholds 返回本战法的全部评分阈值（键名＝载体 params 键名）。
func DeriveCarrierThresholds() map[string]interface{} {
	return map[string]interface{}{
		// 硬闸与门槛
		"gf_min_bars":        gfMinBars,
		"gf_score_threshold": gfDefaultMinScore,
		// 因子一：事件新鲜度（满分档→保底档的线性衰减）
		"gf_event_fresh_age": gfEventFreshAge,
		"gf_event_stale_age": gfEventStaleAge,
		"gf_event_max_score": gfEventMaxScore,
		"gf_event_min_score": gfEventMinScore,
		"gf_event_age_span":  gfEventAgeSpan,
		"gf_event_age_drop":  gfEventAgeDrop,
		// 因子二：兑现幅度
		"gf_gain_start":     gfGainStart,
		"gf_gain_full":      gfGainFull,
		"gf_gain_span":      gfGainSpan,
		"gf_gain_max_score": gfGainMaxScore,
		// 因子三：高位转弱
		"gf_stall_pct":          gfStallPct,
		"gf_stall_score":        gfStallScore,
		"gf_heavy_ratio":        gfHeavyRatio,
		"gf_heavy_score":        gfHeavyScore,
		"gf_below_ma5_score":    gfBelowMA5Score,
		"gf_shadow_ratio":       gfShadowRatio,
		"gf_shadow_score":       gfShadowScore,
		"gf_weakness_max_score": gfWeaknessMaxScore,
		// 防误杀：近几根、涨多少算"主升未止"，以及传导类利好的折扣
		"gf_surge_window":         gfSurgeWindow,
		"gf_surge_step_pct":       gfSurgeStepPct,
		"gf_propagation_discount": gfPropagationDiscount,
	}
}
