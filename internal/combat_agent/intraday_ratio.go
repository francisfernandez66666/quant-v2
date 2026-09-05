// 盘中量比时间窗归一（P2#26）：旧实现把「当日实时累计量」直接与「全日均量」相比——
// 开盘半天的累计量天然远小于全日均量，量比被系统性稀释：同一放量强度在 09:35 测出的
// 量比约为 14:55 的 1/5，导致早晨的放量突破/放量派发信号被误判为"不放量"。
// 修复：按已流逝交易时间比例把累计量折算成"全日等值量"再与均量比较，量比口径在任意
// 时刻与 15:00 收盘时一致（全天交易 240 分钟 = 上午 120 + 下午 120）。
// English: intraday time-window normalization for the volume ratio (P2#26). The old code compared
// today's cumulative volume directly against the full-day average, so a morning cumulative total was
// naturally a fraction of the daily mean — the same volume strength measured ~1/5 as large at 09:35
// vs 14:55, silently suppressing morning breakout/distribution signals. The cumulative volume is now
// prorated to a full-day equivalent by the fraction of the 240 trading minutes already elapsed (120
// morning + 120 afternoon), making the ratio time-of-day invariant.
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/cntime"
)

// tradingMinutesElapsed 返回当日已流逝的交易分钟数（北京时区；A股 240 分钟制）：
// 9:30-11:30 上午 120 分钟 + 13:00-15:00 下午 120 分钟；盘前/午休取已流逝部分，
// 盘后按全日 240 封顶。English: elapsed trading minutes today (CST; 240 = 120 AM + 120 PM);
// pre-open/lunch count only what has passed; after-close caps at 240.
func tradingMinutesElapsed(now time.Time) float64 {
	n := cntime.In(now)
	m := n.Hour()*60 + n.Minute()
	// 常量：交易时段边界（分钟自午夜）
	const (
		amStart = 9*60 + 30  // 09:30
		amEnd   = 11*60 + 30 // 11:30
		pmStart = 13 * 60    // 13:00
		pmEnd   = 15 * 60    // 15:00
	)
	switch {
	case m < amStart:
		return 0
	case m <= amEnd:
		return float64(m - amStart)
	case m <= pmStart:
		return 120 // 午休：上午已满
	case m <= pmEnd:
		return 120 + float64(m-pmStart)
	default:
		return 240
	}
}

// intradayVolumeRatio 盘中时间窗归一的量比：今日实时累计量折算全天等值后 ÷ 前 N 日均量。
// elapsed<=0（盘前）或 now 非交易日/停牌缺口按 1 处理（保守：不放大）。
// English: time-window-normalized intraday volume ratio: today's cumulative volume prorated to a
// full-day equivalent, divided by the prior N-day average volume. Before the open (elapsed<=0) the
// ratio is conservative (no amplification).
func intradayVolumeRatio(now time.Time, cumVol, avgDailyVol float64) float64 {
	if avgDailyVol <= 0 || cumVol <= 0 {
		return 0
	}
	elapsed := tradingMinutesElapsed(now)
	if elapsed <= 0 {
		elapsed = 1 // 盘前按已流逝 1 分钟处理，避免除以零且不放大
	}
	return cumVol * (240 / elapsed) / avgDailyVol
}
