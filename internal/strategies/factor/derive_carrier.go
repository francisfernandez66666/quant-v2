package factor

// derive_carrier.go —— 因子战法**公共尺度与兜底档**的只读导出（§DERIVE-CARRIER-1105）。
//
// 为什么加这一处（2026-11-05）
//   载体（QMT 内置策略 / 本地执行器）把 scoreRule / percentile / finaScore 整段移植成了
//   qmt_gateway/rules_factor.py，那 14 枚它声明要读的 params 里，有一族就是本包的判据本身：
//   缺省权重、缺省方向、缺省门槛、分值域、分位数最少样本数、财务因子的绝对区间。
//   以前这些数只活在函数体的字面量里，载体侧要么抄一份（抄对了也仍是两本账：本包把门槛
//   调到 65，那份 70 不会跟着动，两条腿就在同一轮里用不同的线判同一个分），要么自己猜。
//   现在本包先把它们命名成常量（factor.go 顶部那组），本文件只转调那些常量，
//   一个数字都不在这里落地。
//
// 三件刻意不做的事
//   1. 不导出 min_bars：它与形态战法共用 strategy.ScoreMinBars（两个战法、一枚门槛），
//      由导出工具从那一处单点供给；这里再出一份就是两处供给同一枚数。
//   2. 不导出 entry_side：Go 出信号用的就是命名常量 strategy.ActionBuy，
//      由导出工具按名字转调（与 rule_side / 四枚 *_kind 同一条路），本包不重写那个串。
//   3. 不导出因子库本身（factor_defs）：internal/factor 的注册表带的是 Compute 闭包，
//      没有可导出的"窗口/方向/输入"元数据；那套描述是移植侧自己的表（rules_factor 的
//      FACTOR_LIBRARY），留在载体侧并在那里登记主人，不在这里凭空造一份。
//   English: read-only export of the scorer's own constants; the shared bar guard, the side
//   and the factor library each keep their existing single owner and are not duplicated here.

import "quant-trading-v2/internal/strategy"

// DeriveCarrierScoring 返回因子评分的尺度与兜底档（键名＝载体 params 键名）。
// 返回新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierScoring() map[string]interface{} {
	return map[string]interface{}{
		"default_buy_threshold":  factorDefaultBuyThreshold,
		"default_weight":         factorDefaultWeight,
		"default_direction":      factorDefaultDirection,
		"percentile_min_samples": percentileMinSamples,
	}
}

// DeriveCarrierScoreDomain 返回复合分与置信度的**分值域**四件套。
// 载体按 {floor, shift, divisor, full} 一张表还原 `(x + shift) / divisor * full`
// 再截到 [floor, full]——即 scoreRule 现在写的这个式子，逐件取自它用的那组常量。
// 为什么导成表而不是导成一个数：这四件是**同一条换算**的四个部件，
// 只搬其中三件（比如漏了 shift）载体就会用自己的猜测补第四件，而那正是"公式看着对、
// 数值差一档"的那种错。
// English: the four pieces of one conversion, exported together so the carrier cannot
// invent the missing one.
func DeriveCarrierScoreDomain() map[string]interface{} {
	return map[string]interface{}{
		"floor":   scoreDomainFloor,
		"shift":   scoreDomainShift,
		"divisor": scoreDomainDivisor,
		"full":    scoreDomainFull,
	}
}

// DeriveCarrierFinancialBands 返回财务因子归一化的绝对区间（finaScore 判据读的那张表）。
// 形状与载体侧 rules_factor 的 financial_bands 一致：{因子: {"scale": 分母}}，
// "越低越好"那一档再多带一个 "offset"（先减后除）。
// 只导**登记过**的档：Go 的 default 分支（未登记＝除以 1）在载体侧是**弃权**而不是除以 1，
// 这是移植时刻意收紧的方向（沙箱拿不到某个财务读数的单位时不许猜），
// 所以这里不把 default 铺成一行——铺了就会让载体那档弃权永远命中不到。
// English: the absolute bands the scorer reads, in the shape the carrier indexes;
// the unlisted-id fallback deliberately stays out (the carrier declines there).
func DeriveCarrierFinancialBands() map[string]interface{} {
	out := make(map[string]interface{}, len(finaBands))
	for fid, band := range finaBands {
		row := map[string]interface{}{"scale": band.Scale}
		if band.Offset != 0 {
			row["offset"] = band.Offset
		}
		out[fid] = row
	}
	return out
}

// DeriveCarrierEntrySide 返回因子战法出信号的动作（"买"）。
// 真源是命名常量 strategy.ActionBuy——本函数只做类型转换，不写那个串；
// 载体侧读的是 params["entry_side"]。
// English: the action the factor tactic emits, taken from the already-named strategy constant.
func DeriveCarrierEntrySide() string { return string(strategy.ActionBuy) }
