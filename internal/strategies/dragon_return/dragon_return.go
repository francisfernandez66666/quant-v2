// Package dragon_return 实现龙回头战法（Dragon Return Strategy）。
//
// 本包实现了龙回头战法，这是一种中线策略，核心思想是：
// 寻找前期龙头股在首次主升浪后的回调企稳点，捕捉"龙回头"二波启动机会。
//
// 市场模式：
//   - 强势股第一波拉升（35%~70%）
//   - 缩量回调（15%~25%，5~8天）
//   - 二次启动
//
// 形态要求：
//   - 标的在板块内排名前2
//   - RPS20≥75
//   - 回调缩量到30%以下
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
//
// （English: Implements the "Dragon Return" mid-line strategy — buy the second leg of a former sector leader after its
// first surge (35~70%) pulls back on shrinking volume (15~25%, 5–8 days). Four-factor scoring (DragonID / PullbackHealth
// / DuckHead / Confirm) maps totals to none/first/main/accelerate signals; stop-loss 5%, take-profits at 1.0×/1.25× cost.）
package dragon_return

import (
	"fmt"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// DragonReturnStrategy 龙回头战法策略结构。
// 通过四因子评分识别龙头股回调后的二波启动机会。
// 这是一个中线策略，专注于捕捉强势股回调后的二次启动。
//
// （DragonReturnStrategy is the Dragon Return strategy struct.）
type DragonReturnStrategy struct {
	cfg *config.Manager // 配置管理器（Config manager）
}

// Params 龙回头策略参数。
// 包含首轮涨幅范围、回调幅度/天数/缩量比阈值、评分权重、止损止盈参数。
// 可通过 config/rules.json 覆盖默认值。
// 这些参数支持热加载，可以在运行时动态调整。
//
// （Params are the Dragon Return tunable parameters.）
type Params struct {
	FirstRiseMin        float64 // 首轮最小涨幅（默认 0.35=35%）（Min first-leg rise, default 0.35 = 35%）
	FirstRiseMax        float64 // 首轮最大涨幅（默认 0.70=70%）（Max first-leg rise, default 0.70 = 70%）
	PullbackOptimalLow  float64 // 回调深度最优下限（默认 0.15=15%）（Optimal pullback depth lower bound, default 0.15）
	PullbackOptimalHigh float64 // 回调深度最优上限（默认 0.25=25%）（Optimal pullback depth upper bound, default 0.25）
	PullbackDaysMin     int     // 最少回调天数（默认 5 天）（Min pullback days, default 5）
	PullbackDaysMax     int     // 最多回调天数（默认 8 天）（Max pullback days, default 8）
	VolumeRatioGood     float64 // 缩量比优秀阈值（默认 0.3=30%）（Good volume-shrink threshold, default 0.3）
	ScoreThreshold      float64 // 评分通过阈值（默认 60）（Pass threshold, default 60）
	MainPositionScore   float64 // 主升信号阈值（默认 75）（Main-signal threshold, default 75）
	AccelerateScore     float64 // 加速信号阈值（默认 85）（Accelerate-signal threshold, default 85）
	StopLossPct         float64 // 止损百分比（默认 0.05=5%）（Stop-loss %, default 0.05）
	Target1Multiplier   float64 // 目标价1倍数（默认 1.0，即成本价平推）（Take-profit 1 multiplier, default 1.0 = cost）
	Target2Multiplier   float64 // 目标价2倍数（默认 1.25）（Take-profit 2 multiplier, default 1.25）
	TrailingDrawback    float64 // 移动止损回撤幅度（默认 0.08=8%）（Trailing-stop drawdown, default 0.08）
	MaxHoldDays         int     // 最长持仓天数（默认 8 天）（Max holding days, default 8）
}

// dr* 评分阈值单点（§DERIVE-CARRIER-1105B）。
//
// 为什么集中命名在这里：载体的 qmt_gateway/rules_ladder.py 是从本包逐行移植的，它按
// LADDER_PARAM_KEYS 声明自己读哪些 params，构建闸 g9 因此要求每一枚都在烘进的参数文件里
// 有主人。以前这些数是**函数体里的字面量**：外部进程拿不到，只能人手抄——抄对了也仍然留下
// 两本账（本包改档，载体那一份不会跟着动，两边就在同一轮里用不同的阈值判同一笔信号）。
// 现在生产判定读的就是下面这批常量，derive_carrier.go 只转调、一个数都不再定义。
// English: the scoring code below reads exactly these constants; the carrier export
// delegates to them instead of a second hand-written copy of the same numbers.
const (
	// 龙性识别（dragonIdentity）：硬性前置，任一不达标整笔评分归零。
	drFirstRiseMin = 0.35 // 首轮涨幅下限（不足 35% 不算主升过的龙头）
	drFirstRiseMax = 0.70 // 首轮涨幅上限（高于 70% 视为已透支）
	drSectorRPSMin = 75.0 // 板块 RPS20 门槛（板块强度不达标不做二波）
	drScoreDragon  = 25.0 // 龙性识别的满分值（也是 score() 里"不到 25 即否决"的那条线）
	// 回调幅度档（pullbackHealth 子维度一）。
	drPBBestLo       = 0.15 // 最优带下沿
	drPBBestHi       = 0.20 // 最优带上沿／较优带下沿
	drPBGoodsHi      = 0.25 // 较优带上沿／偏深带下沿
	drPBShallowLo    = 0.10 // 偏浅带下沿
	drPBDeepHi       = 0.35 // 偏深带上沿
	drPBScoreBest    = 8.0  // 15%~20%：回调充分又不破位
	drPBScoreGood    = 6.0  // 20%~25%：仍在可接受范围
	drPBScoreShallow = 5.0  // 10%~15%：调整可能不到位
	drPBScoreDeep    = 4.0  // 25%~35%：趋势可能走弱
	drPBScoreOther   = 1.0  // 其余：不带"健康洗盘"的形状
	// 回调天数档（子维度二）。
	drDaysGoodLo     = 5  // 最优带下沿（天）
	drDaysGoodHi     = 8  // 最优带上沿（天）
	drDaysShortLo    = 3  // 偏短带下沿（天）
	drDaysLongHi     = 12 // 偏长带上沿（天）
	drDaysScoreGood  = 5  // 5~8 天：洗盘充分又不拖沓
	drDaysScoreNear  = 3  // 3~4 天与 9~12 天：动能在磨底里流失
	drDaysScoreOther = 1  // 其余只拿底分
	// 缩量比档（子维度三）。
	drVrGood       = 0.3 // 极度缩量线
	drVrOk         = 0.4
	drVrMid        = 0.5
	drVrBad        = 0.6 // 超过这条就是放量回调（筹码松动）
	drVrScoreGood  = 8.0
	drVrScoreOk    = 7.0
	drVrScoreMid   = 5.0
	drVrScoreBad   = 3.0
	drVrScoreOther = 1.0
	// 鸭头形态（duckHead）。
	drShrinkMax        = 0.5  // 鸭头顶要求缩量比 < 50%
	drDuckTopFull      = 5.0  // 死叉且缩量
	drDuckTopHalf      = 2.0  // 只死叉未缩量
	drNeckSupportRatio = 0.98 // 鸭颈：现价 ≥ MA20×0.98 才算踩到支撑
	drDuckNeckFull     = 8.0  // 均线仍在多头
	drDuckNeckLow      = 5.0  // 已转到空头排列
	drSlopePrevRatio   = 0.99 // MA10 斜率式的回看比例（Go 原式，见函数注释）
	drSlopeDeltaRatio  = 0.01 // MA10 斜率式的步长（同上；该式恒等于 1，照搬含其退化形状）
	drSlopeMin         = 0.5  // 斜率门槛
	drDuckSlopeBonus   = 2.0  // 斜率达标加分
	drMacdGreenNear    = 1.0  // 绿柱"接近零轴"的带宽（-1~0 视为将转红）
	drBeakVrLo         = 0.5  // 鸭嘴量能子条件：温和放量下沿
	drBeakVrHi         = 1.2  // 上沿（暴量不算确认）
	drBeakScoreThree   = 7.0  // 三条全中
	drBeakScoreTwo     = 4.0
	drBeakScoreOne     = 2.0
	drNostrilFull      = 5.0 // 鸭鼻孔：均线粘合待发散
	drNostrilHalf      = 3.0 // 略破 MA20 但仍在支撑带
	// 确认信号（confirmSignal）。
	drConfirmVrLo    = 1.2  // 量能确认带下沿
	drConfirmVrHi    = 1.5  // 量能确认带上沿
	drConfirmVolFull = 7.0  // 温和放量（资金有序流入）
	drConfirmVrMidLo = 0.8  // 平淡带下沿
	drConfirmVolMid  = 3.0  // 尚未确认
	drKHoldRatio     = 1.03 // 站稳短期线：现价 > MA5×1.03
	drKScoreHold     = 5.0
	drKScoreCross    = 2.0  // 同时上穿 MA10 的多头排列确认
	drKNearLoRatio   = 0.98 // 企稳带下沿：MA5×0.98
	drKScoreNear     = 2.0  // 企稳但未突破
	// 总分档位（score/Evaluate/GenerateSignal 三处同一批线）。
	drLevelFirst      = 60.0 // 首次（也是 Pass 闸）
	drLevelMain       = 75.0 // 主升
	drLevelAccelerate = 85.0 // 加速
)

// DefaultParams 返回龙回头策略默认参数。
// 提供一组合理的默认值，用户可以通过配置文件覆盖这些参数。
//
// （DefaultParams returns the default Dragon Return parameters.）
func DefaultParams() Params {
	// 上面那批常量是**评分代码真正读的数**，这里的默认值必须与它们同源：
	// 以前两边各写一遍字面量，改档时只改一边不会有任何东西响（载体侧抄的就是这一份）。
	// 未列出的止损/止盈/持仓天数仍是本结构自己的参数，评分腿不读它们，故不在单点之列。
	return Params{
		FirstRiseMin:        drFirstRiseMin,
		FirstRiseMax:        drFirstRiseMax,
		PullbackOptimalLow:  drPBBestLo,
		PullbackOptimalHigh: drPBGoodsHi,
		PullbackDaysMin:     drDaysGoodLo,
		PullbackDaysMax:     drDaysGoodHi,
		VolumeRatioGood:     drVrGood,
		ScoreThreshold:      drLevelFirst,
		MainPositionScore:   drLevelMain,
		AccelerateScore:     drLevelAccelerate,
		StopLossPct:         0.05,
		Target1Multiplier:   1.0,
		Target2Multiplier:   1.25,
		TrailingDrawback:    0.08,
		MaxHoldDays:         8,
	}
}

// New 创建龙回头战法策略实例。
// 初始化策略配置，返回可直接使用的策略实例。
//
// （New creates a Dragon Return strategy instance.）
func New(cfg *config.Manager) *DragonReturnStrategy {
	return &DragonReturnStrategy{cfg: cfg}
}

// Name 返回策略标识名称"dragon_return"。
// 用于日志输出和前端展示。
//
// （Name returns the strategy identifier "dragon_return".）
func (d *DragonReturnStrategy) Name() string { return "dragon_return" }

// Type 返回信号类型标识。
// 用于信号分类和去重。
//
// （Type returns the signal type identifier.）
func (d *DragonReturnStrategy) Type() strategy.SignalType { return "dragon_return" }

// StockData 龙回头策略的输入数据结构。
// 由外部数据源（Tushare/行情API）填充后传入 Evaluate 执行评分。
// 包含了龙回头评分所需的所有数据：
//   - 价格和成交量数据
//   - 均线数据（MA5/MA10/MA20）
//   - MACD数据
//   - 板块排名和RPS数据
//
// （StockData is the Dragon Return scoring input.）
type StockData struct {
	Code         string  // 股票代码（Stock code）
	Name         string  // 股票名称（Stock name）
	CurrentPrice float64 // 当前价格（Current price）
	FirstRisePct float64 // 首轮涨幅（从启动到最高点的涨幅比例）（First-leg gain ratio, launch to peak）
	PullbackPct  float64 // 回调幅度（从最高点到当前的回调比例）（Pullback depth from peak to now）
	PullbackDays int     // 回调天数（从最高点至今的交易日数）（Pullback days from peak to now）
	VolumeRatio  float64 // 缩量比（回调期间日均量 / 主升期间日均量）（Volume ratio: pullback avg volume / rally avg volume）
	MA5          float64 // 5日移动平均线（5-day moving average）
	MA10         float64 // 10日移动平均线（10-day moving average）
	MA20         float64 // 20日移动平均线（20-day moving average）
	MACDGreen    float64 // MACD 绿柱长度（负数表示绿柱，绝对值越小越接近金叉）（MACD green-bar length; smaller abs ≈ closer to golden cross）
	HighestPrice float64 // 阶段最高价（首轮拉升高点）（Stage high price, first-leg peak）
	PreviousHigh float64 // 前高（更早的阻力位）（Previous swing high, earlier resistance）
	IsSectorTop2 bool    // 是否板块内排名前2（龙头属性）（Whether ranked top-2 in sector, leader attribute）
	SectorRPS20  float64 // 板块20日相对强弱指标 RPS（需≥75）（Sector 20-day RPS, needs ≥75）
	SectorRPS60  float64 // 板块60日相对强弱指标 RPS（Sector 60-day RPS）
	HasRiseFirst bool    // 板块内是否最先涨停（辨识度指标）（Whether first to limit-up in sector, recognition flag）
}

// ScoreResult 四因子评分结果。
// 包含了龙回头评分的四个维度分数和信号级别。
// 这是评分函数的核心输出，用于生成交易信号。
//
// （ScoreResult holds the four-factor scoring outputs.）
type ScoreResult struct {
	Total          float64 // 总分（子项之和，最大85）（Total score, sum of factors, max 85）
	DragonID       float64 // 龙性识别（0~25）（Dragon identity, 0~25）
	PullbackHealth float64 // 回调健康度（0~21）（Pullback health, 0~21）
	DuckHead       float64 // 鸭头形态（0~25）（Duck-head formation, 0~25）
	Confirm        float64 // 确认信号（0~14）（Confirmation signal, 0~14）
	Signal         string  // 信号级别（none/first/main/accelerate）（Signal level: none/first/main/accelerate）
}

// Evaluate 执行龙回头评分。
// 这是 Strategy 接口的标准实现，将输入数据转换为 StockData 后执行评分。
//
// 参数：
//   - code: 股票代码
//   - data: 必须为 *StockData 类型，否则返回空结果
//
// 返回值：
//   - *Evaluation: 评分结果，按总分分档为 none/first/main/accelerate 四级信号级别
//   - error: 错误信息（当前实现不会返回错误）
//
// （Evaluate runs the Dragon Return scoring.）
func (d *DragonReturnStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	sd, ok := data.(*StockData)
	if !ok || sd == nil {
		return &strategy.Evaluation{Pass: false, TotalScore: 0, Confidence: 0}, nil
	}

	sr := d.score(sd)
	// 通过判定：总分 ≥ 60（ScoreThreshold）（Pass: total ≥ 60 (ScoreThreshold)）
	pass := sr.Total >= drLevelFirst

	// 信号级别分档：≥85 加速 / ≥75 主升 / ≥60 首次 / 其余不操作（Signal tiers: ≥85 accelerate / ≥75 main / ≥60 first / else none）
	level := "none"
	if sr.Total >= drLevelAccelerate {
		level = "accelerate"
	} else if sr.Total >= drLevelMain {
		level = "main"
	} else if sr.Total >= drLevelFirst {
		level = "first"
	}
	// 置信度 = 总分 / 100（满分 100 口径）（Confidence = total / 100 on a 100-point scale）
	confidence := sr.Total / 100.0

	// 组装结果：总分 + 四因子明细 + 原始输入指标（供前端/复盘展示）（Assemble total + four factors + raw inputs for display/review）
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
		// §0929DIM 四因子理由：把各维打分依据的原始量（首涨/回调幅度与天数/缩量比/均线）
		// 写成一句话随信号过界。这些量本来就在 StockData 上，此前只有分数进了 Details。
		// English: §0929DIM — the raw quantities behind each factor (first-leg gain, pullback depth/days,
		// volume shrink ratio, MAs) already live on StockData; only their scores used to cross the boundary.
		Reasons: map[string]string{
			"dragon_score":   fmt.Sprintf("板块Top2=%v 首涨%.0f%% RPS20=%.0f", sd.IsSectorTop2, sd.FirstRisePct*100, sd.SectorRPS20),
			"pullback_score": fmt.Sprintf("回调%.0f%%/%d日/缩量%.0f%%", sd.PullbackPct*100, sd.PullbackDays, sd.VolumeRatio*100),
			"duck_score":     fmt.Sprintf("鸭头(MA5 %.2f/MA10 %.2f/MA20 %.2f)", sd.MA5, sd.MA10, sd.MA20),
			"confirm_score":  fmt.Sprintf("确认(现价%.2f/MA5 %.2f/量比%.2f)", sd.CurrentPrice, sd.MA5, sd.VolumeRatio),
		},
	}, nil
}

// score 执行四因子加权评分。
// 这是龙回头评分的核心函数，按顺序计算四个因子：
//  1. 龙性识别（25%权重）：硬性前置条件，不达标直接返回
//  2. 回调健康度（30%权重）：回调幅度、天数、缩量比
//  3. 鸭头形态（25%权重）：技术形态判断
//  4. 确认信号（20%权重）：量能和K线确认
//
// 权重分配: 龙性 25% + 回调健康 30% + 鸭头 25% + 确认 20%
// 龙性未通过（<25 分）时直接跳过后续评分。
//
// （score runs the four-factor weighted scoring.）
func (d *DragonReturnStrategy) score(sd *StockData) ScoreResult {
	var sr ScoreResult

	// 龙性识别是硬性前置条件：不达标直接返回（其余因子不再计算）（Dragon identity is a hard precondition: return early if unmet）
	// 那条线就是龙性维度的满分值（全有或全无），所以它不另立一枚数——两枚数会漂成两档。
	sr.DragonID = d.dragonIdentity(sd)
	if sr.DragonID < drScoreDragon {
		return sr
	}

	sr.PullbackHealth = d.pullbackHealth(sd)
	sr.DuckHead = d.duckHead(sd)
	sr.Confirm = d.confirmSignal(sd)

	sr.Total = sr.DragonID + sr.PullbackHealth + sr.DuckHead + sr.Confirm

	// 依据总分映射信号级别（Map signal level from the total score）
	sr.Signal = "none"
	if sr.Total >= drLevelAccelerate {
		sr.Signal = "accelerate"
	} else if sr.Total >= drLevelMain {
		sr.Signal = "main"
	} else if sr.Total >= drLevelFirst {
		sr.Signal = "first"
	}

	return sr
}

// dragonIdentity 龙性识别（权重 25%，满分 25）。
// 这是龙回头评分的硬性前置条件，全部满足才可继续评分：
//  1. 板块前 2 名（IsSectorTop2）
//  2. 首轮涨幅 35%~70%（排除涨幅不足或过高的标的）
//  3. 板块 RPS20 ≥ 75（板块相对强度达标）
//
// 全部满足返回 25 分，否则返回 0，直接否决整个评分。
//
// （dragonIdentity scores the leader identity, 25% weight, max 25.）
func (d *DragonReturnStrategy) dragonIdentity(sd *StockData) float64 {
	if !sd.IsSectorTop2 {
		return 0
	}
	if sd.FirstRisePct < drFirstRiseMin || sd.FirstRisePct > drFirstRiseMax {
		return 0
	}
	if sd.SectorRPS20 < drSectorRPSMin {
		return 0
	}
	return drScoreDragon
}

// pullbackHealth 回调健康度评分（权重 30%，满分 21~30）。
// 评估回调的质量，回调越温和、缩量越充分，说明抛压衰竭、筹码沉淀良好。
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
//
// （English: sums three sub-scores — pullback depth 0~8, pullback days 0~5, volume-shrink 0~8.）
func (d *DragonReturnStrategy) pullbackHealth(sd *StockData) float64 {
	score := 0.0
	pct := sd.PullbackPct
	if pct >= drPBBestLo && pct < drPBBestHi {
		score += drPBScoreBest
	} else if pct >= drPBBestHi && pct < drPBGoodsHi {
		score += drPBScoreGood
	} else if pct >= drPBShallowLo && pct < drPBBestLo {
		score += drPBScoreShallow
	} else if pct >= drPBGoodsHi && pct < drPBDeepHi {
		score += drPBScoreDeep
	} else {
		score += drPBScoreOther
	}

	// 回调时长维度：5~8 个交易日最理想（洗盘充分又不拖沓）给满分 5，
	// 3~4 天嫌短、9~12 天动能已在磨底中流失，各给 3 分，其余时长只拿 1 分底分。
	days := sd.PullbackDays
	if days >= drDaysGoodLo && days <= drDaysGoodHi {
		score += drDaysScoreGood
	} else if days >= drDaysShortLo && days < drDaysGoodLo {
		score += drDaysScoreNear
	} else if days > drDaysGoodHi && days <= drDaysLongHi {
		score += drDaysScoreNear
	} else {
		score += drDaysScoreOther
	}

	// 缩量维度：回调期量比越低说明抛压越轻（<0.3 拿满 8 分），
	// 逐级递减到 ≥0.6 只给 1 分——放量回调意味着筹码松动，不是健康洗盘。
	vr := sd.VolumeRatio
	if vr < drVrGood {
		score += drVrScoreGood
	} else if vr < drVrOk {
		score += drVrScoreOk
	} else if vr < drVrMid {
		score += drVrScoreMid
	} else if vr < drVrBad {
		score += drVrScoreBad
	} else {
		score += drVrScoreOther
	}

	return score
}

// duckHead 鸭头形态评分（权重 25%，满分 25）。
// 评估调整末期的技术形态，鸭头形态是即将启动的信号。
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
//
// （English: four parts — duck-head top (5) on MA5<MA10 with shrink, duck neck (8) on MA20 support, duck beak (7) on MA5
// cross + MACD green-bar contraction + moderate volume, duck nostril (5) on MA5>MA20 squeeze zone.）
func (d *DragonReturnStrategy) duckHead(sd *StockData) float64 {
	score := 0.0

	// 鸭头顶 (5分): MA5<MA10 且缩量（Duck head (5): MA5<MA10 with volume shrink）
	if sd.MA5 < sd.MA10 && sd.VolumeRatio < drShrinkMax {
		score += drDuckTopFull
	} else if sd.MA5 < sd.MA10 {
		score += drDuckTopHalf
	}

	// 鸭颈部 (8分): 股价在 MA20 附近获得支撑（Duck neck (8): price supported near MA20）
	if sd.MA20 > 0 && sd.MA5 < sd.MA20 && sd.CurrentPrice >= sd.MA20*drNeckSupportRatio {
		// 这条"斜率"式子在 Go 里恒等于 1（分子 MA10−MA10×0.99 = MA10×0.01，分母同为 MA10×0.01），
		// 也就是说 0.5 的门槛永远过。载体是**照搬含这个退化形状**移植的（不是修好它）：
		// 单边改成真斜率会让两边在同一轮里给出不同的分，而这条线现在是影子对照的基准。
		ma10Slope := (sd.MA10 - sd.MA10*drSlopePrevRatio) / (sd.MA10 * drSlopeDeltaRatio)
		if sd.MA10 > sd.MA5 {
			score += drDuckNeckFull
		} else {
			score += drDuckNeckLow
		}
		// MA10 斜率加分：斜率 > 0.5 额外加 2 分（MA10 slope bonus: slope > 0.5 adds 2 points）
		if ma10Slope > drSlopeMin {
			score += drDuckSlopeBonus
		}
	}

	// 鸭嘴部 (7分): 三条件综合（Duck beak (7): weighted by the three sub-conditions）
	duckBeakScore := 0
	if sd.MA5 > sd.MA10 {
		duckBeakScore++
	}
	if sd.MACDGreen < 0 && sd.MACDGreen > -drMacdGreenNear {
		duckBeakScore++
	} else if sd.MACDGreen > 0 {
		duckBeakScore++
	}
	if sd.VolumeRatio > drBeakVrLo && sd.VolumeRatio < drBeakVrHi {
		duckBeakScore++
	}
	switch duckBeakScore {
	case 3:
		score += drBeakScoreThree
	case 2:
		score += drBeakScoreTwo
	case 1:
		score += drBeakScoreOne
	}

	// 鸭鼻孔 (5分): 均线粘合区（Duck nostril (5): MA squeeze zone）
	if sd.MA5 < sd.MA10 && sd.MA5 > sd.MA20 {
		score += drNostrilFull
	} else if sd.MA5 <= sd.MA20 && sd.MA5 > sd.MA20*drNeckSupportRatio {
		score += drNostrilHalf
	}

	return score
}

// confirmSignal 确认信号评分（权重 20%，满分 14）。
// 评估二波启动的技术确认信号，量能和K线同时确认是关键。
//
// ① 量能确认（7分）：
//
//	量比 1.2~1.5 → 7分（温和放量，资金有序流入）
//	量比 0.8~1.2 → 3分（量能平淡，尚未确认）
//
// ② K线形态（7分）：
//
//	股价 > MA5×1.03 → 5分（站稳短期均线）
//	同时 > MA10 → 再加 2分（短期多头排列确认）
//	股价在 MA5 附近（0.98~1.00）×MA5 → 2分（企稳但未突破）
//
// （English: volume confirm (7) on 1.2~1.5 volume ratio, and K-line confirm (7) on holding above MA5 / crossing MA10.）
func (d *DragonReturnStrategy) confirmSignal(sd *StockData) float64 {
	score := 0.0

	// 量能确认 (7分)（Volume confirmation, 7 points）
	if sd.VolumeRatio >= drConfirmVrLo && sd.VolumeRatio <= drConfirmVrHi {
		score += drConfirmVolFull
	} else if sd.VolumeRatio > drConfirmVrMidLo && sd.VolumeRatio < drConfirmVrLo {
		score += drConfirmVolMid
	}

	// K线形态用当前价相对于均线的位置近似 (7分)（K-line confirm approximated by price vs. MA position, 7 points）
	if sd.CurrentPrice > sd.MA5 && sd.CurrentPrice > sd.MA5*drKHoldRatio {
		score += drKScoreHold
		if sd.CurrentPrice > sd.MA5 && sd.CurrentPrice > sd.MA10 {
			score += drKScoreCross
		}
	} else if sd.CurrentPrice >= sd.MA5*drKNearLoRatio && sd.CurrentPrice <= sd.MA5 {
		score += drKScoreNear
	}

	return score
}

// GenerateSignal 将评分结果转化为交易信号。
// 根据评估结果的总分决定交易动作和优先级：
//   - 总分 ≥85 → accelerate（P1 买入）
//   - 总分 ≥75 → main（P2 买入）
//   - 总分 ≥60 → first（P3_5 买入）
//   - <60 不生成信号
//
// 信号生成后，会将评分明细复制到Meta字段，供前端展示四因子分数。
//
// （GenerateSignal converts an evaluation into a trade signal.）
func (d *DragonReturnStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	// 未通过评分（总分<60）直接返回 nil，不产生信号（Not passing (<60) → no signal）
	if !eval.Pass {
		return nil, nil
	}

	// 默认仅观察；按总分档位逐步升级为买入信号并提高优先级（Default watch; escalate to buy and raise priority by total tier）
	action := strategy.ActionWatch
	priority := strategy.P3

	// 总分分档同时决定动作与优先级：≥85 → P1、≥75 → P2、≥60 → P3_5，三档都转买入；
	// 60 以下在上面的 Pass 闸已被拦掉，这里不会再产出弱信号抢占仓位额度。
	total := eval.TotalScore
	if total >= drLevelAccelerate {
		action = strategy.ActionBuy
		priority = strategy.P1
	} else if total >= drLevelMain {
		action = strategy.ActionBuy
		priority = strategy.P2
	} else if total >= drLevelFirst {
		action = strategy.ActionBuy
		priority = strategy.P3_5
	}

	// 组装信号：Meta 直接挂评分明细（四因子分原样透传给前端与回放），
	// Timestamp 取秒级 Unix 时间，与其它战法的信号口径保持一致。
	// §0929DIM Reasons 同样过界——Meta 给的是分数，Reasons 给的是"这个分数凭什么"。
	return &strategy.Signal{
		Code:       code,
		Type:       strategy.SignalType("dragon_return"),
		Action:     action,
		Priority:   priority,
		Confidence: eval.Confidence,
		Timestamp:  time.Now().Unix(),
		Meta:       eval.Details,
		Reasons:    strategy.CopyReasons(eval.Reasons),
	}, nil
}
