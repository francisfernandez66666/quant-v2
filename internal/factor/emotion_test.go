// 情绪因子单元测试（§Phase2 情绪/盘口因子入池）。
// 验证市场情绪列直传与 emo_chg5（5 日变化）计算、NaN 传播、类别归属。
// English: unit tests for the market-sentiment factors (Phase 2) — passthrough of the per-day
// sentiment columns, the 5-day change computation, NaN propagation and category assignment.
package factor

import (
	"math"
	"testing"
)

// TestEmotionFactors 情绪因子直传与 5 日变化。
// English: TestEmotionFactors sentiment-factor passthrough and 5-day change.
func TestEmotionFactors(t *testing.T) {
	s := &StockSeries{
		Dates:        []string{"d0", "d1", "d2", "d3", "d4", "d5", "d6"},
		EmoLimitUp:   []float64{100, 120, 90, 110, 130, 80, 95},
		EmoBreakCnt:  []float64{10, 12, 9, 11, 13, 8, 9},
		EmoBlastRate: []float64{9.1, 9.1, 9.1, 9.1, 9.1, 9.1, 9.1},
		EmoMaxBoard:  []float64{5, 6, 5, 6, 7, 6, 6},
	}
	approx(t, mustGet(t, "emo_limit_up").Compute(s), s.EmoLimitUp)
	approx(t, mustGet(t, "emo_blast_cnt").Compute(s), s.EmoBreakCnt)
	approx(t, mustGet(t, "emo_blast_rate").Compute(s), s.EmoBlastRate)
	approx(t, mustGet(t, "emo_max_board").Compute(s), s.EmoMaxBoard)
	// emo_chg5：i<5 时无前值（NaN）；got[5]=v5-v0=80-100=-20；got[6]=v6-v1=95-120=-25。
	// English: emo_chg5 — indices <5 have no 5-day lag (NaN); got[5]=v5-v0=-20; got[6]=v6-v1=-25.
	got := mustGet(t, "emo_chg5").Compute(s)
	approx(t, []float64{got[5], got[6]}, []float64{-20, -25})
}

// TestEmotionFactorsNaN 含 NaN 输入时 emo_chg5 传播 NaN。
// English: TestEmotionFactorsNaN NaN propagation in emo_chg5.
func TestEmotionFactorsNaN(t *testing.T) {
	// v0 为 NaN → got[5]=v5-v0 应 NaN；前 5 位（无 5 日参考）恒 NaN。
	// English: v0 is NaN so got[5]=v5-v0 is NaN; the first 5 entries (no 5-day lag) are always NaN.
	s := &StockSeries{
		Dates:      []string{"d0", "d1", "d2", "d3", "d4", "d5"},
		EmoLimitUp: []float64{math.NaN(), 120, 90, 110, 130, 80},
	}
	got := mustGet(t, "emo_chg5").Compute(s)
	if !math.IsNaN(got[0]) || !math.IsNaN(got[4]) {
		t.Fatalf("前 5 位应为 NaN，实际 got[0]=%v got[4]=%v", got[0], got[4])
	}
	if !math.IsNaN(got[5]) {
		t.Fatalf("参考位为 NaN 时 emo_chg5[5] 应 NaN，实际 %v", got[5])
	}
	// 空系列返回 nil/空，不 panic
	// English: empty series returns nil without panicking.
	empty := &StockSeries{Dates: []string{"d0"}}
	if got := mustGet(t, "emo_limit_up").Compute(empty); len(got) != 0 {
		t.Fatalf("空情绪系列应返回空，实际 %v", got)
	}
}

// TestEmotionCategory 情绪因子类别归属与中文名。
// English: TestEmotionCategory emotion-factor category and Chinese name.
func TestEmotionCategory(t *testing.T) {
	for _, id := range []string{"emo_limit_up", "emo_blast_rate", "emo_max_board", "emo_blast_cnt", "emo_chg5"} {
		d, ok := Get(id)
		if !ok {
			t.Fatalf("因子 %s 未注册", id)
		}
		if d.Cat != CatSentiment {
			t.Fatalf("因子 %s 类别应为情绪，实际 %d", id, d.Cat)
		}
		if d.Cat.CategoryName() == "未知" {
			t.Fatalf("情绪类别缺中文名")
		}
	}
}
