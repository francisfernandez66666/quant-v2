// Package strategy 提供多种选股策略的实现。（Package strategy provides stock-selection strategy implementations.）
// Laodeng（"捞等"）策略是一个综合评分策略，基于市值、市盈率、换手率和行业板块计算得分。
// （The Laodeng ("捞等") strategy scores stocks by market cap, PE, turnover and industry sector.）
package strategy

import (
	"math"
	"strings"

	"quant-trading-v2/internal/config"
)

// techSectors 科技行业关键词列表，命中该列表的个股会获得额外加权（TechPenalty）。
// 用于识别具有科技属性的行业板块。（Tech-sector keywords; matches add a TechPenalty multiplier to the score.）
var techSectors = []string{
	"半导体", "芯片", "集成电路", "软件", "计算机", "互联网",
	"人工智能", "AI", "云计算", "大数据", "通信", "5G",
	"消费电子", "电子", "信息技术", "机器人", "自动化",
	"新能源", "锂电池", "光伏", "风电", "储能", "数字经济",
}

// ScoreLaodeng 计算"捞等"策略的综合评分。（ScoreLaodeng computes the Laodeng composite score.）
// cfg: Laodeng 策略配置（包含各指标的权重和阈值）。
// marketCap: 个股总市值（元）。
// pe: 个股市盈率（PE）。
// turnover: 个股换手率（百分比）。
// sector: 个股所属行业板块名称。
// 返回 0.0~1.0 范围的综合得分，再乘以 WeightScore 权重系数。
// 评分维度：市值(30%)、市盈率(30%)、换手率(20%)、科技板块加权(20% 乘数)。
// （cfg: strategy config with weights/thresholds; marketCap in yuan; pe; turnover in %; sector name. Returns 0.0~1.0
// multiplied by WeightScore. Dimensions: market cap 30%, PE 30%, turnover 20%, tech sector 20% multiplier.）
func ScoreLaodeng(cfg *config.LaodengConfig, marketCap, pe, turnover float64, sector string) float64 {
	// 配置未启用或为空时直接返回 0（不参与评分）（Disabled or nil config → return 0, not scored）
	if cfg == nil || !cfg.Enabled {
		return 0
	}

	score := 0.0

	// 市值维度（满分 0.3）：达标给满，未达标按比例线性折算（Market cap (max 0.3): full when ≥ min, else linear scaling）
	if marketCap >= cfg.MarketCapMin {
		score += 0.3
	} else {
		score += 0.3 * (marketCap / cfg.MarketCapMin)
	}

	// 市盈率维度（满分 0.3）：PE≤上限给满，超出后线性衰减；无 PE 数据给保底 0.1（PE (max 0.3): full up to the cap, linear decay above; missing PE gets a floor of 0.1）
	if pe > 0 && pe <= cfg.PeMax {
		score += 0.3
	} else if pe > 0 {
		score += 0.3 * math.Max(0, 1-(pe-cfg.PeMax)/cfg.PeMax)
	} else {
		score += 0.1
	}

	// 换手率维度（满分 0.2）：达标给满，未达标按比例线性折算（Turnover (max 0.2): full when ≥ min, else linear scaling）
	if turnover >= cfg.TurnoverMin {
		score += 0.2
	} else {
		score += 0.2 * (turnover / cfg.TurnoverMin)
	}

	// 科技板块加权（乘数）：命中任一科技关键词则整体分数乘 (1+TechPenalty)
	// 行业名做小写归一后子串匹配，命中即跳出循环（Tech-sector multiplier: any keyword hit multiplies the score by (1+TechPenalty);
	// sector name is lowercased and substring-matched, breaking on the first hit）
	s := strings.ToLower(sector)
	for _, ts := range techSectors {
		if strings.Contains(s, strings.ToLower(ts)) {
			score *= (1 + cfg.TechPenalty)
			break
		}
	}

	// 最终得分收敛到 [0,1] 区间后再乘权重系数（Clamp the final score to [0,1], then apply the weight factor）
	return math.Max(0, math.Min(1, score)) * cfg.WeightScore
}

// IsActionWatchOrAbove 判断操作级别是否达到 watch 或以上。（IsActionWatchOrAbove reports whether an action is watch or above.）
// action: 操作指令（buy/sell/hold/watch 等）。
// 返回 true 表示该操作需要被重点关注（watch 及以上级别）。（Returns true when the action deserves attention, i.e. watch or above.）
func IsActionWatchOrAbove(action string) bool {
	switch action {
	case "buy", "sell", "hold", "watch":
		return true
	default:
		return false
	}
}
