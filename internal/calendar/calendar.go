// Package calendar 宏观日历告警，从配置读取重要经济事件，判断是否临近高影响事件。
package calendar

import (
	"time"

	"quant-trading-v2/internal/config"
)

// Event 宏观事件，包含日期、标题、影响程度和提前提醒天数。
type Event struct {
	Date        time.Time
	Title       string
	Impact      string
	DaysAdvance int
}

// Calendar 日历管理器，维护配置中的事件列表。
type Calendar struct {
	events []config.CalendarEvent
}

// New 创建日历实例。
func New(events []config.CalendarEvent) *Calendar {
	return &Calendar{events: events}
}

// UpcomingEvents 返回未来 days 天内即将发生的事件列表。
// DaysAdvance 控制事件提前显示天数。同时会包含进行中的事件（已开始但未结束）。
func (c *Calendar) UpcomingEvents(days int) []Event {
	now := time.Now()
	var out []Event
	for _, e := range c.events {
		t, err := time.Parse("2006-01-02", e.Date)
		if err != nil {
			continue
		}
		// 事件窗口开始 = 事件日期 - DaysAdvance（提前显示）
		start := t.AddDate(0, 0, -e.DaysAdvance)
		// 事件窗口结束 = 事件日期 + days（延续显示）
		end := t.AddDate(0, 0, days)
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
func (c *Calendar) HasHighImpactEvent(days int) bool {
	for _, e := range c.UpcomingEvents(days) {
		if e.Impact == "high" {
			return true
		}
	}
	return false
}
