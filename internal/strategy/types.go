// Package strategy 定义战法策略的核心类型和接口。
// 所有具体战法（Dragon/DoubleBump/NShape/DragonReturn）均实现 Strategy 接口，
// 通过 Evaluate → GenerateSignal 两阶段生成交易信号。
package strategy

import "time"

// SignalType 战法信号类型标识。
type SignalType string

const (
	SignalDragon        SignalType = "dragon"         // 龙回头
	SignalDoubleBump    SignalType = "double_bump"    // 双响炮
	SignalNShape        SignalType = "n_shape"        // N 形超短
	SignalDragonReturn  SignalType = "dragon_return"  // 龙回头(中线)
	SignalShortSkeleton SignalType = "short_skeleton" // 做空骨架
)

// TradeAction 交易动作类型。
type TradeAction string

const (
	ActionBuy   TradeAction = "buy"   // 买入
	ActionSell  TradeAction = "sell"  // 卖出
	ActionHold  TradeAction = "hold"  // 持仓
	ActionWatch TradeAction = "watch" // 观察
)

// Priority 信号优先级（1 最高，5 最低）。
type Priority int

const (
	P1   Priority = 1 // 立即执行
	P2   Priority = 2 // 尽快执行
	P3_5 Priority = 3 // 带条件执行
	P3   Priority = 4 // 普通关注
	P4   Priority = 5 // 仅记录
)

// Signal 交易信号，由 GenerateSignal 生成，供 CombatAgent 消费。
type Signal struct {
	Code       string             `json:"code"`           // 股票代码
	Name       string             `json:"name"`           // 股票名称
	Type       SignalType         `json:"type"`           // 战法类型
	Action     TradeAction        `json:"action"`         // 交易动作
	Priority   Priority           `json:"priority"`       // 优先级
	Price      float64            `json:"price"`          // 当前价格
	Qty        int                `json:"qty"`            // 建议数量
	Amount     float64            `json:"amount"`         // 成交金额
	Reason     string             `json:"reason"`         // 信号理由
	Confidence float64            `json:"confidence"`     // 置信度 0.0~1.0
	Timestamp  int64              `json:"timestamp"`      // 生成时间戳
	Meta       map[string]float64 `json:"meta,omitempty"` // 分数明细
	Reasons    map[string]string  `json:"-"`              // 各维度理由（不入JSON）
}

// SignalResult 批量信号结果。
type SignalResult struct {
	Signals  []Signal `json:"signals"`  // 本次产出的全部信号列表
	Analyzed bool     `json:"analyzed"` // 是否完成了实际分析（false 表示数据不足/未评分）
}

// Evaluation 战法评分结果，由 Evaluate 返回。
type Evaluation struct {
	TotalScore float64            `json:"total_score"` // 综合总分
	Details    map[string]float64 `json:"details"`     // 各维度分数
	Pass       bool               `json:"pass"`        // 是否通过硬闸
	Level      string             `json:"level"`       // 信号级别(full_chain/fail/nodata)
	Confidence float64            `json:"confidence"`  // 置信度
	Reasons    map[string]string  `json:"reasons"`     // 各维度理由
}

// ExitContext 止盈止损评估的上下文参数。
type ExitContext struct {
	Code      string
	Name      string
	CostPrice float64            // 持仓成本
	CurPrice  float64            // 当前价格
	EntryAt   string             // 入场时间
	EntryMeta map[string]float64 // 入场时评分明细
	DailyK    []KLine            // 日 K 线历史
	Now       time.Time          // 当前时间
}

// KLine 简化的 K 线数据结构（用于退出评估）。
type KLine struct {
	Open   float64 // 开盘价
	High   float64 // 最高价
	Low    float64 // 最低价
	Close  float64 // 收盘价
	Volume float64 // 成交量
}

// ExitResult 止盈止损评估结果。
type ExitResult struct {
	Reason   string   // 退出理由（如 "N形硬止损" / "双凸派发信号"）
	Priority Priority // 建议优先级（P1 立即执行 ~ P3 普通关注）
}

// Strategy 战法策略接口。所有具体战法必须实现此接口。
// Evaluate 接收行情数据做评分，GenerateSignal 将评分转为交易信号。
type Strategy interface {
	Name() string                                                  // 策略中文名称
	Type() SignalType                                              // 信号类型
	Evaluate(code string, data interface{}) (*Evaluation, error)   // 评分
	GenerateSignal(code string, eval *Evaluation) (*Signal, error) // 生成信号
}
