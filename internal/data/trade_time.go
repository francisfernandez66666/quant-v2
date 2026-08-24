// Package data — 交易时间判断。
// 所有时间均以 24 小时制 HHMM 整形比较，避免 time.Parse 开销。
// 时间窗口参数可通过 SetTradeTimeConfig 覆盖，默认与 A 股一致。
package data

import "time"

// TradeTimeConfig 交易时段参数，所有值均为 HHMM 整数格式。
type TradeTimeConfig struct {
	TradeOpen      int // 开盘（默认 915）
	TradeClose     int // 收盘（默认 1500）
	FullOpen       int // 完整开盘（默认 915）
	FullClose      int // 完整收盘（默认 1530）
	PreOpenStart   int // 集合竞价开始（默认 915）
	PreOpenEnd     int // 集合竞价结束（默认 925）
	MorningHighEnd int // 早盘高频结束（默认 1000）
	MidFreqStart   int // 中频窗口开始（默认 945）
	AfternoonStart int // 午后高频开始（默认 1300）
	AfternoonEnd   int // 午后高频结束（默认 1330）

}

// defaultTradeTime 默认交易时段参数（A 股标准时段），可用 SetTradeTimeConfig 覆盖。
var defaultTradeTime = TradeTimeConfig{
	TradeOpen: 915, TradeClose: 1500,
	FullOpen: 915, FullClose: 1530,
	PreOpenStart: 915, PreOpenEnd: 925,
	MorningHighEnd: 1000, MidFreqStart: 945,
	AfternoonStart: 1300, AfternoonEnd: 1330,
}

// ApplyConfig 应用配置中的交易时段参数。
func ApplyConfig(cfg TradeTimeConfig) {
	SetTradeTimeConfig(cfg)
}

// SetTradeTimeConfig 覆盖交易时段参数。
func SetTradeTimeConfig(cfg TradeTimeConfig) {
	if cfg.TradeOpen > 0 {
		defaultTradeTime.TradeOpen = cfg.TradeOpen
	}
	if cfg.TradeClose > 0 {
		defaultTradeTime.TradeClose = cfg.TradeClose
	}
	if cfg.FullOpen > 0 {
		defaultTradeTime.FullOpen = cfg.FullOpen
	}
	if cfg.FullClose > 0 {
		defaultTradeTime.FullClose = cfg.FullClose
	}
	if cfg.PreOpenStart > 0 {
		defaultTradeTime.PreOpenStart = cfg.PreOpenStart
	}
	if cfg.PreOpenEnd > 0 {
		defaultTradeTime.PreOpenEnd = cfg.PreOpenEnd
	}
	if cfg.MorningHighEnd > 0 {
		defaultTradeTime.MorningHighEnd = cfg.MorningHighEnd
	}
	if cfg.MidFreqStart > 0 {
		defaultTradeTime.MidFreqStart = cfg.MidFreqStart
	}
	if cfg.AfternoonStart > 0 {
		defaultTradeTime.AfternoonStart = cfg.AfternoonStart
	}
	if cfg.AfternoonEnd > 0 {
		defaultTradeTime.AfternoonEnd = cfg.AfternoonEnd
	}
}

// IsTradeTime 判断当前是否为交易时段。
func IsTradeTime(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.TradeOpen && m < defaultTradeTime.TradeClose
}

// IsFullTradingHours 判断当前是否在完整的交易覆盖范围内。
func IsFullTradingHours(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.FullOpen && m <= defaultTradeTime.FullClose
}

// IsPreOpen 判断是否为集合竞价时段。
func IsPreOpen(now time.Time) bool {
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.PreOpenStart && m < defaultTradeTime.PreOpenEnd
}

// IsMorningHighFreq 早盘高频率窗口（从集合竞价 9:15 起高频扫描，评分按 70 分起步）。
func IsMorningHighFreq(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.FullOpen && m < defaultTradeTime.MorningHighEnd
}

// IsMidFreqWindow 早盘中频窗口（9:45-10:00，早盘高频扫描的次级节奏）。
func IsMidFreqWindow(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.MidFreqStart && m < defaultTradeTime.MorningHighEnd
}

// IsAfternoonHighFreq 午后高频率窗口。
func IsAfternoonHighFreq(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.AfternoonStart && m < defaultTradeTime.AfternoonEnd
}

// ScanInterval 根据当前时间返回合适的扫描间隔（秒）。
func ScanInterval(now time.Time, highFreqSec, midFreqSec, afternoonFreqSec, normalSec int) int {
	if IsMorningHighFreq(now) {
		if IsMidFreqWindow(now) {
			return midFreqSec
		}
		return highFreqSec
	}
	if IsAfternoonHighFreq(now) {
		return afternoonFreqSec
	}
	return normalSec
}

// IsPreMarket 盘前时段 8:30-9:15（可配置）。
func IsPreMarket(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= 830 && m < defaultTradeTime.FullOpen
}

// IsPreAfternoon 午盘前时段 11:30-13:00。
func IsPreAfternoon(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= 1130 && m < defaultTradeTime.AfternoonStart
}

// IsAfterMarket 盘后时段 15:00-次日8:30。
func IsAfterMarket(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.TradeClose
}

// MarketSession 当前市场时段枚举。
type MarketSession int

const (
	SessionPreMarket      MarketSession = iota // 盘前 8:30-9:15
	SessionMorningTrade                        // 上午交易 9:15-11:30
	SessionPreAfternoon                        // 午间 11:30-13:00
	SessionAfternoonTrade                      // 下午交易 13:00-15:00
	SessionAfterMarket                         // 盘后 15:00+
	SessionClosed                              // 非交易日/其他
)

// String 返回市场时段的名称。
func (s MarketSession) String() string {
	switch s {
	case SessionPreMarket:
		return "盘前"
	case SessionMorningTrade:
		return "上午盘"
	case SessionPreAfternoon:
		return "午前"
	case SessionAfternoonTrade:
		return "下午盘"
	case SessionAfterMarket:
		return "盘后"
	default:
		return "休市"
	}
}

// IsActiveSession 判断当前是否处于"活跃行情时段"：盘前/上午盘/午前/下午盘。
// 与 scoreCycle 近实时打分循环的门控集合一致——非活跃时段（盘后/休市）应停止
// 5s 行情采集与实时触发，避免无谓轮询消耗 CPU。
// English: reports whether now is in an "active market session" — premarket / morning /
// pre-afternoon / afternoon trade. Matches the scoreCycle near-realtime gating set; outside
// these windows (after-market / closed) the 5s quote fetcher and real-time trigger should
// pause to avoid wasteful polling.
func IsActiveSession(now time.Time) bool {
	switch CurrentSession(now) {
	case SessionPreMarket, SessionMorningTrade, SessionPreAfternoon, SessionAfternoonTrade:
		return true
	default:
		return false
	}
}

// IsTradingWindow 交易日交易窗口（开盘 9:15 ~ 收盘 TradeClose，含午休）：研究任务禁止窗口。
// 与用户约定口径一致——除交易日交易窗口外（盘前凌晨/盘后/周末全天），研究调度一律放行。
// 直接复用 defaultTradeTime 的 FullOpen/TradeClose 字段，不另设钟点。
// English: trading-day window [FullOpen, TradeClose) incl. lunch break — the only period where
// research tasks are blocked; nights, pre-open and weekends are all eligible for research.
func IsTradingWindow(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false // 周末非交易日：全天允许
	}
	m := now.Hour()*100 + now.Minute()
	return m >= defaultTradeTime.FullOpen && m < defaultTradeTime.TradeClose
}

// CurrentSession 返回当前市场时段。
func CurrentSession(now time.Time) MarketSession {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return SessionClosed
	}
	m := now.Hour()*100 + now.Minute()
	switch {
	case m >= 830 && m < defaultTradeTime.FullOpen:
		return SessionPreMarket
	case m >= defaultTradeTime.FullOpen && m < 1130:
		return SessionMorningTrade
	case m >= 1130 && m < defaultTradeTime.AfternoonStart:
		return SessionPreAfternoon
	case m >= defaultTradeTime.AfternoonStart && m < defaultTradeTime.TradeClose:
		return SessionAfternoonTrade
	case m >= defaultTradeTime.TradeClose:
		return SessionAfterMarket
	default:
		return SessionClosed
	}
}

// BeforeOpenTrade 判断当前时刻是否处于开盘（默认 9:30）之前。
// 非交易日返回 true（此时同样不应产生基于实盘数据的交易信号）。
// 用于盘前压制战法信号：只更新评分，不发布买入/watch 信号。
func BeforeOpenTrade(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return true
	}
	return now.Hour()*100+now.Minute() < 930
}

// NextTradeOpen 返回距离下一个交易时段开盘的等待时长。
func NextTradeOpen(now time.Time) time.Duration {
	for i := 0; i < 7; i++ {
		t := now.AddDate(0, 0, i)
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			continue
		}
		openH := defaultTradeTime.TradeOpen / 100
		openM := defaultTradeTime.TradeOpen % 100
		open := time.Date(t.Year(), t.Month(), t.Day(), openH, openM, 0, 0, t.Location())
		if now.Before(open) {
			return open.Sub(now)
		}
		if i == 0 && now.Hour()*100+now.Minute() < defaultTradeTime.TradeClose {
			return 0
		}
	}
	return 0
}

// TradingDayDate 返回当前交易日日期 YYYYMMDD。
// 周末退回到上一周五，节假日暂不处理（后续可扩展）。
func TradingDayDate(now time.Time) string {
	t := now
	for t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, -1)
	}
	return t.Format("20060102")
}

// AddTradingDays 将日期往后推 n 个交易日，返回 YYYYMMDD。
func AddTradingDays(td string, n int) string {
	t, err := time.Parse("20060102", td)
	if err != nil {
		return td
	}
	added := 0
	for added < n {
		t = t.AddDate(0, 0, 1)
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
			added++
		}
	}
	return t.Format("20060102")
}

// IsTradingDay 判断给定日期是否为交易日（仅检查周末）。
func IsTradingDay(t time.Time) bool {
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

// DurationToNextActiveSession 距下一个活跃交易窗口（工作日 8:30 盘前开盘）的时长。
// 非交易时段的唯一"闹钟"：休市/盘后循环据此一次性长眠，而不是空转节拍器。
// English: duration until the next active session start (weekday 08:30 premarket open) —
// the single alarm an after-hours loop needs to truly hibernate instead of busy-ticking.
func DurationToNextActiveSession(now time.Time) time.Duration {
	for add := 0; add <= 8; add++ { // 最多看一周（跨周末/长假）
		day := now.AddDate(0, 0, add)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), 8, 30, 0, 0, now.Location())
		if start.After(now) {
			return start.Sub(now)
		}
	}
	return 15 * time.Minute // 理论不可达：兜底防时钟异常导致永久睡眠
}
