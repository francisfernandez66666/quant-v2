// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// momentum.go 实现动量分（量价 + MACD + 走势），作为 8a/8b 打分量的一部分。
// 三个子指标均输出 0~1 的比率，按配置权重加权后映射到 0~100 分。
package combat_agent

import (
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy_engine"
)

// MomentumScore 计算动量分（0~100）：量价权重 + MACD权重 + 走势权重（默认 40/30/30，前端可配）。
// 数据来源为 8a/8b 打分池行情（同花顺/新浪实时量价 + 日K + 分钟MACD）。
// 数据缺失时对应子项按 0 降级，不影响整体出分；权重全 0 时回退默认 40/30/30。
// 入参 md 为行情快照，w 为三子项权重；返回四舍五入后的整数动量分。
func MomentumScore(md *strategy_engine.StockMarketData, w config.MomentumConfig) float64 {
	// 权重全 0（未配置）→ 回退默认 40/30/30
	if w.VolumePriceWeight == 0 && w.MACDWeight == 0 && w.TrendWeight == 0 {
		w = config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30}
	}
	if md == nil {
		return 0
	}
	// 三子项比率加权求和（各子项已保证在 0~1 区间）
	s := volumePriceRatio(md)*w.VolumePriceWeight + macdRatio(md)*w.MACDWeight + trendRatio(md)*w.TrendWeight
	if s > 100 {
		s = 100
	}
	return math.Round(s)
}

// volumePriceRatio 量价比（0~1）：量比 + 涨幅，量价齐升得分最高；放量下跌时量能分折半。
// 量能维度：当日实时成交量 / 前20日均量（排除今日）分档打分；
// 价格维度：按涨跌幅分档打分。二者均值作为结果。
func volumePriceRatio(md *strategy_engine.StockMarketData) float64 {
	q := md.Quote
	// 无量价数据 → 0 降级
	if q == nil || q.Price <= 0 || q.Volume <= 0 {
		return 0
	}
	kl := md.KLines
	if len(kl) == 0 {
		return 0
	}
	// 前 20 日均量（去掉当日那根K，近似此前量能基准）
	avgV := avgVol(kl[:len(kl)-1], 20)

	// 量比分档：放量程度越高得分越高
	volScore := 0.0
	if avgV > 0 {
		ratio := q.Volume / avgV
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
	if chg < 0 {
		volScore *= 0.5
	}
	return (volScore + priceScore) / 2
}

// macdRatio MACD 比（0~1）：金叉 + 水上 + 红柱，来自分钟级 MACD，三项各占 1/3。
// DIF/DEA 均为 0（无 MACD 数据）时返回 0 降级。
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
		prev5 := kl[len(kl)-6].Close
		if prev5 > 0 && (last.Close-prev5)/prev5*100 > 0 {
			s += 1
		}
	}
	return s / 4
}
