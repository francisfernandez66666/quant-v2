// Package strategy 提供多种选股策略的实现。
// Laodeng（"捞等"）策略是一个综合评分策略，基于市值、市盈率、换手率和行业板块计算得分。
package strategy

import (
	"math"
	"strings"

	"quant-trading-v2/internal/config"
)

// techSectors 科技行业关键词列表，命中该列表的个股会获得额外加权（TechPenalty）。
// 用于识别具有科技属性的行业板块。
var techSectors = []string{
	"半导体", "芯片", "集成电路", "软件", "计算机", "互联网",
	"人工智能", "AI", "云计算", "大数据", "通信", "5G",
	"消费电子", "电子", "信息技术", "机器人", "自动化",
	"新能源", "锂电池", "光伏", "风电", "储能", "数字经济",
}

// ScoreLaodeng 计算"捞等"策略的综合评分。
// cfg: Laodeng 策略配置（包含各指标的权重和阈值）。
// marketCap: 个股总市值（元）。
// pe: 个股市盈率（PE）。
// turnover: 个股换手率（百分比）。
// sector: 个股所属行业板块名称。
// 返回 0.0~1.0 范围的综合得分，再乘以 WeightScore 权重系数。
// 评分维度：市值(30%)、市盈率(30%)、换手率(20%)、科技板块加权(20% 乘数)。
func ScoreLaodeng(cfg *config.LaodengConfig, marketCap, pe, turnover float64, sector string) float64 {
	if cfg == nil || !cfg.Enabled {
		return 0
	}

	score := 0.0

	if marketCap >= cfg.MarketCapMin {
		score += 0.3
	} else {
		score += 0.3 * (marketCap / cfg.MarketCapMin)
	}

	if pe > 0 && pe <= cfg.PeMax {
		score += 0.3
	} else if pe > 0 {
		score += 0.3 * math.Max(0, 1-(pe-cfg.PeMax)/cfg.PeMax)
	} else {
		score += 0.1
	}

	if turnover >= cfg.TurnoverMin {
		score += 0.2
	} else {
		score += 0.2 * (turnover / cfg.TurnoverMin)
	}

	s := strings.ToLower(sector)
	for _, ts := range techSectors {
		if strings.Contains(s, strings.ToLower(ts)) {
			score *= (1 + cfg.TechPenalty)
			break
		}
	}

	return math.Max(0, math.Min(1, score)) * cfg.WeightScore
}

// IsActionWatchOrAbove 判断操作级别是否达到 watch 或以上。
// action: 操作指令（buy/sell/hold/watch 等）。
// 返回 true 表示该操作需要被重点关注（watch 及以上级别）。
func IsActionWatchOrAbove(action string) bool {
	switch action {
	case "buy", "sell", "hold", "watch":
		return true
	default:
		return false
	}
}
