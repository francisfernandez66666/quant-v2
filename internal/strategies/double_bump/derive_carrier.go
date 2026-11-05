package double_bump

// derive_carrier.go —— 双响炮阈值的**只读导出**（成因与边界见
// internal/strategies/break_down/derive_carrier.go：载体的规则代码是从本包逐行移植的，
// 值必须从同一处取，不许人手抄第二份）。
//
// 与破局龙同一族两处分：
//   - 窗口/比例/档位（10 根门槛、−1.5% 走弱线、20 根基线、近 5 根找第一波、1.05 上破、
//     窄幅带的宽度倍数、MA5/MA10、80% 排列折扣、full_chain 60）在 double_bump.go 的 db* 单点；
//   - 一突/二突放量倍数与三维权重是**账号级配置**（rules.strategy.double_bump），
//     出厂值在 config.defaultStrategyConfig，本文件按"没配过的号会得到什么"取。
//
// db_adjust_max_pct 是**乘出来的**（带 = AdjustVolRatioMax × 宽度倍数）：这不是工具在算数，
// 而是 Go 判据本身就是这个乘积（double_bump.go 的窄幅闸与 §0929DIM 的理由文案同式），
// 导出的是判据的**生效值**；只出一半（3）会让载体自己去乘那枚没人管的倍数。
// English: read-only export; bands come from this package's constants, the volume multiples and
// the three weights from the factory strategy config, and the narrow-band limit is the value the
// Go gate actually compares against (base x width factor, same expression as the scorer).

import "quant-trading-v2/internal/config"

// DeriveCarrierThresholds 返回本战法的全部评分阈值与出厂权重（键名＝载体 params 键名）。
// 返回新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierThresholds() map[string]interface{} {
	// 出厂配置：与 EvaluateReal 里 `dbc := d.strategyCfg()` 在"没配过"时拿到的是同一张表。
	dbc := config.DefaultStrategyConfig().DoubleBump
	return map[string]interface{}{
		// 硬闸与当日方向
		"db_min_bars":       dbMinBars,
		"db_weak_day_pct":   dbWeakDayPct,
		"db_min_change_pct": dbc.MinChangePct,
		// 基线与第一波
		"db_lookback":            dbLookbackBars,
		"db_first_window":        dbFirstWaveWindow,
		"db_break_ratio":         dbBreakRatio,
		"db_first_vol_multiple":  dbc.FirstBreakVolumeMultiple,
		"db_second_vol_multiple": dbc.SecondBreakVolumeMultiple,
		"db_volume_weight":       dbc.VolumeWeight,
		// 调整（窄幅）带
		"db_adjust_max_pct":  dbc.AdjustVolRatioMax * dbAdjustWidthFactor,
		"db_position_weight": dbc.PositionWeight,
		// 均线排列
		"db_ma_short":       dbMaShort,
		"db_ma_mid":         dbMaMid,
		"db_ma_weight":      dbc.MAWeight,
		"db_ma_align_ratio": dbMaAlignRatio,
		// 出单档：只有 full_chain 会变成买入
		"db_full_chain_min": dbFullChainMin,
	}
}
