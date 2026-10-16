// macro_lead_window_test.go — §DYN-SIZE P4 前瞻窗（交易日 → 自然日折算）守护测试。
//
// 反例锁（方案 §5「交易日距离」测试锁）：改造前宏观事件的取数窗是**定长 7 自然日**
// （data/macro_calendar.go 的 168h）。"事件前 5 个交易日渐进收仓"跨一次国庆长假是 15 个自然日，
// 定长窗下节前那几天前瞻曲线取不到任何事件——本文件把那次失效钉成回归锁。
//
// English: guard tests for the §DYN-SIZE P4 lead window. The old fixed 7-natural-day fetch window
// misses events before a long holiday (5 trading days can span 15 natural days), silently killing
// the lead curve. These tests pin that failure mode.
package data

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// nationalDay2026 2026 国庆休市日（10/1、10/2、10/5~10/8）。
var nationalDay2026 = []string{"20261001", "20261002", "20261005", "20261006", "20261007", "20261008"}

// macroEv 构造一条宏观事件（Duration=0 → 影响期只有当日）。
func macroEv(t *testing.T, day, level, impact string) MacroEvent {
	t.Helper()
	return MacroEvent{Date: dayOf(t, day), Level: level, Impact: impact, Duration: 0, Title: level}
}

// TestMacroEventsWithinWidenedWindow 定长 7 自然日窗漏掉的事件，宽窗能取到。
// 反例腿：改造前的 GetActiveMacroEvents 必须**取不到** 10 自然日后的事件。
func TestMacroEventsWithinWidenedWindow(t *testing.T) {
	now := dayOf(t, "20260920") // 周日
	events := []MacroEvent{
		macroEv(t, "20260924", "cpi", "high"),      // +4 天：两种窗都该取到
		macroEv(t, "20260930", "contract", "high"), // +10 天：7 天窗取不到，21 天窗取到
		macroEv(t, "20261020", "fomc", "high"),     // +30 天：两种窗都取不到
	}

	narrow := GetActiveMacroEvents(events, now)
	if len(narrow) != 1 || narrow[0].Level != "cpi" {
		t.Fatalf("定长 7 天窗应只取到 cpi，得 %+v", narrow)
	}

	wide := GetActiveMacroEventsWithin(events, now, 21*24*time.Hour)
	if len(wide) != 2 {
		t.Fatalf("21 天窗应取到 2 条，得 %d：%+v", len(wide), wide)
	}
	levels := map[string]bool{}
	for _, e := range wide {
		levels[e.Level] = true
	}
	if !levels["cpi"] || !levels["contract"] {
		t.Fatalf("宽窗应含 cpi+contract，得 %v", levels)
	}
	// 优先级排序：contract(90) > cpi(60)。
	if wide[0].Level != "contract" {
		t.Fatalf("高优先级事件应排前，得 %s", wide[0].Level)
	}
}

// TestMacroEventsWithinZeroHorizonFallback horizon<=0 回退定长 7 天（防误传 0 把窗开成无限）。
func TestMacroEventsWithinZeroHorizonFallback(t *testing.T) {
	now := dayOf(t, "20260920")
	events := []MacroEvent{macroEv(t, "20260930", "contract", "high")} // +10 天
	if got := GetActiveMacroEventsWithin(events, now, 0); len(got) != 0 {
		t.Fatalf("horizon=0 应回退 7 天窗（取不到 +10 天事件），得 %+v", got)
	}
}

// TestTradingDaySpanAcrossLongHoliday 交易日跨度必须走日历：2026-09-30 起 5 个交易日 = 15 自然日
// （10/1~10/8 休市 8 天）。按自然日算会得出 5，按 7 天定长窗会漏掉一半。
func TestTradingDaySpanAcrossLongHoliday(t *testing.T) {
	withCalendar(t, nationalDay2026)

	if got := TradingDaySpan("20260930", 5); got != 15 {
		t.Fatalf("9/30 起 5 个交易日应跨 15 自然日（10/9、10/12~10/15），得 %d", got)
	}
	// 普通周：5 个交易日 = 7 自然日。
	if got := TradingDaySpan("20260923", 5); got != 7 {
		t.Fatalf("普通周 5 个交易日应跨 7 自然日，得 %d", got)
	}
	// 节前第 2 个交易日（9/29）起算：9/30、10/9、10/12~10/14 = 15 自然日。
	if got := TradingDaySpan("20260929", 5); got != 15 {
		t.Fatalf("9/29 起 5 个交易日应跨 15 自然日，得 %d", got)
	}
}

// TestTradingDaySpanGuards 非法输入一律 0（调用方按 0 走兜底，不得返回负数/垃圾）。
func TestTradingDaySpanGuards(t *testing.T) {
	withCalendar(t, nationalDay2026)
	if got := TradingDaySpan("20260930", 0); got != 0 {
		t.Fatalf("n=0 应返回 0，得 %d", got)
	}
	if got := TradingDaySpan("20260930", -3); got != 0 {
		t.Fatalf("n<0 应返回 0，得 %d", got)
	}
	if got := TradingDaySpan("bad-date", 5); got != 0 {
		t.Fatalf("非法日期应返回 0，得 %d", got)
	}
}

// TestCalendarCoverage 覆盖窗口读数：未加载 / 已加载无窗口 / 有窗口三段。
func TestCalendarCoverage(t *testing.T) {
	// 未加载：loaded=false。
	calMu.Lock()
	oldLoaded, oldWindow := calLoaded, calWindow
	calLoaded, calWindow = false, ""
	calMu.Unlock()
	t.Cleanup(func() {
		calMu.Lock()
		calLoaded, calWindow = oldLoaded, oldWindow
		calMu.Unlock()
	})
	if _, _, loaded := CalendarCoverage(); loaded {
		t.Fatal("未加载时应报告 loaded=false")
	}

	// 已加载但无窗口（SetClosedDays 来源）：loaded=true，窗口为空。
	withCalendar(t, nationalDay2026)
	if mn, mx, loaded := CalendarCoverage(); !loaded || mn != "" || mx != "" {
		t.Fatalf("已加载无窗口应返回空窗口+loaded=true，得 %q %q %v", mn, mx, loaded)
	}

	// 有窗口（API 刷新来源）：解析 min~max。
	calMu.Lock()
	calWindow = "20260101~20261231"
	calMu.Unlock()
	mn, mx, loaded := CalendarCoverage()
	if !loaded || mn != "20260101" || mx != "20261231" {
		t.Fatalf("窗口解析失败：得 %q %q %v", mn, mx, loaded)
	}
}

// TestCalendarForwardCoverage §DYN-SIZE P4 补强：前向覆盖天数 + 健康读数的"看得不够远"告警。
//
// 为什么单列一条锁：`TradingCalendarHealth` 原来只在"覆盖**未到**今日"（停更）时开口，
// 但节前因子要的是"未来那段休市有多长"——窗口覆盖到今日却只到今日+3 天时，国庆整个看不见，
// `ClosedRun` 退化成 2（周末），长假分级**静默**失效且无人知晓。两条判据不是一回事。
// English: the pre-holiday factor needs forward coverage, not merely "covers today". A window
// ending 3 days out silently degrades long-holiday grading to "weekends only".
func TestCalendarForwardCoverage(t *testing.T) {
	calMu.Lock()
	oldLoaded, oldWindow := calLoaded, calWindow
	calMu.Unlock()
	t.Cleanup(func() {
		calMu.Lock()
		calLoaded, calWindow = oldLoaded, oldWindow
		calMu.Unlock()
	})

	// ① 未加载 → -1（不可知，不做正面判断）
	calMu.Lock()
	calLoaded, calWindow = false, ""
	calMu.Unlock()
	if got := CalendarForwardCoverageDays(); got != -1 {
		t.Fatalf("未加载应返回 -1, got %d", got)
	}

	// ② 已加载但无窗口（SetClosedDays 来源）→ -1
	calMu.Lock()
	calLoaded, calWindow = true, ""
	calMu.Unlock()
	if got := CalendarForwardCoverageDays(); got != -1 {
		t.Fatalf("无窗口应返回 -1, got %d", got)
	}

	now := cntime.In(cntime.Now())
	minD := now.AddDate(0, 0, -30).Format("20060102")

	// ③ 窗口只到今日+3 天 → 3 天，健康读数必须告警（< calForwardCoverageMinDays）
	calMu.Lock()
	calLoaded, calWindow = true, minD+"~"+now.AddDate(0, 0, 3).Format("20060102")
	calMu.Unlock()
	if got := CalendarForwardCoverageDays(); got != 3 {
		t.Fatalf("前向覆盖应为 3 天, got %d", got)
	}
	if h := TradingCalendarHealth(); !strings.Contains(h, "前向覆盖仅 3 天") {
		t.Fatalf("覆盖过浅时健康读数应告警, got %q", h)
	}

	// ④ 窗口到今日+30 天 → 充足，健康读数不得出现"前向覆盖仅"
	calMu.Lock()
	calLoaded, calWindow = true, minD+"~"+now.AddDate(0, 0, 30).Format("20060102")
	calMu.Unlock()
	if got := CalendarForwardCoverageDays(); got != 30 {
		t.Fatalf("前向覆盖应为 30 天, got %d", got)
	}
	if h := TradingCalendarHealth(); strings.Contains(h, "前向覆盖仅") {
		t.Fatalf("覆盖充足时不应告警, got %q", h)
	}

	// ⑤ 覆盖未到今日（停更）优先报"停更"而非"前向覆盖"（两条判据不叠加刷屏）
	calMu.Lock()
	calLoaded, calWindow = true, minD+"~"+now.AddDate(0, 0, -5).Format("20060102")
	calMu.Unlock()
	if got := CalendarForwardCoverageDays(); got != -5 {
		t.Fatalf("覆盖未到今日应返回负数, got %d", got)
	}
	if h := TradingCalendarHealth(); !strings.Contains(h, "覆盖未到今日") {
		t.Fatalf("停更应优先报停更, got %q", h)
	}
}
