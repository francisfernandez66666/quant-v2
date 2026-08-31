// Package strategy 提供多种选股策略的实现。
// 本文件实现了 Laodeng（"捞等"）策略，这是一个综合评分策略：
//   - 市值维度（30%）：小市值优先，但有最低门槛
//   - 市盈率维度（30%）：低PE优先，有上限阈值
//   - 换手率维度（20%）：高换手率优先，有最低门槛
//   - 科技板块加权（20%乘数）：科技行业获得额外加权
//
// 评分结果为0.0~1.0范围，再乘以WeightScore权重系数。
//
// （Package strategy provides stock-selection strategy implementations.）
package strategy

import (
	"math"
	"strings"

	"quant-trading-v2/internal/config"
)

// techSectors 科技行业关键词列表，命中该列表的个股会获得额外加权（TechPenalty）。
// 用于识别具有科技属性的行业板块，科技行业通常具有更高的成长性和估值溢价。
// 匹配规则：行业名做小写归一后子串匹配，命中即跳出循环。
// （Tech-sector keywords; matches add a TechPenalty multiplier to the score.）
var techSectors = []string{
	"半导体", "芯片", "集成电路", "软件", "计算机", "互联网",
	"人工智能", "AI", "云计算", "大数据", "通信", "5G",
	"消费电子", "电子", "信息技术", "机器人", "自动化",
	"新能源", "锂电池", "光伏", "风电", "储能", "数字经济",
}

// ScoreLaodeng 计算"捞等"策略的综合评分。
// 这是一个多维度综合评分函数，综合考虑市值、市盈率、换手率和行业属性。
//
// 参数说明：
//   - cfg: Laodeng 策略配置（包含各指标的权重和阈值）
//   - marketCap: 个股总市值（元）
//   - pe: 个股市盈率（PE）
//   - turnover: 个股换手率（百分比）
//   - sector: 个股所属行业板块名称
//
// 返回值：0.0~1.0 范围的综合得分，再乘以 WeightScore 权重系数
//
// 评分维度：
//   - 市值(30%)：小市值优先，有最低门槛
//   - 市盈率(30%)：低PE优先，有上限阈值
//   - 换手率(20%)：高换手率优先，有最低门槛
//   - 科技板块加权(20%乘数)：科技行业获得额外加权
//
// （ScoreLaodeng computes the Laodeng composite score.）
func ScoreLaodeng(cfg *config.LaodengConfig, marketCap, pe, turnover float64, sector string) float64 {
	// 配置未启用或为空时直接返回 0（不参与评分）（Disabled or nil config → return 0, not scored）
	if cfg == nil || !cfg.Enabled {
		return 0
	}

	// §R4-5 修复：市值数据源未接入时（marketCap<=0）不再"假装知道市值"——
	// 跳过市值维度，把剩余两维（PE+换手，满分 0.5）放大回 0.8 满分口径，
	// 与有市值路径输出区间一致；旧调用方曾硬编码 marketCap=600 亿喂分，属数据造假。
	// English: §R4-5 — when market cap is unknown (<=0), skip that dimension and scale
	// PE+turnover (max 0.5) up to the same 0.8 scale, instead of fabricating a cap value.
	var score float64
	if marketCap > 0 {
		// 有市值：市值维度（满分 0.3，达标给满否则线性折算）+ PE + 换手，满分 0.8（旧口径不变）
		capScore := 0.3
		if marketCap < cfg.MarketCapMin {
			capScore = 0.3 * (marketCap / cfg.MarketCapMin)
		}
		score = capScore + peScore(cfg, pe) + turnScore(cfg, turnover)
	} else {
		// 无市值：PE+换手（满分 0.5）等比放大到 0.8 尺度，输出区间与有市值路径一致
		score = (peScore(cfg, pe) + turnScore(cfg, turnover)) * (0.8 / 0.5)
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

// peScore 市盈率维度评分（满分 0.3）。
// 评分规则：
//   - PE≤上限：给满分0.3
//   - PE>上限：线性衰减，超出越多分数越低
//   - 无PE数据（PE≤0）：给保底0.1
//
// 这个函数反映了低估值优先的投资逻辑。
//
// English: the PE dimension (max 0.3) — full up to the cap, linear decay above; missing PE gets a 0.1 floor.
func peScore(cfg *config.LaodengConfig, pe float64) float64 {
	if pe > 0 && pe <= cfg.PeMax {
		return 0.3
	}
	if pe > 0 {
		return 0.3 * math.Max(0, 1-(pe-cfg.PeMax)/cfg.PeMax)
	}
	return 0.1
}

// turnScore 换手率维度评分（满分 0.2）。
// 评分规则：
//   - 换手率≥最低门槛：给满分0.2
//   - 换手率<最低门槛：按比例线性折算
//
// 这个函数反映了高流动性优先的投资逻辑。
//
// English: the turnover dimension (max 0.2) — full when ≥ min, else linear scaling.
func turnScore(cfg *config.LaodengConfig, turnover float64) float64 {
	if turnover >= cfg.TurnoverMin {
		return 0.2
	}
	return 0.2 * (turnover / cfg.TurnoverMin)
}

// IsActionWatchOrAbove 判断操作级别是否达到 watch 或以上。
// 用于过滤需要重点关注的操作指令，排除无关紧要的操作。
//
// 参数：
//   - action: 操作指令（buy/sell/hold/watch 等）
//
// 返回值：true 表示该操作需要被重点关注（watch 及以上级别）
//
// （IsActionWatchOrAbove reports whether an action is watch or above.）
func IsActionWatchOrAbove(action string) bool {
	switch action {
	case "buy", "sell", "hold", "watch":
		return true
	default:
		return false
	}
}
