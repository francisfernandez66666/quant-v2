// Package data — 三角形筹码分布分析。
// 通过 K 线量价数据模拟筹码分布（类指南针/通达信筹码峰），
// 用于判断筹码集中度、获利比例、主力控盘度及洗盘/出货信号。
package data

import (
	"math"
	"sort"
)

// 默认筹码参数
const (
	defaultDecayDelta   = 0.985 // 日衰减系数，越早的 K 线权重越低
	defaultLookbackDays = 90    // 默认回看天数
	defaultSectorTotal  = 300   // 默认板块总数
)

// ChipParams 筹码算法参数，可通过 rules.json 热加载调整。
type ChipParams struct {
	DecayDelta          float64 // 时间衰减系数（每日乘数）
	LookbackDays        int     // K 线回看天数
	Concentration70Good float64 // 70% 筹码集中度优良阈值
	Concentration70Bad  float64 // 70% 筹码集中度较差阈值
	Concentration90Good float64 // 90% 筹码集中度优良阈值
	ProfitRatioHigh     float64 // 高获利比例阈值
	ProfitRatioLow      float64 // 低获利比例阈值
	MainCostTouchBand   float64 // 主力成本触及带宽（相对价格）
	PeakMoveThreshold   float64 // 筹码峰移动判定阈值
}

// DefaultChipParams 返回默认筹码参数。
func DefaultChipParams() ChipParams {
	return ChipParams{
		DecayDelta:          defaultDecayDelta,
		LookbackDays:        defaultLookbackDays,
		Concentration70Good: 0.08,
		Concentration70Bad:  0.25,
		Concentration90Good: 0.15,
		ProfitRatioHigh:     0.9,
		ProfitRatioLow:      0.2,
		MainCostTouchBand:   0.05,
		PeakMoveThreshold:   0.05,
	}
}

// ChipAnalysis 筹码分析结果。
type ChipAnalysis struct {
	Concentration70 float64 // 70% 筹码集中度（价格区间/峰值价），越小越集中
	Concentration90 float64 // 90% 筹码集中度
	ProfitRatio     float64 // 获利比例（当前价以下筹码占比，0-1）
	PeakPrice       float64 // 筹码峰价格（持仓量最大的价位）
	MainCostPrice   float64 // 主力成本价（近似等于筹码峰价格）
	ControlScore    float64 // 主力控盘度评分（0-1）
	Score           float64 // 0-100 综合筹码评分
}

// IsConcentrated 筹码是否高度集中（70% 集中度 < 8%）。
func (c *ChipAnalysis) IsConcentrated() bool { return c.Concentration70 < 0.08 }

// IsDispersed 筹码是否分散（70% 集中度 > 25%）。
func (c *ChipAnalysis) IsDispersed() bool { return c.Concentration70 > 0.25 }

// IsProfitable 是否整体获利丰厚（获利比例 > 90%）。
func (c *ChipAnalysis) IsProfitable() bool { return c.ProfitRatio > 0.9 }

// IsDeepLoss 是否整体深度套牢（获利比例 < 20%）。
func (c *ChipAnalysis) IsDeepLoss() bool { return c.ProfitRatio < 0.2 }

// TriangularChipDistribution 计算三角形筹码分布（Triangular Chip Distribution）。
// 算法：对每根 K 线以 typical price 为中心构建等腰三角形分布，
// 按成交量加权、时间衰减后汇总，通过分位数计算集中度和获利比例。
// 参考 PRD 公式 B3-B5。
func TriangularChipDistribution(klines []KLine, params ChipParams) *ChipAnalysis {
	n := len(klines)
	if n == 0 {
		return nil
	}

	// 价格-持仓量 bin（离散化的筹码柱）
	type priceBin struct {
		price float64
		mass  float64
	}
	var bins []priceBin

	peak := math.Inf(-1)
	peakPrice := 0.0

	for i, k := range klines {
		// B3: 时间衰减权重，越近权重越高
		decay := math.Pow(params.DecayDelta, float64(n-1-i))
		// 用 (H+L+C)/3 作为典型价
		typical := (k.High + k.Low + k.Close) / 3.0
		spread := k.High - k.Low
		if spread < 0.001 {
			spread = 0.001
		}

		steps := 20
		for j := 0; j <= steps; j++ {
			p := typical - spread + (2.0*spread*float64(j))/float64(steps)
			// B4: 等腰三角形分布密度函数
			var tri float64
			if p >= typical-spread && p <= typical {
				tri = (p - (typical - spread)) / (spread * spread / 2.0)
			} else if p > typical && p <= typical+spread {
				tri = ((typical + spread) - p) / (spread * spread / 2.0)
			}
			mass := k.Volume * tri * decay
			bins = append(bins, priceBin{price: p, mass: mass})
			if mass > peak {
				peak = mass
				peakPrice = p
			}
		}
	}

	if len(bins) == 0 {
		return nil
	}

	sort.Slice(bins, func(i, j int) bool { return bins[i].price < bins[j].price })

	totalMass := 0.0
	for _, b := range bins {
		totalMass += b.mass
	}
	if totalMass < 0.001 {
		return nil
	}

	// B5: 分位数计算
	cumSum := 0.0
	p5, p15, p85, p95 := 0.0, 0.0, 0.0, 0.0
	profitRatio := 0.0
	currentPrice := klines[n-1].Close

	for _, b := range bins {
		prev := cumSum
		cumSum += b.mass
		fraction := cumSum / totalMass

		if prev/totalMass < 0.05 && fraction >= 0.05 {
			p5 = b.price
		}
		if prev/totalMass < 0.15 && fraction >= 0.15 {
			p15 = b.price
		}
		if prev/totalMass < 0.85 && fraction >= 0.85 {
			p85 = b.price
		}
		if prev/totalMass < 0.95 && fraction >= 0.95 {
			p95 = b.price
		}
		if b.price < currentPrice {
			profitRatio += b.mass
		}
	}
	profitRatio /= totalMass

	c70 := (p85 - p15) / peakPrice
	c90 := (p95 - p5) / peakPrice

	// 主力控盘度评分（0-1）
	controlScore := 0.0
	if c70 > 0 {
		controlScore += (1 - min(c70, 0.25)/0.25) * 0.4
	}
	if profitRatio > 0.8 {
		controlScore += 0.3
	} else {
		controlScore += (profitRatio / 0.8) * 0.3
	}
	priceDist := math.Abs(currentPrice-peakPrice) / peakPrice
	priceDist = min(priceDist/0.15, 1.0)
	controlScore += (1 - priceDist) * 0.3

	// B6: 综合评分（0-100）
	score := 0.0
	if c70 < params.Concentration70Good {
		score += 30
	} else if c70 < params.Concentration70Bad {
		score += 15
	}
	if c90 < params.Concentration90Good {
		score += 20
	} else {
		score += 10
	}
	if profitRatio > 0.7 {
		score += 25
	} else if profitRatio > 0.4 {
		score += 15
	} else {
		score += 5
	}
	if controlScore > 0.7 {
		score += 25
	} else if controlScore > 0.4 {
		score += 15
	} else {
		score += 5
	}

	return &ChipAnalysis{
		Concentration70: c70,
		Concentration90: c90,
		ProfitRatio:     profitRatio,
		PeakPrice:       peakPrice,
		MainCostPrice:   peakPrice,
		ControlScore:    controlScore,
		Score:           score,
	}
}

// IsWash 判断筹码是否出现洗盘特征。
// 比较前后两个半段的筹码分布变化，得分 >=8 判定为洗盘。
// 洗盘特征：筹码峰不动、获利比例从高位降至 40-60%、集中度提升、价格在主力成本附近。
func IsWash(prevChip, curChip *ChipAnalysis, curPrice, peakPrice float64) (bool, float64) {
	if prevChip == nil || curChip == nil {
		return false, 0
	}
	score := 0.0
	// 筹码峰未明显移动
	if math.Abs(curChip.PeakPrice-prevChip.PeakPrice)/prevChip.PeakPrice < 0.05 {
		score += 4
	}
	// 获利比例从高位（>80%）回落至 40-60%
	if curChip.ProfitRatio > 0.4 && curChip.ProfitRatio < 0.6 && prevChip.ProfitRatio > 0.8 {
		score += 4
	}
	// 筹码集中度提升（值变小）
	if curChip.Concentration70 < prevChip.Concentration70 {
		score += 3
	}
	// 当前价接近主力成本
	if math.Abs(curPrice-curChip.MainCostPrice)/curChip.MainCostPrice < 0.05 {
		score += 4
	}
	return score >= 8, score
}

// IsDistribution 判断筹码是否出现出货特征。
// 条件：筹码峰上移 >10%，或获利比例 >90% 且价格未远离顶部（<2%）。
func IsDistribution(prevChip, curChip *ChipAnalysis, curPrice, peakPrice float64) bool {
	if prevChip == nil || curChip == nil {
		return false
	}
	if math.Abs(curChip.PeakPrice-prevChip.PeakPrice)/prevChip.PeakPrice > 0.1 {
		return true
	}
	if curChip.ProfitRatio > 0.9 && curPrice <= peakPrice*1.02 {
		return true
	}
	return false
}

// WashScore 计算洗盘评分。将 K 线分为前后两半分别计算筹码分布，
// 若 IsWash 判定为洗盘则返回具体分数，否则返回 0。
func WashScore(klines []KLine, params ChipParams) float64 {
	if len(klines) < 20 {
		return 0
	}
	half := len(klines) / 2
	prev := TriangularChipDistribution(klines[:half], params)
	cur := TriangularChipDistribution(klines[half:], params)
	if prev == nil || cur == nil {
		return 0
	}
	isWash, score := IsWash(prev, cur, klines[len(klines)-1].Close, 0)
	if !isWash {
		return 0
	}
	return score
}
