package data

import (
	"fmt"
	"testing"

	"quant-trading-v2/internal/config"
)

func emotionCfgStub() *config.EmotionConfig {
	return &config.EmotionConfig{
		EmoClimaxLimitupMin:  80,
		EmoClimaxBoardMin:    7,
		EmoFermentLimitupMin: 40,
		EmoFermentLimitupMax: 79,
		EmoFermentBoardMax:   6,
		EmoStartLimitupMin:   15,
		EmoStartLimitupMax:   39,
		EmoStartBoardMax:     3,
		EmoIceLimitupMax:     14,
		EmoIceBoardMax:       2,
		EmoRetreatLimitupMax: 14,
		EmoRetreatBoardMax:   3,
		EmoDivergeLimitupDrop: 15,
		EmoDivergeBoardDrop:  2,
	}
}

// poolOf 构造 n 只涨停股，其中 index 0 连板为 maxBoard。
func poolOf(n, maxBoard int) []LimitUpStock {
	pool := make([]LimitUpStock, n)
	for i := range pool {
		pool[i] = LimitUpStock{Code: fmt.Sprintf("%06d", i), LianBan: 1}
	}
	if n > 0 {
		pool[0].LianBan = maxBoard
	}
	return pool
}

func TestDetectEmotionPhaseV2(t *testing.T) {
	cfg := emotionCfgStub()
	cases := []struct {
		name string
		pool []LimitUpStock
		want string
	}{
		{"高潮", poolOf(90, 8), "高潮"},
		{"发酵", poolOf(50, 5), "发酵"},
		{"启动", poolOf(20, 2), "启动"},
		{"冰点", poolOf(10, 1), "冰点"},
		{"空池", nil, "启动"},
	}
	for _, c := range cases {
		if got := DetectEmotionPhaseV2(c.pool, 0, 0, cfg); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
