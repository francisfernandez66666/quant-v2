// robust.go §ENH-B 多重检验稳健性护栏（纯 Go，无新依赖）。
//
// 背景：夜间 grid/坐标上升每晚评估上千组合（#260 实测 5 小时），选出的"最优"天然
// 高估（selection bias / multiple testing）——这是"回测看着好、审批不敢用"的方法论
// 缺口。本模块移植两件轻量武器：
//
//  1. Deflated IR（Deflated Sharpe Ratio 的日频 IC-IR 简化版，Bailey & López de Prado 思路）：
//     N 次独立试验下纯噪声的最大期望 IR ≈ sqrt(2·ln N / T)（标准正态极值期望 × IR 抽样
//     波动 1/√T）。DSR = 观测 IR − 该折减项：DSR≤0 表示"最优值与噪声里海选出来的最好
//     结果不可区分"。此处省略偏度/峰度修正项（日频 IC 近似正态，且仓库护栏本就是
//     均值/标准差口径），保持可复算的极简式。
//     English: simplified Deflated Sharpe for daily IC-IR — observed IR minus the expected
//     maximum of N noise trials (sqrt(2 ln N / T)); skew/kurtosis adjustment omitted by design.
//
//  2. PBO-lite（CSCV 简化版）：把样本外 IC 序列切 K 个连续块，统计与全段同号的块数。
//     同号率 <50% 说明全段 IR 靠个别块撑起（回测过拟合/制度碎片），跨块稳定性不足。
//     English: K-block sign-consistency on the OOS segment; <50% same-sign blocks flags
//     backtest-overfitting risk.
//
// 接入语义（观察期先软标注，不做硬 reject）：DSR<0 → C2 护栏降一档（strong→standard→weak），
// reason 透出 DSR/trials/PBO 供审批人决策。
package research

import "math"

// DeflatedIR 返回折减后 IR：ir − sqrt(2·ln(N)/T)。trials≤1 或 days≤1 时无折减意义，
// 原样返回 ir（单试验无选择性偏差）。
// English: ir minus the expected max IR of `trials` noise runs over `days` observations.
func DeflatedIR(ir float64, trials, days int) float64 {
	if trials <= 1 || days <= 1 {
		return ir
	}
	deflate := math.Sqrt(2 * math.Log(float64(trials)) / float64(days))
	return ir - deflate
}

// PBOSignConsistency 把 OOS IC 行按时间序切 blocks 个连续块，返回 (与全段同号且非零的块数, 非平凡块数)。
// 块内样本 <minPerBlock 或 IR=NaN/0 的块不计入分母（无观测不判定，保守）。
// English: splits OOS rows into contiguous blocks and counts blocks whose IR sign matches
// the whole segment; trivial/empty blocks are excluded from the denominator.
func PBOSignConsistency(rows []ICRow, blocks, minPerBlock int) (int, int) {
	if blocks <= 0 {
		blocks = 4
	}
	if minPerBlock <= 0 {
		minPerBlock = 5
	}
	overall := irOrZero(rows)
	if overall == 0 || len(rows) < blocks*minPerBlock {
		return 0, 0
	}
	per := len(rows) / blocks
	consistent, total := 0, 0
	for b := 0; b < blocks; b++ {
		lo := b * per
		hi := lo + per
		if b == blocks-1 {
			hi = len(rows) // 末块吃掉余数，块间无缝无叠
		}
		ir := irOrZero(rows[lo:hi])
		if ir == 0 {
			continue
		}
		total++
		if (ir > 0) == (overall > 0) {
			consistent++
		}
	}
	return consistent, total
}
