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
