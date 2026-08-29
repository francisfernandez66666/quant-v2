// Package calendar 宏观日历告警，从配置读取重要经济事件，判断是否临近高影响事件。
// （Package calendar provides macro-calendar alerts: it reads key economic events from config and
// detects when a high-impact event is approaching.）
package calendar

import (
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
)

// Event 宏观事件，包含日期、标题、影响程度和提前提醒天数。
// （Event is a macro event with date, title, impact level and days of advance notice.）
type Event struct {
	// 事件发生日期
	Date time.Time
	// 事件标题
	Title string
	// 影响程度（high/medium/low）
	Impact string
	// 提前显示/提醒天数
	DaysAdvance int
}

// Calendar 日历管理器，维护配置中的事件列表。
// （Calendar is the calendar manager holding the configured event list.）
type Calendar struct {
	events []config.CalendarEvent // 事件原始配置列表
}

// New 创建日历实例。
// （New creates a calendar instance.）
func New(events []config.CalendarEvent) *Calendar {
	return &Calendar{events: events}
}

// UpcomingEvents 返回未来 days 天内即将发生的事件列表。
// DaysAdvance 控制事件提前显示天数。同时会包含进行中的事件（已开始但未结束）。
// （UpcomingEvents returns events scheduled within the next days days. DaysAdvance controls how early
// an event shows up; in-progress (already started but not ended) events are also included.）
func (c *Calendar) UpcomingEvents(days int) []Event {
	now := cntime.Now()
	var out []Event
	for _, e := range c.events {
		// 解析事件日期，格式非法的事件直接跳过
		t, err := time.ParseInLocation("2006-01-02", e.Date, cntime.Loc)
		if err != nil {
			continue
		}
		// 事件窗口开始 = 事件日期 - DaysAdvance（提前显示）
		start := t.AddDate(0, 0, -e.DaysAdvance)
		// 事件窗口结束 = 事件日期 + days（延续显示）
		end := t.AddDate(0, 0, days)
		// 当前时间落入 [start, end) 窗口内则纳入结果（含已开始未结束的事件）
		if now.Before(end) && !now.Before(start) {
			out = append(out, Event{
				Date: t, Title: e.Title, Impact: e.Impact,
				DaysAdvance: e.DaysAdvance,
			})
		}
	}
	return out
}

// HasHighImpactEvent 判断未来 days 天内是否存在高影响事件。
// （HasHighImpactEvent reports whether a high-impact event occurs within the next days days.）
func (c *Calendar) HasHighImpactEvent(days int) bool {
	for _, e := range c.UpcomingEvents(days) {
		if e.Impact == "high" {
			return true
		}
	}
	return false
}
