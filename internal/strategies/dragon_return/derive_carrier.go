package dragon_return

// derive_carrier.go —— 龙回头阈值的**只读导出**（成因与边界见
// internal/strategies/break_down/derive_carrier.go：载体的规则代码是从本包逐行移植的，
// 值必须从同一处取，不许人手抄第二份；抄对了也仍然有两本账）。
//
// 本文件一个数字都没有：每一项都转调 dragon_return.go 里**评分代码正在用的**那批常量。
// 唯一不在本包的是输入派生窗口（30 根门槛、MA5/10/20、40 根主升窗）——那几个数写在
// internal/combat_agent/adapter.go 的 dragonReturnDataFromMarketData 里，由那边自己导出，
// 两处各出各的键、绝不互相代填（代填就是第三本账）。
//
// 置信度（total/100）与 Params 里的止损/止盈/持仓天数不导出：载体不出 watch、
// 也不做退出管理，给一个它用不上的数只会多一本账。
// English: read-only export; every value delegates to the constant the scorer itself uses.

// DeriveCarrierThresholds 返回本战法的全部评分阈值（键名＝载体 params 键名）。
// 返回新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierThresholds() map[string]interface{} {
	return map[string]interface{}{
		// 龙性识别（硬性前置，任一不达标整笔归零）
		"dr_first_rise_min": drFirstRiseMin,
		"dr_first_rise_max": drFirstRiseMax,
		"dr_sector_rps_min": drSectorRPSMin,
		"dr_score_dragon":   drScoreDragon,
		// 回调幅度档（边界成对给出，载体按同一顺序判带）
		"dr_pb_best_lo":       drPBBestLo,
		"dr_pb_best_hi":       drPBBestHi,
		"dr_pb_good_hi":       drPBGoodsHi,
		"dr_pb_shallow_lo":    drPBShallowLo,
		"dr_pb_deep_hi":       drPBDeepHi,
		"dr_pb_score_best":    drPBScoreBest,
		"dr_pb_score_good":    drPBScoreGood,
		"dr_pb_score_shallow": drPBScoreShallow,
		"dr_pb_score_deep":    drPBScoreDeep,
		"dr_pb_score_other":   drPBScoreOther,
		// 回调天数档
		"dr_days_good_lo":     drDaysGoodLo,
		"dr_days_good_hi":     drDaysGoodHi,
		"dr_days_short_lo":    drDaysShortLo,
		"dr_days_long_hi":     drDaysLongHi,
		"dr_days_score_good":  drDaysScoreGood,
		"dr_days_score_near":  drDaysScoreNear,
		"dr_days_score_other": drDaysScoreOther,
		// 缩量比档
		"dr_vr_good":        drVrGood,
		"dr_vr_ok":          drVrOk,
		"dr_vr_mid":         drVrMid,
		"dr_vr_bad":         drVrBad,
		"dr_vr_score_good":  drVrScoreGood,
		"dr_vr_score_ok":    drVrScoreOk,
		"dr_vr_score_mid":   drVrScoreMid,
		"dr_vr_score_bad":   drVrScoreBad,
		"dr_vr_score_other": drVrScoreOther,
		// 鸭头形态四部件
		"dr_shrink_max":         drShrinkMax,
		"dr_duck_top_full":      drDuckTopFull,
		"dr_duck_top_half":      drDuckTopHalf,
		"dr_neck_support_ratio": drNeckSupportRatio,
		"dr_duck_neck_full":     drDuckNeckFull,
		"dr_duck_neck_low":      drDuckNeckLow,
		"dr_slope_prev_ratio":   drSlopePrevRatio,
		"dr_slope_delta_ratio":  drSlopeDeltaRatio,
		"dr_slope_min":          drSlopeMin,
		"dr_duck_slope_bonus":   drDuckSlopeBonus,
		"dr_macd_green_near":    drMacdGreenNear,
		"dr_beak_vr_lo":         drBeakVrLo,
		"dr_beak_vr_hi":         drBeakVrHi,
		"dr_beak_score_three":   drBeakScoreThree,
		"dr_beak_score_two":     drBeakScoreTwo,
		"dr_beak_score_one":     drBeakScoreOne,
		"dr_nostril_full":       drNostrilFull,
		"dr_nostril_half":       drNostrilHalf,
		// 确认信号（量能带 + K 线位置）
		"dr_confirm_vr_lo":     drConfirmVrLo,
		"dr_confirm_vr_hi":     drConfirmVrHi,
		"dr_confirm_vol_full":  drConfirmVolFull,
		"dr_confirm_vr_mid_lo": drConfirmVrMidLo,
		"dr_confirm_vol_mid":   drConfirmVolMid,
		"dr_k_hold_ratio":      drKHoldRatio,
		"dr_k_score_hold":      drKScoreHold,
		"dr_k_score_cross":     drKScoreCross,
		"dr_k_near_lo_ratio":   drKNearLoRatio,
		"dr_k_score_near":      drKScoreNear,
		// 总分档位：载体只在 first 档出单（main/accelerate 在 Go 也是买入，同一门槛线）
		"dr_level_first": drLevelFirst,
	}
}
