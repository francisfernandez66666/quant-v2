// e2e 专项验证本次"N 形战法门槛放开"改动：
//
//	信号硬闸门只由「D1 有分 && 总分≥60」决定，D2/D3/D4 纯凑分（软门槛）。
//	直接构造 LeftSideScorer 输入验证三场景，确定性、离线可复现。
package e2e

import (
	"testing"

	"quant-trading-v2/internal/strategies/n_shape"
)

// gateInput 构造一组 D1~D4 可控的评分输入。
type gateInput struct {
	llmD1Score    float64 // LLM D1 评分（0.0~1.0，calcD1 ×MaxD1 映射到 0~40）
	stockPE       float64 // D3：PE<15 → 满 20 分
	auctionChgPct float64 // D2a：1.5~5 → 15 分
	volRatio      float64 // D2b/D4b 量比（相对 20 日均量×时间进度）
	macdDIF       float64 // D4a：MACD 水上
	macdDEA       float64
	excessChg     float64 // D2c 超额收益（个股涨幅-基准涨幅，每 3% 得 7 分）
}

// evaluate 运行 LeftSideScorer 评分，返回结果。
func evaluate(in gateInput) *n_shape.ScoreResult {
	// 时间固定 9:35，情绪"启动"（不触发硬闸/情绪降分），无板块冷清/预支否决。
	ctx := &n_shape.Ctx{
		LLMD1Score:         in.llmD1Score,
		EmotionPhase:       "启动",
		StockPE:            in.stockPE,
		AvgDailyVol:        100000,
		SectorTurnover:     1e11,
		SectorTurnoverMA20: 5e10,
	}

	wa := &n_shape.WaveA{
		AHigh:  100.0,
		ALow:   90.0,
		AClose: 100.0,
	}

	// D4b 量能放大需 当日量 > 20日均量×1.5（按 9:35 时间进度=0.1 折算）。
	const avgVol = 100000.0
	cumVol := avgVol * 1.0 * 0.1 * in.volRatio

	// D2c 超额：个股涨幅 = 基准涨幅 + excessChg
	bench := 0.0
	curPrice := 100.0 * (1 + bench + in.excessChg)

	ib := &n_shape.IntradayB{
		TTime:         935, // 9:35
		CurPrice:      curPrice,
		PrevClose:     100.0,
		CumVol:        cumVol,
		AvgDailyVol:   avgVol,
		AuctionChgPct: in.auctionChgPct,
		BenchCurChg:   bench,
		MinuteMACDDIF: in.macdDIF, MinuteMACDDEA: in.macdDEA,
	}

	return n_shape.NewLeftSideScorer(nil).Evaluate(wa, ib, ctx)
}

// TestNShapeGateD1AndTotal 验证 N 形信号门三个场景。
func TestNShapeGateD1AndTotal(t *testing.T) {
	t.Run("D1有分_总分>=60_出信号", func(t *testing.T) {
		res := evaluate(gateInput{
			llmD1Score:    0.5,               // d1 = 20
			stockPE:       10,                // d3 = 20
			auctionChgPct: 2.0,               // d2a = 15
			volRatio:      2.0,               // d2b = 8, d4b 放量
			macdDIF:       1.0, macdDEA: 0.5, // d4a = 5
			excessChg: 0.03, // d2c = 7 → d2=30, d4=10
		})
		if res.Total != 20+30+20+10 {
			t.Fatalf("总分=%.0f, want 80", res.Total)
		}
		if !res.Valid {
			t.Fatalf("D1>0 且总分>=60 应 Valid, total=%.0f reason=%q", res.Total, res.Reason)
		}
	})

	t.Run("D1有分_总分<60_不出信号", func(t *testing.T) {
		res := evaluate(gateInput{
			llmD1Score: 0.5, // d1 = 20
			// D2/D3/D4 全无效 → 总分 = 20 < 60
			stockPE: 100, auctionChgPct: 0, volRatio: 0, macdDIF: -1, macdDEA: 0,
		})
		if res.Total >= 60 {
			t.Fatalf("此场景总分应<60, got %.0f", res.Total)
		}
		if res.Valid {
			t.Fatalf("D1>0 但总分<60 不应 Valid, total=%.0f", res.Total)
		}
	})

	t.Run("无D1分_总分>=60_不出信号", func(t *testing.T) {
		res := evaluate(gateInput{
			llmD1Score:    0.0,               // d1 = 0，无 D1 分
			stockPE:       10,                // d3 = 20
			auctionChgPct: 2.0,               // d2a = 15
			volRatio:      2.0,               // d2b = 8
			macdDIF:       1.0, macdDEA: 0.5, // d4a = 5
			excessChg: 0.03, // d2c = 7 → d2=30, d3=20, d4=10 → 总分 60
		})
		if res.Total != 60 {
			t.Fatalf("此场景总分应为 60, got %.0f", res.Total)
		}
		if res.Valid {
			t.Fatalf("无 D1 分信号应不出（硬门槛 D1>0）, total=%.0f", res.Total)
		}
	})
}
