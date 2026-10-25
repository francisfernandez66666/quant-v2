// l1.go — §SIZING-EVO L1 离线快筛评估器（周度；纯函数，无 IO）。
//
// 流程（§2.7.3）：
//  1. 反事实重算：对每条已结算快照，用 signalctl.ComputeSizing 在**候选配置**下重算
//     scale（Regime+Signal 画像 → scale）。候选中被硬拦的信号**剔除**（该交易在候选
//     配置下不存在，不是"小一点的交易"）；
//  2. 目标函数：按日期分组的 (scale, 前向收益) 横截面 Spearman IC → IR；
//  3. 真 hold-out：寻优评估只看样本内区间 [ , SplitDay)，样本外仅验证（分段按日期，
//     不用随机洗牌——时序数据随机分段=泄漏）；
//  4. 护栏（确定性顺序，首个不过即拒）：
//     a. 冷启动：样本内可用数 < MinSamples → 不动任何配置；
//     b. MinDays：样本内有效 IC 日 < GuardMinDays；
//     c. GuardMinIR：样本内 IR < 下限（**有方向**：加权必须正向预测前向收益；
//     显著为负是"加权反了"的证据，同样拒绝）；
//     d. Welch t：meanIC/(std/√n) < GuardMinT（显著性，防小样本偶然）；
//     e. hold-out：有效日 < MinHoldoutDays 或 IR ≤ 0 → 样本外不泛化；
//     f. R11 硬护栏：候选中位 scale < 基线中位×MinMedianRatio → "进化到不做生意"，
//     需人工确认（此处直接拒，留待人工通道）；
//     g. 触地板频率 > MaxFloorFreq → 过度保守（辅护栏）；
//  5. R10 分层：样本内按市场状态（bull/range/bear）分组报 IC/IR（晋升判据之一：
//     至少一个主状态分层显著且总体显著）。
//
// 弃权纪律：缺前向收益/Scale<=0 的快照不进样本（不当中性、不伪造）；任一日截面样本
// < MinStocks 或某序列无变差 → 该日跳过。
//
// English: the L1 offline evaluator — counterfactual scale recomputation under the candidate
// config, per-date cross-sectional IC/IR, true date-split hold-out, and ordered guards.
package sizing_evo

import (
	"fmt"
	"math"
	"sort"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
)

// L1Opts L1 评估选项（零值回落内置默认；与 EvoConfig 同名字段一一对应）。
// English: L1 evaluation options (zero values fall back to defaults).
type L1Opts struct {
	MinSamples     int     // 冷启动样本下限（默认 200）
	MinStocks      int     // 每日截面最小样本（默认 10）
	GuardMinIR     float64 // 样本内 IR 下限（默认 0.3，有方向）
	GuardMinDays   int     // 样本内有效 IC 日下限（默认 20）
	GuardMinT      float64 // Welch t 下限（默认 2.0）
	MinHoldoutDays int     // 样本外有效日下限（默认 5）
	SplitDay       string  // hold-out 起始日 YYYYMMDD（空=按 SplitPct 取）
	SplitPct       float64 // 样本内日期占比（默认 0.7）
	MinMedianRatio float64 // R11：候选中位 scale ≥ 基线中位×该值（默认 0.8）
	MaxFloorFreq   float64 // 触地板频率上限（默认 0.5）
	StratMinIR     float64 // R10：分层 IR 下限（默认 0.2）
}

// fill 补零值默认（只补零值，不改显式配置）。
//
// 逐项与 research 侧同名阈值同口径：MinSamples=200 是**冷启动纪律**（样本不足就不动配置），
// GuardMinIR/GuardMinDays/GuardMinT 是"有方向"的三重门（IR 够、天数够、Welch t 显著），
// MinHoldoutDays 保证样本外不是两三天的噪声，SplitPct 落在 (0,1) 之外视为未设。
// MinMedianRatio（R11）与 MaxFloorFreq 是防"进化到不做生意"的护栏，StratMinIR（R10）是分层显著性门。
// English: fills zero-value defaults only. MinSamples=200 is the cold-start discipline; the IR/day/t
// triple is the "has direction" gate; MinMedianRatio/MaxFloorFreq guard against evolving into not
// trading at all; StratMinIR is the per-regime significance gate.
func (o *L1Opts) fill() {
	if o.MinSamples <= 0 {
		o.MinSamples = 200
	}
	if o.MinStocks <= 0 {
		o.MinStocks = 10
	}
	if o.GuardMinIR <= 0 {
		o.GuardMinIR = 0.3
	}
	if o.GuardMinDays <= 0 {
		o.GuardMinDays = 20
	}
	if o.GuardMinT <= 0 {
		o.GuardMinT = 2.0
	}
	if o.MinHoldoutDays <= 0 {
		o.MinHoldoutDays = 5
	}
	if o.SplitPct <= 0 || o.SplitPct >= 1 {
		o.SplitPct = 0.7
	}
	if o.MinMedianRatio <= 0 {
		o.MinMedianRatio = 0.8
	}
	if o.MaxFloorFreq <= 0 {
		o.MaxFloorFreq = 0.5
	}
	if o.StratMinIR <= 0 {
		o.StratMinIR = 0.2
	}
}

// ICStats 一段区间的 IC 汇总。
// English: aggregate IC statistics for one segment.
type ICStats struct {
	ICMean float64 `json:"ic_mean"`
	ICStd  float64 `json:"ic_std"`
	IR     float64 `json:"ir"`
	WelchT float64 `json:"welch_t"`
	NDays  int     `json:"n_days"`
}

// statsOf 由逐日 IC 行算汇总（含 Welch t）。
func statsOf(rows []ICRow) ICStats {
	st := ICStats{NDays: len(rows)}
	if len(rows) == 0 {
		st.ICMean, st.IR, st.WelchT = nan(), nan(), nan()
		return st
	}
	for _, r := range rows {
		st.ICMean += r.IC
	}
	st.ICMean /= float64(len(rows))
	var ss float64
	for _, r := range rows {
		d := r.IC - st.ICMean
		ss += d * d
	}
	if len(rows) > 1 {
		st.ICStd = math.Sqrt(ss / float64(len(rows)-1))
	}
	// 零方差（IC 恒定）或单日 → IR/WelchT 无意义，置 NaN（不伪造显著性）。
	if len(rows) < 2 || st.ICStd <= 0 {
		st.IR, st.WelchT = nan(), nan()
		return st
	}
	st.IR = st.ICMean / st.ICStd
	st.WelchT = st.ICMean / (st.ICStd / math.Sqrt(float64(len(rows))))
	return st
}

// L1Result 一个候选的 L1 评估结果。
// English: the L1 evaluation result for one candidate.
type L1Result struct {
	CandID      string             `json:"cand_id"`
	Origin      string             `json:"origin"`
	Params      map[string]float64 `json:"params"`
	InSample    ICStats            `json:"in_sample"`
	Holdout     ICStats            `json:"holdout"`
	Stratified  map[string]ICStats `json:"stratified,omitempty"` // R10 市场状态分层
	Samples     int                `json:"samples"`              // 样本内可用样本数
	MedianScale float64            `json:"median_scale"`         // 候选中位 scale
	BaseMedian  float64            `json:"base_median"`          // 基线中位 scale
	FloorFreq   float64            `json:"floor_freq"`           // 候选中触地板频率（样本内）
	PassGuard   bool               `json:"pass_guard"`
	Reason      string             `json:"reason"`
	Objective   float64            `json:"objective"` // 寻优目标值（默认 = 样本内 IR）
}

// splitDay 计算 hold-out 起始日（显式 SplitDay 优先；否则按样本内日期数的 SplitPct 取）。
// English: resolves the hold-out start day (explicit first, else by in-sample date share).
func splitDay(snaps []SizingSnapshot, opts L1Opts) string {
	if opts.SplitDay != "" {
		return opts.SplitDay
	}
	seen := map[string]bool{}
	var days []string
	for _, s := range snaps {
		if !s.Usable() || seen[s.Day] {
			continue
		}
		seen[s.Day] = true
		days = append(days, s.Day)
	}
	sort.Strings(days)
	if len(days) < 2 {
		return "" // 不足两天：全部算样本内（hold-out 判"有效日不足"）
	}
	n := int(float64(len(days)) * opts.SplitPct)
	if n < 1 {
		n = 1
	}
	if n >= len(days) {
		n = len(days) - 1
	}
	return days[n]
}

// recomputeScales 在给定配置下重算全部快照的 scale（候选中被硬拦的剔除）。
// 同时返回样本内触地板计数与 scale 序列（R11 中位数/频率用）。
//
// 注意：signalctl.ComputeSizing 从 **Regime.SizeCfg** 读配置（不从入参拿），所以必须把
// 候选配置写进 Regime 副本再调用——直接把 cfg 传给本函数却沿用快照里的旧 Regime 会让
// 候选静默失效（单测 TestL1R11MedianGuard 钉死这一点）。
// English: recomputes scales under cfg; hard-blocked signals are dropped (that trade would
// not exist under the candidate). The candidate config must be injected into the Regime
// copy — ComputeSizing reads Regime.SizeCfg, not a separate argument.
func recomputeScales(cfg config.SizingConfig, snaps []SizingSnapshot) ([]ICSample, []float64, int) {
	var ics []ICSample
	var scales []float64
	floored := 0
	for _, s := range snaps {
		if !s.Usable() {
			continue
		}
		reg := s.Regime
		reg.SizeCfg = cfg // 候选配置注入（ComputeSizing 的唯一读点）
		res := signalctl.ComputeSizing(reg, s.Sig.ToSignal(s.Code))
		if res.Downgrade {
			continue // 候选配置下该买入被硬拦 → 没有这个交易，不进样本
		}
		ret := *s.FwdRet
		ics = append(ics, ICSample{Date: s.Day, Code: s.Code, Scale: res.Scale, Ret: ret, State: s.Regime.MarketState})
		scales = append(scales, res.Scale)
		if cfg.Floor > 0 && res.Scale <= cfg.Floor+1e-9 {
			floored++
		}
	}
	return ics, scales, floored
}

// median 中位数（空序列 → NaN）。
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return nan()
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	n := len(ys)
	if n%2 == 1 {
		return ys[n/2]
	}
	return (ys[n/2-1] + ys[n/2]) / 2
}

// EvaluateCandidate L1 主入口：评估一个候选（确定性，纯函数）。
// English: the L1 entry — evaluates one candidate deterministically.
func EvaluateCandidate(base config.SizingConfig, snaps []SizingSnapshot, c Candidate, ps *ParamSpace, opts L1Opts) L1Result {
	opts.fill()
	cfg := c.Apply(base, ps)
	res := L1Result{CandID: c.ID, Origin: c.Origin, Params: cloneParams(c.Params)}

	// 1~2. 反事实重算 + 基线中位数（R11 对照）。
	icsAll, scales, floored := recomputeScales(cfg, snaps)
	_, baseScales, _ := recomputeScales(base, snaps)
	res.MedianScale = median(scales)
	res.BaseMedian = median(baseScales)
	if len(scales) > 0 {
		res.FloorFreq = float64(floored) / float64(len(scales))
	}

	// 3. 日期分段（样本内/样本外）。
	sd := splitDay(snaps, opts)
	var in, out []ICSample
	for _, s := range icsAll {
		if sd != "" && s.Date >= sd {
			out = append(out, s)
		} else {
			in = append(in, s)
		}
	}
	res.Samples = len(in)
	res.InSample = statsOf(ICByDate(in, opts.MinStocks))
	res.Holdout = statsOf(ICByDate(out, opts.MinStocks))
	res.Objective = res.InSample.IR

	// 4. 护栏（顺序即优先级；首个不过即拒，理由可归因）。
	switch {
	case res.Samples < opts.MinSamples:
		res.Reason = fmt.Sprintf("冷启动样本不足(%d<%d)", res.Samples, opts.MinSamples)
	case res.InSample.NDays < opts.GuardMinDays:
		res.Reason = fmt.Sprintf("样本内有效日不足(%d<%d)", res.InSample.NDays, opts.GuardMinDays)
	case math.IsNaN(res.InSample.IR) || res.InSample.IR < opts.GuardMinIR:
		res.Reason = fmt.Sprintf("样本内IR不足(%v<%.3f)", res.InSample.IR, opts.GuardMinIR)
	case math.IsNaN(res.InSample.WelchT) || res.InSample.WelchT < opts.GuardMinT:
		res.Reason = fmt.Sprintf("显著性不足(t=%v<%.2f)", res.InSample.WelchT, opts.GuardMinT)
	case res.Holdout.NDays < opts.MinHoldoutDays:
		res.Reason = fmt.Sprintf("样本外有效日不足(%d<%d)", res.Holdout.NDays, opts.MinHoldoutDays)
	case math.IsNaN(res.Holdout.IR) || res.Holdout.IR <= 0:
		res.Reason = fmt.Sprintf("样本外不同向(IR=%v)", res.Holdout.IR)
	case !math.IsNaN(res.BaseMedian) && res.MedianScale < res.BaseMedian*opts.MinMedianRatio:
		res.Reason = fmt.Sprintf("中位scale过低(%.3f<基线%.3f×%.2f，R11)", res.MedianScale, res.BaseMedian, opts.MinMedianRatio)
	case res.FloorFreq > opts.MaxFloorFreq:
		res.Reason = fmt.Sprintf("触地板频率过高(%.0f%%>%.0f%%)", res.FloorFreq*100, opts.MaxFloorFreq*100)
	default:
		// R10 分层：至少一个非空状态分层达到 StratMinIR（防"只在牛市有效"被晋升）。
		if !stratPass(in, opts) {
			res.Reason = "市场状态分层不显著（R10）"
			break
		}
		res.Stratified = stratifiedStats(in, opts)
		res.PassGuard = true
		res.Reason = "通过护栏"
	}
	if !res.PassGuard {
		res.Objective = nan() // 未过护栏的候选不进寻优目标
	}
	return res
}

// stratPass R10：至少一个状态分层的 IR ≥ StratMinIR（空键"未分层"也算一层）。
// English: at least one market-state stratum must reach the stratified IR floor.
func stratPass(in []ICSample, opts L1Opts) bool {
	for _, rows := range ICByState(in, opts.MinStocks) {
		if st := statsOf(rows); !math.IsNaN(st.IR) && st.IR >= opts.StratMinIR {
			return true
		}
	}
	return false
}

// stratifiedStats 各状态分层汇总（map[state]ICStats）。
func stratifiedStats(in []ICSample, opts L1Opts) map[string]ICStats {
	byState := ICByState(in, opts.MinStocks)
	out := make(map[string]ICStats, len(byState))
	for st, rows := range byState {
		out[st] = statsOf(rows)
	}
	return out
}

// BestCandidate 从评估结果里取目标最高且过护栏的候选（确定性：同分取 CandID 字典序小者）。
// English: picks the highest-objective passing candidate (ties broken by ID for determinism).
func BestCandidate(results []L1Result) (L1Result, bool) {
	var best L1Result
	found := false
	for _, r := range results {
		if !r.PassGuard || math.IsNaN(r.Objective) {
			continue
		}
		if !found || r.Objective > best.Objective ||
			(r.Objective == best.Objective && r.CandID < best.CandID) {
			best, found = r, true
		}
	}
	return best, found
}
