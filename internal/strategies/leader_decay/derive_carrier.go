package leader_decay

// derive_carrier.go —— 龙头断板阈值的**只读导出**（成因见 break_down/derive_carrier.go：
// 载体的规则代码是从这些战法移植的，值必须从同一处取，不许人手抄第二份）。
//
// 边界
//   本文件一个数字都没有，全部转调 leader_decay.go 里生产判定正在用的常量。
//   ld_retreat_phase_keys 是把**一个词**装进列表：载体侧要的是列表（一条规则可能有多个
//   退潮写法），Go 判的是 Contains——这里只做装形，不做匹配。
//   为什么这个词必须走 params 而不是写死在移植后的 py 里：产物跑在 GBK 终端上，
//   中文字面量会被静默换成 ?，"含退潮"就永远不成立（本仓锤过的形状）。
//   English: read-only export; the retreat marker is shipped as a param list because a
//   Chinese literal inside the carrier would be mangled by the GBK console.

// DeriveCarrierThresholds 返回本战法的全部评分阈值（键名＝载体 params 键名）。
func DeriveCarrierThresholds() map[string]interface{} {
	return map[string]interface{}{
		// 硬闸与存在前提
		"ld_min_bars":        ldMinBars,
		"ld_min_boards":      ldMinBoards,
		"ld_score_threshold": ldDefaultMinScore,
		// 因子一：连板高度
		"ld_board_max_score":   ldBoardMaxScore,
		"ld_board_full_boards": ldBoardFullBoards,
		"ld_board_base_score":  ldBoardBaseScore,
		"ld_board_base_boards": ldBoardBaseBoards,
		"ld_board_span_boards": ldBoardSpanBoards,
		"ld_board_span_score":  ldBoardSpanScore,
		// 因子二：断板弱势
		"ld_big_loss_ratio":     ldBigLossRatio,
		"ld_big_loss_score":     ldBigLossScore,
		"ld_touched_score":      ldTouchedScore,
		"ld_huge_volume_ratio":  ldHugeVolumeRatio,
		"ld_huge_volume_score":  ldHugeVolumeScore,
		"ld_weakness_max_score": ldWeaknessMaxScore,
		// 因子三：情绪退潮
		"ld_sector_drop_ratio":  ldSectorDropRatio,
		"ld_sector_drop_score":  ldSectorDropScore,
		"ld_phase_score":        ldPhaseScore,
		"ld_retreat_phase_keys": []string{ldPhaseRetreatMark},
		"ld_retreat_max_score":  ldRetreatMaxScore,
	}
}
