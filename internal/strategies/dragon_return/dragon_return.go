// Package dragon_return 实现龙回头战法（Dragon Return Strategy）。
//
// 市场模式：寻找前期龙头股在首次主升浪后的回调企稳点，捕捉"龙回头"二波启动机会。
// 核心逻辑：强势股第一波拉升（35%~70%）→ 缩量回调（15%~25%，5~8天）→ 二次启动。
// 形态上要求标的在板块内排名前2、RPS20≥75、回调缩量到 30% 以下。
//
// 四因子评分体系（加权总分 0~100）：
//
//   - 龙性识别 DragonID（权重 25%，满分 25）：
//     条件：板块前2 + 首轮涨幅 35%~70% + 板块 RPS20 ≥ 75。
//     不符合以上任一条件直接否决（Total=0）。
//
//   - 回调健康度 PullbackHealth（权重 30%，满分 21~30）：
//     三子维度：回调幅度（最优 15~20% 得8分）+ 回调天数（5~8天得5分）+ 缩量比（<30% 得8分）
//     回调越温和、缩量越充分，说明抛压衰竭、筹码沉淀良好。
//
//   - 鸭头形态 DuckHead（权重 25%，满分 25）：
//     四部分：鸭头顶（MA5<MA10 且缩量）、鸭颈部（股价在 MA20 附近受支撑）、
//     鸭嘴部（MA5 上穿 + MACD 绿柱收窄 + 温和放量）、鸭鼻孔（MA5<MA10 但 >MA20 的粘合区）。
//     综合评判调整末期、即将启动的技术形态。
//
//   - 确认信号 Confirm（权重 20%，满分 14）：
//     量能确认（量比 1.2~1.5 得7分）+ K线确认（站稳 MA5 且上穿 MA10 得7分）。
//     放量突破均线是二波启动的技术确认信号。
//
// 信号级别（从评分到交易）：
//   - Total ≥ 85 → accelerate（加速信号，P1 买入）
//   - Total ≥ 75 → main（主升信号，P2 买入）
//   - Total ≥ 60 → first（首次信号，P3_5 买入）
//   - Total < 60 → none（不操作）
//
// 仓位管理：信号级别对应不同建仓比例，加速信号可用更高仓位。
// 止损：5%（StopLossPct）；止盈 Target1 = 成本×1.0，Target2 = 成本×1.25
// 持仓上限：8 天（MaxHoldDays），跌破移动止损线（8%）强制平仓。
package dragon_return

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// DragonReturnStrategy 龙回头战法策略结构。
// 通过四因子评分识别龙头股回调后的二波启动机会。
type DragonReturnStrategy struct {
	cfg *config.Manager // 配置管理器
}

// Params 龙回头策略参数。
// 包含首轮涨幅范围、回调幅度/天数/缩量比阈值、评分权重、止损止盈参数。
// 可通过 config/rules.json 覆盖默认值。
type Params struct {
	FirstRiseMin        float64 // 首轮最小涨幅（默认 0.35=35%）
	FirstRiseMax        float64 // 首轮最大涨幅（默认 0.70=70%）
	PullbackOptimalLow  float64 // 回调深度最优下限（默认 0.15=15%）
	PullbackOptimalHigh float64 // 回调深度最优上限（默认 0.25=25%）
	PullbackDaysMin     int     // 最少回调天数（默认 5 天）
	PullbackDaysMax     int     // 最多回调天数（默认 8 天）
	VolumeRatioGood     float64 // 缩量比优秀阈值（默认 0.3=30%）
	ScoreThreshold      float64 // 评分通过阈值（默认 60）
	MainPositionScore   float64 // 主升信号阈值（默认 75）
	AccelerateScore     float64 // 加速信号阈值（默认 85）
	StopLossPct         float64 // 止损百分比（默认 0.05=5%）
	Target1Multiplier   float64 // 目标价1倍数（默认 1.0，即成本价平推）
	Target2Multiplier   float64 // 目标价2倍数（默认 1.25）
	TrailingDrawback    float64 // 移动止损回撤幅度（默认 0.08=8%）
	MaxHoldDays         int     // 最长持仓天数（默认 8 天）
}

// DefaultParams 返回龙回头策略默认参数。
func DefaultParams() Params {
	return Params{
		FirstRiseMin:        0.35,
		FirstRiseMax:        0.70,
		PullbackOptimalLow:  0.15,
		PullbackOptimalHigh: 0.25,
		PullbackDaysMin:     5,
		PullbackDaysMax:     8,
		VolumeRatioGood:     0.3,
		ScoreThreshold:      60,
		MainPositionScore:   75,
		AccelerateScore:     85,
		StopLossPct:         0.05,
		Target1Multiplier:   1.0,
		Target2Multiplier:   1.25,
		TrailingDrawback:    0.08,
		MaxHoldDays:         8,
	}
}

// New 创建龙回头战法策略实例。
func New(cfg *config.Manager) *DragonReturnStrategy {
	return &DragonReturnStrategy{cfg: cfg}
}

// Name 返回策略标识名称"dragon_return"。
func (d *DragonReturnStrategy) Name() string { return "dragon_return" }

// Type 返回信号类型标识。
func (d *DragonReturnStrategy) Type() strategy.SignalType { return "dragon_return" }

// StockData 龙回头策略的输入数据结构。
// 由外部数据源（Tushare/行情API）填充后传入 Evaluate 执行评分。
type StockData struct {
	Code         string  // 股票代码
	Name         string  // 股票名称
	CurrentPrice float64 // 当前价格
	FirstRisePct float64 // 首轮涨幅（从启动到最高点的涨幅比例）
	PullbackPct  float64 // 回调幅度（从最高点到当前的回调比例）
	PullbackDays int     // 回调天数（从最高点至今的交易日数）
	VolumeRatio  float64 // 缩量比（回调期间日均量 / 主升期间日均量）
	MA5          float64 // 5日移动平均线
	MA10         float64 // 10日移动平均线
	MA20         float64 // 20日移动平均线
	MACDGreen    float64 // MACD 绿柱长度（负数表示绿柱，绝对值越小越接近金叉）
	HighestPrice float64 // 阶段最高价（首轮拉升高点）
	PreviousHigh float64 // 前高（更早的阻力位）
	IsSectorTop2 bool    // 是否板块内排名前2（龙头属性）
	SectorRPS20  float64 // 板块20日相对强弱指标 RPS（需≥75）
	SectorRPS60  float64 // 板块60日相对强弱指标 RPS
	HasRiseFirst bool    // 板块内是否最先涨停（辨识度指标）
}

// ScoreResult 四因子评分结果。
type ScoreResult struct {
	Total          float64 // 总分（子项之和，最大85）
	DragonID       float64 // 龙性识别（0~25）
	PullbackHealth float64 // 回调健康度（0~21）
	DuckHead       float64 // 鸭头形态（0~25）
	Confirm        float64 // 确认信号（0~14）
	Signal         string  // 信号级别（none/first/main/accelerate）
}

// Evaluate 执行龙回头评分。
// 输入 data 必须为 *StockData 类型。
func (d *DragonReturnStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	sd, ok := data.(*StockData)
	if !ok || sd == nil {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Confidence: 0}, nil
	}

	sr := d.score(sd)
	pass := sr.Total >= 60

	level := "none"
	if sr.Total >= 85 {
		level = "accelerate"
	} else if sr.Total >= 75 {
		level = "main"
	} else if sr.Total >= 60 {
		level = "first"
	}
	confidence := sr.Total / 100.0

	return &strategy.Evaluation{
		TotalScore: sr.Total,
		Pass:       pass,
		Level:      level,
		Confidence: confidence,
		Details: map[string]float64{
			"dragon_score":   sr.DragonID,
			"pullback_score": sr.PullbackHealth,
			"duck_score":     sr.DuckHead,
			"confirm_score":  sr.Confirm,
			"first_rise":     sd.FirstRisePct,
			"pullback_pct":   sd.PullbackPct,
			"pullback_days":  float64(sd.PullbackDays),
			"volume_ratio":   sd.VolumeRatio,
			"ma5":            sd.MA5,
			"ma10":           sd.MA10,
			"ma20":           sd.MA20,
		},
	}, nil
}

// score 执行四因子加权评分。
// 权重分配: 龙性 25% + 回调健康 30% + 鸭头 25% + 确认 20%
// 龙性未通过（<25 分）时直接跳过后续评分。
func (d *DragonReturnStrategy) score(sd *StockData) ScoreResult {
	var sr ScoreResult

	sr.DragonID = d.dragonIdentity(sd)
	if sr.DragonID < 25 {
		return sr
	}

	sr.PullbackHealth = d.pullbackHealth(sd)
	sr.DuckHead = d.duckHead(sd)
	sr.Confirm = d.confirmSignal(sd)

	sr.Total = sr.DragonID + sr.PullbackHealth + sr.DuckHead + sr.Confirm

	sr.Signal = "none"
	if sr.Total >= 85 {
		sr.Signal = "accelerate"
	} else if sr.Total >= 75 {
		sr.Signal = "main"
	} else if sr.Total >= 60 {
		sr.Signal = "first"
	}

	return sr
}

// dragonIdentity 龙性识别（权重 25%，满分 25）。
// 硬性条件（全部满足才可继续评分）：
//  1. 板块前 2 名（IsSectorTop2）
//  2. 首轮涨幅 35%~70%（排除涨幅不足或过高的标的）
//  3. 板块 RPS20 ≥ 75（板块相对强度达标）
//
// 全部满足返回 25 分，否则返回 0。
func (d *DragonReturnStrategy) dragonIdentity(sd *StockData) float64 {
	if !sd.IsSectorTop2 {
		return 0
	}
	if sd.FirstRisePct < 0.35 || sd.FirstRisePct > 0.70 {
		return 0
	}
	if sd.SectorRPS20 < 75 {
		return 0
	}
	return 25
}

// pullbackHealth 回调健康度评分（权重 30%，满分 21~30）。
//
// 三子维度之和：
//
// ① 回调幅度评分（0~8）：
//
//	15%~20%:  8分（最优，回调充分不破位）
//	20%~25%:  6分（较优，仍在可接受范围）
//	10%~15%:  5分（偏浅，可能调整不到位）
//	25%~35%:  4分（偏深，可能趋势走弱）
//	其他:     1分
//
// ② 回调天数评分（0~5）：
//
//	5~8天:    5分（最优，时间充分不过长）
//	3~5天:    3分（偏短，调整不充分）
//	8~12天:   3分（偏长，关注是否走弱）
//	其他:     1分
//
// ③ 缩量比评分（0~8）：
//
//	<30%:     8分（优秀，极度缩量）
//	30~40%:   7分（良好）
//	40~50%:   5分（一般）
//	50~60%:   3分（较差，抛压未衰竭）
//	≥60%:     1分（差，缩量不明显）
func (d *DragonReturnStrategy) pullbackHealth(sd *StockData) float64 {
	score := 0.0
	pct := sd.PullbackPct
	if pct >= 0.15 && pct < 0.20 {
		score += 8
	} else if pct >= 0.20 && pct < 0.25 {
		score += 6
	} else if pct >= 0.10 && pct < 0.15 {
		score += 5
	} else if pct >= 0.25 && pct < 0.35 {
		score += 4
	} else {
		score += 1
	}

	days := sd.PullbackDays
	if days >= 5 && days <= 8 {
		score += 5
	} else if days >= 3 && days < 5 {
		score += 3
	} else if days > 8 && days <= 12 {
		score += 3
	} else {
		score += 1
	}

	vr := sd.VolumeRatio
	if vr < 0.3 {
		score += 8
	} else if vr < 0.4 {
		score += 7
	} else if vr < 0.5 {
		score += 5
	} else if vr < 0.6 {
		score += 3
	} else {
		score += 1
	}

	return score
}

// duckHead 鸭头形态评分（权重 25%，满分 25）。
//
// 鸭头形态由四部分组成，模拟鸭子头部的形状：
//
// ① 鸭头顶（5分）：MA5 < MA10（短期均线死叉）且缩量 < 50%。
//
//	说明短期回调缩量，是健康的调整。
//
// ② 鸭颈部（8分）：股价回踩 MA20 附近（≥ MA20×0.98）。
//
//	MA10 > MA5 时得8分（均线仍在多头），否则得5分。
//	MA20 支撑是鸭头形态的关键确认点。
//
// ③ 鸭嘴部（7分）：三个子条件得分（0~3项，每项1分 → 7/4/2分）：
//   - MA5 > MA10（即将金叉或已金叉）
//   - MACD 绿柱接近零轴或转红（-1~0 或 >0）
//   - 量比在 0.5~1.2 之间（温和放量，非暴量）
//
// ④ 鸭鼻孔（5分）：MA5 < MA10 但 MA5 > MA20（均线粘合待发散）。
//
//	均线粘合是变盘前兆，发散方向决定二波启动。
func (d *DragonReturnStrategy) duckHead(sd *StockData) float64 {
	score := 0.0

	// 鸭头顶 (5分): MA5<MA10 且缩量
	if sd.MA5 < sd.MA10 && sd.VolumeRatio < 0.5 {
		score += 5
	} else if sd.MA5 < sd.MA10 {
		score += 2
	}

	// 鸭颈部 (8分): 股价在 MA20 附近获得支撑
	if sd.MA20 > 0 && sd.MA5 < sd.MA20 && sd.CurrentPrice >= sd.MA20*0.98 {
		ma10Slope := (sd.MA10 - sd.MA10*0.99) / (sd.MA10 * 0.01)
		if sd.MA10 > sd.MA5 {
			score += 8
		} else {
			score += 5
		}
		// MA10 斜率加分：斜率 > 0.5 额外加 2 分
		if ma10Slope > 0.5 {
			score += 2
		}
	}

	// 鸭嘴部 (7分): 三条件综合
	duckBeakScore := 0
	if sd.MA5 > sd.MA10 {
		duckBeakScore++
	}
	if sd.MACDGreen < 0 && sd.MACDGreen > -1 {
		duckBeakScore++
	} else if sd.MACDGreen > 0 {
		duckBeakScore++
	}
	if sd.VolumeRatio > 0.5 && sd.VolumeRatio < 1.2 {
		duckBeakScore++
	}
	switch duckBeakScore {
	case 3:
		score += 7
	case 2:
		score += 4
	case 1:
		score += 2
	}

	// 鸭鼻孔 (5分): 均线粘合区
	if sd.MA5 < sd.MA10 && sd.MA5 > sd.MA20 {
		score += 5
	} else if sd.MA5 <= sd.MA20 && sd.MA5 > sd.MA20*0.98 {
		score += 3
	}

	return score
}

// confirmSignal 确认信号评分（权重 20%，满分 14）。
//
// ① 量能确认（7分）：量比 1.2~1.5 → 7分（温和放量，资金有序流入）；
//
//	量比 0.8~1.2 → 3分（量能平淡，尚未确认）。
//
// ② K线形态（7分）：股价 > MA5×1.03 → 5分（站稳短期均线）；
//
//	同时 > MA10 → 再加 2分（短期多头排列确认）；
//	股价在 MA5 附近（0.98~1.00）×MA5 → 2分（企稳但未突破）。
func (d *DragonReturnStrategy) confirmSignal(sd *StockData) float64 {
	score := 0.0

	// 量能确认 (7分)
	if sd.VolumeRatio >= 1.2 && sd.VolumeRatio <= 1.5 {
		score += 7
	} else if sd.VolumeRatio > 0.8 && sd.VolumeRatio < 1.2 {
		score += 3
	}

	// K线形态用当前价相对于均线的位置近似 (7分)
	if sd.CurrentPrice > sd.MA5 && sd.CurrentPrice > sd.MA5*1.03 {
		score += 5
		if sd.CurrentPrice > sd.MA5 && sd.CurrentPrice > sd.MA10 {
			score += 2
		}
	} else if sd.CurrentPrice >= sd.MA5*0.98 && sd.CurrentPrice <= sd.MA5 {
		score += 2
	}

	return score
}

// GenerateSignal 将评分结果转化为交易信号。
// 总分 ≥85 → accelerate（P1 买入）
// 总分 ≥75 → main（P2 买入）
// 总分 ≥60 → first（P3_5 买入）
// <60 不生成信号。
func (d *DragonReturnStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	if !eval.Pass {
		return nil, nil
	}

	action := strategy.ActionWatch
	priority := strategy.P3

	total := eval.TotalScore
	if total >= 85 {
		action = strategy.ActionBuy
		priority = strategy.P1
	} else if total >= 75 {
		action = strategy.ActionBuy
		priority = strategy.P2
	} else if total >= 60 {
		action = strategy.ActionBuy
		priority = strategy.P3_5
	}

	return &strategy.Signal{
		Code:       code,
		Type:       strategy.SignalType("dragon_return"),
		Action:     action,
		Priority:   priority,
		Confidence: eval.Confidence,
		Timestamp:  time.Now().Unix(),
		Meta:       eval.Details,
	}, nil
}
