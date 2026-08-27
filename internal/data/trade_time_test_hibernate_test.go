// trade_time_test_hibernate_test.go — 下一活跃交易时段时长单元测试：覆盖盘后、跨周末及开盘前边界。
package data

import (
	"testing"
	"time"
)

// TestDurationToNextActiveSession 验证下一活跃交易时段时长的跨周末/盘后/盘前边界计算。
func TestDurationToNextActiveSession(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// 周三 20:00 → 次日 8:30 = 12.5h
	wed := time.Date(2026, 8, 19, 20, 0, 0, 0, loc)
	if d := DurationToNextActiveSession(wed); d != 12*time.Hour+30*time.Minute {
		t.Fatalf("周三晚: %v", d)
	}
	// 周五 16:00 → 下周一 8:30（跨周末）= 64.5h
	fri := time.Date(2026, 8, 21, 16, 0, 0, 0, loc)
	want := 64*time.Hour + 30*time.Minute
	if d := DurationToNextActiveSession(fri); d != want {
		t.Fatalf("周五盘后: got %v want %v", d, want)
	}
	// 周六全天 → 周一 8:30
	sat := time.Date(2026, 8, 22, 12, 0, 0, 0, loc)
	want2 := 44*time.Hour + 30*time.Minute // 周六12点→周一8:30 = 12+24+8.5h
	if d := DurationToNextActiveSession(sat); d != want2 {
		t.Fatalf("周六: got %v want %v", d, want2)
	}
	// 盘中 10:00（活跃）→ 明早 8:30（调用方只在非活跃时用，这里验证仍给出下窗）
	intraday := time.Date(2026, 8, 19, 10, 0, 0, 0, loc)
	if d := DurationToNextActiveSession(intraday); d <= 0 {
		t.Fatalf("盘中应给正时长: %v", d)
	}
	// 8:29 → 1 分钟
	pre := time.Date(2026, 8, 19, 8, 29, 0, 0, loc)
	if d := DurationToNextActiveSession(pre); d != time.Minute {
		t.Fatalf("开盘前1分钟: %v", d)
	}
}
