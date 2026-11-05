package break_down

// derive_carrier.go —— 放量破位阈值的**只读导出**。
//
// 为什么加这一处（2026-11-05）
//   QMT 内置策略与本地执行器要把这四条做空战法烘进自己的参数文件（券商 11-05 公告之后，
//   "信号完全在终端内部产生"是唯一的自动通道）。载体侧那份 rules_form.py 逐条写着
//   "这个数的真源在 break_down.go:148"，但**值从来没有跟过来**：102 枚键在
//   local_executor_params.json 里一个都不存在，于是产物里每一条移植规则第一次调用就
//   raise KeyError('bd_min_bars')——规则代码搬完了，阈值没搬。
//
//   本文件就是"把值带过来"的那一处：它一个数字都不定义，全部转调 break_down.go 里
//   **生产代码正在用的那批常量**（同一份定义，改这里不会造成第二本账）。
//   键名与载体 params 同形，导出工具因此不需要再翻译一遍。
//
// 边界
//   只读、不改区间、不填默认、不加新常量。Go 侧没命名的数（比如 watch 档的 50、
//   置信度封顶 0.95）一律不导出——载体不收一份它无法核对的数。
//   English: read-only export of the tactic's own constants; no new number lives here.

// DeriveCarrierThresholds 返回本战法的全部评分阈值（键名＝载体 params 键名）。
// 返回的是新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierThresholds() map[string]interface{} {
	return map[string]interface{}{
		// 硬闸与门槛
		"bd_min_bars":        bdMinBars,
		"bd_score_threshold": bdDefaultMinScore,
		// 因子一：破位深度
		"bd_depth_max_score":  bdDepthMaxScore,
		"bd_depth_single_cap": bdDepthSingleCap,
		"bd_depth_full_pct":   bdDepthFullPct,
		// 因子二：量能确认
		"bd_volume_max_score":     bdVolumeMaxScore,
		"bd_volume_full_ratio":    bdVolumeFullRatio,
		"bd_volume_half_ratio":    bdVolumeHalfRatio,
		"bd_volume_shrink_factor": bdVolumeShrink,
		// 因子三：日内弱势（三档边界都带符号，与 Go 判据同向）
		"bd_intraday_max_score":         bdIntradayMaxScore,
		"bd_intraday_severe_pct":        bdIntradaySeverePct,
		"bd_intraday_mid_pct":           bdIntradayMidPct,
		"bd_intraday_mid_base_score":    bdIntradayMidBase,
		"bd_intraday_severe_span_pct":   bdSevereSpanPct,
		"bd_intraday_severe_span_score": bdSevereSpanScore,
		"bd_intraday_flat_span_pct":     bdFlatSpanPct,
		"bd_intraday_flat_span_score":   bdFlatSpanScore,
		"bd_shadow_add_ratio":           bdShadowAddRatio,
		"bd_shadow_add_score":           bdShadowAddScore,
		// 防洗盘缓判
		"bd_shakeout_depth_pct": bdShakeoutDepthPct,
		"bd_shakeout_discount":  bdShakeoutDiscount,
	}
}
