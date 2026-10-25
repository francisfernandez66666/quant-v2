// main_test.go — dynsize_whatif 单测：档位边界、分位、样本解析、矩阵与实盘同函数。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
)

// TestBandOfBoundaries 档位边界：阈值含端点归严（与 signalctl.flowBandOf 同口径）。
func TestBandOfBoundaries(t *testing.T) {
	cfg := config.DefaultSizingConfig().WithDefaults()
	cases := []struct {
		ratio float64
		want  string
	}{
		{3.0, "strong_in"}, {3.1, "strong_in"}, {2.9, "mild_in"},
		{1.0, "mild_in"}, {0.9, "neutral"}, {0.0, "neutral"},
		{-0.9, "neutral"}, {-1.0, "mild_out"}, {-1.1, "mild_out"},
		{-2.9, "mild_out"}, {-3.0, "strong_out"}, {-3.1, "strong_out"},
	}
	for _, c := range cases {
		if got := bandOf(cfg, c.ratio); got != c.want {
			t.Fatalf("bandOf(%v) = %q, want %q", c.ratio, got, c.want)
		}
	}
}

// TestQuantile 线性插值分位。
func TestQuantile(t *testing.T) {
	xs := []float64{-5, -2, -1, 0, 1, 2, 4}
	if got := quantile(xs, 0); got != -5 {
		t.Fatalf("min = %v", got)
	}
	if got := quantile(xs, 1); got != 4 {
		t.Fatalf("max = %v", got)
	}
	if got := quantile(xs, 0.5); got != 0 {
		t.Fatalf("median = %v", got)
	}
	// p25：位置 0.25*6=1.5 → 插值 (-2 + -1)/2 = -1.5
	if got := quantile(xs, 0.25); got != -1.5 {
		t.Fatalf("p25 = %v, want -1.5", got)
	}
}

// TestLoadRatios 样本解析：逗号/空白/注释/空行。
func TestLoadRatios(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.txt")
	body := "# 注释\n-5, -2\n\n1.5\t2.5\n0\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadRatios(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{-5, -2, 1.5, 2.5, 0}
	if len(got) != len(want) {
		t.Fatalf("条数 = %d, want %d（%v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条 = %v, want %v", i, got[i], want[i])
		}
	}
	if _, err := loadRatios(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("缺文件必须报错")
	}
}

// TestMatrixMatchesComputeSizing 矩阵输出与实盘同一合成函数逐格一致（防"试算一套实盘一套"）。
func TestMatrixMatchesComputeSizing(t *testing.T) {
	base := config.DefaultSizingConfig().WithDefaults()
	reg := signalctl.Regime{SizeCfg: base, EmotionPhase: "发酵", MarketState: "bull", BreadthUpRatio: 0.6,
		Holiday: signalctl.HolidayWindow{CalendarLoaded: true}}
	sig := combat_agent.Signal{Code: "600519", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0}

	var out strings.Builder
	renderMatrix(&out, reg, sig, -3, 3, 3, 0, presets()[0])
	text := out.String()
	for _, r := range []float64{-3, 0, 3} {
		s := sig
		s.FlowRatio5D = r
		want := fmt.Sprintf("%.4f", signalctl.ComputeSizing(reg, s).Scale)
		if !strings.Contains(text, want) {
			t.Fatalf("矩阵缺 %+v%% 的 scale=%s\n%s", r, want, text)
		}
	}
}

// TestPresetsDiffer 三套预设在同一点必须给出不同 scale（否则对照无意义）。
func TestPresetsDiffer(t *testing.T) {
	base := config.DefaultSizingConfig().WithDefaults()
	reg := signalctl.Regime{SizeCfg: base, EmotionPhase: "发酵", MarketState: "bull", BreadthUpRatio: 0.6,
		Holiday: signalctl.HolidayWindow{CalendarLoaded: true}}
	sig := combat_agent.Signal{Code: "600519", Price: 10, ATR: 0.2, Confidence: 0.8, QualityWeight: 1.0, FlowRatio5D: 3}
	seen := map[float64]string{}
	for _, p := range presets() {
		cfg := config.DefaultSizingConfig().WithDefaults()
		p.Apply(&cfg)
		reg.SizeCfg = cfg
		sc := signalctl.ComputeSizing(reg, sig).Scale
		if prev, ok := seen[sc]; ok {
			t.Fatalf("预设 %s 与 %s 在 +3%% 流入下 scale 相同（%v），对照无意义", p.Name, prev, sc)
		}
		seen[sc] = p.Name
	}
}

// TestBandsDistributionDiffers 预设阈值不同 → 档位占比必须不同（R4 标定的意义所在）。
func TestBandsDistributionDiffers(t *testing.T) {
	ratios := []float64{-5, -4, -3, -2.5, -2, -1, -0.5, 0, 0.5, 1, 2, 2.5, 3, 4, 5}
	var out strings.Builder
	renderBands(&out, append([]float64(nil), ratios...))
	lines := strings.Split(out.String(), "\n")
	var rows []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "| default |") || strings.HasPrefix(ln, "| aggressive |") || strings.HasPrefix(ln, "| conservative |") {
			rows = append(rows, ln)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("应有三行预设占比，得 %d：%v", len(rows), rows)
	}
	if rows[0] == rows[1] && rows[1] == rows[2] {
		t.Fatalf("三套预设阈值不同，占比不应完全相同：%v", rows)
	}
}
