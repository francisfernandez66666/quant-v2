// trade_calendar.go — 运行时交易日历缓存（§GAP3.1）：
// 启动后经同花顺交易日历接口拉取近一年交易日序列，推导"非周末休市日"集合
// （法定节假日/临时休市），供 IsTradingDay/TradingDayDate/AddTradingDays/
// DurationToNextActiveSession 消费。加载失败时系统按周末口径兜底运行（不阻断启动）。
// English: runtime trading-calendar cache — derives non-weekend closed days (statutory
// holidays / ad-hoc closures) from the THS trading-day series; all trade-time predicates
// consume it, falling back to weekend-only semantics until loaded.
package data

import (
	"fmt"
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
)

var (
	calMu      sync.RWMutex
	closedDays = map[string]bool{} // "20060102" → 非周末休市日
	calLoaded  bool                // 是否至少成功加载过一次
)

// SetClosedDays 注入非周末休市日集合（幂等覆盖）。
// English: installs the closed-day set (idempotent overwrite).
func SetClosedDays(dates []string) {
	calMu.Lock()
	defer calMu.Unlock()
	closedDays = make(map[string]bool, len(dates))
	for _, d := range dates {
		if len(d) == 8 {
			closedDays[d] = true
		}
	}
	calLoaded = true
}

// isClosedDay 查询某 YYYYMMDD 是否为非周末休市日（未加载日历时恒 false=周末口径兜底）。
func isClosedDay(yyyymmdd string) bool {
	calMu.RLock()
	defer calMu.RUnlock()
	return closedDays[yyyymmdd]
}

// CalendarLoaded 日历是否已成功加载（诊断/健康检查用）。
func CalendarLoaded() bool {
	calMu.RLock()
	defer calMu.RUnlock()
	return calLoaded
}

// ClosedDayCount 已加载的休市日数量（诊断用）。
func ClosedDayCount() int {
	calMu.RLock()
	defer calMu.RUnlock()
	return len(closedDays)
}

// RefreshTradingCalendar 拉取同花顺交易日历并推导休市日集合。
// 推导口径：API 覆盖窗口 [min,max] 内所有非周末日期，凡不在交易日序列中即休市日
// （不外推覆盖窗口之外的日期，避免误判未来未公布区间）。
// English: fetches the trading-day series and derives closed days within the covered window.
func RefreshTradingCalendar() error {
	hc, err := NewHithinkClient()
	if err != nil {
		return fmt.Errorf("hithink client: %w", err)
	}
	days, err := hc.TradingDays()
	if err != nil {
		return fmt.Errorf("trading days: %w", err)
	}
	if len(days) == 0 {
		return fmt.Errorf("empty calendar response")
	}
	trade := make(map[string]bool, len(days))
	minD, maxD := days[0].Date, days[0].Date
	for _, d := range days {
		if len(d.Date) != 8 {
			continue
		}
		trade[d.Date] = true
		if d.Date < minD {
			minD = d.Date
		}
		if d.Date > maxD {
			maxD = d.Date
		}
	}
	t0, err := time.ParseInLocation("20060102", minD, cntime.Loc)
	if err != nil {
		return fmt.Errorf("parse min date: %w", err)
	}
	t1, err := time.ParseInLocation("20060102", maxD, cntime.Loc)
	if err != nil {
		return fmt.Errorf("parse max date: %w", err)
	}
	var closed []string
	for t := t0; !t.After(t1); t = t.AddDate(0, 0, 1) {
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			continue
		}
		key := t.Format("20060102")
		if !trade[key] {
			closed = append(closed, key)
		}
	}
	SetClosedDays(closed)
	log.Printf("[calendar] 交易日历已加载: 窗口 %s~%s，非周末休市日 %d 天", minD, maxD, len(closed))
	return nil
}

// LoadTradingCalendarAsync 启动后台日历刷新：立即一次，之后每 24h 重试；
// 失败仅记日志（周末口径兜底），绝不阻断主流程启动。
// English: async calendar loader — immediate attempt then daily refresh; failures log only.
func LoadTradingCalendarAsync() {
	go func() {
		for {
			if err := RefreshTradingCalendar(); err != nil {
				log.Printf("[calendar] 交易日历加载失败（暂按周末口径兜底）: %v", err)
			}
			time.Sleep(24 * time.Hour)
		}
	}()
}
