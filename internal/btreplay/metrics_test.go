// metrics_test.go — §GAP4.5 绩效指标回归：夏普/最大回撤/年化/卡玛 的已知序列精确断言。
package btreplay

import (
	"math"
	"testing"
	"time"
)

// TestPerfMetricsKnownSeries 验证已知序列的绩效指标计算。
func TestPerfMetricsKnownSeries(t *testing.T) {
	// 稳定 +1%/笔 ×100 笔、首末相隔 99 个自然日：均值 1%、std=0 → Sharpe=0；
	// 净值单调上升 → MDD≈0、Calmar≈0；年化按实际跨度复利折算。
	sharpe, mdd, annual, calmar := perfMetrics(
		repeat(1.0, 100), dateSpanN("20250101", 100))
	if sharpe != 0 {
		t.Fatalf("std=0 时 Sharpe 应为 0, got %.4f", sharpe)
	}
	if mdd > 1e-9 || calmar > 1e-9 {
		t.Fatalf("单边上涨无回撤: mdd=%.2e calmar=%.2e", mdd, calmar)
	}
	years := 99.0 / 365.25
	wantAnnual := (math.Pow(math.Pow(1.01, 100), 1/years) - 1) * 100
	if math.Abs(annual-wantAnnual) > 0.5 {
		t.Fatalf("annual=%.2f want=%.2f", annual, wantAnnual)
	}
}

// TestPerfMetricsDrawdownAndSharpe 验证回撤与夏普比率计算。
func TestPerfMetricsDrawdownAndSharpe(t *testing.T) {
	// 序列：+20% 后 -10%（净值 1.2→1.08，MDD=(1.2-1.08)/1.2=10%）
	pnls := []float64{20, -10}
	dates := []string{"20250101", "20250301"}
	sharpe, mdd, _, calmar := perfMetrics(pnls, dates)
	if math.Abs(mdd-10) > 1e-6 {
		t.Fatalf("MDD=%.4f want 10", mdd)
	}
	// 两笔不同收益 std>0 → Sharpe>0
	if sharpe <= 0 {
		t.Fatalf("波动序列 Sharpe 应 >0, got %.4f", sharpe)
	}
	// Calmar = |annual/MDD|：annual=(1.08)^(365/59)-1≈61.9%，calmar≈6.19
	if calmar < 5 || calmar > 8 {
		t.Fatalf("calmar=%.2f 超出合理区间", calmar)
	}
	// 全亏：净值归零 → annual=-100
	_, _, ann2, _ := perfMetrics([]float64{-100, -100}, dates)
	if ann2 != -100 {
		t.Fatalf("净值归零年化应 -100, got %.2f", ann2)
	}
}

// TestPerfMetricsEdge 验证绩效指标边界（空序列/单点等）。
func TestPerfMetricsEdge(t *testing.T) {
	if s, mdd, a, c := perfMetrics(nil, nil); s != 0 || mdd != 0 || a != 0 || c != 0 {
		t.Fatal("空输入应全零")
	}
	if s, _, _, _ := perfMetrics([]float64{5}, []string{"20250101"}); s != 0 {
		t.Fatal("单样本 Sharpe 应为 0")
	}
}

// repeat 生成长度 n 的重复值序列（测试数据辅助）。
func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// dateSpanN 自 first 起 n 个自然日的日期序列（YYYYMMDD）。
func dateSpanN(first string, n int) []string {
	t0, _ := time.Parse("20060102", first)
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = t0.AddDate(0, 0, i).Format("20060102")
	}
	return out
}
