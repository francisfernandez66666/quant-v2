// Package combat_agent 战法引擎核心包：定义战法引擎的所有数据结构，包括扫描输入、信号输出、评分结果等。
// 本文件是 combat_agent 包的类型定义文件，集中管理所有对外暴露的数据结构。
//
// 核心类型说明：
//   - ScanInput: 三大扫描路径（ScanLong/ScanShort/Scan）与持续打分（ScorePool）的统一入参，
//     包含已验证板块、行情数据、D1评分、涨停池、新闻等信息。
//   - Signal: 引擎对外输出的统一信号模型，包含方向、操作、置信度、原因等，
//     由战法评分、持仓止盈止损提醒、涨停池增强三大来源产生。
//   - StockScores: 承载 8a/8b 持续打分结果，记录单只股票的四战法原始分和动量分，
//     无论战法是否通过都记录，供前端展示持续评分。
//   - D1Score: 单只个股的 D1 事件评分结果，由 LLM 分析新闻事件得出。
//   - NewsBrief: 个股关联新闻简报，供预期差检测使用。
//
// 本文件不包含任何业务逻辑，仅定义数据结构和辅助函数。
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// NewsBrief 个股关联新闻简报，供预期差检测使用。
// 当个股有关联新闻时，用于判断新闻预期与实际股价反应的偏差。
// 字段说明：
//   - Title: 新闻标题，用于预期差检测的文本分析
//   - Positive: 事件方向，true=利好事件，false=利空事件
//   - Time: 新闻发布时间，格式为 YYYY-MM-DD HH:MM:SS
type NewsBrief struct {
	Title    string // 新闻标题
	Positive bool   // 事件方向：true=利好 false=利空
	Time     string // 新闻时间（YYYY-MM-DD HH:MM:SS）
}

// ScanInput 战法扫描输入，包含已验证板块、行情数据、D1评分等信息。
// 由上层 Engine 组装传入，是三大扫描路径（ScanLong/ScanShort/Scan）与持续打分（ScorePool）的统一入参。
// 其中 Scores 字段由 Engine 初始化，扫描过程中被写入，供 8a/8b 前端展示持续评分。
//
// 主要字段说明：
//   - Sectors: 已验证的板块列表，包含方向信息（利好/利空）
//   - L1Score: L1 过滤评分结果，用于初步筛选
//   - L1Blocked: L1 过滤阻塞标记，true 表示该股被拦截
//   - IndividualStocks: 个股直接输入，跳过板块验证
//   - MarketData: 行情数据映射，由 Engine 传入
//   - D1Scores: D1 评分结果，由 LLM 分析新闻事件得出
//   - PE: 个股动态市盈率，供 N 形 D3 维度使用
//   - LimitUpPool: 当日涨停池，用于龙头识别和涨停分类
//   - News: 个股关联新闻简报映射，供预期差检测使用
//   - Scores: 8a/8b 打分输出，由 Engine 初始化，扫描过程中写入
//   - EmotionPhase: 情绪阶段，供 N 形评分的情绪硬闸使用
type ScanInput struct {
	Sectors          []sector_agent.VerifiedSector               // 已验证的板块列表（含方向利好/利空）
	L1Score          map[string]float64                          // L1 过滤评分结果
	L1Blocked        map[string]bool                             // L1 过滤阻塞标记（true=该股被拦截）
	IndividualStocks []string                                    // 个股直接输入（跳过板块验证）
	MarketData       map[string]*strategy_engine.StockMarketData // 行情数据（Engine传过来）
	D1Scores         map[string]D1Score                          // D1评分结果
	PE               map[string]float64                          // code → 个股动态市盈率（N 形 D3 用，Engine 预取）
	LimitUpPool      []data.LimitUpStock                         // 当日涨停池（龙头识别/涨停分类）
	News             map[string][]NewsBrief                      // code → 关联新闻简报（预期差）
	Scores           map[string]StockScores                      // 8a/8b 打分输出（engine 初始化，扫描写入）
	EmotionPhase     string                                      // 情绪阶段（供 N 形评分）
}

// StockScores 单只股票的四战法原始分 + 动量分（8a/8b 持续打分输出）。
// 无论战法是否通过都记录原始分，前端据此展示自选/持仓的持续评分。
// JSON 标签供前端按稳定 key 取值展示，确保字段名称在前后端一致。
//
// 字段说明：
//   - Code: 股票代码
//   - NScore: N 形战法分（0~100），由 N 形评分器计算
//   - DragonScore: 龙头战法分（0~100），由龙头评分器计算
//   - DoubleBumpScore: 双响炮战法分（0~100），由双响炮评分器计算
//   - DragonReturnScore: 龙回头战法分（0~100），由龙回头评分器计算
//   - MomentumScore: 动量分（0~100），综合量价、MACD、走势三个维度
//   - MomentumValid: 动量分数据是否完整，量价/走势/MACD 任一缺失为 false
//   - SignalActive: 本轮是否有该股信号，true 表示有信号产生
//   - DataGaps: 数据缺口标记，key 为战法类型，true 表示该战法输入数据不足
//   - UpdatedAt: 打分时间戳
type StockScores struct {
	Code              string          `json:"code"`          // 股票代码
	NScore            float64         `json:"n_score"`       // N 形战法分（0~100）
	DragonScore       float64         `json:"dragon_score"`  // 龙头战法分（0~100）
	DoubleBumpScore   float64         `json:"db_score"`      // 双响炮战法分（0~100）
	DragonReturnScore float64         `json:"dr_score"`      // 龙回头战法分（0~100）
	MomentumScore     float64         `json:"m_score"`       // 动量分（量价+MACD+走势，0~100）
	MomentumValid     bool            `json:"m_valid"`       // 动量分数据是否完整（量价/走势/MACD 任一缺失为 false）
	SignalActive      bool            `json:"signal_active"` // 本轮是否有该股信号
	DataGaps          map[string]bool `json:"data_gaps"`     // 数据缺口标记：key=战法类型，true=该战法输入数据不足（0 分不代表真实 0）
	UpdatedAt         time.Time       `json:"updated_at"`    // 打分时间
}

// Signal 战法引擎输出的信号，包含方向、操作、置信度等信息。
// 三大来源：
//   - 战法评分（ScanLong/ScanShort/Scan/ScorePool）：由四战法（龙头/双响炮/N形/龙回头）产生
//   - 持仓止盈止损提醒：由 CheckPositionAlerts 按预设阈值触发
//   - 涨停池增强：由 ScanLimitUp 产生的龙头识别和预期差提醒
//
// 上层根据 Direction/Action 字段分发处理：
//   - Direction: 做多/做空/提醒
//   - Action: 买入/卖出/watch（仅观察）
//
// 关键字段说明：
//   - ID: 信号唯一标识，格式为 SIG+纳秒时间戳
//   - Code/Name: 股票代码和名称
//   - Strategy: 策略名称，使用规范展示名（龙头/双响炮/N形/龙回头/动量）
//   - Direction: 信号方向（做多/做空/提醒）
//   - Action: 操作建议（买入/卖出/watch）
//   - Tag: 信号标记（如 N 形的一突/二突）
//   - AlertType: 提醒类型（止盈/止损/清仓/减仓等）
//   - Price: 触发价格
//   - Confidence: 置信度（0~1）
//   - ATR: 标的 ATR14，供仓位管理与动态止损参考
//   - Reason: 信号生成原因，包含详细说明
//   - Sector: 所属板块
//   - StrategyID: 战法库规则 ID，用于效果监测
//   - StrategyType: 战法来源类型，用于模拟盘战法分仓
//   - D1Score: D1 事件评分（0~40）
//   - D1Blocked: D1 负面过滤拦截标记
//   - D1Reason: D1 事件分析理由（LLM）
//   - D1Event: D1 关联事件名称
//   - Meta: 策略评分明细，供前端展示真实维度分
//   - DepthFactors: 盘口派生因子，供战法读取买卖压力/封单量
type Signal struct {
	ID          string    `json:"id"`                   // 信号唯一标识
	Code        string    `json:"code"`                 // 股票代码
	Name        string    `json:"name"`                 // 股票名称
	Strategy    string    `json:"strategy"`             // 策略名称
	Direction   string    `json:"direction"`            // 方向：做多/做空/提醒
	Action      string    `json:"action"`               // 操作：买入/卖出/watch
	Tag         string    `json:"tag,omitempty"`        // 信号标记（如 N形 一突/二突）
	AlertType   string    `json:"alert_type,omitempty"` // 提醒类型：止盈/止损
	Price       float64   `json:"price"`                // 触发价格
	Confidence  float64   `json:"confidence"`           // 置信度（0~1）
	ATR         float64   `json:"atr,omitempty"`        // 标的 ATR14（C4/C6 仓位管理与动态止损参考；日K不足时为 0）
	Reason      string    `json:"reason"`               // 信号生成原因
	Sector      string    `json:"sector"`               // 所属板块
	GeneratedAt time.Time `json:"generated_at"`         // 信号生成时间

	// StrategyID 战法库规则 ID（如 "fac_1"）。用于效果监测：把触发信号归属到具体已应用战法规则。
	// 仅多规则战法（因子战法库）填充；其余战法为空。
	// English: strategy-library rule ID (e.g. "fac_1") used to attribute a signal to a specific applied
	// strategy rule for effectiveness monitoring. Populated only for multi-rule strategies (factor library).
	StrategyID string `json:"strategy_id,omitempty"`

	// StrategyType 战法来源类型（runner 类型：dragon/double_bump/n_shape/dragon_return/momentum/factor 规则ID/pattern 规则ID）。
	// 用于模拟盘战法分仓：paper 引擎据此把信号归入对应战法资金池（buy 只扣本池现金）。
	// §动量入模拟盘：momentum 信号（watch/buy）统一携带 "momentum"，归动量池。
	// English: source strategy type (dragon/double_bump/n_shape/dragon_return/momentum/factor rule ID/
	// pattern rule ID) used by the paper engine's strategy-pool allocation. Momentum signals now carry
	// "momentum" so their buys route to the momentum pool.
	StrategyType string `json:"strategy_type,omitempty"`

	// D1 事件信息（新闻归因/LLM 分析，区别于策略 Reason）：
	// D1Score 为该股最近一轮 D1 事件评分（0~40，越高越值得关注；与板块利好/利空事件分解耦，独立 LLM 打分）；
	// D1Blocked 表示是否被负面过滤拦截（立案/减持/质押/解禁等）；D1Reason 为 LLM 对事件的 D1 分析理由；D1Event 为个股关联的事件名称（新闻标题）。
	// English: D1 event info (news attribution / LLM analysis, distinct from the strategy Reason):
	// D1Score is the stock's latest D1 event score (0~40, higher = more noteworthy; decoupled from the
	// sector bull/bear event score and graded independently by the LLM); D1Blocked means the
	// negative filter tripped; D1Reason is the LLM's D1 analysis; D1Event is the linked event title.
	D1Score   float64 `json:"d1_score,omitempty"`   // D1 事件评分（0~40）
	D1Blocked bool    `json:"d1_blocked,omitempty"` // D1 负面过滤拦截标记
	D1Reason  string  `json:"d1_reason,omitempty"`  // D1 事件分析理由（LLM）
	D1Event   string  `json:"d1_event,omitempty"`   // D1 关联事件名称

	// Meta 策略评分明细（key=d1/d2/d3/d4 等），从 strategy.Signal.Meta 原样拷贝，
	// 供前端展示真实维度分（避免把总分复用到各维度）。
	// English: per-dimension score breakdown (keys d1/d2/d3/d4...) copied verbatim from
	// strategy.Signal.Meta, so the frontend shows real dimension scores instead of the total.
	Meta map[string]float64 `json:"meta,omitempty"`

	// DepthFactors 盘口派生因子（免费源五档，Level-2 可扩十档）：供战法读取买卖压力/封单量。
	// 仅当数据可用时填充（omitempty），缺失为零值——战法应容忍因子缺失。
	// English: derived order-book factors (5 levels free / 10 with Level-2) for strategies to read
	// bid/ask pressure & seal volume. Filled only when available; strategies must tolerate zero.
	DepthFactors *data.OrderBookFactors `json:"depth_factors,omitempty"`
}

// SignalLog 单轮策略信号批次快照，记录该轮产出的全部信号与产出时间。
// 供前端"信号日志"弹窗按批次（时间分组）展示，用于复盘信号历史。
// 每次扫描（ScanLong/ScanShort/Scan/ScorePool）产出的信号都会打包成一个 SignalLog。
//
// 字段说明：
//   - ProcessTime: 信号批次产出时间，用于时间分组
//   - RawCount: 本轮原始新闻条数，用于统计信息
//   - Signals: 本轮全部信号（做多/做空/提醒），按产出顺序排列
type SignalLog struct {
	ProcessTime time.Time `json:"process_time"` // 信号批次产出时间
	RawCount    int       `json:"raw_count"`    // 本轮原始新闻条数
	Signals     []Signal  `json:"signals"`      // 本轮全部信号（做多/做空/提醒）
}

// NDiag N 形候选诊断条目：记录每只 N 候选的评分与拦截原因，供日志定位"当日为何无 N 信号"。
// engine 每轮 DrainNDiag 收口后打印一行概要；单只详情可按需展开。
// 用于调试和排查 N 形战法为何没有产生信号。
//
// 字段说明：
//   - Code: 股票代码
//   - Name: 股票名称
//   - D1: D1 事件分（0~40），由 LLM 评分
//   - Total: 四维总分（0~100），综合 d1/d2/d3/d4 四个维度
//   - Level: 判定级别，可能的值：
//     - full_chain: 完整形态确认
//     - left_signal: 一突信号
//     - right_signal: 二突信号
//     - fail: 未通过
//     - noscore: 无评分
//   - Tag: 信号标记（一突/二突）
//   - Pass: 本轮是否通过
//   - Reason: 拦截原因（d1=0/total_below/emotion 等）
type NDiag struct {
	Code   string  `json:"code"`             // 股票代码
	Name   string  `json:"name"`             // 股票名称
	D1     float64 `json:"d1"`               // D1 事件分（0~40）
	Total  float64 `json:"total"`            // 四维总分（0~100）
	Level  string  `json:"level"`            // 判定级别（full_chain/left_signal/right_signal/fail/noscore）
	Tag    string  `json:"tag,omitempty"`    // 信号标记（一突/二突）
	Pass   bool    `json:"pass"`             // 本轮是否 Pass
	Reason string  `json:"reason,omitempty"` // 拦截原因（d1=0/total_below/emotion 等）
}

// ── §战法名称规整：全链路唯一口径 ──
//
// 历史上 Signal.Strategy 混用英文 runner 类型（dragon/double_bump/n_shape/dragon_return）
// 与中文名（动量/龙头识别/预期差/因子战法#N），模拟盘池名又是第三套（poolkey.go）。
// 现统一：规范展示名 = 龙头 / 双响炮 / N形 / 龙回头 / 动量；规则池沿用规则名（fac_N 的库名）。
//
// 这套名称规整机制确保信号、模拟盘、UI 和配置共享同一套命名方案。

// StrategyDisplayName 将战法类型转换为规范展示名。
// 参数：
//   - t: 战法类型字符串（如 "dragon"、"double_bump" 等）
//
// 返回值：
//   - 对应的规范中文展示名（龙头/双响炮/N形/龙回头/动量）
//   - 未知类型原样返回
//
// 使用场景：信号生成时将 runner 类型转换为前端展示名
func StrategyDisplayName(t string) string {
	switch strategy.SignalType(t) {
	case strategy.SignalDragon:
		return "龙头"
	case strategy.SignalDoubleBump:
		return "双响炮"
	case strategy.SignalNShape:
		return "N形"
	case strategy.SignalDragonReturn:
		return "龙回头"
	case strategy.SignalMomentum:
		return "动量"
	}
	return t
}

// NormalizeStrategyName 将任意别名（英文名/旧中文变体）转换为规范展示名。
// 参数：
//   - name: 战法名称，可以是英文名、中文名或旧版变体
//
// 返回值：
//   - 规范中文展示名（龙头/双响炮/N形/龙回头/动量）
//   - 无法识别时原样返回（因子/形态规则名、龙头识别等特例不受影响）
//
// 支持的别名映射：
//   - dragon / 龙头 → 龙头
//   - double_bump / DoubleBump / 双响炮 / 双突破 / 双凸 → 双响炮
//   - n_shape / NShape / N形超短 / N字型 / N字 → N形
//   - dragon_return / DragonReturn / 龙回头 → 龙回头
//   - momentum / Momentum / 动量 → 动量
func NormalizeStrategyName(name string) string {
	switch name {
	case "dragon", "龙头":
		return "龙头"
	case "double_bump", "DoubleBump", "双响炮", "双突破", "双凸":
		return "双响炮"
	case "n_shape", "NShape", "N形超短", "N字型", "N字":
		return "N形"
	case "dragon_return", "DragonReturn", "龙回头":
		return "龙回头"
	case "momentum", "Momentum", "动量":
		return "动量"
	}
	return name
}
