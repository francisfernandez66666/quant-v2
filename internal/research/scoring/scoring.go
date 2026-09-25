// Package scoring 时序分位打分（归一化）模块：实盘/回放/B4 回测共用的打分口径之家。
//
// 【§B3 三套打分口径并存声明（owner 裁决 2026-09-26「保留三套但各自标注清楚」）】
// 旧版包头曾声明「因子发现、B4 回测、实盘三处都引用本函数，保证同因子同口径」——该声明不实
// （AUDIT_20260925EVE B3）。实际全仓并存**三套**打分口径，各写各的、结论互不等价，现逐处标注、
// 不做统一（统一会改写历史候选的超额证据，owner 裁决只标注）：
//   - 口径 A「截面 z 后求和」——internal/research/ic.go CompositeICRange（因子发现预筛）：
//     同一事件日把所有股票某因子做 (v-mean)/std，再 Σw·z 复合。不引用本包。
//   - 口径 B「原始值加权和、复合分再截面 z」——internal/research/discover.go compositeScore
//     （候选超额收益证据）：逐因子不归一直接 Σw·dir·v，Top1/5 vs 其余的超额/t 值出自这里。不引用本包。
//   - 口径 C「时序分位×权重」——本包 + 实盘 internal/strategies/factor.scoreRule（同定义、各一份实现）
//     与 B4 回测 internal/backtest/chain.go（调用本包 ScoreValue）。这是**下单**用的口径。
//     实盘分值域为 [50,100]（(total/used+1)/2×100），故扫参门槛网格 40/45 两档恒不过滤（哑火档，
//     见 btreplay/sweep.go defaultPoolConfig ScoreFrom 注释）。
//
// 三套口径的连锁后果（审计原话）：用 A 规则选权重、用 B 规则算证据、用 C 规则下单——
// 候选的"超额收益"不代表按同一机制下单能复现它。引用本包前请先确认调用点属于口径 C。
//
// 为什么口径 C 选「时序分位」而非「截面 z 分数」：
//  1. 与实盘对齐：实盘天然是单股时序分位（无实时全市场横截面可得），研究/回测应与之同源；
//  2. 稳健：分位秩对极端值不敏感（z 分数除以标准差会放大尾部），且天然落在 [0,1]，便于跨因子加权复合；
//  3. 可解释：out[i] 直接读作「该因子值在过去样本里的相对强弱百分位」，业务含义清晰。
//
// 选择 z 分数的代价：需要整个截面同屏才能算 mean/std，且对个股历史不可比；故口径 C 定为时序分位
// （注意：这只是选定 C 的理由，不代表 A/B 已改写到 C——三套并存见上方 §B3 声明）。
package scoring

import "math"

// MinLookback 最短有效历史长度（与实盘 internal/strategies/factor 的 percentile 一致：
// 序列过短（<5）时历史不足，分位无意义，返回 NaN 避免误判）。
const MinLookback = 5

// ScoreSeries 对「单只股票 / 单因子的整段序列」做时序分位归一化，
// 返回与输入等长、值域 [0,1] 的归一化序列（无效位置返回 NaN）。
//
// 入参：
//   - values：某股票某因子的历史序列（例如 sd.FactorVals[fid]）。调用方应传入截至「当前/事件日」的窗口
//     （即 values[:idx+1]），以保证不引入未来数据（无前视泄漏）。
//
// 出参：
//   - out：与 values 等长；out[i] = 序列「除自身外」严格小于 values[i] 的有效值个数 / 其余有效值总数
//     （经验分位秩，排除自身，与实盘 internal/strategies/factor 的 percentile 完全一致：最低值=0、最高值=1）。
//     取值 [0,1]，NaN 表示 values[i] 无效或有效样本不足 MinLookback。
//
// 算法（经验分位秩，排除自身）：
//  1. 先收集全部非 NaN 的有效值；
//  2. 若有效值数量 < MinLookback，所有位置返回 NaN；
//  3. 对每个位置 i，统计「其余有效值」中严格小于 values[i] 的个数，除以「其余有效值总数」得到分位。
//
// 注意：分位是「相对传入窗口」的秩（排除自身，故极值点落在 0/1）。研究/回测传入截至事件日的历史窗口，
// 实盘传入截至今日的完整序列取末位——**在口径 C 内部**（B4 回测↔实盘/回放）三者定义一致；
// 口径 A/B（因子发现预筛、候选超额证据）不走本包，其数字与口径 C 不可直接互比（§B3 并存声明）。
func ScoreSeries(values []float64) []float64 {
	out := make([]float64, len(values))

	// 统计有效值总数（用于 MinLookback 判定与排除自身的分母）。
	total := 0
	for _, v := range values {
		if !math.IsNaN(v) {
			total++
		}
	}
	// 有效样本不足最短回看长度：分位无意义，全部置 NaN。
	if total < MinLookback {
		for i := range out {
			out[i] = math.NaN()
		}
		return out
	}

	// 逐位置计算经验分位秩（排除自身：比较「其余有效值」中严格小于当前值的比例）。
	for i, v := range values {
		if math.IsNaN(v) {
			out[i] = math.NaN()
			continue
		}
		below := 0
		others := 0
		for j, u := range values {
			if i == j || math.IsNaN(u) {
				continue
			}
			others++
			if u < v {
				below++
			}
		}
		// 无其余有效参照（仅自身有效）时无法定位分位，置 NaN。
		if others <= 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = float64(below) / float64(others)
	}
	return out
}

// ScoreValue 便捷封装：返回 current 在 history 序列中的时序分位（[0,1]，无效/样本不足返回 NaN）。
// 等价于 ScoreSeries(append(history, current)) 末位、且排除自身，避免 O(n^2) 全序列重复计算，
// 供回测逐股逐因子高频调用。
//
// 入参：
//   - history：截至当前/事件日的因子历史序列（通常含 current 自身于末位，不应含未来数据）。
//   - current：当前/事件日的因子值。
//
// 出参：current 在 history（排除自身）中的经验分位秩（[0,1]，最高=1/最低=0）；
//
//	history 有效样本 < MinLookback 或 current 无效时返回 NaN。
func ScoreValue(history []float64, current float64) float64 {
	if math.IsNaN(current) {
		return math.NaN()
	}
	// 统计有效值总数（含 current），并计数严格小于 current 的有效值。
	total, below := 0, 0
	for _, u := range history {
		if math.IsNaN(u) {
			continue
		}
		total++
		if u < current {
			below++
		}
	}
	// 有效样本不足最短回看长度，或 history 中仅有 current 自身、无其余参照。
	if total < MinLookback || total-1 <= 0 {
		return math.NaN()
	}
	// 排除自身：分母用其余有效值个数（total-1）。current 自身等于 current，不计入 below。
	return float64(below) / float64(total-1)
}
