package newsagent

import (
	"testing"
	"time"
)

// tEvent 构造带时间与分数的测试事件。
func tEvent(t *testing.T, dt string, score float64) *NewsEvent {
	return &NewsEvent{Title: "T", Datetime: dt, Score: score, EventType: "公司"}
}

func TestEffectiveScore(t *testing.T) {
	base := "2026-09-09 09:30:00"
	cases := []struct {
		name string
		age  time.Duration
		want float64
	}{
		{name: "age0", age: 0, want: 0.8},
		{name: "1halfLife", age: 60 * time.Minute, want: 0.4},
		{name: "2halfLife", age: 120 * time.Minute, want: 0.2},
		{name: "3halfLife", age: 180 * time.Minute, want: 0.1},
		{name: "expiredBeyond3", age: 200 * time.Minute, want: 0},
	}
	now := time.Date(2026, 9, 9, 9, 30, 0, 0, time.Local)
	for _, tc := range cases {
		ev := tEvent(t, base, 0.8)
		got := ev.EffectiveScore(now.Add(tc.age))
		if got != tc.want {
			t.Errorf("%s: want %.4f got %.4f", tc.name, tc.want, got)
		}
	}
}

func TestEffectiveScore_PolicySlowHalfLife(t *testing.T) {
	ev := &NewsEvent{Title: "P", Datetime: "2026-09-09 09:30:00", Score: 1.0, EventType: "政策"}
	now := time.Date(2026, 9, 9, 9, 30, 0, 0, time.Local).Add(60 * time.Minute)
	// 政策半衰期 120min：age=60min → 0.5^0.5 ≈ 0.7071
	got := ev.EffectiveScore(now)
	if got < 0.70 || got > 0.71 {
		t.Errorf("policy 60min: want ~0.7071 got %.4f", got)
	}
}

func TestEffectiveScore_BadDatetimeKeepsOriginal(t *testing.T) {
	ev := &NewsEvent{Title: "B", Datetime: "not-a-date", Score: 0.9}
	if got := ev.EffectiveScore(time.Now()); got != 0.9 {
		t.Errorf("bad datetime: want 0.9 got %v", got)
	}
}

func TestEffectiveScore_FutureKeepsOriginal(t *testing.T) {
	ev := &NewsEvent{Title: "F", Datetime: "2026-09-09 15:00:00", Score: 0.7}
	now := time.Date(2026, 9, 9, 9, 30, 0, 0, time.Local)
	if got := ev.EffectiveScore(now); got != 0.7 {
		t.Errorf("future event: want 0.7 got %v", got)
	}
}

func TestDefaultHalfLifeFallback(t *testing.T) {
	if hl := decayHalfLife(&NewsEvent{EventType: "未知类型"}); hl != 120*time.Minute {
		t.Errorf("unknown type fallback: want 120m got %v", hl)
	}
	if hl := decayHalfLife(nil); hl != 120*time.Minute {
		t.Errorf("nil event fallback: want 120m got %v", hl)
	}
}

func TestPow2Negative(t *testing.T) {
	if got := pow2(-1); got != 0.5 {
		t.Errorf("pow2(-1): want 0.5 got %v", got)
	}
	if got := pow2(-2); got != 0.25 {
		t.Errorf("pow2(-2): want 0.25 got %v", got)
	}
	// 过深指数循环截断保护应自稳而不是溢出
	if got := pow2(-100); got < 0 || got > 1 {
		t.Errorf("pow2(-100): out of [0,1] got %v", got)
	}
}