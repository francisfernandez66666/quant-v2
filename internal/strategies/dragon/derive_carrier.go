package dragon

// derive_carrier.go —— 破局龙阈值的**只读导出**（成因与边界见
// internal/strategies/break_down/derive_carrier.go：载体的规则代码是从本包逐行移植的，
// 值必须从同一处取，不许人手抄第二份）。
//
// 两族数分开看，这是本文件唯一需要解释的地方：
//   - **档位/容忍/比例**（贴板容忍、90%/10%、板块 3%/1%、溢价 2pp、自身 5%、RS 窗口与
//     10%/5%、full_chain 60）在 Go 是**评分函数里的常量**（dragon.go 的 dg* 单点），这里转调；
//   - **F1~F4 权重**在 Go 是**账号级配置**（rules.strategy.dragon），出厂值来自
//     config.defaultStrategyConfig。导出的是**出厂值**——与整个工具的口径一致
//     （"一个从没被账号改过的号会得到什么"），现网若改过权重，那是账号配置，
//     由装填侧按账号写，不是出厂默认。
//
// 分值域（pct_scale）不在本表：它与双响炮共用 strategy.ScoreScale 一枚，由导出工具单点供给，
// 两处供给同一个载体键正是本工具判红的形状（collector.put 见到重复键就退出）。
// brief/watch 两档不导出：Go 在那两档出的是 ActionWatch，载体不出单，因此没有对应的读点。
// English: read-only export; bands come from this package's own constants, the four weights
// come from the factory strategy config, and the shared score scale is supplied elsewhere.

import "quant-trading-v2/internal/config"

// DeriveCarrierThresholds 返回本战法的全部评分阈值与出厂权重（键名＝载体 params 键名）。
// 返回新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierThresholds() map[string]interface{} {
	// 出厂权重：与 EvaluateReal 里 `dc := d.strategyCfg()` 在"没配过"时拿到的是同一份表。
	dc := config.DefaultStrategyConfig().Dragon
	return map[string]interface{}{
		// 硬闸与封板容忍
		"dragon_min_bars":           dgMinBars,
		"dragon_seal_tolerance_pct": dgSealTolerance,
		// F1 封板质量
		"dragon_f1_weight":     dc.F1SealWeight,
		"dragon_f1_base_ratio": dgSealBaseRatio,
		"dragon_f1_vol_ratio":  dgSealVolRatio,
		// F2 板块共振
		"dragon_f2_weight":     dc.F2ResonanceWeight,
		"dragon_f2_full_pct":   dgSectorFullPct,
		"dragon_f2_half_pct":   dgSectorHalfPct,
		"dragon_f2_half_ratio": dgWeightHalf,
		// F3 溢价率
		"dragon_f3_weight":       dc.F3PremiumWeight,
		"dragon_f3_premium_pct":  dgPremiumPct,
		"dragon_f3_own_gain_pct": dgOwnGainPct,
		"dragon_f3_half_ratio":   dgWeightHalf,
		// F4 五日相对强度
		"dragon_rs_window":     dgRsWindow,
		"dragon_f4_weight":     dc.F4RsWeight,
		"dragon_f4_full_pct":   dgRsFullPct,
		"dragon_f4_half_pct":   dgRsHalfPct,
		"dragon_f4_half_ratio": dgWeightHalf,
		// 出单档：只有 full_chain 会变成买入
		"dragon_full_chain_min": dgFullChainMin,
	}
}
