// flow_freshness.go — §FLOW-FRESHNESS 资金流读数的北京时间自然日新鲜度判据（2026-11-19）。
// 资金流/量比是稀疏上报的日级读数（探针逐日写入 flow_factor_daily），固定秒窗会把
// 早盘帧在午盘误判过期（外脑资金帧 externalBrainReadStaleSec 已因此作废）。本判据只认
// 北京自然日：读数日键与当前北京日同日→全天可用；跨日→丢弃（档2），绝不当作旧值继续用。
// English: Beijing natural-day freshness for daily flow readings — same-day usable all day,
// cross-day discarded; no fixed-second window (that false-expired sparse morning frames).
package signalctl

import (
	"math"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
)

// FlowFresh 资金流读数是否新鲜可用。flowDay 为读数日键（YYYY-MM-DD 或 YYYYMMDD），
// now 为判定时刻；两者按北京自然日比较。flowDay 空/格式不符→false（弃权，绝不猜）。
func FlowFresh(flowDay string, now time.Time) bool {
	if flowDay == "" {
		return false
	}
	today := cntime.DayOf(now) // YYYY-MM-DD
	if len(flowDay) == 8 {
		// YYYYMMDD → YYYY-MM-DD（探针/日表 compact 口径）。
		flowDay = flowDay[:4] + "-" + flowDay[4:6] + "-" + flowDay[6:8]
	}
	if len(flowDay) != 10 || flowDay[4] != '-' || flowDay[7] != '-' {
		return false
	}
	return flowDay == today
}

// FreshFlowDay 从 (读数日键, 当前时刻) 得到可用于装配的安全日键：新鲜→原日键，陈旧/无效→空串
// （调用方据此把 RYest/RMa5 置 NaN 弃权）。English: returns the day key only when fresh, else empty.
func FreshFlowDay(flowDay string, now time.Time) string {
	if FlowFresh(flowDay, now) {
		return cntime.DayOf(now)
	}
	return ""
}

// VolRatiosOfBars 由量能序列（**降序**，最新在前）算 owner 伪码的双比值：
// r_yest = v[-1]/v[-2]（最新完整日 vs 前一日）、r_ma5 = v[-1]/mean(v[-6:-1])（vs 前5日均量）。
// 不足 2 根算不出 r_yest、不足 6 根算不出 r_ma5 → ok=false 整体弃权（绝不拿不完整序列硬算）。
// 零/负量能（停市异常）同样弃权。English: relative-volume ratios from a newest-first amount series.
func VolRatiosOfBars(amounts []float64) (rYest, rMa5 float64, ok bool) {
	if len(amounts) < 6 {
		return 0, 0, false
	}
	last, prev := amounts[0], amounts[1]
	if last <= 0 || prev <= 0 {
		return 0, 0, false
	}
	sum := 0.0
	for _, v := range amounts[1:6] {
		if v <= 0 {
			return 0, 0, false
		}
		sum += v
	}
	return last / prev, last / (sum / 5.0), true
}

// VolSurgeDeltaOf 量态 → 信号强度 delta（放量 +1 / 缩量 −1 / 平量 0），供后置 blend 消费。
// English: maps the volume state to a [-1,1] delta for the flow blend.
func VolSurgeDeltaOf(volState string) float64 {
	switch volState {
	case VolStateSurge:
		return 1.0
	case VolStateShrink:
		return -1.0
	default:
		return 0.0
	}
}

// VolRatiosIntraday 盘中口径的双量比（§FLOW-FACTOR-DAILY 2026-11-19 方案 B）：
// 当日累计额 todayAmount（东财指数日 K 当根，盘中实时累计）按已流逝交易分钟折算全天等值后，
// 与历史完整日序列 histAmounts（**降序**：T-1 在前）比出 r_yest / r_ma5。
//
// 折算口径与 240/elapsed 全仓同一把尺子（combat_agent.TradingMinutesElapsed），
// 资产的量比折算与个股 IntradayVolumeRatio 逐字同源——不许再写第二份时段判定。
// fail-closed：
//   - 盘前/非交易时段（elapsed<=0）→ 弃权：集合竞价量折算会虚高 240 倍；
//   - histAmounts < 5 根、或任一历史额 <=0、或 todayAmount<=0 → 弃权；
//   - 折算后的量比绝不为负/NaN（调用方拿到 ok=false 就 NaN 弃权）。
//
// r_yest = 今日折算全天 / T-1；r_ma5 = 今日折算全天 / mean(T-1..T-5)。
// English: intraday volume ratios — today's cumulative amount prorated to full-day, vs T-1 and 5-day mean.
func VolRatiosIntraday(now time.Time, todayAmount float64, histAmounts []float64) (rYest, rMa5 float64, ok bool) {
	if len(histAmounts) < 5 || todayAmount <= 0 {
		return 0, 0, false
	}
	elapsed := combat_agent.TradingMinutesElapsed(now)
	if elapsed <= 0 {
		return 0, 0, false // 盘前折算会放大 240 倍，宁可弃权
	}
	prorated := todayAmount * (240.0 / elapsed)
	prev := histAmounts[0]
	if prev <= 0 {
		return 0, 0, false
	}
	sum := 0.0
	for _, v := range histAmounts[:5] {
		if v <= 0 {
			return 0, 0, false
		}
		sum += v
	}
	return prorated / prev, prorated / (sum / 5.0), true
}

// flowFullScalePct 资金流归一满档百分比：净流入/成交额 达到 ±3% 时 delta 满档 ±1。
// 与 flowBandOf 的 strong_in_pct=3 同基准（flow_factor.go:47-68），一套刻度两处共用。
const flowFullScalePct = 3.0

// FlowPctToDelta 资金流强度 → delta∈[-1,1]（owner 裁决：分母用**同期成交额**，规避市值口径歧义）。
// pct = 净流入(元)/同期成交额(元)×100；delta = clamp(pct/3, -1, 1)。
// 净流入/成交额任一非正值（取数失败/停市）→ NaN 弃权（绝不当 0 中性）。
// English: money-flow strength to a [-1,1] delta prorated on the same-period turnover; 3% = full scale.
func FlowPctToDelta(netInflow, amount float64) float64 {
	if netInflow == 0 || amount <= 0 || math.IsNaN(netInflow) || math.IsNaN(amount) {
		// netInflow==0 且 amount 正常时是"真零流入"（罕见但合法）→ 0 中性；其余 NaN 弃权。
		if netInflow == 0 && amount > 0 && !math.IsNaN(amount) {
			return 0
		}
		return math.NaN()
	}
	pct := netInflow / amount * 100
	d := pct / flowFullScalePct
	if d > 1 {
		d = 1
	}
	if d < -1 {
		d = -1
	}
	return d
}
