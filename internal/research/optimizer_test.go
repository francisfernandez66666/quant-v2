// B5 优化器测试：权重坐标上升、CompositeIC 复合、护栏判定。
package research

import (
	"fmt"
	"math"
	"testing"

	"quant-trading-v2/internal/factor"
)

// mkStockPanel 构造单只股票面板：因子值按 k 递增（与未来收益正相关）。
// 收盘序列由逐日前瞻收益累乘得到：ret(k,j) = 0.005*(j+1)*k + 0.012*((k+j)%3)，
// 前半项保证 k 越大收益越高（因子有效），后半项是跨截面抖动使逐日 IC 非恒定（std>0 → IR 有限）。
// f1=f2 为预测信号；f3=[4,0,0,0,-4] 为强噪声（等权复合会打乱次序，去掉它 IC 显著上升）。
func mkStockPanel(dates []string, k int) *Panel {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	closes := make([]float64, len(dates))
	closes[0] = 100.0
	for j := 1; j < len(dates); j++ {
		ret := 0.005*float64(j+1)*float64(k) + 0.012*float64((k+j)%3)
		closes[j] = closes[j-1] * (1 + ret)
	}
	noise := []float64{4, 0, 0, 0, -4} // 强噪声：等权复合被打乱，去噪声可显著提升 IC
	return &Panel{
		Code:    fmt.Sprintf("%d", k),
		Series:  &factor.StockSeries{Dates: dates, CloseHfq: closes},
		DateIdx: idx,
		Factors: map[string][]float64{
			"f1": rep(float64(k), len(dates)),
			"f2": rep(float64(k)*0.5, len(dates)),
			"f3": rep(noise[k], len(dates)),
		},
	}
}

func rep(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestCompositeIC(t *testing.T) {
	dates := []string{"20230101", "20230102", "20230103", "20230104", "20230105", "20230106"}
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	// f1+f2 为强信号 → 截面 IC 应明显为正
	rows := CompositeIC(panels, []string{"f1", "f2"}, map[string]float64{"f1": 0.5, "f2": 0.5}, 1, 3)
	if len(rows) == 0 {
		t.Fatal("CompositeIC 无有效行")
	}
	ic := meanIC(rows)
	if ic < 0.5 {
		t.Fatalf("f1+f2 截面平均 IC=%.4f 期望 >0.5", ic)
	}
}

func TestOptimizeWeights(t *testing.T) {
	dates := []string{"20230101", "20230102", "20230103", "20230104", "20230105", "20230106"}
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	// 等权复合（含噪声 f3）作为基线
	base := CompositeIC(panels, []string{"f1", "f2", "f3"},
		map[string]float64{"f1": 1.0 / 3, "f2": 1.0 / 3, "f3": 1.0 / 3}, 1, 3)
	baseIR := IR(base)

	res := OptimizeWeights(panels, OptimizeOpts{
		Factors: []string{"f1", "f2", "f3"}, Horizon: 1, MinStocks: 3,
		Metric: "ir", MaxIter: 10, Step: 0.2, GuardMinIR: 0.3, GuardMinDays: 3,
	})
	// 权重 L1 归一化 = 1
	var sum float64
	for _, v := range res.Weights {
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("权重和应=1，得 %v（%v）", sum, res.Weights)
	}
	// 有效日足够 → 通过护栏
	if res.NDays < 3 {
		t.Fatalf("有效日=%d 期望>=3", res.NDays)
	}
	if isNaN(res.IR) {
		t.Fatalf("IR 不应为 NaN: %v", res.Weights)
	}
	if !res.PassGuard {
		t.Fatalf("应通过护栏，得 %s", res.Reason)
	}
	// 坐标上升不应比等权基线差
	if math.Abs(res.IR) < math.Abs(baseIR) {
		t.Fatalf("优化 IR=%.3f 应不低于等权基线 %.3f", res.IR, baseIR)
	}
	// 噪声因子 f3 权重应被压低：低于信号因子 f1，且不高于等权初始值
	w3 := res.Weights["f3"]
	w1 := res.Weights["f1"]
	if w3 > w1 || w3 > 0.3 {
		t.Fatalf("噪声因子 f3 权重 %.3f 应被压低，权重=%v", w3, res.Weights)
	}
}