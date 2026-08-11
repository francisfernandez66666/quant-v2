package data

import (
	"testing"
	"time"
)

// TestBeforeOpenTrade 开市(9:30)前视为盘前，9:30 起不再压制；周末视为盘前。
func TestBeforeOpenTrade(t *testing.T) {
	tue := time.Date(2026, 8, 4, 0, 0, 0, 0, time.Local) // 2026-08-04 是周二
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
		{"周末任意时刻", time.Date(2026, 8, 8, 10, 0, 0, 0, time.Local), true},
	}
	for _, c := range cases {
		if got := BeforeOpenTrade(c.time); got != c.want {
			t.Errorf("%s: BeforeOpenTrade(%v) = %v, want %v", c.name, c.time, got, c.want)
		}
	}
}