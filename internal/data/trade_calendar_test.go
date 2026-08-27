// trade_calendar_test.go — §GAP3.1 回归：非周末休市日注入后，交易时段/交易日判定全链路消费。
// English: regression for §GAP3.1 — once closed days are injected, every trade-time predicate honors them.
package data

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// TestTradingCalendarConsumption 验证注入休市日后，交易时段/交易日/下一活跃时段判定全链路消费。
func TestTradingCalendarConsumption(t *testing.T) {
	// 已知星期锚点：2026-08-25=周二（env 当日）、26=周三、27=周四、28=周五、29/30=周末、31=周一
	loc := cntime.Loc
	d := func(day int, h, m int) time.Time {
		return time.Date(2026, 8, day, h, m, 0, 0, loc)
	}

	t.Run("未加载日历按周末口径", func(t *testing.T) {
		SetClosedDays(nil)
		if !IsTradingDay(d(26, 10, 0)) {
			t.Fatal("周三在无日历时应视为交易日")
		}
	})

	SetClosedDays([]string{"20260826", "20260827"}) // 假设周四周…周三周四休市
	defer SetClosedDays(nil)

	t.Run("IsTradingDay 消费休市日", func(t *testing.T) {
		if IsTradingDay(d(26, 10, 0)) || IsTradingDay(d(27, 10, 0)) {
			t.Fatal("已注入的休市日不应判为交易日")
		}
		if !IsTradingDay(d(28, 10, 0)) {
			t.Fatal("周五应仍为交易日")
		}
	})

	t.Run("TradingDayDate 回退跨休市日", func(t *testing.T) {
		// 周三盘中：当天与周四都休市 → 交易日回退到周二
		if got := TradingDayDate(d(26, 10, 0)); got != "20260825" {
			t.Fatalf("got %s, want 20260825", got)
		}
		// 周二盘中：自身是交易日
		if got := TradingDayDate(d(25, 14, 0)); got != "20260825" {
			t.Fatalf("got %s, want 20260825", got)
		}
	})

	t.Run("AddTradingDays 跳过休市日", func(t *testing.T) {
		// 周二 +1 交易日 → 周五（跳过周三/周四）
		if got := AddTradingDays("20260825", 1); got != "20260828" {
			t.Fatalf("got %s, want 20260828", got)
		}
	})

	t.Run("DurationToNextActiveSession 跨长假闹钟", func(t *testing.T) {
		// 周二 20:00（盘后）：正常下一窗口应为周三 8:30，但周三/周四休市 → 周五 8:30
		want := d(28, 8, 30).Sub(d(25, 20, 0))
		got := DurationToNextActiveSession(d(25, 20, 0))
		if got != want {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}
