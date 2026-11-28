// replay_inputs_test.go — §REPLAY-INPUTS 2026-11-28：日尺归一的行为锁。
//
// 只钉一件事：**认得的形状归一、不认识的一律空串**，因为调用方拿空串是"弃权"，
// 而任何"猜一个"的实现（比如把 "2026-1-2" 补齐、把空串当今天）都会让
// "永远今天没数"这种静默失效换一种形态重演——本仓已栽过三次（flow_factor_daily 横线、
// sector_map.day 紧凑、daily.trade_date 紧凑，三处各有现网实录）。
//
// English: recognized shapes normalize, anything else returns "" so callers abstain
// rather than inventing a date.
package cntime

import "testing"

func TestCompactDayRecognizedAndUnrecognized(t *testing.T) {
	yes := map[string]string{
		"20261127":     "20261127", // 库里主流形态
		"2026-11-27":   "20261127", // 东财 f51 / flow_factor_daily / d1_scores
		" 2026-11-27 ": "20261127", // 上游偶带空白，trim 是必须的
		"20260101":     "20260101",
	}
	for in, want := range yes {
		if got := CompactDay(in); got != want {
			t.Fatalf("CompactDay(%q)=%q，期望 %q", in, got, want)
		}
	}
	no := []string{"", "   ", "today", "2026/11/27", "2026-1-27", "2026112", "202611271",
		"2026-11-27T15:00", "null", "2026-11-2", "abc12345"}
	for _, in := range no {
		if got := CompactDay(in); got != "" {
			t.Fatalf("CompactDay(%q)=%q，必须是空串（读不懂＝弃权，不是猜一个）", in, got)
		}
	}
}

func TestCompactDayAcceptsItsOwnOutput(t *testing.T) {
	// 幂等：归一一次与归一两次结果相同——装配层会反复过这个函数，不幂等就会漂。
	now := Now()
	once := CompactDay(DayCompactOf(now))
	if twice := CompactDay(once); twice != once || once == "" {
		t.Fatalf("CompactDay 对自身输出必须幂等，得 once=%q twice=%q", once, twice)
	}
	if dashed := CompactDay(DayOf(now)); dashed != once {
		t.Fatalf("横线与紧凑两种输入必须归一到同一个值，得 %q vs %q", dashed, once)
	}
}
