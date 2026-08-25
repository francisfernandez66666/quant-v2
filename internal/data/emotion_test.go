package data

import (
	"fmt"
	"testing"

	"quant-trading-v2/internal/config"
)

// emotionCfgStub 构造测试用情绪阈值配置（各字段对应 rules.json 的 EmotionConfig）。
// English: emotionCfgStub builds an emotion-threshold config for tests (each field corresponds to EmotionConfig in rules.json).
func emotionCfgStub() *config.EmotionConfig {
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

// poolOf 构造 n 只涨停股，其中 index 0 连板为 maxBoard。
// English: poolOf builds n limit-up stocks, where index 0 has a consecutive-limit count of maxBoard.
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

// TestDetectEmotionPhaseBlastByHighGain §GAP2.1 回归：炸板判定必须用"盘中最高涨幅%"而非最高价（元）。
// 高价股（15 元）最高价恒 ≥9.5，旧实现会误判炸板；低价股触板回落（最高涨幅 10%）必须判出。
// English: regression for §GAP2.1 — blast detection must use intraday high GAIN %, never the day-high PRICE.
func TestDetectEmotionPhaseBlastByHighGain(t *testing.T) {
	quote := func(price, high, changePct float64) *MarketSnapshot {
		return &MarketSnapshot{Stocks: map[string]*StockInfo{
			"600001": {Code: "600001", Price: price, High: high, ChangePct: changePct},
		}}
	}

	t.Run("高价股不误判炸板", func(t *testing.T) {
		si := &StockInfo{Price: 15.0, High: 15.30, ChangePct: 2.0} // 最高涨幅≈4%，未触板
		if got := intradayHighGainPct(si); got >= 9.5 {
			t.Fatalf("hiGain=%.2f%% 应 <9.5", got)
		}
	})

	t.Run("低价股触板回落判炸板", func(t *testing.T) {
		si := &StockInfo{Price: 3.12, High: 3.30, ChangePct: 4.0} // 昨收3.0，最高涨幅=10%
		if got := intradayHighGainPct(si); got < 9.5 {
			t.Fatalf("hiGain=%.2f%% 应 >=9.5（触及涨停后回落）", got)
		}
	})

	t.Run("无效行情跳过炸板计数", func(t *testing.T) {
		if got := intradayHighGainPct(&StockInfo{Price: 0, High: 0, ChangePct: 0}); got != 0 {
			t.Fatalf("无效行情 hiGain 应为 0, got %.2f", got)
		}
	})

	t.Run("快照级判定", func(t *testing.T) {
		cfg := emotionCfgStub()
		// 桩配置未含炸板率阈值（零值会无条件命中冰点/退潮），补齐与 rules.json 同量级的阈值
		cfg.EmoClimaxBlastMax = 20
		cfg.EmoFermentBlastMax = 30
		cfg.EmoStartBlastMin = 0
		cfg.EmoStartBlastMax = 40
		cfg.EmoIceBlastMin = 40
		cfg.EmoRetreatBlastMin = 30
		cfg.EmoDivergeBlastRise = 60
		// 单只涨停封死（+10% 未回落）：blastRate=0 → 非冰点
		if got := DetectEmotionPhase(quote(3.30, 3.30, 10.0), cfg); got == "冰点" {
			t.Fatalf("封死涨停不应计入炸板")
		}
		// 单只炸板（最高 +10% 回落至 +4%）：blastRate=100% ≥ 冰点下限 → 冰点
		if got := DetectEmotionPhase(quote(3.12, 3.30, 4.0), cfg); got != "冰点" {
			t.Fatalf("炸板股应触发冰点, got %q", got)
		}
	})
}
