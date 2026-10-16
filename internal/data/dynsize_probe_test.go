package data

import (
	"testing"
	"time"
)

// withCalendar 在测试期间注入一份休市日集合，并在结束后恢复原全局日历状态。
// （测试与本包同 package，可直接读写私有的 closedDays/calLoaded/calWindow。）
func withCalendar(t *testing.T, closed []string) {
	t.Helper()
	calMu.Lock()
	oldClosed, oldLoaded, oldWindow := closedDays, calLoaded, calWindow
	calMu.Unlock()
	SetClosedDays(closed)
	t.Cleanup(func() {
		calMu.Lock()
		closedDays, calLoaded, calWindow = oldClosed, oldLoaded, oldWindow
		calMu.Unlock()
	})
}

func dayOf(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("20060102", s, time.Local)
	if err != nil {
		t.Fatalf("解析日期 %s 失败: %v", s, err)
	}
	return d
}

// TestProbeHolidayWindowLongHoliday 长假（2026 国庆：10/1、10/2、10/5~10/8 休市）。
func TestProbeHolidayWindowLongHoliday(t *testing.T) {
	withCalendar(t, []string{"20261001", "20261002", "20261005", "20261006", "20261007", "20261008"})

	// 2026-09-30（周三）= 长假前最后一个交易日。
	got := ProbeHolidayWindow(dayOf(t, "20260930"))
	if !got.CalendarLoaded {
		t.Fatalf("应报告日历已加载")
	}
	if got.PreHolidayN != 1 {
		t.Fatalf("节前第 N 个交易日期望 1，got %d", got.PreHolidayN)
	}
	if got.ClosedRun != 8 {
		t.Fatalf("连续休市期望 8 天（10/1~10/8），got %d", got.ClosedRun)
	}
	if got.DaysToNextOpen != 9 {
		t.Fatalf("距下一开市期望 9 自然日（9/30→10/9），got %d", got.DaysToNextOpen)
	}

	// 2026-09-29（周二）= 长假前第 2 个交易日。
	got2 := ProbeHolidayWindow(dayOf(t, "20260929"))
	if got2.PreHolidayN != 2 {
		t.Fatalf("节前第 N 个交易日期望 2，got %d", got2.PreHolidayN)
	}
	if got2.ClosedRun != 8 {
		t.Fatalf("连续休市期望 8 天，got %d", got2.ClosedRun)
	}
}

// TestProbeHolidayWindowWeekend 普通周末（2026-10-16 周五）。
func TestProbeHolidayWindowWeekend(t *testing.T) {
	withCalendar(t, nil)

	got := ProbeHolidayWindow(dayOf(t, "20261016")) // 周五
	if got.PreHolidayN != 1 {
		t.Fatalf("周五的节前第 N 个交易日期望 1，got %d", got.PreHolidayN)
	}
	if got.ClosedRun != 2 {
		t.Fatalf("周末连续休市期望 2 天，got %d", got.ClosedRun)
	}
	if got.DaysToNextOpen != 3 {
		t.Fatalf("周五距下周一开市期望 3 自然日，got %d", got.DaysToNextOpen)
	}

	got2 := ProbeHolidayWindow(dayOf(t, "20261015")) // 周四
	if got2.PreHolidayN != 2 {
		t.Fatalf("周四的节前第 N 个交易日期望 2，got %d", got2.PreHolidayN)
	}

	// 周二：周末还在 4 个交易日之外 → 节前因子不应生效（PreHolidayN=4）。
	got3 := ProbeHolidayWindow(dayOf(t, "20261013"))
	if got3.PreHolidayN != 4 {
		t.Fatalf("周二的节前第 N 个交易日期望 4，got %d", got3.PreHolidayN)
	}
}

// TestProbeHolidayWindowUnloaded 日历未加载时 CalendarLoaded=false（调用方据此弃权）。
func TestProbeHolidayWindowUnloaded(t *testing.T) {
	calMu.Lock()
	oldClosed, oldLoaded, oldWindow := closedDays, calLoaded, calWindow
	closedDays, calLoaded, calWindow = map[string]bool{}, false, ""
	calMu.Unlock()
	t.Cleanup(func() {
		calMu.Lock()
		closedDays, calLoaded, calWindow = oldClosed, oldLoaded, oldWindow
		calMu.Unlock()
	})

	got := ProbeHolidayWindow(dayOf(t, "20260930"))
	if got.CalendarLoaded {
		t.Fatalf("未加载时应报告 CalendarLoaded=false")
	}
	// 未加载时仍按周末口径给出读数（只是调用方会弃权），长假不可见 → ClosedRun 只数到周末。
	if got.ClosedRun != 2 {
		t.Fatalf("未加载时连续休市应只识别到周末 2 天，got %d", got.ClosedRun)
	}
}

// TestTradingDaysUntil 交易日距离（跨周末/长假必须按交易日，自然日实现在此红）。
func TestTradingDaysUntil(t *testing.T) {
	withCalendar(t, []string{"20261001", "20261002", "20261005", "20261006", "20261007", "20261008"})

	cases := []struct {
		from, to string
		want     int
	}{
		{"20260930", "20261009", 1},  // 长假 8 天只算 1 个交易日（自然日会算成 9）
		{"20261016", "20261019", 1},  // 跨周末
		{"20261016", "20261023", 5},  // 一周 5 个交易日
		{"20261016", "20261016", 0},  // 事件当日
		{"20261016", "20261015", -1}, // 事件日已过
		{"bad", "20261016", -1},      // 解析失败
	}
	for _, c := range cases {
		if got := TradingDaysUntil(c.from, c.to); got != c.want {
			t.Fatalf("TradingDaysUntil(%s,%s) 期望 %d，got %d", c.from, c.to, c.want, got)
		}
	}
}
