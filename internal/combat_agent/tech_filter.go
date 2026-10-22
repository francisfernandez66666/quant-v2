// tech_filter.go — §C4 买入技术过滤的输入计算（KDJ J 值 + 长均线）。
//
// 只在**已完成的日 K** 上计算：若最后一根 K 线就是今天（盘中未收盘），先剔除，避免未来函数。
// 数据不足时字段保持零值/缺失，signalctl 侧按“弃权”处理，绝不把“没算出来”当“低于均线”。
//
// English: §C4 computes the buy-filter inputs (KDJ J, long MAs) on completed daily bars only —
// today's unfinished bar is dropped to avoid look-ahead. Insufficient data stays zero/absent and
// the signal controller abstains.
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/indicator"
)

// applyTechInputs 把 KDJ J 与 MA20/60/120/250 写入信号的临时字段。
// English: fills the signal's transient KDJ/MA fields from completed daily bars.
func applyTechInputs(s *Signal, klines []data.KLine) {
	if s == nil || len(klines) == 0 {
		return
	}
	// 剔除当日未完成 K 线：只有“今天且尚未收盘（北京时 < 15:00）”才视为未完成；
	// 收盘后当天 K 已定型，必须参与 KDJ/均线。
	// English: drop today's bar only before the 15:00 close; after close the daily bar is final.
	now := cntime.In(time.Now())
	today := now.Format("2006-01-02")
	if n := len(klines); n > 0 && klines[n-1].Date.Format("2006-01-02") == today &&
		now.Hour()*60+now.Minute() < 15*60 {
		klines = klines[:n-1]
	}
	if len(klines) < 2 {
		return
	}
	closes := make([]float64, 0, len(klines))
	highs := make([]float64, 0, len(klines))
	lows := make([]float64, 0, len(klines))
	for _, k := range klines {
		closes = append(closes, k.Close)
		highs = append(highs, k.High)
		lows = append(lows, k.Low)
	}
	if pts := indicator.KDJDefault(closes, highs, lows); len(pts) > 0 {
		s.KDJ_J = pts[len(pts)-1].J
	}
	last := closes[len(closes)-1]
	if last <= 0 {
		return
	}
	s.MA60 = maLast(closes, 60)
	s.MA120 = maLast(closes, 120)
	s.MA250 = maLast(closes, 250)
	ratios := map[string]float64{}
	for key, n := range map[string]int{"ma20": 20, "ma60": 60, "ma120": 120, "ma250": 250} {
		if ma := maLast(closes, n); ma > 0 {
			ratios[key] = last/ma - 1
		}
	}
	s.CloseVsMA = ratios
}

// maLast 返回最后 n 根收盘价的简单均值；不足 n 根返回 0（调用方弃权）。
// English: simple MA of the last n closes; fewer than n bars returns 0 (caller abstains).
func maLast(vals []float64, n int) float64 {
	if n <= 0 || len(vals) < n {
		return 0
	}
	sum := 0.0
	for _, v := range vals[len(vals)-n:] {
		sum += v
	}
	return sum / float64(n)
}
