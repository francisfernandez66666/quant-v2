// emotion_test.go — 情绪阶段检测单元测试：验证六阶段情绪判定阈值，以及炸板率改用盘中最高涨幅口径的回归。
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

// TestDetectEmotionPhaseV2 验证按涨停家数与连板高度阈值判定六阶段情绪（高潮/发酵/启动/冰点）。
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

// TestEmotionBreadthCorrection §MARKET_RISK_GATE P1：真实涨跌家数对涨停池口径的单向纠偏。
// 覆盖：阈值未配→不纠偏；家数缺失→弃权；极端普跌→高潮强制冰点；中度普跌→启动降退潮；只降不升（冰点不被修暖）。
func TestEmotionBreadthCorrection(t *testing.T) {
	base := func(ice, retreat float64) *config.EmotionConfig {
		c := emotionCfgStub()
		c.EmoBreadthIceDownRatio, c.EmoBreadthRetreatDownRatio = ice, retreat
		return c
	}
	cases := []struct {
		name     string
		pool     []LimitUpStock
		up, down int
		ice      float64
		retreat  float64
		want     string
	}{
		{"阈值未配_高潮不受影响", poolOf(90, 8), 100, 4000, 0, 0, "高潮"},
		{"家数缺失_弃权", poolOf(90, 8), 0, 0, 0.85, 0.75, "高潮"},
		{"极端普跌_高潮强制冰点", poolOf(90, 8), 200, 4800, 0.85, 0.75, "冰点"},
		{"中度普跌_启动降退潮", poolOf(20, 2), 900, 3600, 0.85, 0.75, "退潮"}, // downRatio=0.80 ≥0.75 且 <0.85
		{"已更冷_不被修暖", poolOf(10, 1), 200, 4800, 0.70, 0.60, "冰点"},   // 池判冰点，广度也只应维持冰点侧
		{"广度温和_不改启动", poolOf(20, 2), 3000, 1000, 0.85, 0.75, "启动"},
	}
	for _, c := range cases {
		got := DetectEmotionPhaseV2(c.pool, c.up, c.down, base(c.ice, c.retreat))
		if got != c.want {
			t.Errorf("%s: got %q want %q (up=%d down=%d ice=%.2f ret=%.2f)", c.name, got, c.want, c.up, c.down, c.ice, c.retreat)
		}
	}
}

// TestEmotionBreadthReversedConfig 阈值配反（ice<retreat）时取较大值兜底，避免弱广度误判冰点。
func TestEmotionBreadthReversedConfig(t *testing.T) {
	c := emotionCfgStub()
	c.EmoBreadthIceDownRatio, c.EmoBreadthRetreatDownRatio = 0.5, 0.8 // 反配
	// 下跌占比 0.6（介于二者间）：兜底后 ice=0.8 retreat=0.5 → 0.6≥0.5 降退潮，不到 0.8 不冰点
	got := DetectEmotionPhaseV2(poolOf(20, 2), 2000, 3000, c)
	if got != "退潮" {
		t.Fatalf("反配应兜底为退潮, got %q", got)
	}
}
