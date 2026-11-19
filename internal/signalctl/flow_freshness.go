// flow_freshness.go — §FLOW-FRESHNESS 资金流读数的北京时间自然日新鲜度判据（2026-11-19）。
// 资金流/量比是稀疏上报的日级读数（探针逐日写入 flow_factor_daily），固定秒窗会把
// 早盘帧在午盘误判过期（外脑资金帧 externalBrainReadStaleSec 已因此作废）。本判据只认
// 北京自然日：读数日键与当前北京日同日→全天可用；跨日→丢弃（档2），绝不当作旧值继续用。
// English: Beijing natural-day freshness for daily flow readings — same-day usable all day,
// cross-day discarded; no fixed-second window (that false-expired sparse morning frames).
package signalctl

import (
	"time"

	"quant-trading-v2/internal/cntime"
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
