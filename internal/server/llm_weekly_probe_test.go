package server

// §0926E2E-MX3 周度探测调度判据用例：只锤日历语义（周日窗、10 点闸、同周幂等、
// 北京时区锚定），不碰网络与配置——探测本体与 /api/config/llm/probe 同源，
// 其行为面已有既有探测用例覆盖（llm_apply 侧），这里防的是"例行本身跑错日子"。
// 假绿防线：UTC 周日 17:00＝北京周一 01:00 这一枚反证必须红——若判据哪天改回
// time.Now().Weekday()（不带北京锚），它会立刻复活成缺陷（§TZ1 同族）。

import (
	"testing"
	"time"
)

// TestMX3WeeklyProbeDueWindow 周日窗口与小时闸 + 北京时区锚定 + 同周幂等。
func TestMX3WeeklyProbeDueWindow(t *testing.T) {
	// 北京 2026-09-26（周六）23:30 = UTC 15:30：周六深夜不得触发
	sat := time.Date(2026, 9, 26, 15, 30, 0, 0, time.UTC)
	if _, due := weeklyProbeDue(sat, ""); due {
		t.Fatal("周六不得触发周度探测")
	}
	// 北京 2026-09-27（周日）09:59 = UTC 01:59：窗口未开
	sunEarly := time.Date(2026, 9, 27, 1, 59, 0, 0, time.UTC)
	if _, due := weeklyProbeDue(sunEarly, ""); due {
		t.Fatal("北京周日 10:00 前不得触发")
	}
	// 北京 2026-09-27（周日）10:00 整：窗口开启（边界等值含）
	sun := time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	key, due := weeklyProbeDue(sun, "")
	if !due || key == "" {
		t.Fatalf("北京周日 10:00 应触发且给出周键: due=%v key=%q", due, key)
	}
	// 同周幂等：last==本周键 → 不再触发（一小时内一轮 tick 一次）
	if _, due2 := weeklyProbeDue(sun.Add(time.Hour), key); due2 {
		t.Fatal("同周重复触发（幂等锚失效）")
	}
	// 跨周解锁：上周键不挡本周
	if _, due3 := weeklyProbeDue(sun, "2026-W38"); !due3 {
		t.Fatal("上周锚不应挡住本周探测")
	}
	// 北京时区反证：UTC 周日 17:00 = 北京周一 01:00，绝不许按 UTC 星期触发
	utcSunBJSun := time.Date(2026, 9, 27, 17, 0, 0, 0, time.UTC) // 北京已是周一 01:00
	if _, due4 := weeklyProbeDue(utcSunBJSun, ""); due4 {
		t.Fatal("按 UTC 星期触发了（北京口径已是周一凌晨）——§TZ1 时区锚丢失")
	}
	// 周一（北京）任何小时都不触发
	mon := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC) // 北京周一 11:00
	if _, due5 := weeklyProbeDue(mon, ""); due5 {
		t.Fatal("周一不得触发")
	}
}
