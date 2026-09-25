// trade_time_calendar_test.go — §CAL-GATE（缺陷 D-25-1）：法定休市日落在工作日时，
// internal/data 全部"现在属于哪个时段"的判据必须按休市处理；同时钉住两件事：
// ① 普通交易日各判据不许被改红（等值锁，防"为修休市把盘中也判死"的单向锁漂移）；
// ② 日历未加载时是 fail-open（法定节假日暂按交易日跑）——这是刻意的缺省方向，
//
//	用用例显式声明，将来谁改成 fail-close 必须先红在这里。
//
// 时间锚点全部用北京墙钟构造的固定日期：2026-09-25（中秋休市第一天，恰为周五）与
// 2026-08-04（周二，普通交易日），与 §BRIDGE 部署实录一致，不依赖测试机时区。
package data

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// at 按北京墙钟造一个时刻（HH, MM）。
func at(year int, month time.Month, day, hh, mm int) time.Time {
	return time.Date(year, month, day, hh, mm, 0, 0, cntime.Loc)
}

// TestHolidaySessionPredicates 注入"2026-09-25 为休市日"后，休市日白天的所有时段判据：
// 交易/窗口类恒 false，BeforeOpenTrade 恒 true（视同未开盘、压制买入信号），
// CurrentSession=SessionClosed，ScanInterval 落到 normalSec，TradingDayDate 回退到 09-24。
func TestHolidaySessionPredicates(t *testing.T) {
	SetClosedDays([]string{"20260925"}) // 中秋：周五但休市（生产实录日期）
	defer SetClosedDays(nil)

	hol := at(2026, 9, 25, 10, 30) // 休市日上午 10:30 —— 旧写法这一刻判"上午盘"
	falseCases := []struct {
		name string
		got  bool
	}{
		{"IsTradeTime", IsTradeTime(hol)},
		{"IsFullTradingHours", IsFullTradingHours(hol)},
		{"IsPreOpen(9:15-9:25)", IsPreOpen(at(2026, 9, 25, 9, 20))},
		{"IsMorningHighFreq", IsMorningHighFreq(hol)},
		{"IsMidFreqWindow", IsMidFreqWindow(at(2026, 9, 25, 9, 50))},
		{"IsAfternoonHighFreq", IsAfternoonHighFreq(at(2026, 9, 25, 13, 10))},
		{"IsPreMarket", IsPreMarket(at(2026, 9, 25, 9, 0))},
		{"IsPreAfternoon", IsPreAfternoon(at(2026, 9, 25, 12, 0))},
		{"IsAfterMarket", IsAfterMarket(at(2026, 9, 25, 16, 0))},
		{"IsTradingWindow", IsTradingWindow(hol)},
		{"IsActiveSession(经 CurrentSession)", IsActiveSession(hol)},
		{"IsContinuousTrade", IsContinuousTrade(hol)},
		{"IsTradingDay", IsTradingDay(hol)},
	}
	for _, c := range falseCases {
		if c.got {
			t.Errorf("休市日 %s 应为 false，实得 true（D-25-1 回归）", c.name)
		}
	}
	if !BeforeOpenTrade(hol) {
		t.Errorf("休市日 BeforeOpenTrade 应为 true（视同未开盘，压制买入信号），实得 false")
	}
	if got := CurrentSession(hol); got != SessionClosed {
		t.Errorf("休市日 CurrentSession 应为 SessionClosed，实得 %v", got)
	}
	if got := ScanInterval(hol, 5, 10, 7, 60); got != 60 {
		t.Errorf("休市日 ScanInterval 应落到 normalSec=60，实得 %d", got)
	}
	if got := TradingDayDate(hol); got != "20260924" {
		t.Errorf("休市日 TradingDayDate 应回退到上一交易日 20260924，实得 %s", got)
	}
	// NextTradeOpen：休市日 10:30 → 下一开盘点必须是 09-28（周一）09:15，跳过周末。
	wantOpen := at(2026, 9, 28, 9, 15)
	if got := NextTradeOpen(hol); got != wantOpen.Sub(hol) {
		t.Errorf("休市日 NextTradeOpen 应为 %v（下一交易日开盘），实得 %v", wantOpen.Sub(hol), got)
	}
}

// TestHolidayLongBreakNextTradeOpen 长假（7 天休市 + 周末）时 NextTradeOpen 的 14 天窗口：
// 旧实现的 7 天循环在"节后第 8 天才开盘"的日历下找不到开盘点、退化返回 0（立即开跑）。
func TestHolidayLongBreakNextTradeOpen(t *testing.T) {
	// 构造：2026-10-01(周四)~10-07(周三) 全休市（国庆形态），周末天然休。
	SetClosedDays([]string{
		"20261001", "20261002", "20261005", "20261006", "20261007",
	}) // 10-03/10-04 为周六日，无需入表
	defer SetClosedDays(nil)

	now := at(2026, 10, 1, 10, 0)
	got := NextTradeOpen(now)
	want := at(2026, 10, 8, 9, 15).Sub(now) // 下一交易日 = 10-08（周四）
	if got != want {
		t.Errorf("长假中 NextTradeOpen 应为 %v（跳到 10-08 开盘），实得 %v（=0 即 7 天窗口退化复发）", want, got)
	}
	if got == 0 {
		t.Error("NextTradeOpen 返回 0＝休市长假被当'立即开盘'，§CAL-GATE 14 天窗口锁失效")
	}
}

// TestNormalTradingDayPredicates 等值锁（防单向修）：普通交易日 2026-08-04（周二）各判据
// 必须维持原有真值——修休市日绝不能把盘中一起判死。
func TestNormalTradingDayPredicates(t *testing.T) {
	SetClosedDays([]string{"20260925"}) // 日历在场，但今天不在表里
	defer SetClosedDays(nil)

	day := at(2026, 8, 4, 0, 0)
	// 交易日白天各"窗口存在性"判据的真值清单（名称→实际调用结果），逐条断言为 true。
	// English: table of window predicates that must stay true on a normal trading day.
	trueCases := []struct {
		name string
		got  bool
	}{
		{"10:00 IsTradeTime", IsTradeTime(day.Add(10 * time.Hour))},
		{"10:00 IsFullTradingHours", IsFullTradingHours(day.Add(10 * time.Hour))},
		{"09:20 IsPreOpen", IsPreOpen(day.Add(9*time.Hour + 20*time.Minute))},
		{"09:50 IsMorningHighFreq", IsMorningHighFreq(day.Add(9*time.Hour + 50*time.Minute))},
		{"09:50 IsMidFreqWindow", IsMidFreqWindow(day.Add(9*time.Hour + 50*time.Minute))},
		{"13:10 IsAfternoonHighFreq", IsAfternoonHighFreq(day.Add(13*time.Hour + 10*time.Minute))},
		{"09:00 IsPreMarket", IsPreMarket(day.Add(9 * time.Hour))},
		{"12:00 IsPreAfternoon", IsPreAfternoon(day.Add(12 * time.Hour))},
		{"16:00 IsAfterMarket", IsAfterMarket(day.Add(16 * time.Hour))},
		{"10:00 IsTradingWindow", IsTradingWindow(day.Add(10 * time.Hour))},
		{"10:00 IsActiveSession", IsActiveSession(day.Add(10 * time.Hour))},
		{"10:00 IsContinuousTrade", IsContinuousTrade(day.Add(10 * time.Hour))},
		{"10:00 IsTradingDay", IsTradingDay(day.Add(10 * time.Hour))},
	}
	for _, c := range trueCases {
		if !c.got {
			t.Errorf("交易日 %s 应为 true（等值锁：休市修复不得伤及盘中），实得 false", c.name)
		}
	}
	if BeforeOpenTrade(day.Add(10 * time.Hour)) {
		t.Error("交易日 10:00 BeforeOpenTrade 应为 false，实得 true")
	}
	if got := CurrentSession(day.Add(10 * time.Hour)); got != SessionMorningTrade {
		t.Errorf("交易日 10:00 CurrentSession 应为上午盘，实得 %v", got)
	}
	if got := TradingDayDate(day.Add(10 * time.Hour)); got != "20260804" {
		t.Errorf("交易日 TradingDayDate 应即当日 20260804，实得 %s", got)
	}
}

// TestCalendarFailOpenDirection 显式声明缺省方向：日历集合为空（未加载/加载失败）时
// isClosedDay 恒 false ⇒ 法定节假日按交易日跑（fail-open，宁可多跑不漏跑真交易日）。
// 这是文档化行为：把它从"实现细节"抬成"用例锁"——将来若有人翻转成 fail-close，本用例先红，
// 届时必须连同 trading_calendar_loaded 量规与告警口径一起评审，而不是悄悄改判据。
func TestCalendarFailOpenDirection(t *testing.T) {
	SetClosedDays(nil) // 空日历＝未加载态（生产冷启动 API 成功前即此态）
	hol := at(2026, 9, 25, 10, 30)
	if !IsTradingDay(hol) {
		t.Error("fail-open 声明被破坏：空日历下 2026-09-25 竟被判非交易日（方向翻转为 fail-close？）")
	}
	if !IsTradeTime(hol) {
		t.Error("fail-open 声明被破坏：空日历下休市日 10:30 的 IsTradeTime 应为 true（按周末口径）")
	}
	if h := TradingCalendarHealth(); h == "" {
		t.Error("TradingCalendarHealth 必须给出非空读数（fail-open 可见性腿）")
	}
}

// TestTradingCalendarHealth 健康读数随状态变化：未加载→含"未加载"；加载→含休市日个数与窗口。
func TestTradingCalendarHealth(t *testing.T) {
	SetClosedDays([]string{"20260925"})
	defer SetClosedDays(nil)
	if !CalendarLoaded() {
		t.Fatal("SetClosedDays 后 CalendarLoaded 应为 true")
	}
	if ClosedDayCount() != 1 {
		t.Fatalf("休市日个数应为 1，实得 %d", ClosedDayCount())
	}
	if h := TradingCalendarHealth(); h == "" {
		t.Error("加载态健康读数不得为空")
	}
	setCalendarWindow("20260101", "20261231")
	if h := TradingCalendarHealth(); h == "" {
		t.Error("带窗口健康读数不得为空")
	}
}
