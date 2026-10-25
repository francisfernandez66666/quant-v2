// l2.go — §SIZING-EVO L2 灰度实证 + 衰退降级评估器（纯函数，无 IO）。
//
// 方法论同构 research lifecycle（EvaluateGrayscale/EvaluateDemote 同款阈值口径：
// MinTrades/MinIR/MinWinRate/MinProfitFactor/MaxDrawdownPct 与 ConsecDays/MinDailyTrades
// 逐字同名同默认），但**指标体系是 sizing 自己的**，且在本包内独立实现（§2.7.0 铁律：
// 零 import internal/research）：
//
//   - 观测单元 = 影子库快照（shadow 通道=灰度候选照算留痕的那批；live/paper 通道=
//     生效配置实盘/模拟观测）。每条快照带前向收益 FwdRet（5 交易日 horizon）；
//   - Trades      = 已结算（Usable）快照数；
//   - IR          = 逐日横截面 Spearman IC 序列的 mean/std——与 L1 逐字同口径
//     （IC 本身已是日频统计量，不做 research 的 √252 年化，年化只属于收益序列）；
//   - WinRate     = FwdRet>0 的快照占比（"这一笔加权后赚钱"的比例）；
//   - ProfitFactor= 正收益累计/负收益累计绝对值（无负向按 99 记，防 Inf 序列化）；
//   - MaxDrawdown = 逐日均前向收益累计净值曲线的最大回撤（%）。
//
// 判定语义（与 research 逐字对齐）：观察期未满/无观测 → pending；样本达标但任一指标
// 不达标 → reject；全部达标 → promote。降级：逐日滚动指标连续 N 日低于衰退阈值 →
// disable（调用方执行"回默认配置"+ opslog 告警）。
//
// English: L2 grayscale + demotion evaluators — same threshold names/defaults as the
// research lifecycle, sizing-native metrics, independently implemented (zero research import).
package sizing_evo

import (
	"fmt"
	"sort"
	"strings"
)

// l2MinPerDay 逐日 IC 的截面最小样本（SpearmanIC 自身下限=3，这里不放宽）。
// English: minimum cross-section per day for the daily IC (SpearmanIC's own floor).
const l2MinPerDay = 3

// noLossProfitFactor 无负向观测时的盈亏比记值（与 research 同口径，防 Inf）。
// English: profit factor recorded when there are no losing observations (avoids Inf).
const noLossProfitFactor = 99

// L2Opts 灰度晋升评估参数（零值回落内置默认；与 research PromotionOpts 同默认）。
// English: grayscale promotion thresholds (same defaults as the research lifecycle).
type L2Opts struct {
	ObservationDays int     // 观察期（交易日，按影子库实际观测日计），默认 20
	MinTrades       int     // 最小观测样本数，默认 10
	MinIR           float64 // 逐日 IC 序列 IR 下限，默认 0.3
	MinWinRate      float64 // 胜率下限（%），默认 40
	MinProfitFactor float64 // 盈亏比下限，默认 1.2
	MaxDrawdownPct  float64 // 最大回撤上限（%），默认 15
}

// fill 补齐零值字段为内置默认（调用方可不配置，评估仍确定性）。
// English: fills zero L2Opts fields with built-in defaults.
func (o *L2Opts) fill() {
	if o.ObservationDays <= 0 {
		o.ObservationDays = 20
	}
	if o.MinTrades <= 0 {
		o.MinTrades = 10
	}
	if o.MinIR <= 0 {
		o.MinIR = 0.3
	}
	if o.MinWinRate <= 0 {
		o.MinWinRate = 40
	}
	if o.MinProfitFactor <= 0 {
		o.MinProfitFactor = 1.2
	}
	if o.MaxDrawdownPct <= 0 {
		o.MaxDrawdownPct = 15
	}
}

// EvoVerdict 一条灰度候选的 L2 判定。
// English: L2 verdict for one grayscale candidate.
type EvoVerdict struct {
	CandID         string  `json:"cand_id"`
	EnteredDay     string  `json:"entered_day"` // 影子库内该候选首个观测日
	ObservedDays   int     `json:"observed_days"`
	Verdict        string  `json:"verdict"` // promote | reject | pending
	Reason         string  `json:"reason"`
	Trades         int     `json:"trades"`
	IR             float64 `json:"ir"`
	WinRate        float64 `json:"win_rate"`
	ProfitFactor   float64 `json:"profit_factor"`
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
}

// shadowObservations 取某候选的 shadow 通道观测：已结算可用样本 + 全部观测日 +
// 已结算观测日（"有留痕但没结算完"与"根本没留痕"必须能区分，拒因才可归因）。
// English: collects a candidate's shadow observations — settled samples, all observed
// days, and settled days (distinguishes "not settled yet" from "never observed").
func shadowObservations(snaps []SizingSnapshot, candID string) (obs []SizingSnapshot, allDays, settledDays map[string]bool, firstDay string) {
	allDays = map[string]bool{}
	settledDays = map[string]bool{}
	for _, s := range snaps {
		if s.Channel != "shadow" || s.CfgVer != candID {
			continue
		}
		allDays[s.Day] = true
		if firstDay == "" || s.Day < firstDay {
			firstDay = s.Day
		}
		if !s.Usable() {
			continue
		}
		obs = append(obs, s)
		settledDays[s.Day] = true
	}
	return obs, allDays, settledDays, firstDay
}

// EvaluateGrayscale L2 灰度实证判定（确定性，纯函数）。
//
// 观察期按影子库内该候选的**已结算观测日数**计（引擎停跑/前向收益未回填 → 观测日不
// 增长 → 保守 pending，不会把"没数据"误判成"不达标"）。scale 直接用快照里当时照算
// 留痕的值（shadow 通道本就是按候选配置算的，无需反事实重算——与 L1 的本质区别）。
//
// English: grayscale verdict from settled shadow observations; the window counts settled
// observed days (a stopped engine stays conservatively pending).
func EvaluateGrayscale(snaps []SizingSnapshot, candID string, opts L2Opts) EvoVerdict {
	opts.fill()
	v := EvoVerdict{CandID: candID}
	obs, allDays, settledDays, firstDay := shadowObservations(snaps, candID)
	v.EnteredDay = firstDay
	v.ObservedDays = len(settledDays)
	if len(allDays) == 0 {
		v.Verdict = "pending"
		v.Reason = "无 shadow 观测（候选未照算留痕或无成交）"
		return v
	}
	if v.ObservedDays < opts.ObservationDays {
		v.Verdict = "pending"
		v.Reason = fmt.Sprintf("观察期未满（已结算%d日<需%d日，另有%d日待结算）",
			v.ObservedDays, opts.ObservationDays, len(allDays)-len(settledDays))
		return v
	}
	if len(obs) == 0 {
		v.Verdict = "pending"
		v.Reason = "无已结算观测收益（前向收益未回填）"
		return v
	}
	// —— 指标（全部由 obs 派生，确定性）——
	v.Trades = len(obs)
	ics := make([]ICSample, 0, len(obs))
	var win int
	var sumPos, sumNeg float64
	for _, s := range obs {
		ics = append(ics, s.ICSample())
		if r := *s.FwdRet; r > 0 {
			win++
			sumPos += r
		} else {
			sumNeg += r
		}
	}
	v.WinRate = float64(win) / float64(len(obs)) * 100
	if sumNeg != 0 {
		v.ProfitFactor = sumPos / -sumNeg
	} else if sumPos > 0 {
		v.ProfitFactor = noLossProfitFactor
	}
	v.IR = IR(ICByDate(ics, l2MinPerDay))
	v.MaxDrawdownPct = dailyEquityDrawdown(obs)

	reasons := []string{}
	if v.Trades < opts.MinTrades {
		reasons = append(reasons, fmt.Sprintf("样本不足(%d<%d)", v.Trades, opts.MinTrades))
	}
	if isNaN(v.IR) || v.IR < opts.MinIR {
		reasons = append(reasons, fmt.Sprintf("IR不足(%v<%.3f)", v.IR, opts.MinIR))
	}
	if v.WinRate < opts.MinWinRate {
		reasons = append(reasons, fmt.Sprintf("胜率不足(%.1f%%<%.1f%%)", v.WinRate, opts.MinWinRate))
	}
	if v.ProfitFactor < opts.MinProfitFactor {
		reasons = append(reasons, fmt.Sprintf("盈亏比不足(%.2f<%.2f)", v.ProfitFactor, opts.MinProfitFactor))
	}
	if v.MaxDrawdownPct > opts.MaxDrawdownPct {
		reasons = append(reasons, fmt.Sprintf("回撤过大(%.1f%%>%.1f%%)", v.MaxDrawdownPct, opts.MaxDrawdownPct))
	}
	if len(reasons) == 0 {
		v.Verdict = "promote"
		v.Reason = "全部指标达标（样本/IR/胜率/盈亏比/回撤）"
		return v
	}
	v.Verdict = "reject"
	v.Reason = "未过晋升护栏：" + joinReasons(reasons)
	return v
}

// dailyEquityDrawdown 逐日均前向收益累计净值曲线的最大回撤（%）。
// 无观测日跳过（"没观测"≠"零收益"——不当 0 纪律）。
// English: max drawdown of the cumulative equity curve of daily mean forward returns.
func dailyEquityDrawdown(obs []SizingSnapshot) float64 {
	byDay := map[string][]float64{}
	var order []string
	for _, s := range obs {
		r := *s.FwdRet
		if _, ok := byDay[s.Day]; !ok {
			order = append(order, s.Day)
		}
		byDay[s.Day] = append(byDay[s.Day], r)
	}
	sort.Strings(order)
	eq, peak, maxDD := 1.0, 1.0, 0.0
	for _, d := range order {
		mean := 0.0
		for _, r := range byDay[d] {
			mean += r
		}
		mean /= float64(len(byDay[d]))
		eq *= 1 + mean/100
		if eq > peak {
			peak = eq
		}
		if dd := (peak - eq) / peak * 100; dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

// joinReasons 拼接拒因（测试与日志可读）。
// English: joins rejection reasons.
func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "；"
		}
		out += r
	}
	return out
}

// ── 衰退降级（夜间链） ──

// DemoteOpts 衰退降级参数（零值回落内置默认；与 research DemoteOpts 同默认）。
// English: demotion thresholds (same defaults as the research lifecycle).
type DemoteOpts struct {
	ConsecDays     int     // 连续低于阈值的观测日数，默认 3
	MinIR          float64 // 滚动 IR 下限，默认 0（<0 即衰退）
	MinWinRate     float64 // 滚动胜率下限（%），默认 35
	MinDailyTrades int     // 单日样本下限，默认 3
}

// fill 补齐降级阈值零值为默认。
// English: fills zero DemoteOpts fields with built-in defaults.
func (o *DemoteOpts) fill() {
	if o.ConsecDays <= 0 {
		o.ConsecDays = 3
	}
	if o.MinWinRate <= 0 {
		o.MinWinRate = 35
	}
	if o.MinDailyTrades <= 0 {
		o.MinDailyTrades = 3
	}
}

// DailyStat 某生效配置单日的滚动归因指标（IR/胜率/当日样本数）。
// English: one day's rolling attribution stat for an applied config (IR/win-rate/count).
type DailyStat struct {
	IR      float64 `json:"ir"`
	WinRate float64 `json:"win_rate"`
	Trades  int     `json:"trades"`
}

// LiveDailyStats 把某生效配置的 live/paper 通道已结算快照聚合为逐日滚动指标
// （日期升序）：Trades=当日可用快照数、WinRate=当日 FwdRet>0 占比、IR=截至当日的
// 逐日 IC 序列 mean/std（滚动口径；有效日 <2 或零方差 → 0，与 research"样本<2 为 0"同构）。
//
// cfgVer 匹配按**候选 ID 后缀**（CfgVer 的 "#" 后段，"base"=出厂默认）：快照的
// CfgVer 带账号前缀（引擎按各账号盖戳），降级判定不应因账号维度漏聚合——R7 首期
// 全局单回路，账号只作归因不作分组。
// English: aggregates an applied config's live/paper snapshots into per-day rolling stats,
// matching the candidate-ID suffix of CfgVer (account prefix is attribution, not grouping).
func LiveDailyStats(snaps []SizingSnapshot, candID string) []DailyStat {
	want := cfgVerCandID(candID)
	type dayAgg struct {
		rets []float64
		ics  []ICSample
	}
	byDay := map[string]*dayAgg{}
	var order []string
	for _, s := range snaps {
		if cfgVerCandID(s.CfgVer) != want || !s.Usable() {
			continue
		}
		if s.Channel != "live" && s.Channel != "paper" {
			continue // 只看生效配置的真实观测（shadow 是候选的，不算衰退证据）
		}
		d := byDay[s.Day]
		if d == nil {
			d = &dayAgg{}
			byDay[s.Day] = d
			order = append(order, s.Day)
		}
		r := *s.FwdRet
		d.rets = append(d.rets, r)
		d.ics = append(d.ics, s.ICSample())
	}
	sort.Strings(order)
	var icSeries []float64
	out := make([]DailyStat, 0, len(order))
	for _, day := range order {
		d := byDay[day]
		wins := 0
		for _, r := range d.rets {
			if r > 0 {
				wins++
			}
		}
		// 当日截面 IC（不足 l2MinPerDay 或零方差 → 该日无 IC，不进序列）。
		if rows := ICByDate(d.ics, l2MinPerDay); len(rows) > 0 {
			icSeries = append(icSeries, rows[0].IC)
		}
		out = append(out, DailyStat{
			IR:      rollingIR(icSeries),
			WinRate: float64(wins) / float64(len(d.rets)) * 100,
			Trades:  len(d.rets),
		})
	}
	return out
}

// cfgVerCandID 取 CfgVer 的候选 ID 段（"u1#evo1234"→"evo1234"；无 "#" 原样返回）。
// English: extracts the candidate-ID segment of a CfgVer ("u1#evo1234" → "evo1234").
func cfgVerCandID(cfgVer string) string {
	if i := strings.Index(cfgVer, "#"); i >= 0 && i+1 < len(cfgVer) {
		return cfgVer[i+1:]
	}
	return cfgVer
}

// rollingIR 截至当前的逐日 IC 序列 IR（mean/std；有效 <2 或零方差 → 0）。
// 与 research irAnnualized 的"样本不足为 0"同构——此处不年化（IC 已是日频统计量）。
// English: rolling IR of the daily-IC series so far (0 when undefined).
func rollingIR(series []float64) float64 {
	ir := IR(nil)
	if len(series) >= 2 {
		rows := make([]ICRow, len(series))
		for i, v := range series {
			rows[i] = ICRow{IC: v}
		}
		ir = IR(rows)
	}
	if isNaN(ir) {
		return 0
	}
	return ir
}

// DemoteVerdict 一条生效配置的降级判定。
// English: demotion verdict for one applied config.
type DemoteVerdict struct {
	RuleID    string `json:"rule_id"` // = 生效配置定位戳（cfgVer）
	Verdict   string `json:"verdict"` // disable | keep
	Reason    string `json:"reason"`
	ConsecLow int    `json:"consec_low_days"`
}

// EvaluateDemote 按逐日滚动指标判定连续 N 日低于衰退阈值 → disable。
// days 按时间升序传入；样本数不足当日计入"低位"（无统计意义视为不达标）。
// English: flags disable on ConsecDays consecutive days below the decline thresholds;
// thin days also count as below-threshold.
func EvaluateDemote(days []DailyStat, opts DemoteOpts) DemoteVerdict {
	opts.fill()
	v := DemoteVerdict{Verdict: "keep"}
	if len(days) == 0 {
		v.Reason = "无滚动指标（无观测数据，不判定）"
		return v
	}
	lowRun := 0
	for _, d := range days {
		below := d.Trades < opts.MinDailyTrades || d.IR < opts.MinIR || d.WinRate < opts.MinWinRate
		if below {
			lowRun++
		} else {
			lowRun = 0
		}
		if lowRun > v.ConsecLow {
			v.ConsecLow = lowRun
		}
		if lowRun >= opts.ConsecDays {
			v.Verdict = "disable"
			v.Reason = fmt.Sprintf("连续%d个观测日低于衰退阈值（IR/胜率/样本）", lowRun)
			return v
		}
	}
	v.Reason = "滚动指标在阈值之上"
	return v
}

// DemoteSummary 一行式降级摘要（审计日志/任务输出）。
// English: one-line demotion summary for audit logs.
func DemoteSummary(v DemoteVerdict) string {
	if v.Verdict == "disable" {
		return "衰退降级 " + v.RuleID + "：" + v.Reason
	}
	return "保持 " + v.RuleID + "：" + v.Reason
}
