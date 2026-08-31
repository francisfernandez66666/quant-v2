// calendar 宏观日历：事件窗口（提前提醒+延续）与高影响事件探测。
package calendar

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// ev 构造日历事件（测试辅助）。
func ev(date, title, impact string, adv int) config.CalendarEvent {
	return config.CalendarEvent{Date: date, Title: title, Impact: impact, DaysAdvance: adv}
}

// TestUpcomingEvents 提前窗口内的事件应被列出。
func TestUpcomingEvents(t *testing.T) {
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	c := New([]config.CalendarEvent{
		ev(tomorrow, "议息决议", "high", 3), // 提前 3 天显示
	})
	out := c.UpcomingEvents(5)
	if len(out) != 1 || out[0].Impact != "high" {
		t.Fatalf("应返回 1 条高影响事件, got %+v", out)
	}
}

// TestUpcomingEventsExcluded 远期/非法日期事件被排除。
func TestUpcomingEventsExcluded(t *testing.T) {
	far := time.Now().AddDate(0, 0, 100).Format("2006-01-02")
	c := New([]config.CalendarEvent{
		ev(far, "远期事件", "low", 1), // 不在提前窗口
		ev("not-a-date", "非法", "low", 1),
	})
	if out := c.UpcomingEvents(5); len(out) != 0 {
		t.Errorf("远期/非法日期不应出现在结果, got %+v", out)
	}
}

// TestHasHighImpactEvent 存在高影响事件时返回 true。
func TestHasHighImpactEvent(t *testing.T) {
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	c := New([]config.CalendarEvent{
		ev(tomorrow, "CPI 数据", "medium", 2),
	})
	if c.HasHighImpactEvent(5) {
		t.Error("无 high 影响事件不应返回 true")
	}
	c = New([]config.CalendarEvent{ev(tomorrow, "议息决议", "high", 3)})
	if !c.HasHighImpactEvent(5) {
		t.Error("存在 high 影响事件应返回 true")
	}
}
