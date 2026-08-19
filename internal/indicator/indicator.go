// Package indicator 统一技术指标库（B1，先服务研究链路）。
//
// 设计：
//   - 输入一律为 []float64 序列（收盘/最高/最低/成交量），由调用方从行情源提取；
//     本包提供 store.Bar → 序列 的便捷提取函数（ClosesOf/HighsOf/LowsOf/VolumesOf）。
//   - 输出与输入等长的序列；预热期（数据不足）返回 NaN，调用方自行处理（不参与均值等）。
//   - 复权口径：研究链路使用后复权（hfq）价，由调用方在装载时决定，本包不感知复权。
//   - 约定：与现有盘中 MACD（internal/data/macd.go）口径一致——EMA 种子取前 n 根简单
//     平均、系数 k=2/(n+1)、MACD Bar=2×(DIF−DEA)；但盘中旧实现为逐点前缀 O(n²) 且预热期
//     补 0，本库为单遍 O(n) 且预热期 NaN，数值在数据充分后收敛一致（C7 迁移时注意差异）。
//   - 无第三方数值依赖，仅标准库；每个指标配套 golden-data 单测（testdata/golden.txt，
//     由 Python 按相同公式生成并冻结）。
//
// （English: Package indicator is the unified technical-indicator library (B1, serving the
// research chain first). Inputs are []float64 series; outputs are equal-length slices with NaN
// during warm-up. MACD follows the same convention as the existing realtime code.）
package indicator

import (
	"math"

	"quant-trading-v2/internal/store"
)

// ClosesOf 提取日线的收盘价序列。（ClosesOf extracts close prices.）
func ClosesOf(bars []store.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Close
	}
	return out
}

// HighsOf 提取日线的最高价序列。（HighsOf extracts high prices.）
func HighsOf(bars []store.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.High
	}
	return out
}

// LowsOf 提取日线的最低价序列。（LowsOf extracts low prices.）
func LowsOf(bars []store.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Low
	}
	return out
}

// VolumesOf 提取日线的成交量序列。（VolumesOf extracts volumes.）
func VolumesOf(bars []store.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Vol
	}
	return out
}

// nan 返回 NaN，用于预热期占位。
func nan() float64 { return math.NaN() }

// mean 计算切片的算术平均（空切片返回 NaN）。
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return nan()
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
