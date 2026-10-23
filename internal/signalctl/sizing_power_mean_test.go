// sizing_power_mean_test.go — v3 分组幂平均合成锁（§6 / §9.1）。
//
// 覆盖：powerMean 的 p=0/0.5/1、空集、单值；分组 floor；全弃权中性；
// 理由串分组格式；product 旧路径逐字不变。
//
// English: locks for the v3 grouped power-mean synthesis — exponent variants, empty/single sets,
// group floors, all-abstain neutrality, grouped reason format, and the untouched product path.
package signalctl

import (
	"math"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
)

func TestPowerMeanVariants(t *testing.T) {
	cases := []struct {
		name string
		vals []float64
		p    float64
		want float64
	}{
		{"p=0 几何平均", []float64{0.8, 0.9}, 0, math.Sqrt(0.8 * 0.9)},
		{"p=0.5", []float64{0.8, 0.9}, 0.5, math.Pow((math.Sqrt(0.8)+math.Sqrt(0.9))/2, 2)},
		{"p=1 算术平均", []float64{0.8, 0.9}, 1, 0.85},
		{"空集中性", nil, 0.5, 1},
		{"单值原样", []float64{0.73}, 0.5, 0.73},
		{"三值 p=0", []float64{0.5, 0.8, 1.2}, 0, math.Cbrt(0.5 * 0.8 * 1.2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := powerMean(c.vals, c.p); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("powerMean(%v,%v)=%.9f want %.9f", c.vals, c.p, got, c.want)
			}
		})
	}
}

func TestSizingGroupFloorAndReasons(t *testing.T) {
	// 0.5/0.4 的 p=0.5 幂平均 ≈ 0.4486 < 0.45 → 抬到 floor。
	g, reason := sizingGroup("市场组", 0.45, []sizingFactor{
		{"情绪相位", 0.5}, {"市场状态", 0.4},
	}, 0.5)
	if math.Abs(g-0.45) > 1e-9 {
		t.Fatalf("组 floor 应抬到 0.45，got %v", g)
	}
	if !strings.Contains(reason, "(触地板)") || !strings.Contains(reason, "情绪相位×0.50|市场状态×0.40") {
		t.Fatalf("理由串格式错误: %q", reason)
	}

	// 全弃权 → 中性 1.0，理由明确写“无”。
	if g, reason := sizingGroup("个股组", 0.35, []sizingFactor{{"信号质量", 0}, {"LLM利空", 0}}, 0.5); g != 1.0 || reason != "个股组:无→1.00" {
		t.Fatalf("全弃权应为中性，got %v %q", g, reason)
	}

	// 只列 v != 1.0 的因子。
	_, reason = sizingGroup("市场组", 0.45, []sizingFactor{{"风险档", 1.0}, {"情绪相位", 0.8}}, 0.5)
	if strings.Contains(reason, "风险档") || !strings.Contains(reason, "情绪相位×0.80") {
		t.Fatalf("理由串应只列非中性因子: %q", reason)
	}
}

// TestComputeSizingPowerMeanSingleFactor 单因子进入组内、再与另一组的中性值做组间幂平均。
func TestComputeSizingPowerMeanSingleFactor(t *testing.T) {
	r := baseRegime()
	r.SizeCfg.Mode = "power_mean"
	r.EmotionPhase = "高潮" // 0.80，高于市场组 floor 0.45

	res := ComputeSizing(r, baseSig())
	// 市场组除情绪 0.80 外，风险档/大盘跌幅各贡献一个“已知中性 1.00”，一并参与幂平均。
	mg := powerMean([]float64{0.8, 1, 1}, 0.5)
	want := powerMean([]float64{mg, 1}, 0.5)
	if math.Abs(res.Scale-want) > 1e-9 {
		t.Fatalf("Scale=%.9f want %.9f (reasons=%v)", res.Scale, want, res.Reasons)
	}
	joined := strings.Join(res.Reasons, "|")
	if !strings.Contains(joined, "市场组:情绪相位×0.80") || !strings.Contains(joined, "个股组:无→1.00") || !strings.Contains(joined, "组间→") {
		t.Fatalf("分组理由串缺失: %v", res.Reasons)
	}
}

// TestComputeSizingPowerMeanPZero 显式 p=0 必须走几何平均（config 修复后的回归锁）。
func TestComputeSizingPowerMeanPZero(t *testing.T) {
	p0 := 0.0
	r := baseRegime()
	r.SizeCfg.Mode = "power_mean"
	r.SizeCfg.PowerP = &p0
	r.EmotionPhase = "高潮" // 0.80

	res := ComputeSizing(r, baseSig())
	mg := powerMean([]float64{0.8, 1, 1}, 0) // 组内几何平均
	want := powerMean([]float64{mg, 1}, 0)   // 组间几何平均
	if math.Abs(res.Scale-want) > 1e-9 {
		t.Fatalf("p=0 Scale=%.9f want %.9f", res.Scale, want)
	}
}

// TestComputeSizingPowerMeanCapMin 上限类仍取 min，且只写 <1 的上限理由。
func TestComputeSizingPowerMeanCapMin(t *testing.T) {
	r := baseRegime()
	r.SizeCfg.Mode = "power_mean"
	r.Account.TotalPosPct = 0.9 // limit 0.8 → cap 0.8/0.9 = 0.8889

	res := ComputeSizing(r, baseSig())
	want := 0.8 / 0.9
	if math.Abs(res.Scale-want) > 1e-9 {
		t.Fatalf("上限应取 min=%.6f，got %.6f", want, res.Scale)
	}
	if !strings.Contains(strings.Join(res.Reasons, "|"), "总仓位上限×0.89") {
		t.Fatalf("上限理由缺失: %v", res.Reasons)
	}
}

// TestComputeSizingProductPathUnchanged §6.3：旧 product 路径逐字保留，不被 power_mean 污染。
func TestComputeSizingProductPathUnchanged(t *testing.T) {
	r := baseRegime()
	r.EmotionPhase = "退潮" // 0.50
	res := ComputeSizing(r, baseSig())
	if math.Abs(res.Scale-0.50) > 1e-12 {
		t.Fatalf("product 旧路径应恒为 0.50，got %.6f", res.Scale)
	}
	joined := strings.Join(res.Reasons, "|")
	if !strings.Contains(joined, "情绪相位×0.50") || strings.Contains(joined, "市场组:") {
		t.Fatalf("product 理由串应保持旧格式: %v", res.Reasons)
	}
}

// TestComputeSizingPowerMeanFloorCapClamp 最终仍 clamp 到 [Floor, Cap]。
func TestComputeSizingPowerMeanFloorCapClamp(t *testing.T) {
	r := baseRegime()
	r.SizeCfg.Mode = "power_mean"
	r.SizeCfg.Floor = 0.5
	r.SizeCfg.Cap = 0.9
	r.EmotionPhase = "高潮" // 0.80 → 组间约 0.897，低于 Cap
	res := ComputeSizing(r, baseSig())
	if res.Scale > 0.9+1e-12 {
		t.Fatalf("不得越过 Cap，got %v", res.Scale)
	}
	r2 := baseRegime()
	r2.SizeCfg = config.DefaultSizingConfig()
	r2.SizeCfg.Mode = "power_mean"
	r2.EmotionPhase = "退潮" // 0.50 → 组间约 0.724
	r2.SizeCfg.Cap = 0.6
	if got := ComputeSizing(r2, baseSig()).Scale; math.Abs(got-0.6) > 1e-12 {
		t.Fatalf("应触上限 0.6，got %v", got)
	}
}
