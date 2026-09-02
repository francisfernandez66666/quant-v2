// 情绪相位分参回测单元测试（§Phase3）。
// 验证历史情绪阶段标定与区间分布统计。
// English: unit tests for the sentiment-phase parameterization module (Phase 3).
package research

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// phaseCfgStub 构造测试用情绪阈值（与 data 情绪包测试桩同量级）。
// English: emotion-threshold config for tests (same scale as the data package stub).
func phaseCfgStub() *config.EmotionConfig {
	return &config.EmotionConfig{
		EmoClimaxLimitupMin:   80,
		EmoClimaxBoardMin:     7,
		EmoFermentLimitupMin:  40,
		EmoFermentLimitupMax:  79,
		EmoFermentBoardMax:    6,
		EmoStartLimitupMin:    15,
		EmoStartLimitupMax:    39,
		EmoStartBoardMax:      3,
		EmoIceLimitupMax:      14,
		EmoIceBoardMax:        2,
		EmoRetreatLimitupMax:  14,
		EmoRetreatBoardMax:    3,
		EmoDivergeLimitupDrop: 15,
		EmoDivergeBoardDrop:   2,
	}
}

func TestPhaseFromEmotionStat(t *testing.T) {
	cfg := phaseCfgStub()
	cases := []struct {
		name     string
		limitup  int
		maxboard int
		want     string
	}{
		{"高潮", 85, 8, "高潮"},
		{"发酵", 50, 4, "发酵"},
		{"启动", 25, 2, "启动"},
		{"冰点", 8, 1, "冰点"},
		{"无涨停启动", 0, 0, "启动"},
	}
	for _, c := range cases {
		got := PhaseFromEmotionStat(store.EmotionStat{LimitUp: c.limitup, MaxBoard: c.maxboard}, cfg)
		if got != c.want {
			t.Fatalf("[%s] limitup=%d board=%d 期望 %s，实际 %s", c.name, c.limitup, c.maxboard, c.want, got)
		}
	}
	// nil cfg 走内置默认阈值（50/4 应判定发酵——默认阈值与桩同量级）
	if got := PhaseFromEmotionStat(store.EmotionStat{LimitUp: 50, MaxBoard: 4}, nil); got != "发酵" {
		t.Fatalf("nil cfg 默认阈值应判定发酵，实际 %s", got)
	}
}

func TestEmotionPhaseHist(t *testing.T) {
	stats := []store.EmotionStat{
		{Date: "20260101", LimitUp: 8, MaxBoard: 1},  // 冰点
		{Date: "20260102", LimitUp: 85, MaxBoard: 8}, // 高潮
		{Date: "20260103", LimitUp: 10, MaxBoard: 2}, // 冰点
	}
	h := EmotionPhaseHist(stats, phaseCfgStub())
	if h.From != "20260101" || h.To != "20260103" || h.Days != 3 {
		t.Fatalf("区间元信息错误: %+v", h)
	}
	if h.PhaseDays["冰点"] != 2 || h.PhaseDays["高潮"] != 1 {
		t.Fatalf("相位分布错误: %+v", h.PhaseDays)
	}
	if h.Last != "冰点" {
		t.Fatalf("末尾阶段错误: %s", h.Last)
	}
	// 空输入不 panic
	if e := EmotionPhaseHist(nil, nil); e.Days != 0 {
		t.Fatalf("空输入应为零值: %+v", e)
	}
}
