// Package data 提供行情数据获取、多数据源协调、情绪面分析、筹码分析、板块扫描等核心数据能力。
// macd.go 提供 MACD 指标计算，供 8a/8b 持续打分的动量分与 N 形战法使用。
package data

import (
	"math"

	"quant-trading-v2/internal/indicator"
)

// MACD MACD 指标最新值。
type MACD struct {
	DIF float64 // 快线 DIF = EMA12 - EMA26
	DEA float64 // 慢线 DEA = EMA9(DIF)
	Bar float64 // 柱状图 Bar = 2 * (DIF - DEA)
}

// §修复 D7（2026-08-29）：删除 data 包内与 indicator 重复的 O(n²) MACD/EMA 实现，
// 统一委托 indicator.MACD（单遍 O(n)，EMA 种子口径与旧 data.ema 完全一致）→ 全系统唯一 MACD 口径。
// 预热期 indicator 返回 NaN，此处归一为 0，保持 data.MACD 无 NaN 合同（最新根恒有值）。

// CalcMACDSeries 从 K 线收盘价计算整条 MACD 序列（每根一根）。委托 indicator.MACD，NaN 预热期归 0。
// 与 CalcMACD 同口径（EMA12/26 → DIF，DEA=EMA9(DIF)，BAR=2×(DIF−DEA)）。
func CalcMACDSeries(klines []KLine) []MACD {
	closes := make([]float64, len(klines))
	for i, k := range klines {
		closes[i] = k.Close
	}
	pts := indicator.MACD(closes, 12, 26, 9)
	out := make([]MACD, len(pts))
	for i, p := range pts {
		out[i] = MACD{DIF: orZero(p.DIF), DEA: orZero(p.DEA), Bar: orZero(p.Bar)}
	}
	return out
}

// CalcMACD 返回最新一根的 MACD（委托 indicator.MACD 取末根）。K 线不足预热期返回零值。
func CalcMACD(klines []KLine) MACD {
	series := CalcMACDSeries(klines)
	if len(series) == 0 {
		return MACD{}
	}
	return series[len(series)-1]
}

// orZero NaN 预热期归一为 0，保持 data.MACD 无 NaN 合同。
func orZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}
