// 本文件：消息中心"已展示但未进打分池"可观测旁路 logDroppedFromPool 的单测。
// 覆盖：≥0.25 落盘但被 ≥0.5 阈值丢弃的事件被计数；≥0.5 已进有效池的不计；低分(未落盘)不计。
// English: This file: unit tests for the observability side-channel logDroppedFromPool for "shown in the message center but not admitted to the scoring pool".
// Covers: events ≥0.25 persisted but dropped by the ≥0.5 threshold are counted; ≥0.5 events already in the valid pool are not; low-score (not persisted) events are not.
package engine

import (
	"testing"

	"quant-trading-v2/internal/newsagent"
)

// mkEv 构造新闻事件（测试辅助）。
func mkEv(title, level, direction string, score float64) newsagent.NewsEvent {
	return newsagent.NewsEvent{Title: title, Level: level, Direction: direction, Score: score}
}

// TestLogDroppedFromPool 验证：消息中心展示(≥0.25)但在有效池(≥0.5)之外的事件被计入 dropped。
// English: TestLogDroppedFromPool verifies: events shown in the message center (≥0.25) but outside the valid pool (≥0.5) are counted in dropped.
func TestLogDroppedFromPool(t *testing.T) {
	shown := []newsagent.NewsEvent{
		mkEv("A-l", "个股", "利好", 0.8), // ≥0.5 且进池(标题在 valid) → 不计
		// English: ≥0.5 and admitted (title in valid) → not counted.
		mkEv("B-mid", "个股", "利好", 0.4), // 0.25~0.5 落盘但掉阈值 → 计入
		// English: 0.25~0.5 persisted but falls off the threshold → counted.
		mkEv("C-low", "个股", "利好", 0.1), // <0.25 未落盘 → 不计
		// English: <0.25 not persisted → not counted.
		mkEv("D-valid", "板块", "利好", 0.7), // ≥0.5 但标题不在 valid → 计入
		// English: ≥0.5 but title not in valid → counted.
	}
	valid := []newsagent.NewsEvent{
		mkEv("A-l", "个股", "利好", 0.8),
	}
	got := logDroppedFromPool(shown, valid)
	if got != 2 {
		t.Fatalf("期望 dropped=2(B-mid 掉阈值 + D-valid 未入池), got %d", got)
	}
}

// TestLogDroppedFromPoolEmpty 验证空输入不崩溃。
// English: TestLogDroppedFromPoolEmpty verifies empty input does not crash.
func TestLogDroppedFromPoolEmpty(t *testing.T) {
	if got := logDroppedFromPool(nil, nil); got != 0 {
		t.Fatalf("空输入应 dropped=0, got %d", got)
	}
}
