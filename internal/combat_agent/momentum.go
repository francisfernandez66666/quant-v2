// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// momentum.go 实现动量分（量价 + MACD + 走势），作为 8a/8b 打分量的一部分。
// 三个子指标均输出 0~1 的比率，按配置权重加权后映射到 0~100 分。
//
// 动量分计算逻辑：
//   - 量价比：量比 + 涨幅，量价齐升得分最高；放量下跌时量能分折半
//   - MACD比：金叉 + 水上 + 红柱，来自分钟级 MACD，三项各占 1/3
//   - 走势比：站上均线 + 多头排列 + 5日上行，来自日K，四项各占 1/4
//
// 数据来源：8a/8b 打分池行情（同花顺/新浪实时量价 + 日K + 分钟MACD）
// 数据缺失时对应子项按 0 降级，不影响整体出分
package combat_agent

import (
	"math"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy_engine"
)

// MomentumScore 计算动量分（0~100）：量价权重 + MACD权重 + 走势权重（默认 40/30/30，前端可配）。
// 数据来源为 8a/8b 打分池行情（同花顺/新浪实时量价 + 日K + 分钟MACD）。
// 数据缺失时对应子项按 0 降级，不影响整体出分；权重全 0 时回退默认 40/30/30。
//
// 参数：
//   - md: 行情数据快照
//   - w: 三子项权重配置
//
// 返回值：
//   - 四舍五入后的整数动量分（0~100）
func MomentumScore(md *strategy_engine.StockMarketData, w config.MomentumConfig) float64 {
	// 权重全 0（未配置）→ 回退默认 40/30/30
	// All-zero weights (unconfigured) -> fall back to the default 40/30/30.
	if w.VolumePriceWeight == 0 && w.MACDWeight == 0 && w.TrendWeight == 0 {
		w = config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60}
	}
	if md == nil {
		return 0
	}
	// 三子项比率加权求和（各子项已保证在 0~1 区间）
	// Weighted sum of the three sub-item ratios (each is already within [0,1]).
	s := volumePriceRatio(md)*w.VolumePriceWeight + macdRatio(md)*w.MACDWeight + trendRatio(md)*w.TrendWeight
	if s > 100 {
		s = 100
	}
	return math.Round(s)
}

// volumePriceRatio 量价比（0~1）：量比 + 涨幅，量价齐升得分最高；放量下跌时量能分折半。
// 量能维度：当日实时成交量 / 前20日均量（排除今日）分档打分；
// 价格维度：按涨跌幅分档打分。二者均值作为结果。
//
// 分档规则：
//   - 量比 >= 2: 1.0
//   - 量比 >= 1.5: 0.75
//   - 量比 >= 1.2: 0.5
//   - 量比 >= 1: 0.25
//   - 涨幅 >= 5%: 1.0
//   - 涨幅 >= 3%: 0.75
//   - 涨幅 >= 1%: 0.5
//   - 涨幅 >= 0%: 0.25
//
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - 量价比（0~1）
func volumePriceRatio(md *strategy_engine.StockMarketData) float64 {
	q := md.Quote
	// 无量价数据 → 0 降级
	// No volume-price data -> degrade to 0.
	if q == nil || q.Price <= 0 || q.Volume <= 0 {
		return 0
	}
	kl := md.KLines
	if len(kl) == 0 {
		return 0
	}
	// 前 20 日均量（去掉当日那根K，近似此前量能基准）
	// Prior 20-day average volume (excluding today's bar, approximating the volume baseline).
	avgV := avgVol(kl[:len(kl)-1], 20)

	// 量比分档：放量程度越高得分越高。
	// §修复 P2#26：今日实时累计量按已流逝交易分钟折算全天等值后与日均量比较——
	// 否则上午累计量天然偏小、量比被稀释（同一放量 09:40 只显示 1/4 强度），早盘动量
	// 信号常被"量能不足"误压；归一后任意时刻与收盘口径一致。
	// English: P2#26 — normalize today's cumulative volume to a full-day equivalent by elapsed trading
	// minutes before bucketing; otherwise a morning burst reads ~1/4 strength and momentum signals are
	// wrongly throttled early in the session.
	volScore := 0.0
	if avgV > 0 {
		ratio := intradayVolumeRatio(time.Now(), q.Volume, avgV)
		switch {
		case ratio >= 2:
			volScore = 1.0
		case ratio >= 1.5:
			volScore = 0.75
		case ratio >= 1.2:
			volScore = 0.5
		case ratio >= 1:
			volScore = 0.25
		}
	}

	// 涨幅分档：涨幅越大价格维度得分越高
	// Change-% buckets: the higher the gain, the higher the price-dimension score.
	chg := md.ChangePct
	priceScore := 0.0
	switch {
	case chg >= 5:
		priceScore = 1.0
	case chg >= 3:
		priceScore = 0.75
	case chg >= 1:
		priceScore = 0.5
	case chg >= 0:
		priceScore = 0.25
	}

	// 放量下跌：量能分折半（下跌时放量是风险信号，不奖励量能）
	// Falling on expanding volume: halve the volume score (expansion on a drop is a risk, not rewarded).
	if chg < 0 {
		volScore *= 0.5
	}
	return (volScore + priceScore) / 2
}

// macdRatio MACD 比（0~1）：金叉 + 水上 + 红柱，来自分钟级 MACD，三项各占 1/3。
// DIF/DEA 均为 0（无 MACD 数据）时返回 0 降级。
//
// 三项指标：
//   - 金叉：DIF > DEA（多头）
//   - 水上：DIF > 0（在零轴上方）
//   - 红柱：Bar > 0（多头动能）
//
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - MACD比（0~1）
func macdRatio(md *strategy_engine.StockMarketData) float64 {
	m := md.MinuteMACD
	if m.DIF == 0 && m.DEA == 0 {
		return 0
	}
	s := 0.0
	if m.DIF > m.DEA { // 金叉/多头
		s += 1
	}
	if m.DIF > 0 { // 水上（DIF 在零轴上方）
		s += 1
	}
	if m.Bar > 0 { // 红柱（柱体为正，多头动能）
		s += 1
	}
	return s / 3
}

// trendRatio 走势比（0~1）：站上均线 + 多头排列 + 5日上行，来自日K，四项各占 1/4。
// K线不足 5 根时返回 0 降级。
//
// 四项指标：
//   - 收盘站上 MA5
//   - 收盘站上 MA10
//   - 均线多头排列（MA5 > MA10）
//   - 5日上行（现价相对5日前收盘上涨）
//
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - 走势比（0~1）
func trendRatio(md *strategy_engine.StockMarketData) float64 {
	kl := md.KLines
	if len(kl) < 5 {
		return 0
	}
	last := kl[len(kl)-1]
	ma5 := ma(kl, 5)
	ma10 := ma(kl, 10)
	s := 0.0
	if last.Close > ma5 { // 收盘站上 MA5
		s += 1
	}
	if last.Close > ma10 { // 收盘站上 MA10
		s += 1
	}
	if ma5 > ma10 { // 均线多头排列
		s += 1
	}
	if len(kl) >= 6 {
		// 5 日前收盘价为正且现价相对其上涨 → 中期上行
		// The close 5 days ago is positive and the current price is above it -> medium-term uptrend.
		prev5 := kl[len(kl)-6].Close
		if prev5 > 0 && (last.Close-prev5)/prev5*100 > 0 {
			s += 1
		}
	}
	return s / 4
}
