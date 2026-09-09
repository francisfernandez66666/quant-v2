package btreplay

import (
	"math"
	"testing"
)

// TestBonferroniP Bonferroni 校正语义：p×n 封顶 1；n≤1 不校正。
func TestBonferroniP(t *testing.T) {
	cases := []struct {
		p    float64
		n    int
		want float64
	}{
		{0.01, 5, 0.05},
		{0.3, 5, 1},
		{0.05, 20, 1},
		{0.05, 1, 0.05},
		{0.05, 0, 0.05},
		{1, 10, 1},
	}
	for _, c := range cases {
		if got := bonferroniP(c.p, c.n); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("bonferroniP(%v,%d)=%v, want %v", c.p, c.n, got, c.want)
		}
	}
}

// TestOneSampleTP 已知分布数据集的 t 检验 p 值：
//   - 全正且显著 → p 极小；
//   - 均值≈0 → p 接近 1；
//   - 少于 2 笔 → p=1。
func TestOneSampleTP(t *testing.T) {
	strong := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if p := oneSampleTP(strong); p >= 0.05 {
		t.Errorf("显著正收益 p 应<0.05, got %.4f", p)
	}
	noise := []float64{-1, 0.5, 1.5, -2, 2.5, -1, 0.5, 0.2, -0.3, 0.1}
	if p := oneSampleTP(noise); p < 0.05 {
		t.Errorf("噪音数据 p 应≥0.05, got %.4f", p)
	}
	if p := oneSampleTP([]float64{1}); p != 1 {
		t.Errorf("单笔交易 p 应=1, got %v", p)
	}
	// 全同收益（零方差）→ p=1
	if p := oneSampleTP([]float64{2, 2, 2, 2}); p != 1 {
		t.Errorf("零方差 p 应=1, got %v", p)
	}
}

// TestBetaincAgainstKnownValues 不完全 Beta 的已知值对照：
// I_{0.5}(0.5,0.5)=1/π·arcsin(√0.5)*2 ≈ 0.5；I_x(a,1)=x^a。
func TestBetaincAgainstKnownValues(t *testing.T) {
	if got := betainc(0.5, 0.5, 0.5); math.Abs(got-0.5) > 1e-6 {
		t.Errorf("I_0.5(0.5,0.5) 应=0.5, got %.7f", got)
	}
	if got := betainc(0.25, 2, 1); math.Abs(got-0.0625) > 1e-6 {
		t.Errorf("I_0.25(2,1)=0.25²=0.0625, got %.7f", got)
	}
	if got := betainc(0.8, 1, 2); math.Abs(got-0.96) > 1e-6 {
		t.Errorf("I_0.8(1,2)=1-(0.2²)=0.96, got %.7f", got)
	}
}

// TestOneSampleTPKnownT 构造精确已知 t 的数据集校验 p 值：
// returns = μ + σ·z，取 z 使样本均值=μ、样本标准差=σ 精确成立。
// 用中心化再缩放：先取任意 10 个数，去均值除标准差得到 z（样本均值 0、样本方差 1），
// 再 y = μ + σ·z，则样本均值=μ、样本方差=σ² 精确成立，t = μ·√n/σ 精确已知。
func TestOneSampleTPKnownT(t *testing.T) {
	zs := []float64{-1.5, -1.0, -0.5, -0.2, -0.1, 0.1, 0.2, 0.5, 1.0, 1.5}
	// 中心化 → 均值 0
	mz := 0.0
	for _, v := range zs {
		mz += v
	}
	mz /= float64(len(zs))
	z0 := make([]float64, len(zs))
	for i, v := range zs {
		z0[i] = v - mz
	}
	// 缩放 → 样本方差 1
	s2 := 0.0
	for _, v := range z0 {
		s2 += v * v
	}
	s2 /= float64(len(z0))
	sd := math.Sqrt(s2)
	for i := range z0 {
		z0[i] /= sd
	}
	// 目标 t=2.262（df=9 双侧 0.05 临界值）：mean = t·σ/√n，σ=1 → mean=2.262/√10
	mu := 2.262 / math.Sqrt(10)
	returns := make([]float64, len(z0))
	for i, z := range z0 {
		returns[i] = mu + z
	}
	p := oneSampleTP(returns)
	if math.Abs(p-0.05) > 0.005 {
		t.Errorf("t=2.262 df=9 双侧 p 应≈0.05, got %.4f", p)
	}
}
