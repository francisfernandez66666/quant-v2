// golden-data 测试框架：读取 testdata/golden.txt（由 Python 按本包相同公式生成并冻结），
// 提供统一的序列断言工具。
package indicator

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// loadGolden 解析 golden.txt 为 名称→序列 的映射。
// 文件格式：# 名称 行 + 逗号分隔数值行（nan 表示 NaN）。
func loadGolden(t *testing.T) map[string][]float64 {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.txt")
	if err != nil {
		t.Fatalf("读取 golden.txt: %v", err)
	}
	m := make(map[string][]float64)
	name := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		if name == "" {
			continue
		}
		var vals []float64
		for _, tok := range strings.Split(line, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "nan" {
				vals = append(vals, math.NaN())
				continue
			}
			f, err := strconv.ParseFloat(tok, 64)
			if err != nil {
				t.Fatalf("golden 解析 %s 值 %q: %v", name, tok, err)
			}
			vals = append(vals, f)
		}
		m[name] = vals
	}
	return m
}

// assertSeries 对比指标输出与 golden 序列（NaN 位置与数值均需一致，容差 1e-9）。
func assertSeries(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("长度不一致: got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("idx %d: 期望 NaN，得 %v", i, got[i])
			}
			continue
		}
		if d := math.Abs(got[i] - want[i]); d > 1e-9 {
			t.Errorf("idx %d: 期望 %.10f，得 %.10f（差 %.2e）", i, want[i], got[i], d)
		}
	}
}