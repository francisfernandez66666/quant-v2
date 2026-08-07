package double_bump

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// newTest 构造带默认配置的双凸战法实例。
func newTest() *DoubleBumpStrategy {
	return New(config.NewManager(""))
}

// kbump 构造测试用日K线：closes 为收盘序列，量能按需放大。
// 高开≥收盘、低≤收盘，保证 K 线合法。
func kbump(closes []float64, vol []float64) []data.KLine {
	base := time.Now()
	out := make([]data.KLine, len(closes))
	for i, c := range closes {
		v := 100000.0
		if i < len(vol) {
			v = vol[i]
		}
		out[i] = data.KLine{
			Date: base.AddDate(0, 0, -(len(closes) - 1 - i)),
			Open: c + 0.2, High: c + 0.5, Low: c - 0.5, Close: c, Volume: v,
		}
	}
	return out
}

// TestRedDayNotFullChain 验证"全天水下"不会评双凸买入：
// 结构：近 28 日横盘(均价≈10) → 前2日放量突破(收盘11.6/量300k) → 昨日缩量回调(11.3/60k)
// → 今日水下窄幅(涨跌幅-1.3%，>-1.5 不被深跌闸拦)但放量 250k。
// 修复前：量能+调整≈70 会误报；修复后：水下当日量能/调整分归零，仅剩均线分 → 不进 full_chain。
func TestRedDayNotFullChain(t *testing.T) {
	closes := make([]float64, 30)
	vol := make([]float64, 30)
	for i := 0; i < 30; i++ {
		closes[i] = 10.0
		vol[i] = 100000
	}
	// 近5日内：放量突破(第三根) → 缩量回调(第二根)
	closes[27] = 11.6
	vol[27] = 300000
	closes[28] = 11.3
	vol[28] = 60000
	// 今日：水下 -1.3%，放量 250k
	closes[29] = 11.15
	vol[29] = 250000

	si := &data.StockInfo{Code: "600001", Name: "测试", Price: closes[29], ChangePct: -1.3}
	ev := newTest().EvaluateReal("600001", si, kbump(closes, vol))
	if ev == nil {
		t.Fatalf("不应返回 nil")
	}
	// 水下当日：量能分+调整分应为0，且不进 full_chain/不 Pass
	if ev.Level == "full_chain" || ev.Pass {
		t.Fatalf("水下当日不应评 full_chain/买入, got level=%s pass=%v total=%.0f", ev.Level, ev.Pass, ev.TotalScore)
	}
	if ev.Details["vol_score"] != 0 || ev.Details["adjust_score"] != 0 {
		t.Fatalf("水下当日量能/调整分应为0, got vol=%.0f adj=%.0f", ev.Details["vol_score"], ev.Details["adjust_score"])
	}
}

// TestGreenDayFullChain 回归：相似形态但今日上行（ChangePct=+1.2）应正常评双凸买入(≥70)。
func TestGreenDayFullChain(t *testing.T) {
	closes := make([]float64, 30)
	vol := make([]float64, 30)
	for i := 0; i < 30; i++ {
		closes[i] = 10.0
		vol[i] = 100000
	}
	closes[27] = 11.6
	vol[27] = 300000
	closes[28] = 11.3
	vol[28] = 60000
	// 今日：放量上攻 +1.2%，量 250k
	closes[29] = 11.7
	vol[29] = 250000

	ev := newTest().EvaluateReal("600001",
		&data.StockInfo{Price: closes[29], Code: "600001", ChangePct: 1.2}, kbump(closes, vol))
	if ev == nil {
		t.Fatal("不应返回 nil")
	}
	if ev.Level != "full_chain" || !ev.Pass {
		t.Fatalf("上行放量日应评 full_chain, got level=%s pass=%v total=%.0f", ev.Level, ev.Pass, ev.TotalScore)
	}
	if math.Round(ev.TotalScore) < 70 {
		t.Fatalf("绿日放量+多头应≥70, got %.0f", ev.TotalScore)
	}
}