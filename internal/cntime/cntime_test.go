// cntime_test.go — §CAL-COVER-GO（2026-10-12）：交易日历回写窗口的右边界自证。
// 这里只钉三件事，都是现网事故（09-28~30、10-08~09 两段真实交易日被"全部到齐"跳过）
// 的直接反面：① 窗口必须严格晚于今日（写 end=today 就是盲窗本体）；② 同一尺度必须同时
// 给出 compact/dashed 两种口径且落在**同一天**（tushare 与 baostock 两条写入口共用一张
// trade_cal，两个数字不一致时后写的会把前写的末行拖回去）；③ 换算按北京墙钟，宿主时区
// 不得把边界挪走一天（§TZ1/§时区收口 同族）。
package cntime

import (
	"strings"
	"testing"
	"time"
)

// TestCalendarEndDayExtendsFuture 回写右边界＝今日 + CalendarLookaheadDays，且必须真在未来。
func TestCalendarEndDayExtendsFuture(t *testing.T) {
	ref := time.Date(2026, 10, 12, 1, 30, 0, 0, time.UTC) // 任意时刻，只要北京日期是 10-12
	today := DayCompactOf(ref)
	end := CalendarEndDayCompact(ref)
	if end <= today {
		t.Fatalf("日历回写右边界必须晚于今日（end=today 即 §CAL-COVER 盲窗）: today=%s end=%s", today, end)
	}
	want := time.Date(2026, 10, 12, 0, 0, 0, 0, Loc).AddDate(0, 0, CalendarLookaheadDays).Format("20060102")
	if end != want {
		t.Fatalf("右边界应按 CalendarLookaheadDays=%d 天推进: got=%s want=%s", CalendarLookaheadDays, end, want)
	}
	if CalendarLookaheadDays < 30 {
		t.Fatalf("日历前瞻天数不得缩到不足一个长假+春节连休窗口（现网口径 45）: got=%d", CalendarLookaheadDays)
	}
}

// TestCalendarEndDayFormatsAgree compact 与 dashed 两个口径必须是同一个日子——
// 两条写日历的链共用一张表，格式各自漂移等于把末行互相拖回去。
func TestCalendarEndDayFormatsAgree(t *testing.T) {
	ref := time.Date(2026, 9, 30, 15, 0, 0, 0, Loc)
	compact := CalendarEndDayCompact(ref)
	dashed := CalendarEndDay(ref)
	if len(compact) != 8 || len(dashed) != 10 {
		t.Fatalf("口径格式不对: compact=%s dashed=%s", compact, dashed)
	}
	if compact != strings.ReplaceAll(dashed, "-", "") {
		t.Fatalf("两种口径必须同一天: compact=%s dashed=%s", compact, dashed)
	}
}

// TestCalendarEndDayIgnoresHostZone 宿主时区为 UTC 时，北京 10-12 23:30 在 UTC 侧仍是 10-12，
// 而北京 10-13 00:30 在 UTC 侧是 10-12 的 16:30——两种情况下右边界都必须按**北京日历**算，
// 否则"今日"跨时区漂移一天，覆盖判据跟着漂（§时区收口 的原始事故形态）。
func TestCalendarEndDayIgnoresHostZone(t *testing.T) {
	utcLate := time.Date(2026, 10, 12, 15, 30, 0, 0, time.UTC) // = 北京 10-12 23:30
	want := time.Date(2026, 10, 12, 12, 0, 0, 0, Loc).AddDate(0, 0, CalendarLookaheadDays).Format("20060102")
	if got := CalendarEndDayCompact(utcLate); got != want {
		t.Fatalf("UTC 宿主下日历边界漂移: got=%s want=%s", got, want)
	}
	utcNext := time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC) // = 北京 10-13 00:30（北京已跨日）
	wantNext := time.Date(2026, 10, 13, 12, 0, 0, 0, Loc).AddDate(0, 0, CalendarLookaheadDays).Format("20060102")
	if got := CalendarEndDayCompact(utcNext); got != wantNext {
		t.Fatalf("北京跨日后日历边界未跟随: got=%s want=%s", got, wantNext)
	}
}
