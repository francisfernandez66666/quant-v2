// Package strategy 定义战法策略的核心类型和接口。
// 本包是所有战法策略的基础包，定义了：
//   - Strategy 接口：所有具体战法必须实现的接口
//   - Signal: 交易信号结构体
//   - Evaluation: 战法评分结果
//   - ExitContext: 止盈止损评估的上下文参数
//   - ExitResult: 止盈止损评估结果
//   - 各种常量定义：信号类型、交易动作、优先级等
//
// 所有具体战法（Dragon/DoubleBump/NShape/DragonReturn）均实现 Strategy 接口,
// 通过 Evaluate → GenerateSignal 两阶段生成交易信号。
// （Package strategy defines core types and interfaces for strategies.）
package strategy

import "time"

// SignalType 战法信号类型标识。（SignalType identifies a strategy signal type.）
type SignalType string

// 战法信号类型取值（与后端信号落库的 type 字段一致）。
const (
	SignalDragon        SignalType = "dragon"         // 龙回头（Dragon）
	SignalDoubleBump    SignalType = "double_bump"    // 双响炮（Double Bump）
	SignalNShape        SignalType = "n_shape"        // N 形超短（N-shape ultra-short）
	SignalDragonReturn  SignalType = "dragon_return"  // 龙回头(中线)（Dragon Return, mid-line）
	SignalShortSkeleton SignalType = "short_skeleton" // 做空骨架（Short-sell skeleton）
	SignalFactor        SignalType = "factor"         // 因子战法（E6：自动发现的因子组合，实盘信号）（Factor strategy, E6）
	SignalPattern       SignalType = "pattern"        // 形态战法（F3：自动发现的形态模板，实盘信号）（Pattern strategy, F3）
	SignalMomentum      SignalType = "momentum"       // 动量（量价齐升观察/买入，§动量入模拟盘）
)

// TradeAction 交易动作类型。（TradeAction is a trade action type.）
type TradeAction string

// 交易动作类型取值。
const (
	ActionBuy   TradeAction = "buy"   // 买入（Buy）
	ActionSell  TradeAction = "sell"  // 卖出（Sell）
	ActionHold  TradeAction = "hold"  // 持仓（Hold）
	ActionWatch TradeAction = "watch" // 观察（Watch）
)

// Priority 信号优先级（1 最高，5 最低）。（Priority is the signal priority, 1 highest to 5 lowest.）
type Priority int

// 信号优先级取值（1 最高，5 最低）。
const (
	P1   Priority = 1 // 立即执行（Execute immediately）
	P2   Priority = 2 // 尽快执行（Execute soon）
	P3_5 Priority = 3 // 带条件执行（Execute conditionally）
	P3   Priority = 4 // 普通关注（Normal attention）
	P4   Priority = 5 // 仅记录（Log only）
)

// Signal 交易信号，由 GenerateSignal 生成，供 CombatAgent 消费。
// 这是战法策略的核心输出，包含了交易决策的所有关键信息：
//   - Code/Name: 股票标识
//   - Type: 战法类型（dragon/double_bump/n_shape等）
//   - Action: 交易动作（buy/sell/hold/watch）
//   - Priority: 优先级（1-5，1最高）
//   - Confidence: 置信度（0.0-1.0）
//   - Meta: 评分明细（供前端展示）
//
// （Signal is a trade signal produced by GenerateSignal for the CombatAgent.）
type Signal struct {
	Code       string             `json:"code"`           // 股票代码（Stock code）
	Name       string             `json:"name"`           // 股票名称（Stock name）
	Type       SignalType         `json:"type"`           // 战法类型（Strategy type）
	Action     TradeAction        `json:"action"`         // 交易动作（Trade action）
	Priority   Priority           `json:"priority"`       // 优先级（Priority）
	Price      float64            `json:"price"`          // 当前价格（Current price）
	Qty        int                `json:"qty"`            // 建议数量（Suggested quantity）
	Amount     float64            `json:"amount"`         // 成交金额（Trade amount）
	Reason     string             `json:"reason"`         // 信号理由（Signal reason）
	Confidence float64            `json:"confidence"`     // 置信度 0.0~1.0（Confidence 0.0~1.0）
	Timestamp  int64              `json:"timestamp"`      // 生成时间戳（Generation timestamp）
	Meta       map[string]float64 `json:"meta,omitempty"` // 分数明细（Score breakdown）
	Reasons    map[string]string  `json:"-"`              // 各维度理由（不入JSON）（Per-dimension reasons, excluded from JSON）
	// StrategyName 可选：覆盖默认的战法名（string(runner.Type)）。
	// 用于同一战法类型下有多个独立规则时区分信号（如多因子战法各规则），使消息中心去重键互不冲突。
	// English: optional override for the default strategy name (string(runner.Type)). Used when a single
	// strategy type hosts several independent rules (e.g. multiple factor strategies), so each signal's
	// message-center dedup key stays distinct.
	StrategyName string `json:"strategy_name,omitempty"`
}

// SignalResult 批量信号结果。（SignalResult is a batch signal result.）
type SignalResult struct {
	Signals  []Signal `json:"signals"`  // 本次产出的全部信号列表（All signals produced this round）
	Analyzed bool     `json:"analyzed"` // 是否完成了实际分析（false 表示数据不足/未评分）（Whether scoring actually ran; false = insufficient data / unscored）
}

// Evaluation 战法评分结果，由 Evaluate 返回。
// 这是战法评分的核心输出，包含了：
//   - TotalScore: 综合总分（0-100）
//   - Details: 各维度分数明细
//   - Pass: 是否通过硬闸
//   - Level: 信号级别（full_chain/brief/watch/nodata）
//   - Confidence: 置信度（0.0-1.0）
//   - Reasons: 各维度理由（供前端展示）
//
// （Evaluation is the scoring result returned by Evaluate.）
type Evaluation struct {
	TotalScore float64            `json:"total_score"` // 综合总分（Composite total score）
	Details    map[string]float64 `json:"details"`     // 各维度分数（Per-dimension scores）
	Pass       bool               `json:"pass"`        // 是否通过硬闸（Whether the hard gate passed）
	Level      string             `json:"level"`       // 信号级别(full_chain/fail/nodata)（Signal level: full_chain/fail/nodata）
	Confidence float64            `json:"confidence"`  // 置信度（Confidence）
	Reasons    map[string]string  `json:"reasons"`     // 各维度理由（Per-dimension reasons）
}

// ExitContext 止盈止损评估的上下文参数。
// 包含了持仓的所有关键信息，用于评估是否应该止盈或止损：
//   - Code/Name: 股票标识
//   - CostPrice: 持仓成本
//   - CurPrice: 当前价格
//   - EntryAt: 入场时间
//   - EntryMeta: 入场时评分明细（包含highest_price等）
//   - DailyK: 日K线历史
//   - Now: 当前时间
//   - ATR/ATRStopMult: ATR动态止损参数
//
// （ExitContext carries parameters for take-profit / stop-loss evaluation.）
type ExitContext struct {
	Code      string             // 股票代码（Stock code）
	Name      string             // 股票名称（Stock name）
	CostPrice float64            // 持仓成本（Cost price）
	CurPrice  float64            // 当前价格（Current price）
	EntryAt   string             // 入场时间（Entry time）
	EntryMeta map[string]float64 // 入场时评分明细（Score details at entry）
	DailyK    []KLine            // 日 K 线历史（Daily K-line history）
	Now       time.Time          // 当前时间（Current time）

	// C4 ATR 动态止损：ATR 为标的当前 ATR（通常 ATR14，缺日K时为 0），
	// ATRStopMult 为止损倍数（≤0 表示未启用、回退固定百分比）。
	// English: C4 ATR dynamic stop — ATR is the instrument's current ATR (usually ATR14, 0 when daily
	// bars are missing); ATRStopMult is the stop multiplier (≤0 = disabled, fall back to fixed percent).
	ATR         float64
	ATRStopMult float64
}

// ATRStopPct 返回本持仓的有效止损亏损百分比（相对成本）：
// ATRStopMult>0 且 ATR>0 时返回 ATR×mult/成本×100（动态 ATR 止损），否则返回 fallbackPct（固定百分比）。
// English: returns this position's effective stop-loss loss percent (vs cost): ATR×mult/cost×100 when
// ATR stopping is active (ATRStopMult>0 and ATR>0), else the fixed fallbackPct.
func (c *ExitContext) ATRStopPct(fallbackPct float64) float64 {
	if c.ATRStopMult > 0 && c.ATR > 0 && c.CostPrice > 0 {
		if atrPct := c.ATR * c.ATRStopMult / c.CostPrice * 100; atrPct > 0 {
			return atrPct
		}
	}
	return fallbackPct
}

// KLine 简化的 K 线数据结构（用于退出评估）。（KLine is a simplified bar used for exit evaluation.）
type KLine struct {
	Open   float64 // 开盘价（Open）
	High   float64 // 最高价（High）
	Low    float64 // 最低价（Low）
	Close  float64 // 收盘价（Close）
	Volume float64 // 成交量（Volume）
}

// ExitResult 止盈止损评估结果。
// 包含退出理由和建议优先级，供交易执行层决策。
// 返回nil表示继续持有，返回ExitResult表示应该退出。
//
// （ExitResult is the take-profit / stop-loss evaluation result.）
type ExitResult struct {
	Reason   string   // 退出理由（如 "N形硬止损" / "双凸派发信号"）（Exit reason, e.g. N-shape hard stop / Double Bump distribution）
	Priority Priority // 建议优先级（P1 立即执行 ~ P3 普通关注）（Suggested priority, P1 immediate to P3 normal）
}

// Strategy 战法策略接口。所有具体战法必须实现此接口。
// 这是所有战法的统一接口，确保不同战法可以被相同的方式调用和管理。
// 主要方法：
//   - Name: 返回策略中文名称
//   - Type: 返回信号类型标识
//   - Evaluate: 接收行情数据做评分
//   - GenerateSignal: 将评分转为交易信号
//
// （Strategy is the interface all concrete strategies must implement.）
type Strategy interface {
	Name() string                                                  // 策略中文名称（Strategy display name）
	Type() SignalType                                              // 信号类型（Signal type）
	Evaluate(code string, data interface{}) (*Evaluation, error)   // 评分（Scoring）
	GenerateSignal(code string, eval *Evaluation) (*Signal, error) // 生成信号（Signal generation）
}
