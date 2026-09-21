// trade_time_test.go — 交易时段判定单元测试：验证盘前、午休及盘中各时间窗口的边界判定。
package data

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// TestBeforeOpenTrade 开市(9:30)前视为盘前，9:30 起不再压制；周末视为盘前。
func TestBeforeOpenTrade(t *testing.T) {
	tue := time.Date(2026, 8, 4, 0, 0, 0, 0, cntime.Loc) // 2026-08-04 是周二（按北京墙钟，CI UTC 不漂移）
	cases := []struct {
		name string
		time time.Time
		want bool
	}{
		{"09:00 盘前", tue.Add(9 * time.Hour), true},
		{"09:15 盘前(竞价)", tue.Add(9*time.Hour + 15*time.Minute), true},
		{"09:29:59 盘前", tue.Add(9*time.Hour + 29*time.Minute + 59*time.Second), true},
		{"09:30:00 开盘", tue.Add(9*time.Hour + 30*time.Minute), false},
		{"10:00 盘中", tue.Add(10 * time.Hour), false},
		{"14:30 午后", tue.Add(14*time.Hour + 30*time.Minute), false},
		{"周末任意时刻", time.Date(2026, 8, 8, 10, 0, 0, 0, cntime.Loc), true},
	}
	for _, c := range cases {
		if got := BeforeOpenTrade(c.time); got != c.want {
			t.Errorf("%s: BeforeOpenTrade(%v) = %v, want %v", c.name, c.time, got, c.want)
		}
	}
}

// TestIsPreAfternoon 午休(11:30-13:00)判定：午休返回 true，盘中/盘前/非交易日返回 false。
func TestIsPreAfternoon(t *testing.T) {
	tue := time.Date(2026, 8, 4, 0, 0, 0, 0, cntime.Loc) // 2026-08-04 是周二（按北京墙钟）
	cases := []struct {
		name string
		time time.Time
		want bool
	}{
		{"11:29:59 上午盘末", tue.Add(11*time.Hour + 29*time.Minute + 59*time.Second), false},
		{"11:30 午休开始", tue.Add(11*time.Hour + 30*time.Minute), true},
		{"12:00 午休中", tue.Add(12 * time.Hour), true},
		{"12:59:59 午休末", tue.Add(12*time.Hour + 59*time.Minute + 59*time.Second), true},
		{"13:00 午后开盘", tue.Add(13 * time.Hour), false},
		{"10:00 盘中", tue.Add(10 * time.Hour), false},
		{"周末 12:00", time.Date(2026, 8, 8, 12, 0, 0, 0, cntime.Loc), false},
	}
	for _, c := range cases {
		if got := IsPreAfternoon(c.time); got != c.want {
			t.Errorf("%s: IsPreAfternoon(%v) = %v, want %v", c.name, c.time, got, c.want)
		}
	}
}

// TestIsContinuousTrade 连续竞价推送窗口（9:30-11:30 / 13:00-14:57）边界判定：
// §CB-TICKWINDOW 回归——盘前/开盘竞价/午休/收盘竞价必须为 false（桥心跳 tick 驱动，
// 这些窗口静默属正常，计入失联会每天误熔，2026-09-21 实录）。
func TestIsContinuousTrade(t *testing.T) {
	tue := time.Date(2026, 8, 4, 0, 0, 0, 0, cntime.Loc) // 2026-08-04 是周二（按北京墙钟）
	cases := []struct {
		name string
		time time.Time
		want bool
	}{
		{"09:00 盘前", tue.Add(9 * time.Hour), false},
		{"09:15 开盘竞价", tue.Add(9*time.Hour + 15*time.Minute), false},
		{"09:29:59 竞价结束前", tue.Add(9*time.Hour + 29*time.Minute + 59*time.Second), false},
		{"09:30:00 连续竞价开始", tue.Add(9*time.Hour + 30*time.Minute), true},
		{"11:29:59 上午盘末", tue.Add(11*time.Hour + 29*time.Minute + 59*time.Second), true},
		{"11:30 午休", tue.Add(11*time.Hour + 30*time.Minute), false},
		{"12:59 午休末", tue.Add(12*time.Hour + 59*time.Minute), false},
		{"13:00 下午盘开始", tue.Add(13 * time.Hour), true},
		{"14:56:59 收盘竞价前", tue.Add(14*time.Hour + 56*time.Minute + 59*time.Second), true},
		{"14:57 收盘竞价", tue.Add(14*time.Hour + 57*time.Minute), false},
		{"15:30 盘后", tue.Add(15*time.Hour + 30*time.Minute), false},
		{"周末 10:00", time.Date(2026, 8, 8, 10, 0, 0, 0, cntime.Loc), false},
	}
	for _, c := range cases {
		if got := IsContinuousTrade(c.time); got != c.want {
			t.Errorf("%s: IsContinuousTrade(%v) = %v, want %v", c.name, c.time, got, c.want)
		}
	}
}
